package store

import (
	"context"
	"time"
)

// Run is durable scheduler progress for one paper's local calendar date.
type Run struct {
	PaperID       string `json:"paper_id"`
	Date          string `json:"date"`
	State         string `json:"state"`
	Attempts      int    `json:"attempts"`
	DraftAttempts int    `json:"draft_attempts"`
}

const MaxRunAttempts = 3

func (s *Store) EnsureRun(ctx context.Context, paper, date string) (Run, error) {
	_, err := s.db.ExecContext(ctx, "INSERT INTO runs(paper_id,date,updated_at) VALUES (?,?,?) ON CONFLICT(paper_id,date) DO NOTHING", paper, date, now())
	if err != nil {
		return Run{}, err
	}
	var r Run
	err = s.db.QueryRowContext(ctx, "SELECT paper_id,date,state,attempts,draft_attempts FROM runs WHERE paper_id=? AND date=?", paper, date).Scan(&r.PaperID, &r.Date, &r.State, &r.Attempts, &r.DraftAttempts)
	return r, err
}

// BeginRunAttempt bounds retries across process restarts, including interrupted attempts.
func (s *Store) BeginRunAttempt(ctx context.Context, paper, date string, drafting bool) (bool, error) {
	column, state := "attempts", "running"
	if drafting {
		column, state = "draft_attempts", "drafting"
	}
	result, err := s.db.ExecContext(ctx, "UPDATE runs SET "+column+"="+column+"+1,state=?,updated_at=? WHERE paper_id=? AND date=? AND "+column+"<? AND state NOT IN ('published','held','failed')", state, now(), paper, date, MaxRunAttempts)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
func (s *Store) FinishRun(ctx context.Context, paper, date, state string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE runs SET state=?,updated_at=? WHERE paper_id=? AND date=?", state, now(), paper, date)
	return err
}

// ApplyDeadline is a trusted scheduler operation. The caller validates the stored
// revision and retains its images first; finishDraft checks that revision again.
func (s *Store) ApplyDeadline(ctx context.Context, paper, date string, revision int64, hold bool, images map[string]Image, layouts ...*EditionLayout) error {
	return s.ApplyDeadlinePolicy(ctx, paper, date, revision, hold, true, images, layouts...)
}

// ApplyDeadlinePolicy records whether a deadline publication missed an expected
// review. A current approval overrides missed-review hold/unreviewed flags in
// finishDraft's transaction; callers use unreviewed=false for mandatory holds
// such as invalid content. Held drafts require explicit publication afterwards.
func (s *Store) ApplyDeadlinePolicy(ctx context.Context, paper, date string, revision int64, hold, unreviewed bool, images map[string]Image, layouts ...*EditionLayout) error {
	layout, err := encodeLayout(layouts)
	if err != nil {
		return err
	}
	_, err = s.finishDraft(ctx, paper, date, revision, false, true, hold, unreviewed, "scheduler", images, layout)
	return err
}
func (s *Store) RunPublished(ctx context.Context, paper, date string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM editions WHERE paper_id=? AND date=?)", paper, date).Scan(&exists)
	return exists, err
}

// NoDeadlineDraft marks a run missed when its deadline passed with nothing to
// publish: no draft, or one still gathering. A worker may draft a late edition
// for the paper's current local date (ClaimLateDraftRun); the scheduler applies
// the deadline policy once that draft has a body. Earlier dates stay missed.
func (s *Store) NoDeadlineDraft(ctx context.Context, paper, date string) error {
	return s.FinishRun(ctx, paper, date, "missed")
}

// LiveDraftLease reports whether a worker holds an unexpired draft lease.
func (s *Store) LiveDraftLease(ctx context.Context, paper, date string, at time.Time) (bool, error) {
	var live bool
	err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM draft_run_leases WHERE paper_id=? AND date=? AND outcome IS NULL AND expires_at>?)", paper, date, at.Unix()).Scan(&live)
	return live, err
}
