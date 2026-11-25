package store_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/candidate"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/store"
)

func TestCandidateGroupsInterleavedChunks(t *testing.T) {
	s, dir := open(t)
	a, b := scope(t, s, "a"), scope(t, s, "b")
	firstGroup, secondGroup := "run-a", "run-b"
	first := exampleCandidate()
	first.FetchedAt = time.Now().UTC().AddDate(0, 0, -2)
	first.CanonicalURL, first.Outlet = "https://outlet0.example/story", "Outlet 0"
	first.Aliases, first.Repeat, first.WireGroup = nil, false, &firstGroup
	second, third := first, first
	second.CanonicalURL, second.Outlet = "https://outlet5.example/story", "Outlet 5"
	third.CanonicalURL, third.Outlet, third.WireGroup = "https://outlet3.example/story", "Outlet 3", &secondGroup
	// Run A posts its first chunk from {first, second}.
	if err := s.UpsertCandidates(ctx, a, []candidate.Candidate{first}); err != nil {
		t.Fatal(err)
	}
	// Run B merges {first, third} before A posts its delayed second chunk.
	linked := first
	linked.WireGroup = &secondGroup
	for _, batch := range [][]candidate.Candidate{{linked, third}, {second}} {
		if err := s.UpsertCandidates(ctx, a, batch); err != nil {
			t.Fatal(err)
		}
	}
	check := func(want string) {
		t.Helper()
		pool, err := s.Candidates(ctx, a, "", time.Time{})
		if err != nil || len(pool) != 3 {
			t.Fatal(pool, err)
		}
		for _, c := range pool {
			if c.WireGroup == nil || *c.WireGroup != want {
				t.Fatalf("interleaving disconnected %s: group=%v, want %s", c.CanonicalURL, c.WireGroup, want)
			}
		}
	}
	check(secondGroup)
	// Aliases stay paper-scoped, and later merges redirect earlier aliases.
	if err := s.UpsertCandidates(ctx, b, []candidate.Candidate{first, second}); err != nil {
		t.Fatal(err)
	}
	thirdGroup := "run-c"
	linked.WireGroup = &thirdGroup
	for _, batch := range [][]candidate.Candidate{{linked}, {first, third, second}, {first, third, second}} {
		if err := s.UpsertCandidates(ctx, a, batch); err != nil {
			t.Fatal(err)
		}
	}
	check(thirdGroup)
	pool, err := s.Candidates(ctx, b, "", time.Time{})
	if err != nil || len(pool) != 2 {
		t.Fatal(pool, err)
	}
	for _, c := range pool {
		if c.WireGroup == nil || *c.WireGroup != firstGroup {
			t.Fatal("alias crossed paper scope", c)
		}
	}
	ed := draftEdition()
	ed.Schema = 2
	item := &ed.Sections[0].Items[0]
	item.Source, item.URL = first.Outlet, first.CanonicalURL
	item.Sources = []edition.Source{{Outlet: second.Outlet, URL: second.CanonicalURL}, {Outlet: third.Outlet, URL: third.CanonicalURL}}
	layout := &store.EditionLayout{Ratings: coverageRatings(), Sections: []store.LayoutSection{{Key: "news", Lean: true}}}
	if err := s.Publish(ctx, a, "2026-10-30", ed, nil, layout); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.EditionCoverage(ctx, a, "2026-10-30")
	if err != nil || snapshot == nil {
		t.Fatal(snapshot, err)
	}
	if bar := snapshot.Items[item.ID]; bar.Right != 1 || bar.Left != 0 || bar.Center != 0 {
		t.Fatalf("interleaved wire copies counted more than once: %+v", bar)
	}
	// Retention can remove every current member. Delayed chunks must still
	// resolve the old group id after pruning and a server restart.
	if err := s.PruneCandidates(ctx, a, 1); err != nil {
		t.Fatal(err)
	}
	if pool, err := s.Candidates(ctx, a, "", time.Time{}); err != nil || len(pool) != 0 {
		t.Fatal(pool, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	second.FetchedAt = time.Now().UTC()
	if err := s.UpsertCandidates(ctx, a, []candidate.Candidate{second}); err != nil {
		t.Fatal(err)
	}
	pool, err = s.Candidates(ctx, a, "", time.Time{})
	if err != nil || len(pool) != 1 || pool[0].WireGroup == nil || *pool[0].WireGroup != thirdGroup {
		t.Fatal("pruning or restart discarded group aliases", pool, err)
	}
}

func exampleCandidate() candidate.Candidate {
	published := time.Now().UTC().Add(-time.Hour)
	group := "wire-123"
	return candidate.Candidate{Section: "news", CanonicalURL: "https://example.com/story", Aliases: []string{"https://example.com/redirect"}, Outlet: "Example", Title: "Original title", PublishedAt: &published, FetchedAt: time.Now().UTC(), Byline: []string{"Jane Reporter"}, Text: "Original source text.", State: "ok", WireGroup: &group, Repeat: true}
}

func TestCandidatesImmutableAndIsolated(t *testing.T) {
	s, _ := open(t)
	a, b := scope(t, s, "a"), scope(t, s, "b")
	original := exampleCandidate()
	original.Repeat = false
	if err := s.UpsertCandidates(ctx, a, []candidate.Candidate{original}); err != nil {
		t.Fatal(err)
	}
	pool, err := s.Candidates(ctx, a, "news", time.Time{})
	if err != nil || len(pool) != 1 || pool[0].ID == 0 {
		t.Fatal(pool, err)
	}
	original.ID = pool[0].ID
	if !reflect.DeepEqual(pool[0], original) {
		t.Fatalf("got %#v, want %#v", pool[0], original)
	}
	changed := original
	changed.ID = 999
	changed.Section, changed.Title, changed.Outlet = "world", "Revised", "Other"
	changed.Text, changed.State = "", "unavailable"
	changed.PublishedAt, changed.Byline, changed.WireGroup, changed.Repeat = nil, nil, nil, false
	changed.FetchedAt = time.Now().UTC().Add(time.Hour)
	changed.Aliases = []string{original.CanonicalURL, original.Aliases[0], "https://example.com/alias"}
	if err := s.UpsertCandidates(ctx, a, []candidate.Candidate{changed, changed}); err != nil {
		t.Fatal(err)
	}
	pool, err = s.Candidates(ctx, a, "", time.Time{})
	original.Aliases = append(original.Aliases, "https://example.com/alias")
	if err != nil || len(pool) != 1 || !reflect.DeepEqual(pool[0], original) {
		t.Fatalf("snapshot changed: %#v (%v)", pool, err)
	}
	for _, link := range append([]string{original.CanonicalURL}, original.Aliases...) {
		text, ok, err := s.CandidateText(ctx, a, link)
		if err != nil || !ok || text != original.Text {
			t.Fatalf("lookup %s: %q %v %v", link, text, ok, err)
		}
		if _, ok, err := s.CandidateText(ctx, b, link); err != nil || ok {
			t.Fatalf("cross-tenant lookup: %v %v", ok, err)
		}
	}
	if pool, err := s.Candidates(ctx, b, "", time.Time{}); err != nil || len(pool) != 0 {
		t.Fatal(pool, err)
	}
	if pool, err := s.Candidates(ctx, a, "world", time.Time{}); err != nil || len(pool) != 0 {
		t.Fatal(pool, err)
	}
	if pool, err := s.Candidates(ctx, a, "", original.FetchedAt.Add(time.Nanosecond)); err != nil || len(pool) != 0 {
		t.Fatal(pool, err)
	}
	changed.Title = "B snapshot"
	if err := s.UpsertCandidates(ctx, b, []candidate.Candidate{changed}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.CandidateText(ctx, b, changed.CanonicalURL); err != nil || ok {
		t.Fatalf("unavailable text considered verified: %v %v", ok, err)
	}
}

func TestCandidateGroupsAcrossGatheringRuns(t *testing.T) {
	s, dir := open(t)
	a, b := scope(t, s, "a"), scope(t, s, "b")
	first := exampleCandidate()
	first.WireGroup, first.Repeat = nil, false
	first.Aliases = []string{}
	if err := s.UpsertCandidates(ctx, a, []candidate.Candidate{first}); err != nil {
		t.Fatal(err)
	}
	pool, err := s.Candidates(ctx, a, "", time.Time{})
	if err != nil || len(pool) != 1 {
		t.Fatal(pool, err)
	}
	first = pool[0]
	group := "first-pair"
	copy := first
	copy.CanonicalURL += "/copy"
	copy.WireGroup = &group
	linked := first
	linked.WireGroup = &group
	linked.Title, linked.Text = "Changed title", "Changed text"
	if err := s.UpsertCandidates(ctx, a, []candidate.Candidate{copy, linked}); err != nil {
		t.Fatal(err)
	}
	pool, err = s.Candidates(ctx, a, "", time.Time{})
	if err != nil || len(pool) != 2 {
		t.Fatal(pool, err)
	}
	for _, c := range pool {
		if c.WireGroup == nil || *c.WireGroup != group {
			t.Fatalf("existing snapshot not linked: %+v", c)
		}
		if c.CanonicalURL == first.CanonicalURL {
			c.WireGroup = nil
			if !reflect.DeepEqual(c, first) {
				t.Fatalf("grouping changed snapshot: %+v, want %+v", c, first)
			}
		}
	}
	// Paging reads the same mutable membership as the unpaged pool.
	page, err := s.CandidatePage(ctx, a, "", time.Time{}, time.Time{}, 0, 10)
	if err != nil || len(page) != 2 {
		t.Fatal(page, err)
	}
	for _, c := range page {
		if c.WireGroup == nil || *c.WireGroup != group {
			t.Fatalf("paged snapshot has stale group: %+v", c)
		}
	}
	// A second existing group is joined by a later batch containing only
	// one member from each group. Every member must share the merged group.
	otherGroup := "second-pair"
	third, fourth := first, first
	third.CanonicalURL += "/third"
	fourth.CanonicalURL += "/fourth"
	third.WireGroup, fourth.WireGroup = &otherGroup, &otherGroup
	for _, paper := range []auth.PaperScope{a, b} {
		if err := s.UpsertCandidates(ctx, paper, []candidate.Candidate{third, fourth}); err != nil {
			t.Fatal(err)
		}
	}
	joined := "joined"
	linked.WireGroup, third.WireGroup = &joined, &joined
	// Separate calls also exercise worker batches split across HTTP requests.
	for _, c := range []candidate.Candidate{linked, third} {
		if err := s.UpsertCandidates(ctx, a, []candidate.Candidate{c}); err != nil {
			t.Fatal(err)
		}
	}
	// A later run which has no grouping evidence must retain known links.
	if err := s.UpsertCandidates(ctx, a, []candidate.Candidate{first}); err != nil {
		t.Fatal(err)
	}
	pool, err = s.Candidates(ctx, a, "", time.Time{})
	if err != nil || len(pool) != 4 {
		t.Fatal(pool, err)
	}
	for _, c := range pool {
		if c.WireGroup == nil || *c.WireGroup != joined {
			t.Fatalf("group merge left a member behind: %+v", c)
		}
	}
	pool, err = s.Candidates(ctx, b, "", time.Time{})
	if err != nil || len(pool) != 2 {
		t.Fatal(pool, err)
	}
	for _, c := range pool {
		if c.WireGroup == nil || *c.WireGroup != otherGroup {
			t.Fatalf("group merge crossed paper boundary: %+v", c)
		}
	}
	// The immutable column still describes the first observation.
	db, err := sql.Open("sqlite", filepath.Join(dir, "nightdesk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var stored sql.NullString
	if err := db.QueryRow("SELECT wire_group FROM candidates WHERE paper_id=? AND canonical_url=?", "a", first.CanonicalURL).Scan(&stored); err != nil || stored.Valid {
		t.Fatal("original snapshot membership was rewritten", stored, err)
	}
}

func TestCandidateValidationAndScope(t *testing.T) {
	s, _ := open(t)
	a := scope(t, s, "a")
	valid := exampleCandidate()
	invalid := valid
	invalid.CanonicalURL = "file:///etc/passwd"
	if err := s.UpsertCandidates(ctx, a, []candidate.Candidate{valid, invalid}); err == nil || !strings.HasPrefix(err.Error(), "candidates[1].canonical_url:") {
		t.Fatal("invalid input accepted", err)
	}
	if pool, err := s.Candidates(ctx, a, "", time.Time{}); err != nil || len(pool) != 0 {
		t.Fatal("batch partially inserted", pool, err)
	}
	zero := auth.PaperScope{}
	if err := s.UpsertCandidates(ctx, zero, []candidate.Candidate{valid}); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal(err)
	}
	if _, err := s.Candidates(ctx, zero, "", time.Time{}); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal(err)
	}
	if _, _, err := s.CandidateText(ctx, zero, valid.CanonicalURL); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal(err)
	}
	if err := s.PruneCandidates(ctx, zero, 14); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal(err)
	}
	if _, err := s.RecentCandidateURLs(ctx, zero, time.Now()); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal(err)
	}
}

