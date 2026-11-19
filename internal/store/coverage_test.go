package store_test

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/candidate"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/lean"
	"github.com/irashack/night-desk/internal/store"
)

func coverageRatings() *lean.Ratings {
	r := &lean.Ratings{Source: lean.Source{Name: "Test", URL: "https://ratings.example", License: "CC0", Attribution: "Original ratings"}}
	for i, side := range []lean.Lean{lean.Right, lean.LeanRight, lean.Right, lean.Center, lean.Center, lean.Left, lean.Left} {
		r.Ratings = append(r.Ratings, lean.Rating{Outlet: fmt.Sprintf("Outlet %d", i), Domains: []string{fmt.Sprintf("outlet%d.example", i)}, Lean: side})
	}
	return r
}

func TestCoverageSnapshot(t *testing.T) {
	s, _ := open(t)
	a, b := scope(t, s, "a"), scope(t, s, "b")
	ratings := coverageRatings()
	layout := &store.EditionLayout{Ratings: ratings, Sections: []store.LayoutSection{{Key: "news", Lean: true}, {Key: "off"}}}
	ed := draftEdition()
	ed.Schema = 2
	item := &ed.Sections[0].Items[0]
	item.Source, item.URL = "Outlet 0", "https://outlet0.example/alias"
	fetched := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	group := "same-wire"
	for i := 0; i < 7; i++ {
		c := candidate.Candidate{Section: "news", CanonicalURL: fmt.Sprintf("https://outlet%d.example/story", i), Outlet: fmt.Sprintf("Outlet %d", i), Title: "Headline", Text: "Source text", State: "ok", FetchedAt: fetched}
		if i == 0 {
			c.Aliases = []string{item.URL}
		}
		if i == 5 {
			c.WireGroup = &group
		}
		if i == 0 {
			c.WireGroup = &group
		}
		if i == 6 {
			c.State, c.Text = "unavailable", ""
		}
		if err := s.UpsertCandidates(ctx, a, []candidate.Candidate{c}); err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			item.Sources = append(item.Sources, edition.Source{Outlet: c.Outlet, URL: c.CanonicalURL})
		}
	}
	item.Sources = append(item.Sources, edition.Source{Outlet: "Unknown", URL: "https://unknown.example/story"})
	opinion := *item
	opinion.ID, opinion.Kind = "opinion", "opinion"
	ed.Sections[0].Items = append(ed.Sections[0].Items, opinion)
	ed.Sections = append(ed.Sections, edition.Section{Key: "off", Items: []edition.Item{{ID: "off", URL: item.URL}}})
	if err := s.Publish(ctx, a, "2026-10-30", ed, nil, layout); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.EditionCoverage(ctx, a, "2026-10-30")
	if err != nil || snapshot == nil {
		t.Fatal(snapshot, err)
	}
	bar := snapshot.Items["one"]
	if bar.Left != 0 || bar.Center != 2 || bar.Right != 3 || bar.Unrated != 1 || bar.FetchFailed != 1 || bar.Blindspot != "left" || bar.Attribution != "Original ratings" {
		t.Fatalf("bar: %+v", bar)
	}
	if bar, ok := snapshot.Items["off"]; ok {
		t.Fatalf("disabled section: %+v", bar)
	}
	if bar := snapshot.Items["opinion"]; bar.Left+bar.Center+bar.Right != 0 || bar.Opinion != 6 || bar.FetchFailed != 1 {
		t.Fatalf("opinion: %+v", bar)
	}
	if snapshot.From != fetched.Format("2006-01-02T15:04:05.000000000Z") || snapshot.Through == "" || !reflect.DeepEqual(snapshot.Ratings, *ratings) {
		t.Fatalf("missing snapshot metadata: %+v", snapshot)
	}
	if _, err := s.EditionCoverage(ctx, b, "2026-10-30"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("coverage crossed tenant boundary: %v", err)
	}
	if _, err := s.EditionCoverage(ctx, auth.PaperScope{}, "2026-10-30"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("invalid scope: %v", err)
	}
	ratings.Ratings[0].Lean = lean.Left
	ratings.Source.Attribution = "Changed ratings"
	again, err := s.EditionCoverage(ctx, a, "2026-10-30")
	if err != nil || !reflect.DeepEqual(again, snapshot) {
		t.Fatalf("history changed: %+v %v", again, err)
	}
	// Replacing with disabled coverage clears the old snapshot atomically.
	layout.Ratings = nil
	if err := s.Publish(ctx, a, "2026-10-30", ed, nil, layout); err != nil {
		t.Fatal(err)
	}
	if got, err := s.EditionCoverage(ctx, a, "2026-10-30"); err != nil || got != nil {
		t.Fatalf("replacement retained coverage: %+v %v", got, err)
	}
}

func TestCoverageUsesGroupsDiscoveredInLaterRuns(t *testing.T) {
	s, _ := open(t)
	a := scope(t, s, "a")
	first := candidate.Candidate{Section: "news", CanonicalURL: "https://outlet0.example/story", Outlet: "Outlet 0", Title: "Headline", Text: "Original text", State: "ok", FetchedAt: time.Now().UTC()}
	if err := s.UpsertCandidates(ctx, a, []candidate.Candidate{first}); err != nil {
		t.Fatal(err)
	}
	group := "later-wire"
	first.WireGroup = &group
	second := first
	second.CanonicalURL, second.Outlet = "https://outlet5.example/story", "Outlet 5"
	if err := s.UpsertCandidates(ctx, a, []candidate.Candidate{first, second}); err != nil {
		t.Fatal(err)
	}
	ed := draftEdition()
	ed.Schema = 2
	item := &ed.Sections[0].Items[0]
	item.Source, item.URL = first.Outlet, first.CanonicalURL
	item.Sources = []edition.Source{{Outlet: second.Outlet, URL: second.CanonicalURL}}
	layout := &store.EditionLayout{Ratings: coverageRatings(), Sections: []store.LayoutSection{{Key: "news", Lean: true}}}
	if err := s.Publish(ctx, a, "2026-10-30", ed, nil, layout); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.EditionCoverage(ctx, a, "2026-10-30")
	if err != nil || snapshot == nil {
		t.Fatal(snapshot, err)
	}
	if bar := snapshot.Items[item.ID]; bar.Right != 1 || bar.Left != 0 || bar.Center != 0 {
		t.Fatalf("later wire grouping counted twice: %+v", bar)
	}
}
