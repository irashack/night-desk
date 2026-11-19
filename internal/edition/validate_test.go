package edition

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

func validEdition() *Edition {
	return &Edition{Schema: 1, Lead: "economy-1", Sections: []Section{{Key: "economy", Items: []Item{{ID: "economy-1", Kind: "news", Title: "A title", Summary: "A summary", Source: "A source", URL: "https://example.com/story"}}}}}
}
func paper() PaperRules {
	return PaperRules{Sections: []SectionRule{{Key: "economy", Required: true}, {Key: "health"}}}
}
func market(e *Edition) *Markets {
	e.Boxes = &Boxes{Markets: &Markets{Source: "Exchange", AsOf: "2026-10-27T12:00:00Z", Quotes: []Quote{{Symbol: "EXM", Label: "Example", Value: "1,234.56", Change: "+1.00", Direction: "up"}}}}
	return e.Boxes.Markets
}

func TestValidateRules(t *testing.T) {
	tests := []struct {
		name       string
		change     func(*Edition, *PaperRules)
		path, rule string
		severity   Severity
	}{
		{"schema", func(e *Edition, _ *PaperRules) { e.Schema = 3 }, "schema", "must be 1", Error},
		{"lead", func(e *Edition, _ *PaperRules) { e.Lead = "missing" }, "lead", "must reference", Error},
		{"unknown section", func(e *Edition, _ *PaperRules) {
			e.Sections = append(e.Sections, Section{Key: "unknown", Items: []Item{}})
		}, "sections[1].key", "paper section", Error},
		{"duplicate section", func(e *Edition, _ *PaperRules) {
			e.Sections = append(e.Sections, Section{Key: "economy", Items: []Item{e.Sections[0].Items[0]}})
		}, "sections[1].key", "unique", Error},
		{"missing required", func(_ *Edition, p *PaperRules) { p.Sections[1].Required = true }, "sections", `"health" must be present`, Error},
		{"empty required", func(e *Edition, _ *PaperRules) { e.Sections[0].Items = []Item{} }, "sections[0].items", "non-empty", Error},
		{"section order", func(e *Edition, _ *PaperRules) {
			e.Sections = append([]Section{{Key: "health", Items: []Item{}}}, e.Sections...)
		}, "sections[1].key", "paper's section order", Warning},
		{"duplicate id", func(e *Edition, _ *PaperRules) {
			e.Sections = append(e.Sections, Section{Key: "health", Items: []Item{e.Sections[0].Items[0]}})
		}, "sections[1].items[0].id", "unique across", Error},
		{"invalid id", func(e *Edition, _ *PaperRules) { e.Sections[0].Items[0].ID = "Bad_ID" }, "sections[0].items[0].id", "must match", Error},
		{"long id", func(e *Edition, _ *PaperRules) { e.Sections[0].Items[0].ID = strings.Repeat("a", 65) }, "sections[0].items[0].id", "must match", Error},
		{"bad item kind", func(e *Edition, _ *PaperRules) { e.Sections[0].Items[0].Kind = "article" }, "sections[0].items[0].kind", "one of", Error},
		{"blank title", func(e *Edition, _ *PaperRules) { e.Sections[0].Items[0].Title = "" }, "sections[0].items[0].title", "trimmed non-empty", Error},
		{"untrimmed", func(e *Edition, _ *PaperRules) { e.Sections[0].Items[0].Summary = " summary " }, "sections[0].items[0].summary", "trimmed non-empty", Error},
		{"control", func(e *Edition, _ *PaperRules) { e.Sections[0].Items[0].Source = "a\nb" }, "sections[0].items[0].source", "control", Error},
		{"unicode control", func(e *Edition, _ *PaperRules) { e.Sections[0].Items[0].Title = "a\u0085b" }, "sections[0].items[0].title", "control", Error},
		{"html", func(e *Edition, _ *PaperRules) { e.Sections[0].Items[0].Title = "A <tag>title" }, "sections[0].items[0].title", "plain text", Error},
		{"closing html", func(e *Edition, _ *PaperRules) { e.Sections[0].Items[0].Title = "A </TAG>" }, "sections[0].items[0].title", "plain text", Error},
		{"markdown", func(e *Edition, _ *PaperRules) { e.Sections[0].Items[0].Summary = "[story](https://example.com)" }, "sections[0].items[0].summary", "plain text", Error},
		{"why present empty", func(e *Edition, _ *PaperRules) { e.Sections[0].Items[0].Why = ptr("") }, "sections[0].items[0].why", "non-empty", Error},
		{"http", func(e *Edition, _ *PaperRules) { e.Sections[0].Items[0].URL = "http://example.com" }, "sections[0].items[0].url", "must be https", Error},
		{"relative url", func(e *Edition, _ *PaperRules) { e.Sections[0].Items[0].URL = "/story" }, "sections[0].items[0].url", "absolute", Error},
		{"hostless url", func(e *Edition, _ *PaperRules) { e.Sections[0].Items[0].URL = "https:///story" }, "sections[0].items[0].url", "host", Error},
		{"opaque url", func(e *Edition, _ *PaperRules) { e.Sections[0].Items[0].URL = "https:story" }, "sections[0].items[0].url", "absolute", Error},
		{"userinfo", func(e *Edition, _ *PaperRules) { e.Sections[0].Items[0].URL = "https://user:password@example.com" }, "sections[0].items[0].url", "userinfo", Error},
		{"malformed url", func(e *Edition, _ *PaperRules) { e.Sections[0].Items[0].URL = "https://example.com/%zz" }, "sections[0].items[0].url", "absolute https", Error},
		{"url whitespace", func(e *Edition, _ *PaperRules) { e.Sections[0].Items[0].URL = "https://example.com/a b" }, "sections[0].items[0].url", "absolute", Error},
		{"image http", func(e *Edition, _ *PaperRules) { e.Sections[0].Items[0].ImageURL = ptr("http://example.com/image") }, "sections[0].items[0].image_url", "must be https", Error},
		{"minutes zero", func(e *Edition, _ *PaperRules) { e.Sections[0].Items[0].Minutes = ptr(0) }, "sections[0].items[0].minutes", "1 and 240", Error},
		{"minutes negative", func(e *Edition, _ *PaperRules) { e.Sections[0].Items[0].Minutes = ptr(-1) }, "sections[0].items[0].minutes", "1 and 240", Error},
		{"minutes high", func(e *Edition, _ *PaperRules) { e.Sections[0].Items[0].Minutes = ptr(241) }, "sections[0].items[0].minutes", "1 and 240", Error},
		{"motto kind", func(e *Edition, _ *PaperRules) { e.Motto = &Motto{Kind: "poem", Text: "Words", Source: "Writer"} }, "motto.kind", "one of", Error},
		{"motto source", func(e *Edition, _ *PaperRules) { e.Motto = &Motto{Kind: "quotation", Text: "Words"} }, "motto.source", "non-empty", Error},
		{"motto text", func(e *Edition, _ *PaperRules) { e.Motto = &Motto{Kind: "scripture", Source: "Citation"} }, "motto.text", "non-empty", Error},
		{"markets source", func(e *Edition, _ *PaperRules) { market(e).Source = "" }, "boxes.markets.source", "non-empty", Error},
		{"markets as of", func(e *Edition, _ *PaperRules) { market(e).AsOf = "2026-10-27" }, "boxes.markets.as_of", "RFC 3339", Error},
		{"markets direction", func(e *Edition, _ *PaperRules) { market(e).Quotes[0].Direction = "sideways" }, "boxes.markets.quotes[0].direction", "one of", Error},
		{"quote symbol", func(e *Edition, _ *PaperRules) { market(e).Quotes[0].Symbol = "" }, "boxes.markets.quotes[0].symbol", "non-empty", Error},
		{"quote label", func(e *Edition, _ *PaperRules) { market(e).Quotes[0].Label = "<b>Label" }, "boxes.markets.quotes[0].label", "plain text", Error},
		{"quote value", func(e *Edition, _ *PaperRules) { market(e).Quotes[0].Value = "" }, "boxes.markets.quotes[0].value", "non-empty", Error},
		{"quote label length", func(e *Edition, _ *PaperRules) { market(e).Quotes[0].Label = strings.Repeat("x", 41) }, "boxes.markets.quotes[0].label", "at most 40", Error},
		{"quote count", func(e *Edition, _ *PaperRules) {
			q := market(e).Quotes[0]
			market(e).Quotes = slices.Repeat([]Quote{q}, 13)
		}, "boxes.markets.quotes", "at most 12 quotes", Error},
		{"quote change", func(e *Edition, _ *PaperRules) { market(e).Quotes[0].Change = "\t" }, "boxes.markets.quotes[0].change", "control", Error},
		{"nil quotes", func(e *Edition, _ *PaperRules) { market(e).Quotes = nil }, "boxes.markets.quotes", "array", Error},
		{"nil sections", func(e *Edition, _ *PaperRules) { e.Sections = nil }, "sections", "array", Error},
		{"nil items", func(e *Edition, _ *PaperRules) { e.Sections[0].Items = nil }, "sections[0].items", "array", Error},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, p := validEdition(), paper()
			tt.change(e, &p)
			for _, problem := range Validate(e, p) {
				if problem.Path == tt.path && strings.Contains(problem.Rule, tt.rule) && problem.Severity == tt.severity {
					return
				}
			}
			t.Fatalf("missing %s %q (%s) in %v", tt.path, tt.rule, tt.severity, Validate(e, p))
		})
	}
}

