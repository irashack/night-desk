package reader

import (
	"strings"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/candidate"
	"github.com/irashack/night-desk/internal/edition"
)

func TestSameStory(t *testing.T) {
	transit := [2]string{"City council approves downtown transit budget", "The council voted 7-2 to fund the Riverside streetcar extension and new bus lanes."}
	rail := [2]string{"Senate passes bipartisan railway safety bill", "The bill tightens rules for hazardous freight after the East Palestine derailment."}
	boeing := [2]string{"Boeing halts 737 Max deliveries over wiring flaw", "The planemaker found a wiring defect in some jets awaiting delivery."}
	storm := [2]string{"Hurricane Delia makes landfall in Florida", "The category 3 storm came ashore near Cedar Key with 120 mph winds."}
	strike := [2]string{"Teachers strike shuts Cedar Falls schools", "Thousands of teachers walked out over pay, closing 80 schools."}
	court := [2]string{"Court blocks Texas immigration law", "A federal judge halted the state law that let police arrest migrants."}
	rates := [2]string{"Federal Reserve holds interest rates steady", "The Fed kept its benchmark rate unchanged and signalled two cuts this year."}
	for _, tc := range []struct {
		item      [2]string
		candidate string
		want      bool
	}{
		{transit, "Downtown transit budget faces lawsuit from council critics", true},
		{rail, "House takes up Senate railway safety bill", true},
		{boeing, "Boeing resumes 737 Max deliveries after wiring fix", true},
		{storm, "Hurricane Delia leaves 200,000 without power in Florida", true},
		{strike, "Cedar Falls teachers' strike enters second week", true},
		{court, "Appeals court reinstates Texas immigration law", true},
		// Same topic, different story.
		{transit, "County council approves parks budget", false},
		{rates, "Bank of England holds interest rates", false},
		{strike, "Nurses strike at Cedar Falls General hospital", false},
		{storm, "Tropical storm Ernesto forms in the Gulf, could threaten Florida", false},
		{boeing, "Airbus delays A350 deliveries over supplier shortages", false},
		// Shared words only in the summary do not make the titles alike.
		{transit, "Riverside streetcar extension and bus lanes get federal grant", false},
		// Stopwords and short words never count.
		{rail, "What they said about the bill this week", false},
	} {
		title, words := significant(tc.item[0]), significant(tc.item[0]+" "+tc.item[1])
		if got := sameStory(title, words, significant(tc.candidate)); got != tc.want {
			t.Errorf("%q vs %q: got %v", tc.item[0], tc.candidate, got)
		}
	}
}

