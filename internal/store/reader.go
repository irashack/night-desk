package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/irashack/night-desk/internal/auth"
)

func (s *Store) InsertSession(ctx context.Context, v auth.Session) error {
	groups, err := encodeGroups(v.Groups)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO sessions(id_hash,issuer,subject,created_at,expires_at,last_seen_at,csrf,groups,groups_checked,client) VALUES (?,?,?,?,?,?,?,?,?,?)`, v.Hash[:], v.Issuer, v.Subject, v.Created.UTC().Format(time.RFC3339Nano), v.Expires.UTC().Format(time.RFC3339Nano), v.LastSeen.UTC().Format(time.RFC3339Nano), v.CSRF, groups, v.GroupsChecked, nullable(v.Client)); err != nil {
		return err
	}
	// Each sign-in refreshes the groups the reader's push subscriptions carry.
	if _, err = tx.ExecContext(ctx, `UPDATE push_subscriptions SET reader_groups=? WHERE reader_issuer=? AND reader_subject=?`, groups, v.Issuer, v.Subject); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Session(ctx context.Context, hash [32]byte) (auth.Session, error) {
	v := auth.Session{Hash: hash}
	var created, expires, seen, groups string
	var client sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT issuer,subject,created_at,expires_at,last_seen_at,csrf,groups,groups_checked,client FROM sessions WHERE id_hash=?`, hash[:]).Scan(&v.Issuer, &v.Subject, &created, &expires, &seen, &v.CSRF, &groups, &v.GroupsChecked, &client)
	if err != nil {
		return v, err
	}
	v.Client = client.String
	if v.Groups, err = decodeGroups(groups); err != nil {
		return v, err
	}
	if v.Created, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return v, err
	}
	if v.Expires, err = time.Parse(time.RFC3339Nano, expires); err != nil {
		return v, err
	}
	v.LastSeen, err = time.Parse(time.RFC3339Nano, seen)
	return v, err
}
func encodeGroups(groups []string) (string, error) {
	if groups == nil {
		groups = []string{}
	}
	b, err := json.Marshal(groups)
	return string(b), err
}
func decodeGroups(v string) ([]string, error) {
	var groups []string
	if err := json.Unmarshal([]byte(v), &groups); err != nil {
		return nil, err
	}
	return groups, nil
}

// SlideWindow bounds how often a session row is rewritten: a reader's requests
// within it neither move the expiry nor last_seen_at, so browsing does not
// write to the database on every hit. The expiry may lag by up to this much.
const SlideWindow = time.Minute

func (s *Store) SlideSession(ctx context.Context, hash [32]byte, now, expires time.Time) error {
	var current, seen string
	err := s.db.QueryRowContext(ctx, `SELECT expires_at,last_seen_at FROM sessions WHERE id_hash=?`, hash[:]).Scan(&current, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.ErrUnauthorized
	}
	if err != nil {
		return err
	}
	currentExpires, err := time.Parse(time.RFC3339Nano, current)
	if err != nil {
		return err
	}
	lastSeen, err := time.Parse(time.RFC3339Nano, seen)
	if err != nil {
		return err
	}
	if !currentExpires.After(now) {
		return auth.ErrUnauthorized
	}
	// last_seen_at moves at most hourly; the expiry only when it drifts by a
	// window, so a shortened lifetime still applies on the next request.
	drift := expires.Sub(currentExpires)
	// A lifetime shorter than two windows slides at half its length instead,
	// or a reader would be signed out mid-use.
	window := min(SlideWindow, expires.Sub(now)/2)
	if drift < window && drift > -window && lastSeen.After(now.Add(-time.Hour)) {
		return nil
	}
	// julianday avoids lexicographic comparisons between fractional timestamp widths.
	result, err := s.db.ExecContext(ctx, `UPDATE sessions SET expires_at=?,last_seen_at=CASE WHEN julianday(last_seen_at)<=julianday(?) THEN ? ELSE last_seen_at END WHERE id_hash=? AND julianday(expires_at)>julianday(?)`, expires.UTC().Format(time.RFC3339Nano), now.Add(-time.Hour).UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), hash[:], now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return auth.ErrUnauthorized
	}
	return err
}
func (s *Store) DeleteSession(ctx context.Context, hash [32]byte) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash=?`, hash[:])
	return err
}
func (s *Store) Dates(ctx context.Context, scope auth.PaperScope, offset, limit int) ([]string, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT date FROM editions WHERE paper_id=? ORDER BY date DESC LIMIT ? OFFSET ?`, paper, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	dates := []string{}
	for rows.Next() {
		var date string
		if err = rows.Scan(&date); err != nil {
			return nil, err
		}
		dates = append(dates, date)
	}
	return dates, rows.Err()
}

// ArchiveDay is one archive row: an edition's date and the title of its lead
// story, empty when the stored edition names no lead that it contains.
type ArchiveDay struct {
	Date string
	Lead string
}

