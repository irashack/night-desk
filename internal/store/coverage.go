package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/lean"
)

// EditionCoverage is an immutable publication snapshot, independent of later
// configuration, candidate retention and ratings-file changes. From and Through
// bound the fetch times of the named candidates through publication, in UTC.
type EditionCoverage struct {
	Ratings lean.Ratings        `json:"ratings"`
	Items   map[string]lean.Bar `json:"items"`
	From    string              `json:"from,omitempty"`
	Through string              `json:"through"`
}

func coverageFor(ctx context.Context, tx *sql.Tx, paper string, ed *edition.Edition, layout *EditionLayout, publishedAt string) (any, error) {
	if layout == nil || layout.Ratings == nil {
		return nil, nil
	}
	enabled := map[string]bool{}
	for _, section := range layout.Sections {
		enabled[section.Key] = section.Lean
	}
	coverage := EditionCoverage{Ratings: *layout.Ratings, Items: map[string]lean.Bar{}, Through: publishedAt}
	wires := map[string]int{}
	for _, section := range ed.Sections {
		if !enabled[section.Key] {
			continue
		}
		for _, item := range section.Items {
			sources := []lean.Source{{Outlet: item.Source, URL: item.URL, Opinion: item.Kind == "opinion"}}
			for _, src := range item.Sources {
				sources = append(sources, lean.Source{Outlet: src.Outlet, URL: src.URL, Opinion: item.Kind == "opinion"})
			}
			available := make([]lean.Source, 0, len(sources))
			failed := map[string]bool{}
			for _, source := range sources {
				var state, fetched, canonical string
				var wire sql.NullString
				err := tx.QueryRowContext(ctx, `SELECT c.canonical_url,c.state,g.group_id,c.fetched_at FROM candidates c
				 LEFT JOIN candidate_groups g ON g.paper_id=c.paper_id AND g.candidate_id=c.id
				 WHERE c.paper_id=? AND (c.canonical_url=? OR EXISTS (SELECT 1 FROM json_each(c.aliases) WHERE value=?))
				 ORDER BY (c.canonical_url=?) DESC,c.id LIMIT 1`, paper, source.URL, source.URL, source.URL).Scan(&canonical, &state, &wire, &fetched)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return nil, err
				}
				if err == nil {
					if coverage.From == "" || fetched < coverage.From {
						coverage.From = fetched
					}
					source.URL = canonical
					if state == "unavailable" {
						failed[canonical] = true
						continue
					}
					if wire.Valid && wire.String != "" {
						if wires[wire.String] == 0 {
							wires[wire.String] = len(wires) + 1
						}
						source.WireGroup = wires[wire.String]
					}
				}
				available = append(available, source)
			}
			coverage.Items[item.ID] = lean.Coverage(layout.Ratings, available, len(failed))
		}
	}
	if len(coverage.Items) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(coverage)
	if err != nil {
		return nil, err
	}
	return string(raw), nil
}

// EditionCoverage returns nil for editions without a coverage snapshot.
func (s *Store) EditionCoverage(ctx context.Context, scope auth.PaperScope, date string) (*EditionCoverage, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return nil, err
	}
	var raw sql.NullString
	if err := s.db.QueryRowContext(ctx, "SELECT coverage FROM editions WHERE paper_id=? AND date=?", paper, date).Scan(&raw); err != nil {
		return nil, err
	}
	if !raw.Valid {
		return nil, nil
	}
	var coverage EditionCoverage
	if err := json.Unmarshal([]byte(raw.String), &coverage); err != nil {
		return nil, err
	}
	return &coverage, nil
}
