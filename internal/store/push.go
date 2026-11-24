package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/irashack/night-desk/internal/auth"
)

// PushDeviceLimit caps the devices one reader may register for one paper.
const PushDeviceLimit = 10

// PushAttempts bounds sends per subscription and edition.
const PushAttempts = 4

var ErrPushLimit = errors.New("too many push subscriptions")

// AddPushSubscription records (or renews) one device for the session's reader
// and paper. A device already registered moves to the current session.
func (s *Store) AddPushSubscription(ctx context.Context, scope auth.ReaderScope, endpoint string, expires *time.Time) error {
	paper, err := scopeID(scope.PaperScope())
	if err != nil {
		return err
	}
	session := scope.Session()
	var expiry any
	if expires != nil {
		expiry = expires.UTC().Format(time.RFC3339Nano)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var others int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM push_subscriptions WHERE paper_id=? AND reader_issuer=? AND reader_subject=? AND endpoint<>?`, paper, session.Issuer, session.Subject, endpoint).Scan(&others); err != nil {
		return err
	}
	if others >= PushDeviceLimit {
		return ErrPushLimit
	}
	groups, err := encodeGroups(session.Groups)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO push_subscriptions(paper_id,reader_issuer,reader_subject,session_hash,endpoint,expires_at,created_at,reader_groups) VALUES (?,?,?,?,?,?,?,?)
	 ON CONFLICT(paper_id,reader_issuer,reader_subject,endpoint) DO UPDATE SET session_hash=excluded.session_hash,expires_at=excluded.expires_at,reader_groups=excluded.reader_groups`, paper, session.Issuer, session.Subject, session.Hash[:], endpoint, expiry, now(), groups); err != nil {
		return err
	}
	return tx.Commit()
}

// RemovePushSubscription removes one of the session reader's own devices from
// the scope's paper and reports how many of the reader's other papers the
// device still notifies for.
func (s *Store) RemovePushSubscription(ctx context.Context, scope auth.ReaderScope, endpoint string) (int, error) {
	paper, err := scopeID(scope.PaperScope())
	if err != nil {
		return 0, err
	}
	session := scope.Session()
	if _, err = s.db.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE paper_id=? AND reader_issuer=? AND reader_subject=? AND endpoint=?`, paper, session.Issuer, session.Subject, endpoint); err != nil {
		return 0, err
	}
	var remaining int
	err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM push_subscriptions WHERE reader_issuer=? AND reader_subject=? AND endpoint=?`, session.Issuer, session.Subject, endpoint).Scan(&remaining)
	return remaining, err
}

// HasPushSubscription reports whether the device notifies the session reader
// of the scope's paper.
func (s *Store) HasPushSubscription(ctx context.Context, scope auth.ReaderScope, endpoint string) (bool, error) {
	paper, err := scopeID(scope.PaperScope())
	if err != nil {
		return false, err
	}
	session := scope.Session()
	var on bool
	err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM push_subscriptions WHERE paper_id=? AND reader_issuer=? AND reader_subject=? AND endpoint=?)`, paper, session.Issuer, session.Subject, endpoint).Scan(&on)
	return on, err
}

// PushSubscriptions counts the session reader's registered devices.
func (s *Store) PushSubscriptions(ctx context.Context, scope auth.ReaderScope) (int, error) {
	paper, err := scopeID(scope.PaperScope())
	if err != nil {
		return 0, err
	}
	session := scope.Session()
	var n int
	err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM push_subscriptions WHERE paper_id=? AND reader_issuer=? AND reader_subject=?`, paper, session.Issuer, session.Subject).Scan(&n)
	return n, err
}

// SignOut ends a session together with the push subscriptions it registered.
func (s *Store) SignOut(ctx context.Context, hash [32]byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE session_hash=?`, hash[:]); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash=?`, hash[:]); err != nil {
		return err
	}
	return tx.Commit()
}

