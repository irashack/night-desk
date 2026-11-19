package store

import (
	"context"
	"time"

	"github.com/irashack/night-desk/internal/auth"
)

func (s *Store) InsertSession(ctx context.Context, v auth.Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions VALUES (?,?,?,?,?,?,?)`, v.Hash[:], v.Issuer, v.Subject, v.Created.UTC().Format(time.RFC3339Nano), v.Expires.UTC().Format(time.RFC3339Nano), v.LastSeen.UTC().Format(time.RFC3339Nano), v.CSRF)
	return err
}
func (s *Store) Session(ctx context.Context, hash [32]byte) (auth.Session, error) {
	v := auth.Session{Hash: hash}
	var created, expires, seen string
	err := s.db.QueryRowContext(ctx, `SELECT issuer,subject,created_at,expires_at,last_seen_at,csrf FROM sessions WHERE id_hash=?`, hash[:]).Scan(&v.Issuer, &v.Subject, &created, &expires, &seen, &v.CSRF)
	if err != nil {
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
func (s *Store) SlideSession(ctx context.Context, hash [32]byte, now, expires time.Time) error {
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
