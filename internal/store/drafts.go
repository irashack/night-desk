package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/webhook"
)

var ErrDraftConflict = errors.New("draft revision or state conflict")

// Draft revisions include state and review changes, not just body replacements.
type Draft struct {
	Date             string           `json:"date"`
	State            string           `json:"state"`
	Revision         int64            `json:"revision"`
	Body             *edition.Edition `json:"body"`
	ApprovedRevision *int64           `json:"approved_revision,omitempty"`
	Unreviewed       bool             `json:"unreviewed"`
	UpdatedAt        string           `json:"updated_at"`
	Actions          []DraftAction    `json:"actions"`
}
type DraftAction struct {
	Revision int64  `json:"revision"`
	Who      string `json:"who"`
	Action   string `json:"action"`
	At       string `json:"at"`
	Note     string `json:"note"`
}

const draftColumns = "date,state,revision,body,approved_revision,unreviewed,updated_at"

type scanner interface{ Scan(...any) error }

func scanDraft(row scanner) (Draft, error) {
	var d Draft
	var body sql.NullString
	err := row.Scan(&d.Date, &d.State, &d.Revision, &body, &d.ApprovedRevision, &d.Unreviewed, &d.UpdatedAt)
	if err == nil && body.Valid {
		d.Body, err = edition.Decode(strings.NewReader(body.String))
	}
	d.Actions = []DraftAction{}
	return d, err
}
func (s *Store) Draft(ctx context.Context, scope auth.PaperScope, date string) (Draft, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return Draft{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Draft{}, err
	}
	defer tx.Rollback()
	d, err := scanDraft(tx.QueryRowContext(ctx, "SELECT "+draftColumns+" FROM drafts WHERE paper_id=? AND date=?", paper, date))
	if err != nil {
		return d, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT revision,who,action,at,note FROM draft_actions WHERE paper_id=? AND date=? ORDER BY revision", paper, date)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	for rows.Next() {
		var a DraftAction
		if err = rows.Scan(&a.Revision, &a.Who, &a.Action, &a.At, &a.Note); err != nil {
			return d, err
		}
		d.Actions = append(d.Actions, a)
	}
	return d, rows.Err()
}

// RunDraft is for the trusted scheduler, whose paper IDs come from operator config.
func (s *Store) RunDraft(ctx context.Context, paper, date string) (Draft, error) {
	return scanDraft(s.db.QueryRowContext(ctx, "SELECT "+draftColumns+" FROM drafts WHERE paper_id=? AND date=?", paper, date))
}
func (s *Store) Drafts(ctx context.Context, scope auth.PaperScope, state string) ([]Draft, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+draftColumns+" FROM drafts WHERE paper_id=? AND (?='' OR state=?) ORDER BY date DESC", paper, state, state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Draft{}
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func draftActionTx(ctx context.Context, tx *sql.Tx, paper string, d Draft, who, action, note string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO draft_actions(paper_id,date,revision,who,action,at,note) VALUES (?,?,?,?,?,?,?)", paper, d.Date, d.Revision, who, action, now(), note)
	return err
}

// StartDraft records gathering before a worker has produced an edition.
func (s *Store) StartDraft(ctx context.Context, scope auth.PaperScope, date, who string) error {
	paper, err := scopeID(scope)
	if err != nil {
		return err
	}
	if _, err = edition.ParseDate(date); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, "INSERT INTO drafts(paper_id,date,state,revision,updated_at) VALUES (?,?,'gathering',1,?)", paper, date, now())
	if err != nil {
		return err
	}
	if err = draftActionTx(ctx, tx, paper, Draft{Date: date, Revision: 1}, who, "gather", ""); err != nil {
		return err
	}
	return tx.Commit()
}

// SubmitDraft replaces an unpublished draft and clears any previous approval.
func (s *Store) SubmitDraft(ctx context.Context, scope auth.PaperScope, date, who string, body *edition.Edition) (int64, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return 0, err
	}
	if _, err = edition.ParseDate(date); err != nil {
		return 0, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	eventPaper := s.eventPaper(paper)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var rev int64
	err = tx.QueryRowContext(ctx, `INSERT INTO drafts(paper_id,date,state,revision,body,updated_at) VALUES (?,?,'drafted',1,?,?)
 ON CONFLICT(paper_id,date) DO UPDATE SET state='drafted',revision=drafts.revision+1,body=excluded.body,approved_revision=NULL,updated_at=excluded.updated_at
 WHERE drafts.state NOT IN ('published','held') RETURNING revision`, paper, date, string(raw), now()).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrDraftConflict
	}
	if err != nil {
		return 0, err
	}
	if err = draftActionTx(ctx, tx, paper, Draft{Date: date, Revision: rev}, who, "submit", ""); err != nil {
		return 0, err
	}
	if err = enqueueEventTx(ctx, tx, eventPaper, webhook.EventDraftReady, map[string]any{"date": date, "revision": rev}); err != nil {
		return 0, err
	}
	if eventPaper.Role() == "reviewer" {
		if err = enqueueEventTx(ctx, tx, eventPaper, webhook.EventReviewRequested, map[string]any{"date": date, "revision": rev}); err != nil {
			return 0, err
		}
	}
	return rev, tx.Commit()
}