// ArchiveDays lists a paper's edition dates, newest first, each with its lead
// headline, in a single query.
func (s *Store) ArchiveDays(ctx context.Context, scope auth.PaperScope, offset, limit int) ([]ArchiveDay, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.date,COALESCE((SELECT json_extract(i.value,'$.title')
	 FROM json_each(e.body,'$.sections') AS s, json_each(s.value,'$.items') AS i
	 WHERE json_extract(i.value,'$.id')=json_extract(e.body,'$.lead') LIMIT 1),'')
	 FROM editions AS e WHERE e.paper_id=? ORDER BY e.date DESC LIMIT ? OFFSET ?`, paper, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	days := []ArchiveDay{}
	for rows.Next() {
		var d ArchiveDay
		if err = rows.Scan(&d.Date, &d.Lead); err != nil {
			return nil, err
		}
		days = append(days, d)
	}
	return days, rows.Err()
}
func (s *Store) EditionNumber(ctx context.Context, scope auth.PaperScope, date string) (int, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return 0, err
	}
	var n int
	err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM editions WHERE paper_id=? AND date<=?`, paper, date).Scan(&n)
	return n, err
}
func (s *Store) HasImage(ctx context.Context, scope auth.PaperScope, sha string) (bool, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return false, err
	}
	var exists bool
	err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM edition_images WHERE paper_id=? AND image_sha256=?)`, paper, sha).Scan(&exists)
	return exists, err
}

type ItemFeedback struct {
	Vote  string
	Saved bool
}

func (s *Store) ReaderFeedback(ctx context.Context, scope auth.ReaderScope, date string) (map[string]ItemFeedback, error) {
	paper, err := scopeID(scope.PaperScope())
	if err != nil {
		return nil, err
	}
	session := scope.Session()
	rows, err := s.db.QueryContext(ctx, `SELECT item_id,kind FROM feedback WHERE paper_id=? AND date=? AND reader_issuer=? AND reader_subject=? ORDER BY id`, paper, date, session.Issuer, session.Subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]ItemFeedback{}
	for rows.Next() {
		var item, kind string
		if err = rows.Scan(&item, &kind); err != nil {
			return nil, err
		}
		state := result[item]
		switch kind {
		case "up", "down":
			state.Vote = kind
		case "save":
			state.Saved = true
		case "unsave":
			state.Saved = false
		}
		result[item] = state
	}
	return result, rows.Err()
}

// PruneSessions deletes expired sessions. Their push subscriptions stay, as
// before: a reader keeps notifications until they sign out or lose the paper.
func (s *Store) PruneSessions(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE julianday(expires_at)<=julianday(?)`, now.UTC().Format(time.RFC3339Nano))
	return err
}

// CountSessions reports how many sessions are stored.
func (s *Store) CountSessions(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sessions`).Scan(&n)
	return n, err
}

// DeleteSessionsNotFrom deletes every session (and its push subscriptions)
// whose issuer the current sign-in mode does not accept, so a session from an
// earlier mode cannot come back if that mode is chosen again.
func (s *Store) DeleteSessionsNotFrom(ctx context.Context, accepts func(issuer string) bool) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT issuer FROM sessions`)
	if err != nil {
		return 0, err
	}
	var drop []string
	for rows.Next() {
		var issuer string
		if err = rows.Scan(&issuer); err != nil {
			rows.Close()
			return 0, err
		}
		if !accepts(issuer) {
			drop = append(drop, issuer)
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, issuer := range drop {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return n, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE session_hash IN (SELECT id_hash FROM sessions WHERE issuer=?)`, issuer); err != nil {
			tx.Rollback()
			return n, err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE issuer=?`, issuer)
		if err != nil {
			tx.Rollback()
			return n, err
		}
		if err = tx.Commit(); err != nil {
			return n, err
		}
		c, _ := res.RowsAffected()
		n += int(c)
	}
	return n, nil
}

// SetPushGroups records the groups a reader's push subscriptions carry, for
// proxy mode when the proxy reports new groups without a new session.
func (s *Store) SetPushGroups(ctx context.Context, issuer, subject string, groups []string) error {
	encoded, err := encodeGroups(groups)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE push_subscriptions SET reader_groups=? WHERE reader_issuer=? AND reader_subject=?`, encoded, issuer, subject)
	return err
}

// nullable stores an empty string as NULL.
func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// PreviousDate is the paper's edition before date, or "" when there is none.
func (s *Store) PreviousDate(ctx context.Context, scope auth.PaperScope, date string) (string, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return "", err
	}
	var previous string
	err = s.db.QueryRowContext(ctx, `SELECT date FROM editions WHERE paper_id=? AND date<? ORDER BY date DESC LIMIT 1`, paper, date).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return previous, err
}
