package reader

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/lean"
	"github.com/irashack/night-desk/internal/store"
)

func TestReaderCoverageSnapshot(t *testing.T) {
	f := setup(t)
	ratings := &lean.Ratings{Source: lean.Source{Attribution: "Ratings by Example Cooperative"}}
	item := edition.Item{ID: "one", Kind: "news", Title: "Story", Summary: "Summary", Source: "Right 0", URL: "https://right0.example/story"}
	for i := 0; i < 5; i++ {
		domain := fmt.Sprintf("right%d.example", i)
		outlet := fmt.Sprintf("Right %d", i)
		ratings.Ratings = append(ratings.Ratings, lean.Rating{Outlet: outlet, Domains: []string{domain}, Lean: lean.Right})
		if i > 0 {
			item.Sources = append(item.Sources, edition.Source{Outlet: outlet, URL: "https://" + domain + "/story"})
		}
	}
	layout := &store.EditionLayout{Masthead: "Published masthead", Ratings: ratings, Sections: []store.LayoutSection{{Key: "economy", Title: "Published section", Lean: true}}}
	ed := &edition.Edition{Schema: 2, Lead: "one", Sections: []edition.Section{{Key: "economy", Items: []edition.Item{item}}}}
	if err := f.db.Publish(context.Background(), f.a, "2026-10-30", ed, nil, layout); err != nil {
		t.Fatal(err)
	}
	// Live ratings and section policy are irrelevant to the archived rendering.
	f.cfg.Lean.Ratings = &lean.Ratings{Source: lean.Source{Attribution: "Changed attribution"}}
	f.cfg.Papers[0].Sections = []config.Section{{Key: "other", Title: "New section"}}
	r := f.signIn(t, "")
	r.Body.Close()
	page := body(t, f.get(t, "/e/2026-10-30"))
	for _, want := range []string{
		`class="coverage-bar" aria-hidden="true"`,
		`class="coverage-right"`,
		"Coverage found among monitored sources: 0 left · 0 center · 5 right",
		"Blindspot: little coverage from the left",
		"Ratings by Example Cooperative",
		`<footer class="coverage-attribution">`,
		"As of publication",
		"Side searches enabled for this section",
		"0 unrated · 0 opinion excluded · 0 failed fetches",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("missing %q: %s", want, page)
		}
	}
	if strings.Contains(page, "Changed attribution") {
		t.Fatal("archive used live attribution")
	}
	// Enabling a section without installed ratings leaves the reader unchanged.
	layout.Ratings = nil
	if err := f.db.Publish(context.Background(), f.a, "2026-10-29", ed, nil, layout); err != nil {
		t.Fatal(err)
	}
	page = body(t, f.get(t, "/e/2026-10-29"))
	if strings.Contains(page, `class="coverage"`) || strings.Contains(page, `class="coverage-attribution"`) || strings.Contains(page, "Blindspot") {
		t.Fatal("reader displayed coverage without ratings")
	}
}

func TestCoverageWindow(t *testing.T) {
	snapshot := &store.EditionCoverage{From: "2026-10-29T08:00:00.000000000Z", Through: "2026-10-30T08:30:00Z"}
	if got, want := coverageWindow(snapshot), "Named sources fetched Oct 29, 2026 08:00 UTC through publication Oct 30, 2026 08:30 UTC"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