func TestPruneCandidates(t *testing.T) {
	s, _ := open(t)
	a, b := scope(t, s, "a"), scope(t, s, "b")
	old := exampleCandidate()
	old.FetchedAt = time.Now().UTC().AddDate(0, 0, -15)
	recent := exampleCandidate()
	recent.CanonicalURL += "/new"
	for _, scope := range []auth.PaperScope{a, b} {
		if err := s.UpsertCandidates(ctx, scope, []candidate.Candidate{old, recent}); err != nil {
			t.Fatal(err)
		}
	}
	for _, days := range []int{0, 91} {
		if err := s.PruneCandidates(ctx, a, days); err == nil {
			t.Fatal("invalid retention accepted")
		}
	}
	if err := s.PruneCandidates(ctx, a, 14); err != nil {
		t.Fatal(err)
	}
	if pool, err := s.Candidates(ctx, a, "", time.Time{}); err != nil || len(pool) != 1 || pool[0].CanonicalURL != recent.CanonicalURL {
		t.Fatal(pool, err)
	}
	if pool, err := s.Candidates(ctx, b, "", time.Time{}); err != nil || len(pool) != 2 {
		t.Fatal("prune crossed tenant", pool, err)
	}
}

func TestRecentCandidateURLs(t *testing.T) {
	s, dir := open(t)
	a, b := scope(t, s, "a"), scope(t, s, "b")
	c := exampleCandidate()
	if err := s.UpsertCandidates(ctx, a, []candidate.Candidate{c}); err != nil {
		t.Fatal(err)
	}
	ed := &edition.Edition{Schema: 1, Lead: "story", Sections: []edition.Section{{Key: "news", Items: []edition.Item{{ID: "story", URL: c.Aliases[0]}}}}}
	if err := s.Publish(ctx, a, "2026-10-30", ed, nil); err != nil {
		t.Fatal(err)
	}
	ed.Sections[0].Items[0].URL = "https://private-paper.example/story"
	if err := s.Publish(ctx, b, "2026-10-30", ed, nil); err != nil {
		t.Fatal(err)
	}
	// Store a schema-v2-shaped body directly to exercise coverage without tying
	// the compatibility lookup to the edition package's current schema.
	db, err := sql.Open("sqlite", filepath.Join(dir, "nightdesk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO editions(paper_id,date,schema,body,published_at) VALUES (?,?,?,?,?)`, "a", "2026-10-29", 2, `{"schema":2,"sections":[{"items":[{"url":"https://example.com/second","coverage":[{"url":"https://other.example/coverage"}]}]}]}`, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	urls, err := s.RecentCandidateURLs(ctx, a, time.Now().AddDate(0, 0, -7))
	want := []string{"https://example.com/redirect", "https://example.com/second", "https://example.com/story", "https://other.example/coverage"}
	if err != nil || !reflect.DeepEqual(urls, want) {
		t.Fatal(urls, err)
	}
	if urls, err := s.RecentCandidateURLs(ctx, a, time.Now().Add(time.Hour)); err != nil || len(urls) != 0 {
		t.Fatal(urls, err)
	}
}

func TestCandidateRepeatTracksRecentEditions(t *testing.T) {
	s, dir := open(t)
	a, b := scope(t, s, "a"), scope(t, s, "b")
	c := exampleCandidate()
	c.Repeat = false
	for _, paper := range []auth.PaperScope{a, b} {
		if err := s.UpsertCandidates(ctx, paper, []candidate.Candidate{c}); err != nil {
			t.Fatal(err)
		}
	}
	pool, err := s.Candidates(ctx, a, "", time.Time{})
	if err != nil || len(pool) != 1 || pool[0].Repeat {
		t.Fatal("unpublished candidate repeated", pool, err)
	}
	ed := &edition.Edition{Schema: 1, Lead: "story", Sections: []edition.Section{{Key: "news", Items: []edition.Item{{ID: "story", URL: c.Aliases[0]}}}}}
	if err := s.Publish(ctx, a, time.Now().UTC().Format("2006-01-02"), ed, nil); err != nil {
		t.Fatal(err)
	}
	changed := c
	changed.Text = "Fetched again after publication."
	if err := s.UpsertCandidates(ctx, a, []candidate.Candidate{changed}); err != nil {
		t.Fatal(err)
	}
	pool, err = s.Candidates(ctx, a, "", time.Time{})
	if err != nil || len(pool) != 1 || !pool[0].Repeat || pool[0].Text != c.Text {
		t.Fatal("new edition did not mark immutable snapshot as repeat", pool, err)
	}
	if text, ok, err := s.CandidateText(ctx, a, c.CanonicalURL); err != nil || !ok || text != c.Text {
		t.Fatal("repeat update changed text", text, ok, err)
	}
	if pool, err := s.Candidates(ctx, b, "", time.Time{}); err != nil || len(pool) != 1 || pool[0].Repeat {
		t.Fatal("repeat crossed paper scopes", pool, err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "nightdesk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var storedRepeat bool
	if err := db.QueryRow("SELECT repeat FROM candidates WHERE paper_id=? AND canonical_url=?", "a", c.CanonicalURL).Scan(&storedRepeat); err != nil || storedRepeat {
		t.Fatal("query changed stored snapshot repeat flag", storedRepeat, err)
	}
	if _, err := db.Exec("UPDATE editions SET published_at=? WHERE paper_id=?", time.Now().UTC().AddDate(0, 0, -8).Format(time.RFC3339Nano), "a"); err != nil {
		t.Fatal(err)
	}
	pool, err = s.Candidates(ctx, a, "", time.Time{})
	if err != nil || len(pool) != 1 || pool[0].Repeat {
		t.Fatal("expired edition still holds candidate back", pool, err)
	}
}

func TestLaterCandidates(t *testing.T) {
	s, _ := open(t)
	a, b := scope(t, s, "a"), scope(t, s, "b")
	now := time.Now().UTC()
	first, merged := "run-a", "run-b"
	source := exampleCandidate()
	source.FetchedAt, source.WireGroup = now.AddDate(0, 0, -3), &first
	later := exampleCandidate()
	later.CanonicalURL, later.Aliases, later.FetchedAt, later.WireGroup = "https://later.example/a", nil, now.AddDate(0, 0, -1), &merged
	link := source
	link.WireGroup = &merged
	plain := later
	plain.CanonicalURL, plain.FetchedAt, plain.WireGroup = "https://plain.example/a", now, nil
	if err := s.UpsertCandidates(ctx, a, []candidate.Candidate{source, later, link, plain}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCandidates(ctx, b, []candidate.Candidate{later}); err != nil {
		t.Fatal(err)
	}
	// The edition named the source by its alias; the merged group still links them.
	got, err := s.LaterCandidates(ctx, a, now.AddDate(0, 0, -2), []string{"https://example.com/redirect"}, 10)
	if err != nil || len(got) != 2 {
		t.Fatal(got, err)
	}
	if got[0].CanonicalURL != plain.CanonicalURL || len(got[0].Grouped) != 0 || got[1].CanonicalURL != later.CanonicalURL || !reflect.DeepEqual(got[1].Grouped, []string{"https://example.com/redirect"}) {
		t.Fatalf("later candidates: %+v", got)
	}
	if got, err = s.LaterCandidates(ctx, a, now.AddDate(0, 0, -2), nil, 1); err != nil || len(got) != 1 || got[0].CanonicalURL != plain.CanonicalURL {
		t.Fatal("limit", got, err)
	}
	if got, err = s.LaterCandidates(ctx, b, now.AddDate(0, 0, -2), []string{"https://example.com/redirect"}, 10); err != nil || len(got) != 1 || len(got[0].Grouped) != 0 {
		t.Fatal("paper isolation", got, err)
	}
}

func TestUnavailableCandidateRecoversOnLaterFetch(t *testing.T) {
	s, _ := open(t)
	a := scope(t, s, "a")
	failed := exampleCandidate()
	failed.Text, failed.State, failed.Byline, failed.Title = "", "unavailable", nil, "example.com"
	failed.FetchedAt = time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if err := s.UpsertCandidates(ctx, a, []candidate.Candidate{failed}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.CandidateText(ctx, a, failed.CanonicalURL); err != nil || ok {
		t.Fatalf("unavailable snapshot considered verified: %v %v", ok, err)
	}
	fetched := exampleCandidate()
	fetched.FetchedAt = time.Now().UTC().Truncate(time.Second)
	if err := s.UpsertCandidates(ctx, a, []candidate.Candidate{fetched}); err != nil {
		t.Fatal(err)
	}
	pool, err := s.Candidates(ctx, a, "", time.Time{})
	if err != nil || len(pool) != 1 {
		t.Fatal(pool, err)
	}
	got := pool[0]
	if got.State != "ok" || got.Text != fetched.Text || got.Title != fetched.Title || !reflect.DeepEqual(got.Byline, fetched.Byline) || !got.FetchedAt.Equal(fetched.FetchedAt) {
		t.Fatalf("unavailable snapshot not replaced by the later fetch: %#v", got)
	}
	if text, ok, err := s.CandidateText(ctx, a, failed.Aliases[0]); err != nil || !ok || text != fetched.Text {
		t.Fatalf("alias lookup after recovery: %q %v %v", text, ok, err)
	}
	// A later failure or a different successful fetch leaves the snapshot alone.
	again := failed
	again.FetchedAt = time.Now().UTC().Add(time.Hour)
	changed := exampleCandidate()
	changed.Text, changed.Title = "Rewritten text.", "Rewritten"
	if err := s.UpsertCandidates(ctx, a, []candidate.Candidate{again, changed}); err != nil {
		t.Fatal(err)
	}
	if pool, err := s.Candidates(ctx, a, "", time.Time{}); err != nil || len(pool) != 1 || pool[0].Text != fetched.Text || pool[0].State != "ok" {
		t.Fatalf("ok snapshot changed: %#v %v", pool, err)
	}
}

func TestCandidateTextPrefersFetchedSnapshot(t *testing.T) {
	s, _ := open(t)
	a := scope(t, s, "a")
	// The first pass failed at the feed's link; a later pass reached the page,
	// whose canonical URL differs, and kept the feed's link as an alias.
	failed := exampleCandidate()
	failed.CanonicalURL, failed.Aliases = "https://example.com/feed-link", nil
	failed.Text, failed.State, failed.Byline = "", "unavailable", nil
	fetched := exampleCandidate()
	fetched.Aliases = []string{"https://example.com/feed-link"}
	if err := s.UpsertCandidates(ctx, a, []candidate.Candidate{failed}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCandidates(ctx, a, []candidate.Candidate{fetched}); err != nil {
		t.Fatal(err)
	}
	if text, ok, err := s.CandidateText(ctx, a, "https://example.com/feed-link"); err != nil || !ok || text != fetched.Text {
		t.Fatalf("feed link lookup: %q %v %v", text, ok, err)
	}
}