// enqueuePushTx queues one notification per subscription the first time a date
// is published, and only when it is the paper's newest edition: a replaced
// edition or a backfilled earlier date never notifies.
func enqueuePushTx(ctx context.Context, tx *sql.Tx, paper, date string, at time.Time) error {
	result, err := tx.ExecContext(ctx, `INSERT INTO push_editions(paper_id,date,created_at) VALUES (?,?,?) ON CONFLICT DO NOTHING`, paper, date, at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n == 0 {
		return err
	}
	var later bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM editions WHERE paper_id=? AND date>?)`, paper, date).Scan(&later); err != nil || later {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO push_deliveries(subscription_id,date,next_attempt_at) SELECT id,?,? FROM push_subscriptions WHERE paper_id=?`, date, at.Unix(), paper)
	return err
}

// PushDelivery is one claimed notification. The endpoint and reader identity
// are for sending and authorization only, never for logs.
type PushDelivery struct {
	Subscription    int64
	Paper, Date     string
	Endpoint        string
	Issuer, Subject string
	// Groups is the reader's groups claim as of their latest sign-in.
	Groups   []string
	Attempts int
}

// ClaimPush reserves one due notification before sending, like ClaimDelivery:
// the lease survives a crash and an interrupted attempt counts toward the bound.
// Expired subscriptions and exhausted deliveries are removed first.
func (s *Store) ClaimPush(ctx context.Context, at time.Time) (PushDelivery, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PushDelivery{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE expires_at IS NOT NULL AND julianday(expires_at)<=julianday(?)`, at.UTC().Format(time.RFC3339Nano)); err != nil {
		return PushDelivery{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM push_deliveries WHERE attempts>=? AND next_attempt_at<=?`, PushAttempts, at.Unix()); err != nil {
		return PushDelivery{}, err
	}
	var d PushDelivery
	var groups string
	err = tx.QueryRowContext(ctx, `SELECT d.subscription_id,s.paper_id,d.date,s.endpoint,s.reader_issuer,s.reader_subject,s.reader_groups,d.attempts FROM push_deliveries d JOIN push_subscriptions s ON s.id=d.subscription_id WHERE d.next_attempt_at<=? ORDER BY d.next_attempt_at,d.subscription_id LIMIT 1`, at.Unix()).Scan(&d.Subscription, &d.Paper, &d.Date, &d.Endpoint, &d.Issuer, &d.Subject, &groups, &d.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		if err = tx.Commit(); err != nil {
			return d, err
		}
		return d, sql.ErrNoRows
	}
	if err != nil {
		return d, err
	}
	if d.Groups, err = decodeGroups(groups); err != nil {
		return d, err
	}
	d.Attempts++
	if _, err = tx.ExecContext(ctx, `UPDATE push_deliveries SET attempts=?,next_attempt_at=? WHERE subscription_id=? AND date=?`, d.Attempts, at.Add(time.Minute).Unix(), d.Subscription, d.Date); err != nil {
		return d, err
	}
	return d, tx.Commit()
}

// FinishPush settles a claimed notification. Gone removes the subscription (the
// push service no longer knows it); otherwise a zero retry time removes the
// delivery and a later one schedules another attempt within PushAttempts.
func (s *Store) FinishPush(ctx context.Context, d PushDelivery, gone bool, retry time.Time) error {
	var err error
	switch {
	case gone:
		_, err = s.db.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE id=?`, d.Subscription)
	case retry.IsZero() || d.Attempts >= PushAttempts:
		_, err = s.db.ExecContext(ctx, `DELETE FROM push_deliveries WHERE subscription_id=? AND date=? AND attempts=?`, d.Subscription, d.Date, d.Attempts)
	default:
		_, err = s.db.ExecContext(ctx, `UPDATE push_deliveries SET next_attempt_at=? WHERE subscription_id=? AND date=? AND attempts=?`, retry.Unix(), d.Subscription, d.Date, d.Attempts)
	}
	return err
}