func TestTextCaps(t *testing.T) {
	tests := []struct {
		name string
		cap  int
		set  func(*Edition, string)
	}{
		{"title", 160, func(e *Edition, s string) { e.Sections[0].Items[0].Title = s }},
		{"summary", 600, func(e *Edition, s string) { e.Sections[0].Items[0].Summary = s }},
		{"why", 300, func(e *Edition, s string) { e.Sections[0].Items[0].Why = &s }},
		{"source", 80, func(e *Edition, s string) { e.Sections[0].Items[0].Source = s }},
		{"motto text", 400, func(e *Edition, s string) { e.Motto = &Motto{Kind: "quotation", Text: s, Source: "Writer"} }},
		{"motto source", 120, func(e *Edition, s string) { e.Motto = &Motto{Kind: "quotation", Text: "Words", Source: s} }},
		{"markets source", 80, func(e *Edition, s string) { market(e).Source = s }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := validEdition()
			tt.set(e, strings.Repeat("界", tt.cap))
			if got := Validate(e, paper()); len(got) != 0 {
				t.Fatalf("at cap: %v", got)
			}
			tt.set(e, strings.Repeat("界", tt.cap+1))
			if got := Validate(e, paper()); len(got) != 1 || !strings.Contains(got[0].Rule, "at most") {
				t.Fatalf("over cap: %v", got)
			}
		})
	}
}

