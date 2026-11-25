package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/candidate"
)

// Fixed fractional precision makes stored timestamps compare chronologically.
const candidateTime = "2006-01-02T15:04:05.000000000Z"

// UpsertCandidates atomically inserts snapshots. An existing canonical URL keeps
// its original snapshot, including unavailable state. Aliases and wire group
// membership accumulate independently; new links merge existing wire groups.
func (s *Store) UpsertCandidates(ctx context.Context, scope auth.PaperScope, batch []candidate.Candidate) error {
	paper, err := scopeID(scope)
	if err != nil {
		return err
	}
	for i, c := range batch {
		if problems := candidate.Validate(c); len(problems) > 0 {
			return fmt.Errorf("candidates[%d].%s", i, problems[0].String())
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range batch {
		var old string
		err := tx.QueryRowContext(ctx, "SELECT aliases FROM candidates WHERE paper_id=? AND canonical_url=?", paper, c.CanonicalURL).Scan(&old)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		aliases := []string{}
		if err == nil {
			if err := json.Unmarshal([]byte(old), &aliases); err != nil {
				return err
			}
		}
		seen := make(map[string]bool, len(aliases)+len(c.Aliases))
		seen[c.CanonicalURL] = true
		merged := []string{}
		for _, alias := range append(aliases, c.Aliases...) {
			if !seen[alias] {
				seen[alias] = true
				merged = append(merged, alias)
			}
		}
		aliasJSON, err := json.Marshal(merged)
		if err != nil {
			return err
		}
		byline := c.Byline
		if byline == nil {
			byline = []string{}
		}
		bylineJSON, err := json.Marshal(byline)
		if err != nil {
			return err
		}
		var published any
		if c.PublishedAt != nil {
			published = c.PublishedAt.UTC().Format(candidateTime)
		}
		var id int64
		if err = tx.QueryRowContext(ctx, `INSERT INTO candidates
		 (paper_id,section,canonical_url,aliases,outlet,title,published_at,fetched_at,byline,text,state,wire_group,repeat)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(paper_id,canonical_url) DO UPDATE SET aliases=excluded.aliases RETURNING id`,
			paper, c.Section, c.CanonicalURL, string(aliasJSON), c.Outlet, c.Title, published, c.FetchedAt.UTC().Format(candidateTime), string(bylineJSON), c.Text, c.State, c.WireGroup, c.Repeat).Scan(&id); err != nil {
			return err
		}
		if c.WireGroup != nil {
			if err = linkCandidateGroup(ctx, tx, paper, id, *c.WireGroup); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func linkCandidateGroup(ctx context.Context, tx *sql.Tx, paper string, id int64, group string) error {
	// Merges keep aliases pointing directly to the current group. Resolve a
	// delayed chunk before linking it so it cannot recreate an obsolete group.
	var canonical string
	err := tx.QueryRowContext(ctx, "SELECT group_id FROM candidate_group_aliases WHERE paper_id=? AND alias_id=?", paper, group).Scan(&canonical)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		group = canonical
	}
	var old string
	err = tx.QueryRowContext(ctx, "SELECT group_id FROM candidate_groups WHERE paper_id=? AND candidate_id=?", paper, id).Scan(&old)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && old != group {
		// Renaming the whole old group preserves transitive links, including
		// members absent from this batch. The incoming group may already exist.
		if _, err = tx.ExecContext(ctx, "UPDATE candidate_groups SET group_id=? WHERE paper_id=? AND group_id=?", group, paper, old); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE candidate_group_aliases SET group_id=? WHERE paper_id=? AND group_id=?", group, paper, old); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO candidate_group_aliases(paper_id,alias_id,group_id) VALUES (?,?,?)", paper, old, group); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO candidate_groups(paper_id,candidate_id,group_id) VALUES (?,?,?)
	 ON CONFLICT(paper_id,candidate_id) DO UPDATE SET group_id=excluded.group_id`, paper, id, group)
	return err
}

// Candidates returns a paper's pool, newest first. Empty section and zero since
// omit their corresponding filters. Repeat reflects current seven-day edition
// history rather than the immutable snapshot's original gathering-time flag.
func (s *Store) Candidates(ctx context.Context, scope auth.PaperScope, section string, since time.Time) ([]candidate.Candidate, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return nil, err
	}
	recentURLs, err := s.RecentCandidateURLs(ctx, scope, time.Now().UTC().AddDate(0, 0, -7))
	if err != nil {
		return nil, err
	}
	recent := make(map[string]bool, len(recentURLs))
	for _, link := range recentURLs {
		recent[link] = true
	}
	cutoff := ""
	if !since.IsZero() {
		cutoff = since.UTC().Format(candidateTime)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT c.id,c.section,c.canonical_url,c.aliases,c.outlet,c.title,c.published_at,c.fetched_at,c.byline,c.text,c.state,g.group_id,c.repeat
	 FROM candidates c LEFT JOIN candidate_groups g ON g.paper_id=c.paper_id AND g.candidate_id=c.id
	 WHERE c.paper_id=? AND (?='' OR c.section=?) AND (?='' OR c.fetched_at>=?) ORDER BY c.fetched_at DESC,c.id DESC`, paper, section, section, cutoff, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []candidate.Candidate{}
	for rows.Next() {
		var c candidate.Candidate
		var aliases, byline, fetched string
		var published sql.NullString
		if err := rows.Scan(&c.ID, &c.Section, &c.CanonicalURL, &aliases, &c.Outlet, &c.Title, &published, &fetched, &byline, &c.Text, &c.State, &c.WireGroup, &c.Repeat); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(aliases), &c.Aliases); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(byline), &c.Byline); err != nil {
			return nil, err
		}
		if c.FetchedAt, err = time.Parse(time.RFC3339Nano, fetched); err != nil {
			return nil, err
		}
		if published.Valid {
			t, err := time.Parse(time.RFC3339Nano, published.String)
			if err != nil {
				return nil, err
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

// ErrNoSnapshot is a URL no gathered snapshot matches: the source was never
// gathered for the paper, as opposed to gathered with a failed fetch.
var ErrNoSnapshot = errors.New("no stored snapshot")

// CandidateText finds available text by canonical URL or an observed alias. A
// missing or unavailable snapshot returns ok=false, without an error.
func (s *Store) CandidateText(ctx context.Context, scope auth.PaperScope, link string) (string, bool, error) {
	text, ok, err := s.SnapshotText(ctx, scope, link)
	if errors.Is(err, ErrNoSnapshot) {
		return "", false, nil
	}
	return text, ok, err
}

// SnapshotText is CandidateText, but a URL with no snapshot at all returns
// ErrNoSnapshot, so source checks can say a source was never gathered.
func (s *Store) SnapshotText(ctx context.Context, scope auth.PaperScope, link string) (string, bool, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return "", false, err
	}
	return s.candidateText(ctx, paper, link)
}

// RunSnapshotText is SnapshotText for the trusted scheduler, using
// operator-configured paper IDs.
func (s *Store) RunSnapshotText(ctx context.Context, paper, link string) (string, bool, error) {
	return s.candidateText(ctx, paper, link)
}

func (s *Store) candidateText(ctx context.Context, paper, link string) (string, bool, error) {
	var text, state string
	err := s.db.QueryRowContext(ctx, `SELECT text,state FROM candidates
	 WHERE paper_id=? AND (canonical_url=? OR EXISTS (SELECT 1 FROM json_each(candidates.aliases) WHERE value=?))
	 ORDER BY (canonical_url=?) DESC,id LIMIT 1`, paper, link, link, link).Scan(&text, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, ErrNoSnapshot
	}
	if err != nil {
		return "", false, err
	}
	if state != "ok" {
		return "", false, nil
	}
	return text, true, nil
}

// PruneCandidates removes snapshots fetched before the paper's retention window.
func (s *Store) PruneCandidates(ctx context.Context, scope auth.PaperScope, retentionDays int) error {
	paper, err := scopeID(scope)
	if err != nil {
		return err
	}
	return s.pruneCandidates(ctx, paper, retentionDays)
}

// RunPruneCandidates is PruneCandidates for the trusted scheduler, using
// operator-configured paper IDs, so snapshots expire even when no worker uploads.
func (s *Store) RunPruneCandidates(ctx context.Context, paper string, retentionDays int) error {
	return s.pruneCandidates(ctx, paper, retentionDays)
}

func (s *Store) pruneCandidates(ctx context.Context, paper string, retentionDays int) error {
	if retentionDays < 1 || retentionDays > 90 {
		return errors.New("retention_days: must be between 1 and 90")
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM candidates WHERE paper_id=? AND fetched_at<?", paper, time.Now().UTC().AddDate(0, 0, -retentionDays).Format(candidateTime))
	return err
}

// RecentCandidateURLs returns source URLs named by recently published editions,
// including other coverage and aliases observed in this paper's candidate pool.
func (s *Store) RecentCandidateURLs(ctx context.Context, scope auth.PaperScope, since time.Time) ([]string, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT body FROM editions WHERE paper_id=? AND julianday(published_at)>=julianday(?)", paper, since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var collect func(any)
	collect = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if link, ok := v["url"].(string); ok && link != "" {
				seen[link] = true
			}
			for _, child := range v {
				collect(child)
			}
		case []any:
			for _, child := range v {
				collect(child)
			}
		}
	}
	for rows.Next() {
		var body string
		if err = rows.Scan(&body); err != nil {
			break
		}
		var ed struct {
			Sections []struct {
				Items []any `json:"items"`
			} `json:"sections"`
		}
		if err = json.Unmarshal([]byte(body), &ed); err != nil {
			break
		}
		for _, section := range ed.Sections {
			collect(section.Items)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	// Read provenance alone: a paper may hold many large article texts.
	rows, err = s.db.QueryContext(ctx, "SELECT canonical_url,aliases FROM candidates WHERE paper_id=?", paper)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var canonical, raw string
		if err := rows.Scan(&canonical, &raw); err != nil {
			return nil, err
		}
		var aliases []string
		if err := json.Unmarshal([]byte(raw), &aliases); err != nil {
			return nil, err
		}
		matched := seen[canonical]
		for _, alias := range aliases {
			matched = matched || seen[alias]
		}
		if matched {
			seen[canonical] = true
			for _, alias := range aliases {
				seen[alias] = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(seen))
	for link := range seen {
		out = append(out, link)
	}
	sort.Strings(out)
	return out, nil
}

// LaterCandidate is an available snapshot gathered after an edition, without
// its text. Grouped holds the requested links whose own snapshots, by
// canonical URL or alias, share this candidate's group.
type LaterCandidate struct {
	CanonicalURL  string
	Aliases       []string
	Outlet, Title string
	FetchedAt     time.Time
	Grouped       []string
}

// LaterCandidates returns at most limit available snapshots fetched at or after
// since, newest first, in one query. Article text is never read.
func (s *Store) LaterCandidates(ctx context.Context, scope auth.PaperScope, since time.Time, links []string, limit int) ([]LaterCandidate, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return nil, err
	}
	if links == nil {
		links = []string{}
	}
	wanted, err := json.Marshal(links)
	if err != nil {
		return nil, err
	}
	// Anchors are the groups of the edition's own sources, which were usually
	// fetched before the edition and so fall outside the later window.
	rows, err := s.db.QueryContext(ctx, `WITH anchors AS MATERIALIZED (
	 SELECT DISTINCT g.group_id,u.value AS link FROM candidate_groups g
	 JOIN candidates a ON a.paper_id=g.paper_id AND a.id=g.candidate_id
	 JOIN json_each(json_insert(a.aliases,'$[#]',a.canonical_url)) u
	 WHERE g.paper_id=? AND u.value IN (SELECT value FROM json_each(?)))
	 SELECT c.canonical_url,c.aliases,c.outlet,c.title,c.fetched_at,
	 (SELECT json_group_array(x.link) FROM anchors x WHERE x.group_id=g.group_id)
	 FROM candidates c LEFT JOIN candidate_groups g ON g.paper_id=c.paper_id AND g.candidate_id=c.id
	 WHERE c.paper_id=? AND c.state='ok' AND c.fetched_at>=? ORDER BY c.fetched_at DESC,c.id DESC LIMIT ?`,
		paper, string(wanted), paper, since.UTC().Format(candidateTime), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LaterCandidate{}
	for rows.Next() {
		var c LaterCandidate
		var aliases, fetched, grouped string
		if err := rows.Scan(&c.CanonicalURL, &aliases, &c.Outlet, &c.Title, &fetched, &grouped); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(aliases), &c.Aliases); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(grouped), &c.Grouped); err != nil {
			return nil, err
		}
		if c.FetchedAt, err = time.Parse(time.RFC3339Nano, fetched); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// StorySnapshot is the stored text of a story for the reading view.
type StorySnapshot struct {
	Text      string
	FetchedAt time.Time
}

// ReadableSnapshot finds a story's available snapshot by canonical URL or an
// observed alias. A missing or unavailable snapshot returns ok=false.
func (s *Store) ReadableSnapshot(ctx context.Context, scope auth.PaperScope, link string) (StorySnapshot, bool, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return StorySnapshot{}, false, err
	}
	var snap StorySnapshot
	var fetched string
	err = s.db.QueryRowContext(ctx, `SELECT text,fetched_at FROM candidates
	 WHERE paper_id=? AND state='ok' AND (canonical_url=? OR EXISTS (SELECT 1 FROM json_each(candidates.aliases) WHERE value=?))
	 ORDER BY (canonical_url=?) DESC,id LIMIT 1`, paper, link, link, link).Scan(&snap.Text, &fetched)
	if errors.Is(err, sql.ErrNoRows) {
		return StorySnapshot{}, false, nil
	}
	if err != nil {
		return StorySnapshot{}, false, err
	}
	if snap.FetchedAt, err = time.Parse(time.RFC3339Nano, fetched); err != nil {
		return StorySnapshot{}, false, err
	}
	return snap, true, nil
}

// ReadableLinks reports which of links have an available snapshot, by
// canonical URL or alias, in one pass over the paper's pool.
func (s *Store) ReadableLinks(ctx context.Context, scope auth.PaperScope, links []string) (map[string]bool, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	if len(links) == 0 {
		return out, nil
	}
	list, err := json.Marshal(links)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT l.value FROM json_each(?) l WHERE EXISTS (SELECT 1 FROM candidates c
	 WHERE c.paper_id=? AND c.state='ok' AND (c.canonical_url=l.value OR EXISTS (SELECT 1 FROM json_each(c.aliases) a WHERE a.value=l.value)))`, string(list), paper)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var link string
		if err := rows.Scan(&link); err != nil {
			return nil, err
		}
		out[link] = true
	}
	return out, rows.Err()
}
