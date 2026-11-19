package reader

import (
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/edition"
)

func TestReaderEditionV2(t *testing.T) {
	f := setup(t)
	ed := sampleEdition()
	ed.Schema = 2
	byline := "Reporter & Editor"
	otherByline := "Second & Writer"
	news := ed.Sections[1].Items[0]
	news.Byline = &byline
	news.Sources = []edition.Source{
		{Outlet: "Other & Outlet", URL: "https://other.example/story?a=1&b=2", Byline: &otherByline},
		{Outlet: "Third outlet", URL: "https://third.example/story"},
	}
	opinion := news
	opinion.ID, opinion.Kind, opinion.Title = "opinion-first", "opinion", "First opinion"
	opinion.Sources = nil
	secondNews := news
	secondNews.ID, secondNews.Title = "news-second", "Second news"
	secondOpinion := opinion
	secondOpinion.ID, secondOpinion.Title = "opinion-second", "Second opinion"
	// Deliberately interleave opinion before news. Both groups retain their order.
	ed.Sections[1].Items = []edition.Item{opinion, news, secondOpinion, secondNews}
	f.publish(t, f.a, "2026-10-27", ed, nil)
	r := f.signIn(t, "")
	r.Body.Close()
	html := body(t, f.get(t, "/"))
	for _, want := range []string{
		`<span>Wire</span><span class="byline">Reporter &amp; Editor</span>`,
		`Also covered by <a href="https://other.example/story?a=1&amp;b=2" rel="noopener noreferrer">Other &amp; Outlet</a>, <span class="byline">Second &amp; Writer</span>`,
		`<a href="https://third.example/story" rel="noopener noreferrer">Third outlet</a>`,
		`<span class="tag opinion">Opinion</span>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %s", want)
		}
	}
	start := strings.Index(html, `id="section-economy"`)
	end := strings.Index(html, `id="section-faith"`)
	if start < 0 || end <= start {
		t.Fatal("missing sections")
	}
	section := html[start:end]
	previous := -1
	for _, id := range []string{"story-a", "news-second", "opinion-first", "opinion-second"} {
		position := strings.Index(section, `data-item="`+id+`"`)
		if position <= previous {
			t.Fatalf("item %s out of order", id)
		}
		previous = position
	}
	front := html[:start]
	if strings.Contains(front, `class="tag opinion"`) {
		t.Fatal("opinion rendered on front page")
	}
	if !strings.Contains(front, "Also covered by") {
		t.Fatal("lead omitted further coverage")
	}
	// Rendering must not mutate the stored editorial order.
	stored, err := f.db.Edition(t.Context(), f.a, "2026-10-27")
	if err != nil || stored.Sections[1].Items[0].ID != "opinion-first" {
		t.Fatal("stored order changed", err)
	}
}

func TestReaderEditionV2Escaping(t *testing.T) {
	f := setup(t)
	ed := sampleEdition()
	ed.Schema = 2
	malicious := `<script>alert("byline")</script>`
	i := &ed.Sections[1].Items[0]
	i.Byline = &malicious
	i.Sources = []edition.Source{{Outlet: malicious, URL: "https://other.example", Byline: &malicious}}
	f.publish(t, f.a, "2026-10-27", ed, nil)
	r := f.signIn(t, "")
	r.Body.Close()
	html := body(t, f.get(t, "/"))
	if strings.Contains(html, "<script>alert") || !strings.Contains(html, "&lt;script&gt;alert") {
		t.Fatal("new source text was not escaped")
	}
}

func TestOpinionShownBesideItsStory(t *testing.T) {
	f := setup(t)
	ed := sampleEdition()
	ed.Schema = 2
	news := ed.Sections[1].Items[0]
	linked := news
	linked.ID, linked.Kind, linked.Title, linked.Source, linked.URL = "opinion-linked", "opinion", "The plan & its <costs>", "Op & Ed", "https://opinion.example/column"
	about, stance := news.ID, "Argues the plan costs too much & helps too few."
	linked.About, linked.Stance = &about, &stance
	loose := linked
	loose.ID, loose.Title, loose.About, loose.Stance = "opinion-loose", "A column on its own", nil, nil
	ed.Sections[1].Items = []edition.Item{linked, news, loose}
	f.publish(t, f.a, "2026-10-27", ed, nil)
	f.signIn(t, "").Body.Close()
	html := body(t, f.get(t, "/"))
	start := strings.Index(html, `id="section-economy"`)
	section := html[start:strings.Index(html, `id="section-faith"`)]
	story := section[strings.Index(section, `data-item="`+news.ID+`"`):]
	block := story[:strings.Index(story, `class="actions`)]
	want := `<div class="opinions"><span class="label">Opinion on this story</span><p class="sources"><b>Op &amp; Ed</b>: Argues the plan costs too much &amp; helps too few. <a href="https://opinion.example/column" target="_blank" rel="noopener noreferrer">The plan &amp; its &lt;costs&gt; ›</a></p></div>`
	if !strings.Contains(block, want) {
		t.Fatalf("opinion missing under its story:\n%s", block)
	}
	if strings.Contains(section, `data-item="opinion-linked"`) {
		t.Error("a linked opinion is shown beside its story, not again in the section")
	}
	if !strings.Contains(section, `data-item="opinion-loose"`) {
		t.Error("an opinion without a story keeps its own place")
	}
}