func TestItemLimits(t *testing.T) {
	for _, tt := range []struct {
		name              string
		count, perSection int
		limits            Limits
		want              bool
	}{
		{"six", 6, 6, Limits{}, false}, {"seven", 7, 7, Limits{}, true},
		{"eighty", 80, 6, Limits{}, false}, {"eighty one", 81, 6, Limits{}, true},
		{"custom section", 3, 3, Limits{ItemsPerSection: 2}, true},
		{"custom total", 4, 2, Limits{ItemsPerEdition: 3}, true},
		{"cannot relax section", 7, 7, Limits{ItemsPerSection: 100}, true},
		{"cannot relax total", 81, 6, Limits{ItemsPerEdition: 100}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := validEdition()
			item := e.Sections[0].Items[0]
			e.Sections = nil
			p := PaperRules{Limits: tt.limits}
			for i := 0; i < tt.count; i++ {
				if i%tt.perSection == 0 {
					key := fmt.Sprintf("section-%d", i)
					e.Sections = append(e.Sections, Section{Key: key, Items: []Item{}})
					p.Sections = append(p.Sections, SectionRule{Key: key})
				}
				item.ID = fmt.Sprintf("item-%d", i)
				last := len(e.Sections) - 1
				e.Sections[last].Items = append(e.Sections[last].Items, item)
			}
			e.Lead = "item-0"
			if got := Validate(e, p); (len(got) > 0) != tt.want {
				t.Fatalf("got %v; want problem=%v", got, tt.want)
			}
		})
	}
}

