package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"

	"github.com/irashack/night-desk/internal/auth"
)

type ReaderNotes struct {
	Revision int64  `json:"revision"`
	Text     string `json:"text"`
	SavedAt  string `json:"saved_at,omitempty"`
}

func (s *Store) ReaderNotes(ctx context.Context, scope auth.PaperScope) (ReaderNotes, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return ReaderNotes{}, err
	}
	var n ReaderNotes
	err = s.db.QueryRowContext(ctx, "SELECT revision,text,saved_at FROM paper_notes WHERE paper_id=? ORDER BY revision DESC LIMIT 1", paper).Scan(&n.Revision, &n.Text, &n.SavedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return n, err
}
func (s *Store) SaveReaderNotes(ctx context.Context, scope auth.PaperScope, text string) (ReaderNotes, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return ReaderNotes{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReaderNotes{}, err
	}
	defer tx.Rollback()
	n := ReaderNotes{Text: text, SavedAt: now()}
	if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(revision),0)+1 FROM paper_notes WHERE paper_id=?", paper).Scan(&n.Revision); err != nil {
		return n, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO paper_notes(paper_id,revision,text,saved_at) VALUES (?,?,?,?)", paper, n.Revision, n.Text, n.SavedAt); err != nil {
		return n, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM paper_notes WHERE paper_id=? AND revision<=?", paper, n.Revision-KeptRevisions); err != nil {
		return n, err
	}
	return n, tx.Commit()
}

// AgentKeyID returns an audit identity, never the key or its hash.
func (s *Store) AgentKeyID(ctx context.Context, key string) (string, error) {
	hash := sha256.Sum256([]byte(key))
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM agent_keys WHERE hash=? AND revoked_at IS NULL", hash[:]).Scan(&id)
	return id, err
}
