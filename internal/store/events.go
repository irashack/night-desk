package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/webhook"
)

type eventSettings struct {
	sync.RWMutex
	config func() *config.Config
}

// SetEventConfig supplies operator webhook targets and agent roles. Configure it
// before accepting writes; a nil provider still records the polling event stream.
// Snapshots are read before acquiring a database transaction.
func (s *Store) SetEventConfig(fn func() *config.Config) {
	s.events.Lock()
	defer s.events.Unlock()
	s.events.config = fn
}
func (s *Store) eventPaper(id string) config.Paper {
	s.events.RLock()
	fn := s.events.config
	s.events.RUnlock()
	if fn != nil {
		if p, ok := fn().Paper(id); ok {
			return p
		}
	}
	return config.Paper{ID: id}
}

func enqueueEventTx(ctx context.Context, tx *sql.Tx, p config.Paper, kind string, data any) error {
	return enqueueEventAtTx(ctx, tx, p, kind, data, time.Now().UTC())
}

func enqueueEventAtTx(ctx context.Context, tx *sql.Tx, p config.Paper, kind string, data any, at time.Time) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	id := make([]byte, 16)
	if _, err = rand.Read(id); err != nil {
		return err
	}
	eventID := hex.EncodeToString(id)
	var minute any
	if kind == webhook.EventFeedbackNew {
		minute = at.Unix() / 60
		// A receiver may already be processing an in-flight attempt. Release
		// its slot before adding feedback, even if another target is pending.
		if _, err = tx.ExecContext(ctx, `UPDATE events SET coalesce_minute=NULL
		 WHERE paper=? AND type=? AND coalesce_minute=? AND EXISTS (
		 SELECT 1 FROM deliveries WHERE event_id=events.id
		 AND (attempts>0 OR delivered_at IS NOT NULL OR failed_at IS NOT NULL))`, p.ID, kind, minute); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO events(id,paper,type,data,created_at,coalesce_minute) VALUES (?,?,?,?,?,?) ON CONFLICT(paper,type,coalesce_minute) WHERE coalesce_minute IS NOT NULL DO NOTHING`, eventID, p.ID, kind, string(raw), at.Format(time.RFC3339Nano), minute)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	for _, w := range p.Webhooks {
		if !w.Accepts(kind) {
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO deliveries(event_id,webhook_url,next_attempt_at) VALUES (?,?,?)`, eventID, w.URL, at.Unix()); err != nil {
			return err
		}
	}
	return nil
}

var ErrEventCursor = errors.New("after: must be an event ID belonging to this paper")

// Events uses the private sequence for ordering, never the random public ID.
// Returning a page closes its feedback coalescing slots atomically with the
// read, so later feedback cannot be hidden behind the returned cursor.
func (s *Store) Events(ctx context.Context, scope auth.PaperScope, after string, limit int) ([]webhook.Event, string, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return nil, after, err
	}
	if limit < 1 || limit > 500 {
		return nil, after, errors.New("limit: must be 1..500")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, after, err
	}
	defer tx.Rollback()
	var seq int64
	if after != "" {
		if err = tx.QueryRowContext(ctx, `SELECT sequence FROM events WHERE paper=? AND id=?`, paper, after).Scan(&seq); errors.Is(err, sql.ErrNoRows) {
			return nil, after, ErrEventCursor
		} else if err != nil {
			return nil, after, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,paper,type,data,created_at FROM events WHERE paper=? AND sequence>? ORDER BY sequence LIMIT ?`, paper, seq, limit)
	if err != nil {
		return nil, after, err
	}
	defer rows.Close()
	out := []webhook.Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, after, err
		}
		out = append(out, e)
		after = e.ID
	}
	if err = rows.Err(); err != nil {
		return nil, after, err
	}
	if err = rows.Close(); err != nil {
		return nil, after, err
	}
	// Include the supplied cursor when a poll has no new events. It can come
	// from a webhook, or from a poll served before this behavior was added.
	if after != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE events SET coalesce_minute=NULL
		 WHERE paper=? AND coalesce_minute IS NOT NULL
		 AND sequence<=(SELECT sequence FROM events WHERE paper=? AND id=?)`, paper, paper, after); err != nil {
			return nil, after, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, after, err
	}
	return out, after, nil
}
func scanEvent(row scanner) (webhook.Event, error) {
	var e webhook.Event
	var raw, at string
	err := row.Scan(&e.ID, &e.Paper, &e.Type, &raw, &at)
	if err == nil {
		e.At, err = time.Parse(time.RFC3339Nano, at)
	}
	e.Data = json.RawMessage(raw)
	return e, err
}

type Delivery struct {
	Event    webhook.Event
	URL      string
	Attempts int
}

// ClaimDelivery reserves one due attempt before sending. Its lease survives a
// crash; interrupted attempts count toward the bound and retry after one minute.
// The delivery loop is a singleton per server, with atomic claims for safety.
func (s *Store) ClaimDelivery(ctx context.Context, at time.Time) (Delivery, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Delivery{}, err
	}
	defer tx.Rollback()
	stamp := at.UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `UPDATE deliveries SET failed_at=? WHERE attempts>=12 AND next_attempt_at<=? AND delivered_at IS NULL AND failed_at IS NULL`, stamp, at.Unix()); err != nil {
		return Delivery{}, err
	}
	var d Delivery
	var raw, created string
	err = tx.QueryRowContext(ctx, `SELECT e.id,e.paper,e.type,e.data,e.created_at,d.webhook_url,d.attempts FROM deliveries d JOIN events e ON e.id=d.event_id WHERE d.delivered_at IS NULL AND d.failed_at IS NULL AND d.attempts<12 AND d.next_attempt_at<=? ORDER BY d.next_attempt_at,e.sequence,d.webhook_url LIMIT 1`, at.Unix()).Scan(&d.Event.ID, &d.Event.Paper, &d.Event.Type, &raw, &created, &d.URL, &d.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		// Persist expired final attempts even when there is nothing left to claim.
		if err = tx.Commit(); err != nil {
			return d, err
		}
		return d, sql.ErrNoRows
	}
	if err != nil {
		return d, err
	}
	d.Event.Data = json.RawMessage(raw)
	d.Event.At, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return d, err
	}
	d.Attempts++
	_, err = tx.ExecContext(ctx, `UPDATE deliveries SET attempts=?,next_attempt_at=? WHERE event_id=? AND webhook_url=?`, d.Attempts, at.Add(time.Minute).Unix(), d.Event.ID, d.URL)
	if err != nil {
		return d, err
	}
	return d, tx.Commit()
}

func (s *Store) FinishDelivery(ctx context.Context, d Delivery, at time.Time, status int, success, retry bool, delay time.Duration) error {
	var delivered, failed any
	stamp := at.UTC().Format(time.RFC3339Nano)
	if success {
		delivered = stamp
	} else if !retry || d.Attempts >= 12 {
		failed = stamp
	}
	_, err := s.db.ExecContext(ctx, `UPDATE deliveries SET next_attempt_at=?,delivered_at=?,failed_at=?,last_status=? WHERE event_id=? AND webhook_url=? AND attempts=? AND delivered_at IS NULL AND failed_at IS NULL`, at.Add(delay).Unix(), delivered, failed, status, d.Event.ID, d.URL, d.Attempts)
	return err
}