func TestValidVariants(t *testing.T) {
	for _, kind := range []string{"news", "seasonal", "evergreen", "brief"} {
		for _, minutes := range []int{1, 240} {
			e := validEdition()
			i := &e.Sections[0].Items[0]
			i.Kind = kind
			i.Minutes = &minutes
			i.ID = strings.Repeat("a", 64)
			e.Lead = i.ID
			i.Why = ptr("A reason")
			i.ImageURL = ptr("https://example.com/image")
			i.Title = "3 < 5 and 5 > 3"
			e.Boxes = &Boxes{}
			e.Sections = append(e.Sections, Section{Key: "health", Items: []Item{}})
			if got := Validate(e, paper()); len(got) != 0 {
				t.Fatal(got)
			}
		}
	}
	for _, kind := range []string{"scripture", "quotation"} {
		for _, direction := range []string{"up", "down", "flat"} {
			e := validEdition()
			e.Motto = &Motto{Kind: kind, Text: "Words", Source: "Citation"}
			market(e).Quotes[0].Direction = direction
			if got := Validate(e, paper()); len(got) != 0 {
				t.Fatal(got)
			}
		}
	}
	e := validEdition()
	market(e).Quotes = []Quote{}
	if got := Validate(e, paper()); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestCollectsAllProblems(t *testing.T) {
	e := validEdition()
	e.Schema = 3
	e.Lead = "missing"
	e.Sections[0].Items[0].Title = ""
	e.Sections[0].Items[0].URL = "http://example.com"
	e.Sections = append([]Section{{Key: "health", Items: []Item{}}}, e.Sections...)
	got := Validate(e, paper())
	if len(got) != 5 {
		t.Fatalf("want five problems, got %v", got)
	}
	if got[1].Severity != Warning {
		t.Fatalf("want warning alongside errors: %v", got)
	}
	if got[3].String() != "sections[1].items[0].url: must be https" {
		t.Fatalf("unexpected error format: %v", got[3])
	}
	if got := Validate(nil, paper()); len(got) != 1 || got[0].Severity != Error {
		t.Fatal(got)
	}
}

func TestDecode(t *testing.T) {
	b, _ := json.Marshal(validEdition())
	valid := string(b)
	for _, tt := range []struct {
		name, input string
		wantErr     bool
	}{
		{"valid", valid, false}, {"whitespace", valid + " \n\t", false},
		{"unknown root", strings.Replace(valid, `"schema":1`, `"extra":0,"schema":1`, 1), true},
		{"unknown nested", strings.Replace(valid, `"title":"A title"`, `"title":"A title","extra":0`, 1), true},
		{"case alias", strings.Replace(valid, `"schema"`, `"Schema"`, 1), true},
		{"trailing object", valid + `{}`, true}, {"trailing null", valid + `null`, true}, {"trailing junk", valid + `!`, true},
		{"empty", "", true}, {"null", "null", true}, {"array", "[]", true}, {"malformed", "{", true},
		{"wrong type", strings.Replace(valid, `"schema":1`, `"schema":"1"`, 1), true},
		{"missing schema", strings.Replace(valid, `"schema":1,`, "", 1), true},
		{"null scalar", strings.Replace(valid, `"schema":1`, `"schema":null`, 1), true},
		{"null optional", strings.Replace(valid, `"schema":1`, `"schema":1,"motto":null`, 1), true},
		{"null array", `{"schema":1,"lead":"a","sections":null}`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decode(strings.NewReader(tt.input))
			if (err != nil) != tt.wantErr {
				t.Fatalf("Decode error = %v", err)
			}
		})
	}
}

func TestParseDate(t *testing.T) {
	for _, tt := range []struct {
		input string
		valid bool
	}{
		{"2026-10-27", true}, {"2024-02-29", true}, {"2000-02-29", true}, {"0000-01-01", true},
		{"2026-02-29", false}, {"1900-02-29", false}, {"2026-04-31", false}, {"2026-13-01", false}, {"2026-00-01", false},
		{"2026-01-00", false}, {"2026-1-01", false}, {"2026-01-1", false}, {"26-01-01", false}, {"2026-10-27T00:00:00Z", false}, {" 2026-10-27", false}, {"", false},
	} {
		t.Run(tt.input, func(t *testing.T) {
			date, err := ParseDate(tt.input)
			if (err == nil) != tt.valid {
				t.Fatalf("date=%v err=%v", date, err)
			}
			if tt.valid && (date.Format("2006-01-02") != tt.input || date.Location() != time.UTC) {
				t.Fatal(date)
			}
		})
	}
}

func TestDailyFixture(t *testing.T) {
	f, err := os.Open("../../testdata/valid/daily.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	e, err := Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	keys := strings.Split("health,spiritual,faith,travel,fishing,hunting,fashion,economy,real-estate,business,startups-investing,technology", ",")
	p := PaperRules{}
	if len(e.Sections) != len(keys) {
		t.Fatalf("want twelve sections")
	}
	for _, key := range keys {
		p.Sections = append(p.Sections, SectionRule{Key: key, Required: true})
	}
	if got := Validate(e, p); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestMarketTimestamp(t *testing.T) {
	for _, tt := range []struct {
		value string
		valid bool
	}{
		{"2026-10-27T08:00:00Z", true},
		{"2026-10-27T08:00:00.123-05:00", true},
		{"2026-10-27T08:00:00+05:30", true},
		{"2026-10-27T8:00:00Z", false},
		{"2026-10-27T08:00:00,123Z", false},
		{"2026-10-27T08:00:00+24:00", false},
		{"2026-10-27T08:00:00+00:60", false},
		{"2026-02-30T08:00:00Z", false},
		{"", false},
	} {
		t.Run(tt.value, func(t *testing.T) {
			e := validEdition()
			market(e).AsOf = tt.value
			if got := Validate(e, paper()); (len(got) == 0) != tt.valid {
				t.Fatalf("problems=%v valid=%v", got, tt.valid)
			}
		})
	}
}
