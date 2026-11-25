package reader

import (
	"strings"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/candidate"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/store"
)

func TestProvenanceFromWarnings(t *testing.T) {
	ed := &edition.Edition{Sections: []edition.Section{
		{Key: "a", Items: []edition.Item{
			{ID: "clean", Source: "Wire"},
			{ID: "numbers", Source: "Wire", URL: "https://wire.example/n", Sources: []edition.Source{{URL: "https://second.example/x"}}},
		}},
		{Key: "b", Items: []edition.Item{
			{ID: "unchecked", Source: "Herald", URL: "https://herald.example/a"},
			{ID: "partial", Source: "Herald", URL: "https://herald.example/b", Sources: []edition.Source{{URL: "https://c.example/1", Outlet: "C"}, {URL: "https://d.example/1", Outlet: "D"}}},
			{ID: "twice", Source: "Wire", URL: "https://wire.example/1", Sources: []edition.Source{{URL: "https://wire.example/2", Outlet: "Wire"}, {URL: "https://wire.example/3", Outlet: "Wire"}}},
		}},
	}}
	warnings := []store.DiagnosticProblem{
		{Path: "sections[0].items[1].summary", Rule: "unsupported-number: 42"},
		{Path: "sections[0].items[1].summary", Rule: "unsupported-number: 42"},
		{Path: "sections[0].items[1].title", Rule: "unsupported-name: Smith"},
		{Path: "sections[0].items[1].sources[0].url", Rule: "source fetch failed; claim unverified"},
		{Path: "sections[1].items[0].url", Rule: "no stored snapshot of this source; claim unverified"},
		{Path: "sections[1].items[1].sources[1].url", Rule: "no stored snapshot of this source; claim unverified"},
		{Path: "sections[1].items[2].sources[0].url", Rule: "no stored snapshot of this source; claim unverified"},
		{Path: "sections[1].items[2].sources[1].url", Rule: "no stored snapshot of this source; claim unverified"},
		// Not about a story's sources, or out of range: ignored.
		{Path: "lead", Rule: "missing"},
		{Path: "sections[4].items[0].title", Rule: "unsupported-name: X"},
		{Path: "sections[0].items[0].kind", Rule: "unknown"},
	}
	at := time.Date(2026, 11, 25, 5, 4, 0, 0, time.UTC)
	got := provenanceFrom(ed, warnings, at)
	if v := got["clean"]; v.Status != "checked" || len(v.Notes) != 0 || v.Checked != "Nov 25, 5:04 am UTC" {
		t.Fatalf("clean: %+v", v)
	}
	v := got["numbers"]
	if v.Status != "notes" || v.Summary != "2 notes from the source check · checked against 1 of 2 sources" || len(v.Notes) != 3 {
		t.Fatalf("numbers: %+v", v)
	}
	want := []string{"The figure “42” in the summary was not found in the source.", "The name “Smith” in the headline was not found in the source.", "Not checked against second.example: no text could be read from the page."}
	for i := range want {
		if v.Notes[i] != want[i] {
			t.Fatalf("note %d = %q, want %q", i, v.Notes[i], want[i])
		}
	}
	if v := got["unchecked"]; v.Status != "unchecked" || v.Notes[0] != "Not checked against Herald: Night Desk kept no copy of that page." {
		t.Fatalf("unchecked: %+v", v)
	}
	if v := got["partial"]; v.Status != "notes" || v.Summary != "Checked against 2 of 3 sources" || v.Notes[0] != "Not checked against D: Night Desk kept no copy of that page." {
		t.Fatalf("partial: %+v", v)
	}
	if v := got["twice"]; v.Summary != "Checked against 1 of 3 sources" || len(v.Notes) != 1 || v.Notes[0] != "Not checked against Wire: Night Desk kept no copy of that page. (2 pages)" || v.Compared != 1 || v.Cited != 3 {
		t.Fatalf("twice: %+v", v)
	}
}

func TestProvenanceOnPages(t *testing.T) {
	f := setup(t)
	ed := sampleEdition()
	date := "2026-11-24"
	// Without a publish record the paper shows no source-check lines.
	f.publish(t, f.a, date, ed, nil)
	r := f.signIn(t, "")
	r.Body.Close()
	if html := body(t, f.get(t, "/e/"+date)); strings.Contains(html, `class="pv`) {
		t.Fatal("source-check line without a record")
	}
	// The story's text is stored so the reading view has it.
	if err := f.db.UpsertCandidates(t.Context(), f.a, []candidate.Candidate{{Section: "economy", CanonicalURL: "https://example.test/story", Outlet: "Wire", Title: "Story", FetchedAt: time.Now().UTC(), Text: "Text.", State: "ok"}}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.PublishWithDiagnostics(t.Context(), f.a, date, ed, nil, store.PublishDiagnostics{Path: "put", SourceChecks: []store.SourceCheckCount{{Status: "failed", Rule: "unsupported-number", Count: 1}}, Warnings: []store.DiagnosticProblem{
		{Path: "sections[1].items[0].summary", Rule: "unsupported-number: 1,200"},
		{Path: "sections[0].items[0].url", Rule: "no stored snapshot of this source; claim unverified"},
	}}); err != nil {
		t.Fatal(err)
	}
	html := body(t, f.get(t, "/e/"+date))
	for _, want := range []string{
		`<details class="pv notes"><summary>1 note from the source check</summary><ul><li>The figure “1,200” in the summary was not found in the source.</li>`,
		`<details class="pv unchecked"><summary>Not checked against the source</summary><ul><li>Not checked against Local: Night Desk kept no copy of that page.</li>`,
		`<p class="pv-at">Checked against sources `,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("edition lacks %q", want)
		}
	}
	html = body(t, f.get(t, "/read/"+date+"/story-a"))
	if !strings.Contains(html, `<details class="pv notes"><summary>1 note from the source check</summary><p>When this edition went out on `) || !strings.Contains(html, "1,200") {
		t.Fatal("reading view lacks the source check")
	}
	// A later publish without findings replaces the record.
	if err := f.db.PublishWithDiagnostics(t.Context(), f.a, date, ed, nil, store.PublishDiagnostics{Path: "put", SourceChecks: []store.SourceCheckCount{{Status: "passed", Rule: "source text", Count: 1}}}); err != nil {
		t.Fatal(err)
	}
	if html := body(t, f.get(t, "/e/"+date)); strings.Contains(html, "1,200") || !strings.Contains(html, `<p class="pv">Checked against the source</p>`) {
		t.Fatal("stale source-check record shown")
	}
}
