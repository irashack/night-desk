package edition

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func v2Edition() *Edition {
	e := validEdition()
	e.Schema = 2
	e.Sections[0].Items[0].Byline = ptr("A Writer")
	e.Sections[0].Items[0].Sources = []Source{{Outlet: "Another outlet", URL: "https://other.example/story", Byline: ptr("Another Writer")}}
	opinion := e.Sections[0].Items[0]
	opinion.Sources = append([]Source{}, opinion.Sources...)
	opinion.ID, opinion.Kind = "opinion-1", "opinion"
	e.Sections[0].Items = append(e.Sections[0].Items, opinion)
	return e
}

func TestV2RoundTrip(t *testing.T) {
	for _, e := range []*Edition{validEdition(), v2Edition()} {
		data, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Decode(bytes.NewReader(data))
		if err != nil || !reflect.DeepEqual(e, got) {
			t.Fatalf("round trip: %v, %v", got, err)
		}
		if problems := Validate(got, paper()); len(problems) != 0 {
			t.Fatal(problems)
		}
	}
	// V2 also accepts an unchanged v1 item, omitted new fields, and an empty list.
	e := validEdition()
	e.Schema = 2
	e.Sections[0].Items[0].Sources = []Source{}
	if problems := Validate(e, paper()); len(problems) != 0 {
		t.Fatal(problems)
	}
	data, _ := json.Marshal(e)
	// Marshal omits empty sources; explicitly exercise an empty array on the wire.
	input := strings.Replace(string(data), `"source":"A source"`, `"source":"A source","sources":[]`, 1)
	got, err := Decode(strings.NewReader(input))
	if err != nil || got.Sections[0].Items[0].Sources == nil {
		t.Fatal("empty sources array rejected", err)
	}
}

func TestV1RejectsV2(t *testing.T) {
	for _, field := range []string{"byline", "sources"} {
		for _, value := range []any{nil, "", []any{}, false} {
			t.Run(fmt.Sprintf("%s/%v", field, value), func(t *testing.T) {
				data, _ := json.Marshal(validEdition())
				var root map[string]any
				if err := json.Unmarshal(data, &root); err != nil {
					t.Fatal(err)
				}
				item := root["sections"].([]any)[0].(map[string]any)["items"].([]any)[0].(map[string]any)
				item[field] = value
				data, _ = json.Marshal(root)
				_, err := Decode(bytes.NewReader(data))
				want := "sections[0].items[0]." + field + ": requires schema 2"
				if err == nil || err.Error() != want {
					t.Fatalf("got %v, want %s", err, want)
				}
			})
		}
	}
	for _, tt := range []struct {
		name   string
		change func(*Item)
		rule   string
	}{
		{"byline", func(i *Item) { i.Byline = ptr("Writer") }, "requires schema 2"},
		{"sources", func(i *Item) { i.Sources = []Source{} }, "requires schema 2"},
		{"kind", func(i *Item) { i.Kind = "opinion" }, "must be one of news, seasonal, evergreen, brief"},
	} {
		e := validEdition()
		tt.change(&e.Sections[0].Items[0])
		e.Lead = "other"
		got := Validate(e, paper())
		want := Problem{"sections[0].items[0]." + tt.name, tt.rule, Error}
		if len(got) == 0 || got[0] != want {
			t.Fatalf("%s: %v, want %v", tt.name, got, want)
		}
	}
}

