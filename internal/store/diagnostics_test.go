package store_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/store"
)

func diagnosticsDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "nightdesk.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestPublishDiagnosticsAtomicAndRetained(t *testing.T) {
	s, dir := open(t)
	a := scope(t, s, "daily")
	other := scope(t, s, "other")
	db := diagnosticsDB(t, dir)
	day := time.Now().UTC().Format("2006-01-02")
	d := store.PublishDiagnostics{Path: "put", RetentionDays: 2, Warnings: []store.DiagnosticProblem{{Path: "sections[0].items[0].title", Rule: "unsupported-entity: PRIVATE TITLE"}}, SourceChecks: []store.SourceCheckCount{{Status: "failed", Rule: "unsupported-entity", Count: 1}, {Status: "unverified", Rule: "no source text", Count: 2}}}
	if err := s.PublishWithDiagnostics(ctx, a, day, qualityTestBodyV04(1, 0), nil, d); err != nil {
		t.Fatal(err)
	}
	var path, rawWarnings, rawErrors, checks string
	var published int
	if err := db.QueryRow(`SELECT path,published,warnings,errors,source_checks FROM publish_diagnostics WHERE paper_id='daily'`).Scan(&path, &published, &rawWarnings, &rawErrors, &checks); err != nil {
		t.Fatal(err)
	}
	if path != "put" || published != 1 || !strings.Contains(rawWarnings, `"path":"sections[0].items[0].title"`) || !strings.Contains(rawWarnings, "PRIVATE TITLE") || rawErrors != "[]" || !strings.Contains(checks, `"count":2`) {
		t.Fatal(path, published, rawWarnings, rawErrors, checks)
	}
	data, err := s.Quality(ctx, "daily", day, day, nil)
	if err != nil || data.WarningsByRule["unsupported-entity"] != 1 || data.FailedSourceChecksByRule["unsupported-entity"] != 1 || data.UnverifiedSourceChecksByRule["no source text"] != 2 {
		t.Fatal(data, err)
	}
	for rule := range data.WarningsByRule {
		if strings.Contains(rule, "PRIVATE") {
			t.Fatal("claim in aggregate", rule)
		}
	}
	// A failing diagnostics write must roll back the edition replacement too.
	if _, err = db.Exec(`CREATE TRIGGER reject_diagnostics BEFORE INSERT ON publish_diagnostics BEGIN SELECT RAISE(ABORT,'diagnostics failed'); END`); err != nil {
		t.Fatal(err)
	}
	replacement := qualityTestBodyV04(1, 0)
	replacement.Sections[0].Items[0].Title = "replacement"
	if err = s.PublishWithDiagnostics(ctx, a, day, replacement, nil, d); err == nil {
		t.Fatal("diagnostics failure ignored")
	}
	ed, err := s.Edition(ctx, a, day)
	if err != nil || ed.Sections[0].Items[0].Title == "replacement" {
		t.Fatal(ed, err)
	}
	if _, err = db.Exec(`DROP TRIGGER reject_diagnostics`); err != nil {
		t.Fatal(err)
	}
	if err = s.Publish(ctx, other, day, qualityTestBodyV04(1, 0), nil); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE publish_diagnostics SET recorded_at='2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	d.Errors = []store.DiagnosticProblem{{Path: "lead", Rule: "must name an item"}}
	if err = s.RecordPublishDiagnostics(ctx, "daily", day, d); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM publish_diagnostics WHERE paper_id='daily'`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM publish_diagnostics WHERE paper_id='other'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("pruned another paper", count, err)
	}
	if err = db.QueryRow(`SELECT published,errors FROM publish_diagnostics WHERE paper_id='daily'`).Scan(&published, &rawErrors); err != nil || published != 0 || !strings.Contains(rawErrors, `"path":"lead"`) {
		t.Fatal(published, rawErrors, err)
	}
	if _, err = s.Edition(ctx, a, day); err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosticsVolumeAndDraftConflict(t *testing.T) {
	s, dir := open(t)
	a := scope(t, s, "daily")
	db := diagnosticsDB(t, dir)
	day := time.Now().UTC().Format("2006-01-02")
	_, err := db.Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<10000)
 INSERT INTO publish_diagnostics(paper_id,date,path,published,warnings,errors,source_checks,recorded_at)
 SELECT 'daily',?,'put',0,'[]','[]','[]',? FROM n`, day, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	d := store.PublishDiagnostics{Path: "draft"}
	rev, err := s.SubmitDraft(ctx, a, day, "writer", qualityTestBodyV04(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishDraftWithDiagnostics(ctx, a, day, rev+1, true, "writer", nil, d); !errors.Is(err, store.ErrDraftConflict) {
		t.Fatal(err)
	}
	bad := d
	bad.Path = "invalid"
	if _, err = s.PublishDraftWithDiagnostics(ctx, a, day, rev, true, "writer", nil, bad); err == nil {
		t.Fatal("bad diagnostics accepted")
	}
	draft, err := s.Draft(ctx, a, day)
	if err != nil || draft.State == "published" || draft.Revision != rev {
		t.Fatal(draft, err)
	}
	var before int
	if err = db.QueryRow(`SELECT count(*) FROM publish_diagnostics`).Scan(&before); err != nil || before != 10000 {
		t.Fatal(before, err)
	}
	if _, err = s.PublishDraftWithDiagnostics(ctx, a, day, rev, true, "writer", nil, d); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM publish_diagnostics WHERE paper_id='daily'`).Scan(&count); err != nil || count != 10000 {
		t.Fatal(count, err)
	}
	var path string
	if err = db.QueryRow(`SELECT path FROM publish_diagnostics ORDER BY id DESC LIMIT 1`).Scan(&path); err != nil || path != "draft" {
		t.Fatal(path, err)
	}
}

func TestQualityCountsSendBackActions(t *testing.T) {
	s, _ := open(t)
	a := scope(t, s, "daily")
	rev, err := s.SubmitDraft(ctx, a, "2026-10-30", "writer", qualityTestBodyV04(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReviewDraft(ctx, a, "2026-10-30", rev, "reviewer", "send_back", "PRIVATE NOTE", nil); err != nil {
		t.Fatal(err)
	}
	data, err := s.Quality(ctx, "daily", "2026-10-30", "2026-10-30", nil)
	if err != nil || data.DraftsSentBack != 1 {
		t.Fatal(data, err)
	}
	data, err = s.Quality(ctx, "other", "2026-10-30", "2026-10-30", nil)
	if err != nil || data.DraftsSentBack != 0 {
		t.Fatal(data, err)
	}
}

func TestPublishedChecks(t *testing.T) {
	s, dir := open(t)
	a := scope(t, s, "daily")
	db := diagnosticsDB(t, dir)
	day := time.Now().UTC().Format("2006-01-02")
	// A publish that ran no source checks gives no record to show.
	if err := s.Publish(ctx, a, day, qualityTestBodyV04(1, 0), nil); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := s.PublishedChecks(ctx, a, day, 14); ok || err != nil {
		t.Fatal("record without source checks", ok, err)
	}
	d := store.PublishDiagnostics{Path: "put", Warnings: []store.DiagnosticProblem{{Path: "sections[0].items[0].title", Rule: "unsupported-name: Smith"}}, SourceChecks: []store.SourceCheckCount{{Status: "failed", Rule: "unsupported-name", Count: 1}}}
	if err := s.PublishWithDiagnostics(ctx, a, day, qualityTestBodyV04(1, 0), nil, d); err != nil {
		t.Fatal(err)
	}
	// A rejected attempt afterwards does not replace the published record.
	if err := s.RecordPublishDiagnostics(ctx, "daily", day, store.PublishDiagnostics{Path: "put", Errors: []store.DiagnosticProblem{{Path: "lead", Rule: "x"}}, SourceChecks: d.SourceChecks}); err != nil {
		t.Fatal(err)
	}
	warnings, at, ok, err := s.PublishedChecks(ctx, a, day, 14)
	if err != nil || !ok || len(warnings) != 1 || warnings[0].Rule != "unsupported-name: Smith" || time.Since(at) > time.Minute {
		t.Fatal(warnings, at, ok, err)
	}
	// A record older than the paper's retention is hidden before pruning runs.
	if _, err = db.Exec(`UPDATE publish_diagnostics SET recorded_at=?`, time.Now().UTC().AddDate(0, 0, -3).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err = s.PublishedChecks(ctx, a, day, 2); ok || err != nil {
		t.Fatal("expired record shown", ok, err)
	}
	if _, _, ok, err = s.PublishedChecks(ctx, a, day, 14); !ok || err != nil {
		t.Fatal("record within retention hidden", ok, err)
	}
}
