package reader

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/candidate"
)

func TestParagraphs(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"One.\n\nTwo  words.\r\n \r\nThree", []string{"One.", "Two words.", "Three"}},
		{"Line one\nLine two\n", []string{"Line one", "Line two"}},
		{"First para\nwraps here.\n\nSecond.", []string{"First para wraps here.", "Second."}},
		{"  \n\n ", []string{}},
	} {
		if got := paragraphs(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("paragraphs(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	long := strings.Repeat("word ", 400)
	two, ten := 2, 10
	if !partial("Only a teaser.", nil) || partial(long, nil) || partial(long, &two) || !partial(long, &ten) {
		t.Fatal("partial heuristics")
	}
}

func TestReadingView(t *testing.T) {
	f := setup(t)
	const date = "2026-10-28"
	f.publish(t, f.a, date, sampleEdition(), nil)
	fetched := time.Date(2026, 10, 28, 4, 30, 0, 0, time.UTC)
	text := "The council met \"late\" & voted 7<9.\n\n" + strings.TrimSpace(strings.Repeat("More reporting follows here. ", 90))
	snap := func(link, text, state string) candidate.Candidate {
		return candidate.Candidate{Section: "economy", CanonicalURL: link, Outlet: "Wire", Title: "Story", FetchedAt: fetched, Text: text, State: state}
	}
	if err := f.db.UpsertCandidates(t.Context(), f.a, []candidate.Candidate{snap("https://example.test/story", text, "ok"), snap("https://example.test/faith", "", "unavailable")}); err != nil {
		t.Fatal(err)
	}
	// Another paper's snapshot of the same page is not this paper's copy.
	if err := f.db.UpsertCandidates(t.Context(), f.b, []candidate.Candidate{snap("https://example.test/faith", text, "ok")}); err != nil {
		t.Fatal(err)
	}
	r := f.signIn(t, "")
	r.Body.Close()

	page := body(t, f.get(t, "/e/"+date))
	if !strings.Contains(page, `<a href="/read/`+date+`/story-a" class="read-here">Read here ›</a><a href="https://example.test/story" target="_blank" rel="noopener noreferrer">At Wire</a>`) {
		t.Fatal("edition has no reading link for a story with text")
	}
	if strings.Contains(page, "/read/"+date+"/faith-a") {
		t.Fatal("reading link for a story without text")
	}

	r = f.get(t, "/read/"+date+"/story-a")
	html := body(t, r)
	if r.StatusCode != 200 || r.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("story: %d %q", r.StatusCode, r.Header.Get("Cache-Control"))
	}
	for _, want := range []string{
		`<a href="/e/` + date + `#item-story-a" class="back">`, "Back to the paper",
		`<a href="https://example.test/story" target="_blank" rel="noopener noreferrer" class="open-at">Open at Wire`,
		`<p>The council met &#34;late&#34; &amp; voted 7&lt;9.</p>`,
		"gathered October 27 at 11:30 pm", // the paper's own time zone
		`data-story="a/` + date + `/story-a"`, "story.js", "story.css",
		`<h1>&lt;script&gt;alert(&#39;x&#39;)&lt;/script&gt;</h1>`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("story page lacks %q:\n%s", want, html)
		}
	}
	if strings.Contains(html, "story-note") {
		t.Fatal("a full snapshot is flagged as partial")
	}

	html = body(t, f.get(t, "/read/"+date+"/faith-a"))
	if !strings.Contains(html, "kept no copy of this piece's text. Read it at Local.") || !strings.Contains(html, `href="https://example.test/faith"`) || strings.Contains(html, "More reporting") {
		t.Fatalf("story without a snapshot:\n%s", html)
	}

	// A teaser is shown with an honest note.
	if err := f.db.UpsertCandidates(t.Context(), f.a, []candidate.Candidate{snap("https://example.test/teaser", "Subscribe to keep reading.", "ok")}); err != nil {
		t.Fatal(err)
	}
	ed := sampleEdition()
	ed.Sections[1].Items[0].URL = "https://example.test/teaser"
	f.publish(t, f.a, "2026-10-29", ed, nil)
	html = body(t, f.get(t, "/read/2026-10-29/story-a"))
	if !strings.Contains(html, `class="story-note"`) || !strings.Contains(html, "Subscribe to keep reading.") {
		t.Fatalf("partial snapshot without a note:\n%s", html)
	}

	for _, path := range []string{"/read/" + date + "/missing", "/read/2026-01-01/story-a", "/read/not-a-date/story-a", "/p/b/read/" + date + "/faith-a"} {
		if r := f.get(t, path); r.StatusCode != 404 {
			t.Fatalf("%s: %d", path, r.StatusCode)
		} else {
			r.Body.Close()
		}
	}
}
