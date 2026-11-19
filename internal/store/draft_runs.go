package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/webhook"
)

var ErrRunConflict = errors.New("draft run is leased, exhausted or complete")

const DraftLeaseDuration = 10 * time.Minute

type DraftBudget struct {
	MaxCalls  int `json:"max_calls" minimum:"1"`
	MaxTokens int `json:"max_tokens" minimum:"1"`
}
type DraftSpend struct {
	Calls  int `json:"calls" minimum:"0"`
	Tokens int `json:"tokens" minimum:"0"`
	// CostMicros is the worker's estimate in millionths of a US dollar from its
	// configured tier prices; zero when it has none.
	CostMicros int64 `json:"cost_micros,omitempty" minimum:"0"`
}
type DraftLease struct {
	ID        string      `json:"lease_id"`
	ExpiresAt time.Time   `json:"expires_at"`
	Remaining DraftBudget `json:"remaining"`
}

// ClaimDraftRun reserves the daily remainder before any model call so the
// exclusive lease can fund a complete pipeline attempt.
// An interrupted attempt forfeits its reservation; a report refunds unused spend.
// The first claim fixes the daily cap: later workers can lower, never raise it.
// A missed run (its deadline passed with nothing to publish) is never claimed;
// see ClaimLateDraftRun.
func (s *Store) ClaimDraftRun(ctx context.Context, scope auth.PaperScope, date string, limits DraftBudget, at time.Time) (DraftLease, error) {
	return s.claimDraftRun(ctx, scope, date, limits, at, false)
}

// ClaimLateDraftRun claims only a missed run, for a late edition after the
// paper's deadline on its current local date. The caller checks the date.
func (s *Store) ClaimLateDraftRun(ctx context.Context, scope auth.PaperScope, date string, limits DraftBudget, at time.Time) (DraftLease, error) {
	return s.claimDraftRun(ctx, scope, date, limits, at, true)
}

