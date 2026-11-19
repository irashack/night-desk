package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/edition"
)

// DiagnosticProblem records the exact validation location and rule.
type DiagnosticProblem struct {
	Path string `json:"path"`
	Rule string `json:"rule"`
}

// SourceCheckCount counts advisory findings by stable rule (without claim text).
// Status is passed, failed, or unverified. Failed checks remain publish warnings.
type SourceCheckCount struct {
	Status string `json:"status"`
	Rule   string `json:"rule"`
	Count  int    `json:"count"`
}

// PublishDiagnostics is input to the publish transaction, not edition content.
// Path is put, draft, or deadline. RetentionDays defaults to 14.
type PublishDiagnostics struct {
	Path          string
	Warnings      []DiagnosticProblem
	Errors        []DiagnosticProblem
	SourceChecks  []SourceCheckCount
	RetentionDays int
}

func diagnosticsTx(ctx context.Context, tx *sql.Tx, paper, date string, published bool, d PublishDiagnostics) error {
	if paper == "" {
		return errors.New("paper is required")
	}
	if _, err := edition.ParseDate(date); err != nil {
		return err
	}
	if d.Path != "put" && d.Path != "draft" && d.Path != "deadline" {
		return errors.New("invalid publish path")
	}
	if d.RetentionDays == 0 {
		d.RetentionDays = 14
	}
	if d.RetentionDays < 1 || d.RetentionDays > 90 {
		return errors.New("invalid diagnostic retention")
	}
	if d.Warnings == nil {
		d.Warnings = []DiagnosticProblem{}
	}
	if d.Errors == nil {
		d.Errors = []DiagnosticProblem{}
	}
	if d.SourceChecks == nil {
		d.SourceChecks = []SourceCheckCount{}
	}
	warnings, err := json.Marshal(d.Warnings)
	if err != nil {
		return err
	}
	problems, err := json.Marshal(d.Errors)
	if err != nil {
		return err
	}
	for _, check := range d.SourceChecks {
		if check.Count < 0 || (check.Status != "passed" && check.Status != "failed" && check.Status != "unverified") {
			return errors.New("invalid source check count")
		}
	}
	checks, err := json.Marshal(d.SourceChecks)
	if err != nil {
		return err
	}
	at := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, `INSERT INTO publish_diagnostics(paper_id,date,path,published,warnings,errors,source_checks,recorded_at) VALUES (?,?,?,?,?,?,?,?)`, paper, date, d.Path, published, string(warnings), string(problems), string(checks), at.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	// Bound both age and volume, including repeated failed attempts or PUT retries.
	_, err = tx.ExecContext(ctx, `DELETE FROM publish_diagnostics WHERE paper_id=? AND (recorded_at<? OR id NOT IN (SELECT id FROM publish_diagnostics WHERE paper_id=? ORDER BY id DESC LIMIT 10000))`, paper, at.AddDate(0, 0, -d.RetentionDays).Format(time.RFC3339Nano), paper)
	return err
}

// RecordPublishDiagnostics records a rejected publish attempt. Successful
// publishes use the transaction-aware methods below instead.
func (s *Store) RecordPublishDiagnostics(ctx context.Context, paper, date string, d PublishDiagnostics) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = diagnosticsTx(ctx, tx, paper, date, false, d); err != nil {
		return err
	}
	return tx.Commit()
}

// PublishWithDiagnostics replaces an edition and records its checks atomically.
func (s *Store) PublishWithDiagnostics(ctx context.Context, scope auth.PaperScope, date string, ed *edition.Edition, images map[string]Image, diagnostics PublishDiagnostics, layouts ...*EditionLayout) error {
	return s.publish(ctx, scope, date, ed, images, &diagnostics, layouts...)
}

// PublishDraftWithDiagnostics binds diagnostics to the checked draft revision.
func (s *Store) PublishDraftWithDiagnostics(ctx context.Context, scope auth.PaperScope, date string, revision int64, author bool, who string, images map[string]Image, diagnostics PublishDiagnostics, layouts ...*EditionLayout) (int64, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return 0, err
	}
	layout, err := encodeLayout(layouts)
	if err != nil {
		return 0, err
	}
	return s.finishDraft(ctx, paper, date, revision, author, false, false, false, who, images, layout, &diagnostics)
}

// ApplyDeadlinePolicyWithDiagnostics records checks with the deadline transition.
func (s *Store) ApplyDeadlinePolicyWithDiagnostics(ctx context.Context, paper, date string, revision int64, hold, unreviewed bool, images map[string]Image, diagnostics PublishDiagnostics, layouts ...*EditionLayout) error {
	layout, err := encodeLayout(layouts)
	if err != nil {
		return err
	}
	_, err = s.finishDraft(ctx, paper, date, revision, false, true, hold, unreviewed, "scheduler", images, layout, &diagnostics)
	return err
}

// qualityDiagnostics returns rule counts only. Claim values in advisory source
// warnings are stripped before aggregation so operator templates never see them.
func qualityDiagnostics(ctx context.Context, tx *sql.Tx, paper, start, end string, data *QualityData) error {
	data.WarningsByRule = map[string]int{}
	data.FailedSourceChecksByRule = map[string]int{}
	data.UnverifiedSourceChecksByRule = map[string]int{}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM draft_actions WHERE paper_id=? AND date>=? AND date<=? AND action='send_back'`, paper, start, end).Scan(&data.DraftsSentBack); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT warnings,source_checks FROM publish_diagnostics WHERE paper_id=? AND date>=? AND date<=? ORDER BY id`, paper, start, end)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var rawWarnings, rawChecks string
		if err = rows.Scan(&rawWarnings, &rawChecks); err != nil {
			return err
		}
		var warnings []DiagnosticProblem
		var checks []SourceCheckCount
		if err = json.Unmarshal([]byte(rawWarnings), &warnings); err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(rawChecks), &checks); err != nil {
			return err
		}
		for _, warning := range warnings {
			data.WarningsByRule[diagnosticRule(warning.Rule)]++
		}
		for _, check := range checks {
			switch check.Status {
			case "failed":
				data.FailedSourceChecksByRule[diagnosticRule(check.Rule)] += check.Count
			case "unverified":
				data.UnverifiedSourceChecksByRule[diagnosticRule(check.Rule)] += check.Count
			}
		}
	}
	return rows.Err()
}

func diagnosticRule(rule string) string {
	// Source checker rules append the unsupported claim after a colon. Only the
	// fixed rule name belongs in an operator report.
	name, _, _ := strings.Cut(rule, ": ")
	return name
}
