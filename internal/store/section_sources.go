package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/irashack/night-desk/internal/auth"
)

// FoundSource is a feed the worker found and verified for a section.
type FoundSource struct {
	Feed   string `json:"feed"`
	Outlet string `json:"outlet"`
}

// SectionSources is what discovery has found for one section so far.
type SectionSources struct {
	Key     string        `json:"key"`
	Sources []FoundSource `json:"sources" nullable:"false"`
	// AttemptedAt is the last discovery attempt (RFC 3339), empty if none.
	AttemptedAt string `json:"attempted_at,omitempty"`
}

// MaxFoundSources bounds the feeds kept per section.
const MaxFoundSources = 5

// FoundSources returns found feeds and discovery attempts for the given section
// keys, in key order, oldest feed first.
func (s *Store) FoundSources(ctx context.Context, paper string, keys []string) ([]SectionSources, error) {
	out := make([]SectionSources, 0, len(keys))
	for _, key := range keys {
		ss := SectionSources{Key: key, Sources: []FoundSource{}}
		rows, err := s.db.QueryContext(ctx, `SELECT feed,outlet FROM section_sources WHERE paper_id=? AND section_key=? ORDER BY added_at,feed`, paper, key)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var f FoundSource
			if err = rows.Scan(&f.Feed, &f.Outlet); err != nil {
				rows.Close()
				return nil, err
			}
			ss.Sources = append(ss.Sources, f)
		}
		if err = rows.Close(); err != nil {
			return nil, err
		}
		err = s.db.QueryRowContext(ctx, `SELECT attempted_at FROM section_discovery WHERE paper_id=? AND section_key=?`, paper, key).Scan(&ss.AttemptedAt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		out = append(out, ss)
	}
	return out, nil
}

// AddFoundSources records a discovery attempt and keeps new feeds up to
// MaxFoundSources for the section. It returns how many were added.
func (s *Store) AddFoundSources(ctx context.Context, scope auth.PaperScope, section string, found []FoundSource, at time.Time) (int, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stamp := at.UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO section_discovery(paper_id,section_key,attempted_at) VALUES (?,?,?)
ON CONFLICT(paper_id,section_key) DO UPDATE SET attempted_at=excluded.attempted_at`, paper, section, stamp); err != nil {
		return 0, err
	}
	var have int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM section_sources WHERE paper_id=? AND section_key=?`, paper, section).Scan(&have); err != nil {
		return 0, err
	}
	added := 0
	for _, f := range found {
		if have+added >= MaxFoundSources {
			break
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO section_sources(paper_id,section_key,feed,outlet,added_at) VALUES (?,?,?,?,?) ON CONFLICT DO NOTHING`, paper, section, f.Feed, f.Outlet, stamp)
		if err != nil {
			return 0, err
		}
		if n, _ := result.RowsAffected(); n == 1 {
			added++
		}
	}
	return added, tx.Commit()
}