func TestSinceFollowUps(t *testing.T) {
	f := setup(t)
	loc, _ := time.LoadLocation("America/Chicago")
	at := func(day string, hour int) time.Time {
		d, err := time.ParseInLocation("2006-01-02", day, loc)
		if err != nil {
			t.Fatal(err)
		}
		return d.Add(time.Duration(hour) * time.Hour)
	}
	f.s.now = func() time.Time { return at("2026-10-30", 18) }
	ed := sampleEdition()
	ed.Schema = 2
	story := &ed.Sections[1].Items[0]
	story.Title = "City council approves downtown transit budget"
	story.Summary = "The council voted 7-2 to fund the Riverside streetcar extension and new bus lanes."
	story.Sources = []edition.Source{{Outlet: "Second", URL: "https://second.example/transit"}}
	opinion := *story
	opinion.ID, opinion.Kind, opinion.URL, opinion.Sources = "transit-view", "opinion", "https://opinion.example/transit", nil
	ed.Sections[1].Items = append(ed.Sections[1].Items, opinion)
	for _, date := range []string{"2026-10-01", "2026-10-27", "2026-10-30"} {
		f.publish(t, f.a, date, ed, nil)
	}
	group := "wire-transit"
	snap := func(link, title string, fetched time.Time) candidate.Candidate {
		return candidate.Candidate{Section: "economy", CanonicalURL: link, Outlet: "Outlet " + link[8:12], Title: title, FetchedAt: fetched, Text: "Text.", State: "ok"}
	}
	match := "Downtown transit budget faces lawsuit from council critics"
	source := snap("https://example.test/story", story.Title, at("2026-10-26", 9))
	source.WireGroup = &group
	grouped := snap("https://grouped.example/a", "Officials respond to riders", at("2026-10-29", 10))
	grouped.WireGroup = &group
	hostile := snap("https://hostile.example/a?x=1&y=2", `Council critics sue over downtown transit budget & "fees" <3 'x'`, at("2026-10-29", 12))
	mirror := snap("https://mirror.example/story", match, at("2026-10-29", 13))
	mirror.Aliases = []string{"https://example.test/story"}
	unavailable := snap("https://gone.example/a", match, at("2026-10-29", 14))
	unavailable.State, unavailable.Text = "unavailable", ""
	batch := []candidate.Candidate{
		source, grouped, hostile, mirror, unavailable,
		snap("https://newest.example/a", "Transit budget vote: council downtown plan stalls", at("2026-10-30", 8)),
		snap("https://oldest.example/a", match, at("2026-10-28", 9)),
		snap("https://sameday.example/a", match, at("2026-10-27", 22)),
		snap("https://earlier.example/a", match, at("2026-10-25", 9)),
		snap("https://second.example/transit", match, at("2026-10-29", 9)),
		snap("https://nearmiss.example/a", "County council approves parks budget", at("2026-10-29", 9)),
		snap("https://pruned.example/a", match, at("2026-10-10", 9)),
	}
	if err := f.db.UpsertCandidates(t.Context(), f.a, batch); err != nil {
		t.Fatal(err)
	}
	if err := f.db.UpsertCandidates(t.Context(), f.b, []candidate.Candidate{snap("https://otherpaper.example/a", match, at("2026-10-30", 9))}); err != nil {
		t.Fatal(err)
	}
	r := f.signIn(t, "")
	r.Body.Close()
	html := body(t, f.get(t, "/e/2026-10-27"))
	start := strings.Index(html, `id="item-story-a"`)
	if start < 0 {
		t.Fatal("missing story")
	}
	item := html[start:]
	item = item[:strings.Index(item, `class="actions`)]
	want := `<p class="sources">Since published: <a href="https://newest.example/a" target="_blank" rel="noopener noreferrer">Transit budget vote: council downtown plan stalls</a> (Outlet newe, <time datetime="2026-10-30">Oct 30</time>) · ` +
		`<a href="https://hostile.example/a?x=1&amp;y=2" target="_blank" rel="noopener noreferrer">Council critics sue over downtown transit budget &amp; &#34;fees&#34; &lt;3 &#39;x&#39;</a> (Outlet host, <time datetime="2026-10-29">Oct 29</time>) · ` +
		`<a href="https://grouped.example/a" target="_blank" rel="noopener noreferrer">Officials respond to riders</a> (Outlet grou, <time datetime="2026-10-29">Oct 29</time>)</p>`
	if !strings.Contains(item, want) {
		t.Fatalf("follow-ups:\n%s", item)
	}
	if strings.Count(html, "Since published") != 2 || strings.Contains(html, "oldest.example") {
		t.Fatal("follow-ups must show on the lead and its story only, three at most")
	}
	// Window edges, unavailable snapshots and the item's own sources, and other
	// papers' candidates, never appear; the cap hides oldest.example above.
	for _, absent := range []string{"sameday.example", "earlier.example", "gone.example", "mirror.example", "nearmiss.example", "otherpaper.example", "pruned.example", `href="https://second.example/transit" target`} {
		if strings.Contains(html, absent) {
			t.Errorf("unexpected follow-up %s", absent)
		}
	}
	// Retention bounds an old edition's window.
	old := body(t, f.get(t, "/e/2026-10-01"))
	if !strings.Contains(old, "newest.example") || strings.Contains(old, "pruned.example") {
		t.Fatal("retention window not applied")
	}
	// Today's edition has no "since" yet.
	if today := body(t, f.get(t, "/e/2026-10-30")); strings.Contains(today, "Since published") {
		t.Fatal("today's edition showed follow-ups")
	}
}
