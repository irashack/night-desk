package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/candidate"
)

// CandidatePage uses a (fetched_at,id) cursor so concurrent ingestion cannot
// shift offsets. It bounds database reads as well as the HTTP response.
func (s *Store) CandidatePage(ctx context.Context, scope auth.PaperScope, section string, since, before time.Time, beforeID int64, limit int) ([]candidate.Candidate, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 21 {
		return nil, errors.New("invalid candidate page limit")
	}
	recentURLs, err := s.RecentCandidateURLs(ctx, scope, time.Now().UTC().AddDate(0, 0, -7))
	if err != nil {
		return nil, err
	}
	recent := map[string]bool{}
	for _, link := range recentURLs {
		recent[link] = true
	}
	cutoff, cursor := "", ""
	if !since.IsZero() {
		cutoff = since.UTC().Format(candidateTime)
	}
	if !before.IsZero() {
		cursor = before.UTC().Format(candidateTime)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT c.id,c.section,c.canonical_url,c.aliases,c.outlet,c.title,c.published_at,c.fetched_at,c.byline,c.text,c.state,g.group_id,c.repeat
 FROM candidates c LEFT JOIN candidate_groups g ON g.paper_id=c.paper_id AND g.candidate_id=c.id
 WHERE c.paper_id=? AND (?='' OR c.section=?) AND (?='' OR c.fetched_at>=?)
 AND (?='' OR c.fetched_at<? OR (c.fetched_at=? AND c.id<?)) ORDER BY c.fetched_at DESC,c.id DESC LIMIT ?`, paper, section, section, cutoff, cutoff, cursor, cursor, cursor, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []candidate.Candidate{}
	for rows.Next() {
		var c candidate.Candidate
		var aliases, byline, fetched string
		var published sql.NullString
		if err = rows.Scan(&c.ID, &c.Section, &c.CanonicalURL, &aliases, &c.Outlet, &c.Title, &published, &fetched, &byline, &c.Text, &c.State, &c.WireGroup, &c.Repeat); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(aliases), &c.Aliases); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(byline), &c.Byline); err != nil {
			return nil, err
		}
		if c.FetchedAt, err = time.Parse(time.RFC3339Nano, fetched); err != nil {
			return nil, err
		}
		if published.Valid {
			t, e := time.Parse(time.RFC3339Nano, published.String)
			if e != nil {
				return nil, e
			}
			c.PublishedAt = &t
		}
		c.Repeat = recent[c.CanonicalURL]
		for _, alias := range c.Aliases {
			c.Repeat = c.Repeat || recent[alias]
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
