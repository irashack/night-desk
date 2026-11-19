package reader

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/store"
)

func TestHistoricalLayoutRendersEveryStory(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		name := "legacy"
		if snapshot {
			name = "snapshot"
		}
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			ed := sampleEdition()
			ed.Sections = append(ed.Sections, edition.Section{Key: "forgotten", Items: []edition.Item{{ID: "forgotten-1", Title: "Never hide this story", Source: "Archive", URL: "https://example.test/old"}}})
			var layout *store.EditionLayout
			if snapshot {
				layout = &store.EditionLayout{Masthead: "The Historical Gazette", Sections: []store.LayoutSection{{Key: "faith", Title: "Historical Faith"}, {Key: "economy", Title: "Historical Economy"}}}
			}
			if err := f.db.Publish(context.Background(), f.a, "2026-10-27", ed, nil, layout); err != nil {
				t.Fatal(err)
			}
			// Remove faith, rename economy, and add a section after publication.
			f.cfg.Papers[0].Masthead = "The Current Gazette"
			f.cfg.Papers[0].Sections = []config.Section{{Key: "economy", Title: "Current Economy"}, {Key: "new", Title: "New section"}}
			r := f.signIn(t, "")
			r.Body.Close()
			r = f.get(t, "/e/2026-10-27")
			status := r.StatusCode
			html := body(t, r)
			if status != 200 {
				t.Fatalf("render: %d %s", status, html)
			}
			for _, want := range []string{`data-item="faith-a"`, `data-item="story-a"`, `data-item="forgotten-1"`, `data-title="forgotten"`, "Never hide this story"} {
				if !strings.Contains(html, want) {
					t.Errorf("missing %s", want)
				}
			}
			masthead, titles := "The Current Gazette", []string{"Current Economy", "New section", "faith", "forgotten"}
			if snapshot {
				masthead, titles = "The Historical Gazette", []string{"Historical Faith", "Historical Economy", "forgotten"}
				if strings.Contains(html, `data-title="New section"`) {
					t.Error("new section leaked into history")
				}
			}
			if !strings.Contains(html, `<h1 class="mast-name">`+masthead+`</h1>`) {
				t.Error("wrong masthead")
			}
			prev := -1
			for _, title := range titles {
				pos := strings.Index(html, `data-title="`+title+`"`)
				if pos <= prev {
					t.Errorf("section order %s (%d after %d)", title, pos, prev)
				}
				prev = pos
			}
		})
	}
}

func TestLayoutSectionFallbacks(t *testing.T) {
	p := config.Paper{Sections: []config.Section{{Key: "current", Title: "Current title"}}}
	ed := &edition.Edition{Sections: []edition.Section{{Key: "current"}, {Key: "retired"}}}
	for _, tc := range []struct {
		name   string
		layout *store.EditionLayout
	}{
		{"before layouts", nil},
		{"missing section", &store.EditionLayout{}},
		{"missing title", &store.EditionLayout{Sections: []store.LayoutSection{{Key: "current"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := []store.LayoutSection{{Key: "current", Title: "Current title"}, {Key: "retired", Title: "retired"}}
			if got := editionSections(p, ed, tc.layout); !reflect.DeepEqual(got, want) {
				t.Fatalf("sections: %+v", got)
			}
		})
	}
}