// ReviewDraft applies a validated review result only to the revision it read.
// Body is the validated result for cut/swap/edit; other actions leave it unchanged.
func (s *Store) ReviewDraft(ctx context.Context, scope auth.PaperScope, date string, revision int64, who, action, note string, body *edition.Edition) (int64, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return 0, err
	}
	if action != "approve" && action != "cut" && action != "swap" && action != "edit" && action != "note" && action != "send_back" {
		return 0, ErrDraftConflict
	}
	eventPaper := s.eventPaper(paper)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	d, err := scanDraft(tx.QueryRowContext(ctx, "SELECT "+draftColumns+" FROM drafts WHERE paper_id=? AND date=?", paper, date))
	if err != nil {
		return 0, err
	}
	if d.Revision != revision || d.Body == nil || (d.State != "drafted" && d.State != "in_review" && d.State != "held") {
		return 0, ErrDraftConflict
	}
	held := d.State == "held"
	enteredReview := d.State == "drafted"
	d.Revision++
	// Review remains available after a hold, without reopening auto-publication.
	if !held {
		d.State = "in_review"
	}
	d.ApprovedRevision = nil
	if action == "approve" {
		rev := d.Revision
		d.ApprovedRevision = &rev
	}
	if action == "send_back" && !held {
		d.State = "drafted"
	}
	if action == "cut" || action == "swap" || action == "edit" {
		if body == nil {
			return 0, ErrDraftConflict
		}
		d.Body = body
	}
	raw, err := json.Marshal(d.Body)
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE drafts SET state=?,revision=?,body=?,approved_revision=?,updated_at=? WHERE paper_id=? AND date=?", d.State, d.Revision, string(raw), d.ApprovedRevision, now(), paper, date)
	if err != nil {
		return 0, err
	}
	if err = draftActionTx(ctx, tx, paper, d, who, action, note); err != nil {
		return 0, err
	}
	if enteredReview && d.State == "in_review" {
		if err = enqueueEventTx(ctx, tx, eventPaper, webhook.EventReviewRequested, map[string]any{"date": date, "revision": d.Revision}); err != nil {
			return 0, err
		}
	}
	return d.Revision, tx.Commit()
}

// PublishDraft never replaces an existing edition. Images must be fetched before
// this call; the body is read inside the transaction, never supplied by the caller.
func (s *Store) PublishDraft(ctx context.Context, scope auth.PaperScope, date string, revision int64, author bool, who string, images map[string]Image, layouts ...*EditionLayout) (int64, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return 0, err
	}
	layout, err := encodeLayout(layouts)
	if err != nil {
		return 0, err
	}
	return s.finishDraft(ctx, paper, date, revision, author, false, false, false, who, images, layout)
}
func (s *Store) finishDraft(ctx context.Context, paper, date string, revision int64, author, deadline, hold, unreviewed bool, who string, images map[string]Image, layout *EditionLayout, diagnostics ...*PublishDiagnostics) (int64, error) {
	eventPaper := s.eventPaper(paper)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	d, err := scanDraft(tx.QueryRowContext(ctx, "SELECT "+draftColumns+" FROM drafts WHERE paper_id=? AND date=?", paper, date))
	if err != nil {
		return 0, err
	}
	if d.Revision != revision || d.State == "published" || (deadline && d.State == "held") {
		return 0, ErrDraftConflict
	}
	approved := d.ApprovedRevision != nil && *d.ApprovedRevision == revision
	// A revision-bound approval satisfies the expected review. Explicit holds
	// for missing or invalid content use unreviewed=false and still take effect.
	if deadline && unreviewed && approved {
		hold, unreviewed = false, false
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM editions WHERE paper_id=? AND date=?)", paper, date).Scan(&exists); err != nil {
		return 0, err
	}
	if exists {
		return 0, ErrDraftConflict
	}
	diagnostic := PublishDiagnostics{Path: "draft", RetentionDays: eventPaper.RetentionDays}
	if deadline {
		diagnostic.Path = "deadline"
	}
	if len(diagnostics) > 0 && diagnostics[0] != nil {
		diagnostic = *diagnostics[0]
	}
	if hold {
		if err = diagnosticsTx(ctx, tx, paper, date, false, diagnostic); err != nil {
			return 0, err
		}
	}
	if !hold {
		if d.Body == nil || (!deadline && !author && !approved) {
			return 0, ErrDraftConflict
		}
		for i := range d.Body.Sections {
			for j := range d.Body.Sections[i].Items {
				d.Body.Sections[i].Items[j].ImageURL = nil
			}
		}
		if err = publishTx(ctx, tx, eventPaper, date, d.Body, images, layout, &diagnostic); err != nil {
			return 0, err
		}
		d.Unreviewed = deadline && unreviewed
		if _, err = tx.ExecContext(ctx, "INSERT INTO edition_review(paper_id,date,unreviewed) VALUES (?,?,?)", paper, date, d.Unreviewed); err != nil {
			return 0, err
		}
	}
	d.Revision++
	d.State = "published"
	action := "publish"
	if hold {
		d.State = "held"
		action = "hold"
	}
	if deadline && unreviewed && !hold {
		action = "publish-unreviewed"
	}
	if _, err = tx.ExecContext(ctx, "UPDATE drafts SET state=?,revision=?,unreviewed=?,approved_revision=NULL,updated_at=? WHERE paper_id=? AND date=?", d.State, d.Revision, d.Unreviewed, now(), paper, date); err != nil {
		return 0, err
	}
	if err = draftActionTx(ctx, tx, paper, d, who, action, ""); err != nil {
		return 0, err
	}
	if deadline || !hold {
		if _, err = tx.ExecContext(ctx, "UPDATE runs SET state=?,updated_at=? WHERE paper_id=? AND date=?", d.State, now(), paper, date); err != nil {
			return 0, err
		}
	}
	return d.Revision, tx.Commit()
}
func (s *Store) EditionUnreviewed(ctx context.Context, scope auth.PaperScope, date string) (bool, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return false, err
	}
	var value bool
	err = s.db.QueryRowContext(ctx, "SELECT unreviewed FROM edition_review WHERE paper_id=? AND date=?", paper, date).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return value, err
}
