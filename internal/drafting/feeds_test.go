package drafting

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/irashack/night-desk/internal/edition"
)

func feedCandidate(id, section string) Candidate {
	return Candidate{ID: id, Section: section, URL: "https://example.com/" + id,
		Outlet: "The Desk", Title: "Story " + id, Text: "First sentence. Second sentence. Third sentence."}
}

func TestFeedsOnlyOrderAndDedupe(t *testing.T) {
	p := Paper{Sections: []Section{{Key: "empty"}, {Key: "local"}, {Key: "world"}}}
	first := feedCandidate("first", "local")
	first.WireGroup = "wire"
	wire := feedCandidate("wire", "world")
	wire.WireGroup = first.WireGroup
	sameURL := feedCandidate("same-url", "world")
	sameURL.URL = wire.URL
	sameTitle := feedCandidate("same-title", "world")
	sameTitle.Title = sameURL.Title
	other := feedCandidate("other", "world")
	candidates := []Candidate{sameTitle, sameURL, wire, other, first}
	e, problems, err := FeedsOnly(p, candidates)
	if err != nil || len(problems) != 0 {
		t.Fatal(problems, err)
	}
	if len(e.Sections) != 2 || e.Sections[0].Key != "local" || e.Sections[1].Key != "world" ||
		len(e.Sections[0].Items) != 1 || len(e.Sections[1].Items) != 1 {
		t.Fatalf("unexpected sections: %+v", e.Sections)
	}
	lead := e.Sections[0].Items[0]
	if e.Lead != lead.ID || lead.Title != first.Title || lead.URL != first.URL || lead.Source != first.Outlet ||
		lead.Summary != "First sentence. Second sentence." || e.Sections[1].Items[0].URL != other.URL {
		t.Fatalf("unexpected edition: %+v", e)
	}
	again, _, _ := FeedsOnly(p, candidates)
	if !reflect.DeepEqual(again, e) {
		t.Fatal("edition changed for identical input")
	}
}

func TestFeedsOnlyLimits(t *testing.T) {
	p := Paper{Sections: []Section{{Key: "local"}, {Key: "world", Required: true}},
		Limits: edition.Limits{ItemsPerSection: 2, ItemsPerEdition: 3}}
	var candidates []Candidate
	for _, section := range p.Sections {
		for i := range 8 {
			candidates = append(candidates, feedCandidate(fmt.Sprintf("%s-%d", section.Key, i), section.Key))
		}
	}
	e, _, err := FeedsOnly(p, candidates)
	if err != nil || len(e.Sections) != 2 || len(e.Sections[0].Items) != 2 || len(e.Sections[1].Items) != 1 {
		t.Fatalf("edition = %+v, error = %v", e, err)
	}
	p.Limits = edition.Limits{}
	e, _, err = FeedsOnly(p, candidates)
	if err != nil || len(e.Sections) != 2 || len(e.Sections[0].Items) != 6 || len(e.Sections[1].Items) != 6 {
		t.Fatalf("edition = %+v, error = %v", e, err)
	}
}

func TestFeedsOnlyRequiredSectionAndLead(t *testing.T) {
	p := Paper{Sections: []Section{{Key: "required", Required: true}, {Key: "local"}}}
	_, problems, err := FeedsOnly(p, []Candidate{feedCandidate("first", "local")})
	if err == nil || len(problems) == 0 || problems[0].Rule != "required section must be non-empty" {
		t.Fatal(problems, err)
	}
	p.Sections[0].Required = false
	_, problems, err = FeedsOnly(p, nil)
	if err == nil || len(problems) != 1 || problems[0].Path != "lead" {
		t.Fatal(problems, err)
	}
}

func TestFeedsOnlyPlainTextAndExcerpt(t *testing.T) {
	p := Paper{Sections: []Section{{Key: "local"}}}
	c := feedCandidate("one", "local")
	c.Title = "<b>City</b> &amp; County " + strings.Repeat("café ", 40)
	c.Outlet = "<strong>" + strings.Repeat("Desk ", 30) + "</strong>"
	c.Text = "<p>First <em>sentence</em>.</p><script>hidden</script><p>" + strings.Repeat("Second ", 150) + "sentence.</p>"
	e, _, err := FeedsOnly(p, []Candidate{c})
	if err != nil {
		t.Fatal(err)
	}
	item := e.Sections[0].Items[0]
	if item.Summary != "First sentence." || !strings.HasPrefix(item.Title, "City & County café") ||
		!strings.HasSuffix(item.Title, "café") || !strings.HasSuffix(item.Source, "Desk") ||
		strings.ContainsAny(item.Title+item.Source, "<>") || utf8.RuneCountInString(item.Title) > 160 ||
		utf8.RuneCountInString(item.Source) > 80 {
		t.Fatalf("unexpected item: %+v", item)
	}
	c.Text = strings.Repeat("café ", 150) + "finishes."
	e, _, err = FeedsOnly(p, []Candidate{c})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := e.Sections[0].Items[0].Summary, strings.Repeat("café ", 119)+"café…"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}
