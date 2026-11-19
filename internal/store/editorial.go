package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/irashack/night-desk/internal/auth"
)

// KeptRevisions bounds each paper's editorial history.
const KeptRevisions = 50

// EditorialRevision is one saved state of a paper's agent-edited editorial fields.
// A nil Body means the paper follows its declared configuration again.
type EditorialRevision struct {
	Revision int64           `json:"revision"`
	Body     json.RawMessage `json:"editorial"`
	SavedAt  string          `json:"saved_at"`
}

// SaveEditorial records body (nil: back to the declaration) as the paper's next revision.
func (s *Store) SaveEditorial(ctx context.Context, scope auth.PaperScope, body json.RawMessage) (int64, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var rev int64
	if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(revision),0)+1 FROM paper_editorial WHERE paper_id=?", paper).Scan(&rev); err != nil {
		return 0, err
	}
	var value any
	if body != nil {
		value = string(body)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO paper_editorial VALUES (?,?,?,?)", paper, rev, value, now()); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM paper_editorial WHERE paper_id=? AND revision<=?", paper, rev-KeptRevisions); err != nil {
		return 0, err
	}
	return rev, tx.Commit()
}

// CurrentEditorials returns each paper's latest revision.
func (s *Store) CurrentEditorials(ctx context.Context) (map[string]EditorialRevision, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT e.paper_id,e.revision,e.body,e.saved_at FROM paper_editorial e WHERE e.revision=(SELECT max(revision) FROM paper_editorial WHERE paper_id=e.paper_id)")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]EditorialRevision{}
	for rows.Next() {
		var paper string
		r, err := scanRevision(rows, &paper)
		if err != nil {
			return nil, err
		}
		out[paper] = r
	}
	return out, rows.Err()
}

// EditorialRevisions lists a paper's revisions, newest first.
func (s *Store) EditorialRevisions(ctx context.Context, scope auth.PaperScope, limit int) ([]EditorialRevision, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT paper_id,revision,body,saved_at FROM paper_editorial WHERE paper_id=? ORDER BY revision DESC LIMIT ?", paper, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EditorialRevision{}
	for rows.Next() {
		var id string
		r, err := scanRevision(rows, &id)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EditorialRevision returns one revision; sql.ErrNoRows when it was never kept.
func (s *Store) EditorialRevision(ctx context.Context, scope auth.PaperScope, revision int64) (EditorialRevision, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return EditorialRevision{}, err
	}
	var id string
	return scanRevision(s.db.QueryRowContext(ctx, "SELECT paper_id,revision,body,saved_at FROM paper_editorial WHERE paper_id=? AND revision=?", paper, revision), &id)
}

func scanRevision(row interface{ Scan(...any) error }, paper *string) (EditorialRevision, error) {
	var r EditorialRevision
	var body sql.NullString
	if err := row.Scan(paper, &r.Revision, &body, &r.SavedAt); err != nil {
		return EditorialRevision{}, err
	}
	if body.Valid {
		r.Body = json.RawMessage(body.String)
	}
	return r, nil
}
