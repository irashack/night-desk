package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/lean"
)

// EditionLayout records the masthead and ordered sections used at publication.
// It is stored separately from the agent's edition payload.
type EditionLayout struct {
	Masthead string          `json:"masthead"`
	Sections []LayoutSection `json:"sections"`
	// Ratings is publication input; the transaction stores it in coverage.
	Ratings *lean.Ratings `json:"-"`
}

type LayoutSection struct {
	Lean  bool   `json:"lean,omitempty"`
	Key   string `json:"key"`
	Title string `json:"title"`
}

// EditionWithLayout reads an edition and its publication layout. A nil layout
// identifies editions published before layouts were recorded.
func (s *Store) EditionWithLayout(ctx context.Context, scope auth.PaperScope, date string) (*edition.Edition, *EditionLayout, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return nil, nil, err
	}
	return s.editionWithLayout(ctx, paper, date)
}

func (s *Store) editionWithLayout(ctx context.Context, paper, date string) (*edition.Edition, *EditionLayout, error) {
	var schema int
	var body []byte
	var raw sql.NullString
	if err := s.db.QueryRowContext(ctx, "SELECT schema,body,layout FROM editions WHERE paper_id=? AND date=?", paper, date).Scan(&schema, &body, &raw); err != nil {
		return nil, nil, err
	}
	ed, err := edition.DecodeStored(schema, body)
	if err != nil {
		return nil, nil, err
	}
	var layout *EditionLayout
	if raw.Valid {
		if err = json.Unmarshal([]byte(raw.String), &layout); err != nil {
			return nil, nil, err
		}
	}
	return ed, layout, nil
}
