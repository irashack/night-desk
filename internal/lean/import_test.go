package lean

import (
	"strings"
	"testing"
)

func exampleSource() Source {
	return Source{
		Name:        "Example Ratings Cooperative",
		URL:         "https://ratings.example.org/method",
		License:     "CC0-1.0",
		Attribution: "Example Ratings Cooperative",
	}
}

func TestImportCSVWithoutHeader(t *testing.T) {
	const doc = `One Example,one.example,Far Left,very-high
Two Example,two.example,Lean Left,high
Three Example,three.example,Left-Center,
Four Example,four.example,Center,mixed
Five Example,five.example,Right-Center,low
Six Example,six.example,Lean Right,very-low
Seven Example,seven.example,Far Right,
`
	got, problems, err := ImportCSV(strings.NewReader(doc), exampleSource())
	if err != nil {
		t.Fatalf("ImportCSV: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	want := []Lean{Left, LeanLeft, LeanLeft, Center, LeanRight, LeanRight, Right}
	if len(got.Ratings) != len(want) {
		t.Fatalf("got %d ratings, want %d", len(got.Ratings), len(want))
	}
	for i, lean := range want {
		if got.Ratings[i].Lean != lean {
			t.Errorf("ratings[%d].Lean = %q, want %q", i, got.Ratings[i].Lean, lean)
		}
	}
	if got.Ratings[0].Factuality != VeryHigh || got.Ratings[1].Factuality != High ||
		got.Ratings[2].Factuality != "" || got.Ratings[3].Factuality != Mixed ||
		got.Ratings[4].Factuality != Low || got.Ratings[5].Factuality != VeryLow {
		t.Fatalf("factualities = %+v", got.Ratings)
	}
	if got.Source != exampleSource() {
		t.Fatalf("source = %+v", got.Source)
	}
}

func TestImportCSVWithHeader(t *testing.T) {
	const doc = "\ufeffoutlet,domain,lean,factuality\n" +
		"A, a.example , Center , Mostly Factual \n" +
		"B,b.example,Centre,High\n"
	got, problems, err := ImportCSV(strings.NewReader(doc), exampleSource())
	if err != nil || len(problems) != 0 {
		t.Fatalf("ImportCSV = %v, %v, %v", got, problems, err)
	}
	if len(got.Ratings) != 2 {
		t.Fatalf("got %d ratings, want 2", len(got.Ratings))
	}
	if got.Ratings[0].Outlet != "A" || got.Ratings[0].Domains[0] != "a.example" ||
		got.Ratings[0].Lean != Center || got.Ratings[0].Factuality != MostlyFactual {
		t.Fatalf("first rating = %+v", got.Ratings[0])
	}
	if got.Ratings[1].Lean != Center {
		t.Fatalf("second rating = %+v", got.Ratings[1])
	}
}

func TestImportCSVLeanSpellings(t *testing.T) {
	tests := map[string]Lean{
		"Left":            Left,
		"far-left":        Left,
		"Extreme Left":    Left,
		"left wing":       Left,
		"Liberal":         Left,
		"Lean Left":       LeanLeft,
		"Left-Center":     LeanLeft,
		"Center-Left":     LeanLeft,
		"left_of_center":  LeanLeft,
		"Center":          Center,
		"centrist":        Center,
		"Middle":          Center,
		"Moderate":        Center,
		"Lean Right":      LeanRight,
		"Right-Center":    LeanRight,
		"Center-Right":    LeanRight,
		"right of centre": LeanRight,
		"Right":           Right,
		"far right":       Right,
		"Right Wing":      Right,
		"Conservative":    Right,
	}
	for label, want := range tests {
		t.Run(label, func(t *testing.T) {
			doc := "Example,example.com," + label + "\n"
			got, problems, err := ImportCSV(strings.NewReader(doc), exampleSource())
			if err != nil || len(problems) != 0 {
				t.Fatalf("ImportCSV = %v, %v, %v", got, problems, err)
			}
			if got.Ratings[0].Lean != want {
				t.Fatalf("lean = %q, want %q", got.Ratings[0].Lean, want)
			}
		})
	}
}

func TestImportCSVRejects(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		src  Source
		path string
		rule string
	}{
		{
			name: "missing attribution",
			doc:  "Example,example.com,center\n",
			src:  Source{Name: "Example", URL: "https://example.org", License: "MIT"},
			path: "source.attribution", rule: "must be trimmed non-empty text",
		},
		{
			name: "too few fields",
			doc:  "Example,example.com\n",
			path: "ratings[0]", rule: "must have 3 or 4 fields",
		},
		{
			name: "too many fields",
			doc:  "Example,example.com,center,high,extra\n",
			path: "ratings[0]", rule: "must have 3 or 4 fields",
		},
		{
			name: "bad outlet",
			doc:  ",example.com,center\n",
			path: "ratings[0].outlet", rule: "must be trimmed non-empty text",
		},
		{
			name: "bad domain",
			doc:  "Example,https://example.com,center\n",
			path: "ratings[0].domains[0]", rule: "must be a domain",
		},
		{
			name: "bad lean",
			doc:  "Example,example.com,sideways\n",
			path: "ratings[0].lean", rule: leanRule,
		},
		{
			name: "bad factuality",
			doc:  "Example,example.com,center,very\n",
			path: "ratings[0].factuality", rule: factualityRule,
		},
		{
			name: "duplicate domain",
			doc:  "One,example.com,left\nTwo,www.example.com,right\n",
			path: "ratings[1].domains[0]", rule: "must be unique",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := tt.src
			if src.Name == "" {
				src = exampleSource()
			}
			got, problems, err := ImportCSV(strings.NewReader(tt.doc), src)
			if err != nil {
				t.Fatalf("ImportCSV returned error: %v", err)
			}
			if got != nil {
				t.Fatalf("got ratings %+v, want nil", got)
			}
			if !hasProblem(problems, tt.path, tt.rule) {
				t.Fatalf("problems = %v, want %s: %s", problems, tt.path, tt.rule)
			}
		})
	}
}

func TestImportCSVEmptyAndSyntaxError(t *testing.T) {
	got, problems, err := ImportCSV(strings.NewReader(""), exampleSource())
	if err != nil || len(problems) != 0 {
		t.Fatalf("ImportCSV = %v, %v, %v", got, problems, err)
	}
	if got.Ratings == nil || len(got.Ratings) != 0 {
		t.Fatalf("ratings = %#v, want empty non-nil slice", got.Ratings)
	}
	if _, _, err := ImportCSV(strings.NewReader("Example,\"unterminated\n"), exampleSource()); err == nil {
		t.Fatal("ImportCSV accepted malformed CSV")
	}
	if _, _, err := ImportCSV(nil, exampleSource()); err == nil {
		t.Fatal("ImportCSV accepted a nil reader")
	}
}
