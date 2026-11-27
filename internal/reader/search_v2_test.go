package reader

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/edition"
)

func ptr(s string) *string { return &s }

// v2Edition has a byline, further coverage with a writer, and an opinion column.
func v2Edition() *edition.Edition {
	ed := sampleEdition()
	ed.Schema = 2
	story := &ed.Sections[1].Items[0]
	story.Title = "Council approves the river plan"
	story.Summary = "Councillors voted for the flood plan on Tuesday."
	story.Byline = ptr("Ana Ruiz")
	story.Sources = []edition.Source{
		{Outlet: "County Herald", URL: "https://herald.example/river", Byline: ptr("Lee Park")},
		{Outlet: "Valley Radio", URL: "https://radio.example/river"},
	}
	ed.Sections[1].Items = append(ed.Sections[1].Items, edition.Item{
		ID: "column-a", Kind: "opinion", Title: "The plan is too timid", Summary: "A column on the vote.",
		Source: "Wire Opinion", URL: "https://example.test/column", Byline: ptr("Sam Hill"),
		About: ptr("story-a"), Stance: ptr("The levees should go higher"),
	})
	return ed
}

func TestArchiveSearchByWriterStanceAndOutlets(t *testing.T) {
	f := setup(t)
	f.publish(t, f.a, "2026-10-27", v2Edition(), nil)
	foreign := v2Edition()
	foreign.Sections[1].Items[0].Title = "Another paper's river plan"
	f.publish(t, f.b, "2026-10-27", foreign, nil)
	f.signIn(t, "").Body.Close()
	story, column := `/e/2026-10-27#item-story-a`, `/e/2026-10-27#item-column-a`
	for _, tc := range []struct {
		in, query     string
		story, column bool
	}{
		{"", "ruiz", true, false},
		{"", "herald", true, false},
		{"", "levees", false, true},
		{"writer", "ana ruiz", true, false},
		{"writer", "lee park", true, false}, // a further-coverage writer
		{"writer", "sam", false, true},
		{"writer", "council", false, false}, // only in the title
		{"writer", "herald", false, false},  // an outlet, not a writer
		{"stance", "levees higher", false, true},
		{"stance", "timid", false, false}, // only in the column's title
		{"outlets", "county herald", true, false},
		{"outlets", "valley", true, false},
		{"outlets", "park", false, false}, // a writer, not an outlet
		{"outlets", "wire", false, false}, // the item's own source
	} {
		html := body(t, f.get(t, "/search?in="+tc.in+"&q="+url.QueryEscape(tc.query)))
		if strings.Contains(html, story) != tc.story || strings.Contains(html, column) != tc.column {
			t.Errorf("in=%q q=%q: story %v column %v, want %v %v", tc.in, tc.query,
				strings.Contains(html, story), strings.Contains(html, column), tc.story, tc.column)
		}
		if strings.Contains(html, "Another paper") {
			t.Fatalf("in=%q q=%q leaked another paper", tc.in, tc.query)
		}
	}
	html := body(t, f.get(t, "/search?in=writer&q=sam"))
	for _, want := range []string{`<span class="tag opinion">Opinion</span>`, `Argues: The levees should go higher`,
		`href="/search?in=writer&amp;q=Sam%20Hill"`, `value="writer" checked`} {
		if !strings.Contains(html, want) {
			t.Errorf("writer result missing %s", want)
		}
	}
	html = body(t, f.get(t, "/search?q=river"))
	for _, want := range []string{`Also covered by`, `href="/search?in=outlets&amp;q=County%20Herald"`,
		`>Valley Radio</a>`, `href="/search?in=writer&amp;q=Ana%20Ruiz"`, `value="" checked`} {
		if !strings.Contains(html, want) {
			t.Errorf("result missing %s", want)
		}
	}
	// Replacing the edition replaces the indexed bylines and outlets.
	ed := v2Edition()
	ed.Sections[1].Items[0].Byline = ptr("Kim Ode")
	ed.Sections[1].Items[0].Sources = nil
	f.publish(t, f.a, "2026-10-27", ed, nil)
	for _, path := range []string{"/search?in=writer&q=ruiz", "/search?in=outlets&q=herald"} {
		if strings.Contains(body(t, f.get(t, path)), story) {
			t.Errorf("%s matched a replaced byline or outlet", path)
		}
	}
	if !strings.Contains(body(t, f.get(t, "/search?in=writer&q=ode")), story) {
		t.Error("replacement byline not indexed")
	}
}

func TestArchiveSearchFieldCannotInjectSyntax(t *testing.T) {
	f := setup(t)
	f.publish(t, f.a, "2026-10-27", v2Edition(), nil)
	f.signIn(t, "").Body.Close()
	for _, in := range []string{"byline", "title", "{title}", "writer stance", "WRITER"} {
		r := f.get(t, "/search?q=ruiz&in="+url.QueryEscape(in))
		if r.StatusCode != http.StatusBadRequest || !strings.Contains(body(t, r), "in: must be writer, stance, outlets or omitted") {
			t.Errorf("in=%q accepted", in)
		}
	}
	// Query words stay quoted inside a column filter.
	for _, q := range []string{`ruiz) OR (council`, `ruiz NOT ana`, `title:council`, `*`} {
		r := f.get(t, "/search?in=writer&q="+url.QueryEscape(q))
		if r.StatusCode != 200 {
			t.Errorf("q=%q returned %d", q, r.StatusCode)
		}
		if html := body(t, r); strings.Contains(html, `/e/2026-10-27#item-`) {
			t.Errorf("q=%q matched through operators", q)
		}
	}
	// Saved-only combines with a field.
	if html := body(t, f.get(t, "/search?in=writer&q=ruiz&saved=1")); strings.Contains(html, `/e/2026-10-27#item-`) {
		t.Error("unsaved story matched a saved-only writer search")
	}
}