func TestV2SourceShape(t *testing.T) {
	for _, tt := range []struct{ name, fields, path, rule string }{
		{"null byline", `"byline":null`, "byline", "must not be null"},
		{"wrong byline", `"byline":12`, "byline", "must be a string"},
		{"null sources", `"sources":null`, "sources", "must not be null"},
		{"object sources", `"sources":{}`, "sources", "must be an array"},
		{"null source", `"sources":[null]`, "sources[0]", "must not be null"},
		{"missing outlet", `"sources":[{"url":"https://other.example"}]`, "sources[0].outlet", "is required"},
		{"missing url", `"sources":[{"outlet":"Other"}]`, "sources[0].url", "is required"},
		{"wrong outlet", `"sources":[{"outlet":true,"url":"https://other.example"}]`, "sources[0].outlet", "must be a string"},
		{"source null byline", `"sources":[{"outlet":"Other","url":"https://other.example","byline":null}]`, "sources[0].byline", "must not be null"},
		{"unknown source field", `"sources":[{"outlet":"Other","url":"https://other.example","author":"Writer"}]`, "sources[0]", `unknown field "author"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data, _ := json.Marshal(validEdition())
			input := strings.Replace(string(data), `"schema":1`, `"schema":2`, 1)
			input = strings.Replace(input, `"source":"A source"`, `"source":"A source",`+tt.fields, 1)
			_, err := Decode(strings.NewReader(input))
			want := "sections[0].items[0]." + tt.path + ": " + tt.rule
			if err == nil || err.Error() != want {
				t.Fatalf("got %v, want %s", err, want)
			}
			var value any
			if err := json.Unmarshal([]byte(input), &value); err != nil {
				t.Fatal(err)
			}
			schema, _ := loadSchema(t, 2)
			if structuralMatch(schema, value) {
				t.Fatal("published schema accepts invalid shape")
			}
		})
	}
}

func TestV2Rules(t *testing.T) {
	for _, tt := range []struct {
		name       string
		change     func(*Edition)
		path, rule string
	}{
		{"opinion lead", func(e *Edition) { e.Lead = "opinion-1" }, "lead", "must not reference an opinion item"},
		{"duplicate source", func(e *Edition) { i := &e.Sections[0].Items[0]; i.Sources = append(i.Sources, i.Sources[0]) }, "sections[0].items[0].sources[1].url", "must be unique within the item sources"},
		{"repeated primary", func(e *Edition) { i := &e.Sections[0].Items[0]; i.Sources[0].URL = i.URL }, "sections[0].items[0].sources[0].url", "must not repeat the item URL"},
		{"too many sources", func(e *Edition) {
			i := &e.Sections[0].Items[0]
			for n := 1; n < 13; n++ {
				i.Sources = append(i.Sources, Source{Outlet: "Other", URL: fmt.Sprintf("https://other.example/%d", n)})
			}
		}, "sections[0].items[0].sources", "must contain at most 12 sources"},
		{"empty byline", func(e *Edition) { e.Sections[0].Items[0].Byline = ptr("") }, "sections[0].items[0].byline", "must be trimmed non-empty text"},
		{"byline html", func(e *Edition) { e.Sections[0].Items[0].Byline = ptr("<b>Writer</b>") }, "sections[0].items[0].byline", "must be plain text (no HTML tags or Markdown links)"},
		{"source byline control", func(e *Edition) { e.Sections[0].Items[0].Sources[0].Byline = ptr("A\nWriter") }, "sections[0].items[0].sources[0].byline", "must not contain control characters"},
		{"outlet trimmed", func(e *Edition) { e.Sections[0].Items[0].Sources[0].Outlet = " Other " }, "sections[0].items[0].sources[0].outlet", "must be trimmed non-empty text"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := v2Edition()
			tt.change(e)
			want := Problem{tt.path, tt.rule, Error}
			for _, p := range Validate(e, paper()) {
				if p == want {
					return
				}
			}
			t.Fatalf("missing %v in %v", want, Validate(e, paper()))
		})
	}
}

func TestV2SourceURLs(t *testing.T) {
	// Each source URL must receive exactly the same URL errors as the primary URL.
	for _, value := range []string{"http://example.com", "/story", "https:///story", "https:story", "https://user@example.com", "https://example.com/%zz", "https://example.com/a b", "https://other.example/valid"} {
		t.Run(value, func(t *testing.T) {
			e := v2Edition()
			i := &e.Sections[0].Items[0]
			i.URL, i.Sources[0].URL = value, value
			var primary, source []string
			for _, p := range Validate(e, paper()) {
				if p.Path == "sections[0].items[0].url" {
					primary = append(primary, p.Rule)
				}
				if p.Path == "sections[0].items[0].sources[0].url" && p.Rule != "must not repeat the item URL" {
					source = append(source, p.Rule)
				}
			}
			if !reflect.DeepEqual(primary, source) {
				t.Fatalf("primary=%v source=%v", primary, source)
			}
		})
	}
}

func TestV2TextAndSourceLimits(t *testing.T) {
	for _, tt := range []struct {
		name string
		cap  int
		set  func(*Item, string)
	}{
		{"byline", 120, func(i *Item, s string) { i.Byline = &s }},
		{"source byline", 120, func(i *Item, s string) { i.Sources[0].Byline = &s }},
		{"outlet", 80, func(i *Item, s string) { i.Sources[0].Outlet = s }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := v2Edition()
			tt.set(&e.Sections[0].Items[0], strings.Repeat("界", tt.cap))
			if p := Validate(e, paper()); len(p) != 0 {
				t.Fatal(p)
			}
			tt.set(&e.Sections[0].Items[0], strings.Repeat("界", tt.cap+1))
			if p := Validate(e, paper()); len(p) != 1 || p[0].Rule != fmt.Sprintf("must be at most %d characters", tt.cap) {
				t.Fatal(p)
			}
		})
	}
	e := v2Edition()
	i := &e.Sections[0].Items[0]
	for n := 1; n < 12; n++ {
		i.Sources = append(i.Sources, Source{Outlet: "Other", URL: fmt.Sprintf("https://other.example/%d", n)})
	}
	if p := Validate(e, paper()); len(p) != 0 {
		t.Fatal(p)
	}
	data, _ := json.Marshal(e)
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	schema, _ := loadSchema(t, 2)
	if !structuralMatch(schema, value) {
		t.Fatal("schema rejects valid v2")
	}
}

func TestOpinionAboutAndStance(t *testing.T) {
	lead := v2Edition().Sections[0].Items[0].ID
	linked := func() *Edition {
		e := v2Edition()
		e.Sections[0].Items[1].About, e.Sections[0].Items[1].Stance = ptr(lead), ptr("Argues the plan costs too much.")
		return e
	}
	if problems := Validate(linked(), paper()); len(problems) != 0 {
		t.Fatal(problems)
	}
	data, _ := json.Marshal(linked())
	if got, err := Decode(bytes.NewReader(data)); err != nil || *got.Sections[0].Items[1].About != lead {
		t.Fatal("round trip", err)
	}
	for _, tc := range []struct {
		name, path, rule string
		change           func(*Edition)
	}{
		{"unknown story", "sections[0].items[1].about", "must reference an item id in this edition", func(e *Edition) { e.Sections[0].Items[1].About = ptr("missing") }},
		{"opinion about opinion", "sections[0].items[1].about", "must reference a story, not an opinion item", func(e *Edition) { e.Sections[0].Items[1].About = ptr("opinion-1") }},
		{"news with about", "sections[0].items[0].about", "is only for opinion items", func(e *Edition) { e.Sections[0].Items[0].About = ptr("opinion-1") }},
		{"news with stance", "sections[0].items[0].stance", "is only for opinion items", func(e *Edition) { e.Sections[0].Items[0].Stance = ptr("A view.") }},
		{"long stance", "sections[0].items[1].stance", "must be at most 140 characters", func(e *Edition) { e.Sections[0].Items[1].Stance = ptr(strings.Repeat("x", 141)) }},
		{"untrimmed stance", "sections[0].items[1].stance", "must be trimmed non-empty text", func(e *Edition) { e.Sections[0].Items[1].Stance = ptr(" A view.") }},
		{"v1 about", "sections[0].items[0].about", "requires schema 2", func(e *Edition) { *e = *validEdition(); e.Sections[0].Items[0].About = ptr(lead) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := linked()
			tc.change(e)
			for _, p := range Validate(e, paper()) {
				if p.Path == tc.path && strings.Contains(p.Rule, tc.rule) && p.Severity == Error {
					return
				}
			}
			t.Fatalf("missing %s: %s in %v", tc.path, tc.rule, Validate(e, paper()))
		})
	}
}