func (s *Store) claimDraftRun(ctx context.Context, scope auth.PaperScope, date string, limits DraftBudget, at time.Time, late bool) (DraftLease, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return DraftLease{}, err
	}
	if _, err = edition.ParseDate(date); err != nil {
		return DraftLease{}, err
	}
	if limits.MaxCalls < 1 || limits.MaxTokens < 1 {
		return DraftLease{}, ErrRunConflict
	}
	var random [32]byte
	if _, err = rand.Read(random[:]); err != nil {
		return DraftLease{}, err
	}
	lease := DraftLease{ID: hex.EncodeToString(random[:]), ExpiresAt: at.Add(DraftLeaseDuration)}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DraftLease{}, err
	}
	defer tx.Rollback()
	// Starting with a write serializes claims even across server processes.
	_, err = tx.ExecContext(ctx, `INSERT INTO runs(paper_id,date,updated_at) VALUES (?,?,?) ON CONFLICT(paper_id,date) DO UPDATE SET updated_at=excluded.updated_at`, paper, date, now())
	if err != nil {
		return DraftLease{}, err
	}
	var state string
	var attempts, maxCalls, maxTokens, calls, tokens, live int
	err = tx.QueryRowContext(ctx, `SELECT state,draft_attempts,draft_max_calls,draft_max_tokens,draft_calls,draft_tokens,
 (SELECT count(*) FROM draft_run_leases WHERE paper_id=? AND date=? AND outcome IS NULL AND expires_at>?)
 FROM runs WHERE paper_id=? AND date=?`, paper, date, at.Unix(), paper, date).Scan(&state, &attempts, &maxCalls, &maxTokens, &calls, &tokens, &live)
	if err != nil {
		return DraftLease{}, err
	}
	if live > 0 || attempts >= MaxRunAttempts || state == "published" || state == "held" || state == "failed" || (state == "missed") != late {
		return DraftLease{}, ErrRunConflict
	}
	var published bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM editions WHERE paper_id=? AND date=?)", paper, date).Scan(&published); err != nil {
		return DraftLease{}, err
	}
	if published {
		return DraftLease{}, ErrRunConflict
	}
	if maxCalls == 0 {
		maxCalls = limits.MaxCalls
	}
	if maxTokens == 0 {
		maxTokens = limits.MaxTokens
	}
	maxCalls = min(maxCalls, limits.MaxCalls)
	maxTokens = min(maxTokens, limits.MaxTokens)
	if calls >= maxCalls || tokens >= maxTokens {
		return DraftLease{}, ErrRunConflict
	}
	lease.Remaining = DraftBudget{MaxCalls: maxCalls - calls, MaxTokens: maxTokens - tokens}
	_, err = tx.ExecContext(ctx, `UPDATE runs SET draft_attempts=draft_attempts+1,state='drafting',draft_max_calls=?,draft_max_tokens=?,draft_calls=draft_calls+?,draft_tokens=draft_tokens+? WHERE paper_id=? AND date=?`, maxCalls, maxTokens, lease.Remaining.MaxCalls, lease.Remaining.MaxTokens, paper, date)
	if err != nil {
		return DraftLease{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO draft_run_leases(id,paper_id,date,expires_at,reserved_calls,reserved_tokens) VALUES (?,?,?,?,?,?)`, lease.ID, paper, date, lease.ExpiresAt.Unix(), lease.Remaining.MaxCalls, lease.Remaining.MaxTokens)
	if err != nil {
		return DraftLease{}, err
	}
	return lease, tx.Commit()
}

// ReportDraftRun settles spending and, on success, submits the edition in the
// same transaction, fenced by both the lease and expected draft revision (0 for
// no draft). A delayed worker cannot replace an editor's work or approval.
// Repeating a settled report returns the original revision without refunding twice.
func (s *Store) ReportDraftRun(ctx context.Context, scope auth.PaperScope, date, leaseID, who, outcome string, spend DraftSpend, expected int64, body *edition.Edition, at time.Time) (int64, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return 0, err
	}
	if spend.Calls < 0 || spend.Tokens < 0 || spend.CostMicros < 0 || (outcome != "failed" && outcome != "submitted") || (outcome == "submitted") != (body != nil) {
		return 0, ErrRunConflict
	}
	eventPaper := s.eventPaper(paper)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// Acquire the writer lock before checking the lease.
	_, err = tx.ExecContext(ctx, "UPDATE runs SET updated_at=? WHERE paper_id=? AND date=?", now(), paper, date)
	if err != nil {
		return 0, err
	}
	var expires int64
	var reservedCalls, reservedTokens int
	var oldOutcome sql.NullString
	var oldCalls, oldTokens sql.NullInt64
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT expires_at,reserved_calls,reserved_tokens,outcome,calls,tokens,revision FROM draft_run_leases WHERE id=? AND paper_id=? AND date=?`, leaseID, paper, date).Scan(&expires, &reservedCalls, &reservedTokens, &oldOutcome, &oldCalls, &oldTokens, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrRunConflict
	}
	if err != nil {
		return 0, err
	}
	if oldOutcome.Valid {
		if oldOutcome.String == outcome && oldCalls.Int64 == int64(spend.Calls) && oldTokens.Int64 == int64(spend.Tokens) {
			return revision, nil
		}
		return 0, ErrRunConflict
	}
	if at.Unix() >= expires {
		return 0, ErrRunConflict
	}
	if body != nil {
		var runState string
		if err = tx.QueryRowContext(ctx, "SELECT state FROM runs WHERE paper_id=? AND date=?", paper, date).Scan(&runState); err != nil {
			return 0, err
		}
		if runState == "published" || runState == "held" || runState == "failed" {
			return 0, ErrRunConflict
		}
		var state string
		var approved sql.NullInt64
		err = tx.QueryRowContext(ctx, "SELECT revision,state,approved_revision FROM drafts WHERE paper_id=? AND date=?", paper, date).Scan(&revision, &state, &approved)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		if revision != expected || state == "published" || state == "held" || approved.Valid {
			return 0, ErrDraftConflict
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		revision++
		_, err = tx.ExecContext(ctx, `INSERT INTO drafts(paper_id,date,state,revision,body,updated_at) VALUES (?,?,'drafted',?,?,?)
  ON CONFLICT(paper_id,date) DO UPDATE SET state='drafted',revision=excluded.revision,body=excluded.body,approved_revision=NULL,updated_at=excluded.updated_at`, paper, date, revision, string(raw), now())
		if err != nil {
			return 0, err
		}
		if err = draftActionTx(ctx, tx, paper, Draft{Date: date, Revision: revision}, who, "submit", ""); err != nil {
			return 0, err
		}
		if err = enqueueEventTx(ctx, tx, eventPaper, webhook.EventDraftReady, map[string]any{"date": date, "revision": revision}); err != nil {
			return 0, err
		}
		if eventPaper.Role() == "reviewer" {
			if err = enqueueEventTx(ctx, tx, eventPaper, webhook.EventReviewRequested, map[string]any{"date": date, "revision": revision}); err != nil {
				return 0, err
			}
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE runs SET draft_calls=draft_calls-?+?,draft_tokens=draft_tokens-?+?,draft_cost_micros=draft_cost_micros+?,updated_at=? WHERE paper_id=? AND date=?`, reservedCalls, spend.Calls, reservedTokens, spend.Tokens, spend.CostMicros, now(), paper, date)
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE draft_run_leases SET outcome=?,calls=?,tokens=?,cost_micros=?,revision=? WHERE id=?`, outcome, spend.Calls, spend.Tokens, spend.CostMicros, revision, leaseID)
	if err != nil {
		return 0, err
	}
	return revision, tx.Commit()
}
