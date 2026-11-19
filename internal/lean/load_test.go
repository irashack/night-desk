package lean

import (
	"os"
	"strings"
	"testing"
)

func hasProblem(problems []Problem, path, rule string) bool {
	for _, p := range problems {
		if p.Path == path && p.Rule == rule {
			return true
		}
	}
	return false
}

func TestLoadExampleFile(t *testing.T) {
	f, err := os.Open("testdata/example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	got, problems, err := Load(f)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("Load problems: %v", problems)
	}
	if got.Source.Name != "Example Ratings Cooperative" ||
		got.Source.URL != "https://ratings.example.org/method" ||
		got.Source.License != "CC0-1.0" ||
		got.Source.Attribution != "Example Ratings Cooperative (example.org), CC0 1.0" {
		t.Fatalf("source = %+v", got.Source)
	}
	if len(got.Ratings) != 6 {
		t.Fatalf("got %d ratings, want 6", len(got.Ratings))
	}
	first := got.Ratings[0]
	if first.Outlet != "The Example Ledger" || first.Lean != Left || first.Factuality != High {
		t.Fatalf("first rating = %+v", first)
	}
	if len(first.Domains) != 1 || first.Domains[0] != "example.com" {
		t.Fatalf("first domains = %v", first.Domains)
	}
	if got.Ratings[5].Factuality != "" {
		t.Fatalf("omitted factuality = %q, want empty", got.Ratings[5].Factuality)
	}
}

func TestLoadJSON(t *testing.T) {
	doc := `{
	  "source": {
	    "name": "JSON Example",
	    "url": "https://example.org/ratings",
	    "license": "MIT",
	    "attribution": "JSON Example"
	  },
	  "ratings": [
	    {"outlet": "JSON Daily", "domains": ["json.example"], "lean": "lean-left", "factuality": "mostly-factual"}
	  ]
	}`
	got, problems, err := Load(strings.NewReader(doc))
	if err != nil || len(problems) != 0 {
		t.Fatalf("Load = %v, %v, %v", got, problems, err)
	}
	if got.Ratings[0].Lean != LeanLeft || got.Ratings[0].Factuality != MostlyFactual {
		t.Fatalf("rating = %+v", got.Ratings[0])
	}
}

func TestLoadAcceptsEmptyRatings(t *testing.T) {
	doc := `
source:
  name: Empty Example
  url: https://example.org/ratings
  license: CC0-1.0
  attribution: Empty Example
ratings: []
`
	got, problems, err := Load(strings.NewReader(doc))
	if err != nil || len(problems) != 0 {
		t.Fatalf("Load = %v, %v, %v", got, problems, err)
	}
	if got.Ratings == nil || len(got.Ratings) != 0 {
		t.Fatalf("ratings = %#v, want empty non-nil slice", got.Ratings)
	}
}

func TestLoadRejects(t *testing.T) {
	const validSource = `
source:
  name: Example
  url: https://example.org/ratings
  license: MIT
  attribution: Example
`
	tests := []struct {
		name string
		doc  string
		path string
		rule string
	}{
		{
			name: "unknown top-level key",
			doc: validSource + `
version: 1
ratings: []
`,
			path: "version", rule: "unknown field",
		},
		{
			name: "unknown source key",
			doc: `
source:
  name: Example
  url: https://example.org/ratings
  license: MIT
  attribution: Example
  website: https://example.org
ratings: []
`,
			path: "source.website", rule: "unknown field",
		},
		{
			name: "unknown rating key",
			doc: validSource + `
ratings:
  - outlet: Example
    domains: [example.com]
    lean: center
    url: https://example.com
`,
			path: "ratings[0].url", rule: "unknown field",
		},
		{
			name: "missing attribution",
			doc: `
source:
  name: Example
  url: https://example.org/ratings
  license: MIT
ratings: []
`,
			path: "source.attribution", rule: "must be trimmed non-empty text",
		},
		{
			name: "missing source",
			doc: `
ratings: []
`,
			path: "source", rule: "must be an object",
		},
		{
			name: "bad source URL",
			doc: `
source:
  name: Example
  url: example.org/ratings
  license: MIT
  attribution: Example
ratings: []
`,
			path: "source.url", rule: "must be an absolute http(s) URL with a host",
		},
		{
			name: "bad lean",
			doc: validSource + `
ratings:
  - outlet: Example
    domains: [example.com]
    lean: Lean Left
`,
			path: "ratings[0].lean", rule: leanRule,
		},
		{
			name: "missing lean",
			doc: validSource + `
ratings:
  - outlet: Example
    domains: [example.com]
`,
			path: "ratings[0].lean", rule: leanRule,
		},
		{
			name: "bad factuality",
			doc: validSource + `
ratings:
  - outlet: Example
    domains: [example.com]
    lean: center
    factuality: mostly
`,
			path: "ratings[0].factuality", rule: factualityRule,
		},
		{
			name: "missing outlet",
			doc: validSource + `
ratings:
  - domains: [example.com]
    lean: center
`,
			path: "ratings[0].outlet", rule: "must be trimmed non-empty text",
		},
		{
			name: "missing domains",
			doc: validSource + `
ratings:
  - outlet: Example
    lean: center
`,
			path: "ratings[0].domains", rule: "must be a non-empty array of domains",
		},
		{
			name: "empty domains",
			doc: validSource + `
ratings:
  - outlet: Example
    domains: []
    lean: center
`,
			path: "ratings[0].domains", rule: "must be a non-empty array of domains",
		},
		{
			name: "bad domain",
			doc: validSource + `
ratings:
  - outlet: Example
    domains: [https://example.com/path]
    lean: center
`,
			path: "ratings[0].domains[0]", rule: "must be a domain",
		},
		{
			name: "single-label domain",
			doc: validSource + `
ratings:
  - outlet: Example
    domains: [localhost]
    lean: center
`,
			path: "ratings[0].domains[0]", rule: "must be a domain",
		},
		{
			name: "duplicate domain in one rating",
			doc: validSource + `
ratings:
  - outlet: Example
    domains: [example.com, www.example.com]
    lean: center
`,
			path: "ratings[0].domains[1]", rule: "must be unique",
		},
		{
			name: "duplicate domain across ratings",
			doc: validSource + `
ratings:
  - outlet: One
    domains: [example.com]
    lean: left
  - outlet: Two
    domains: [Example.COM]
    lean: right
`,
			path: "ratings[1].domains[0]", rule: "must be unique",
		},
		{
			name: "rating not object",
			doc: validSource + `
ratings: ["example.com"]
`,
			path: "ratings[0]", rule: "must be an object",
		},
		{
			name: "ratings not array",
			doc: validSource + `
ratings: example.com
`,
			path: "ratings", rule: "must be an array",
		},
		{
			name: "document not object",
			doc:  `[]`,
			path: "$", rule: "must be a ratings object",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, problems, err := Load(strings.NewReader(tt.doc))
			if err != nil {
				t.Fatalf("Load returned error: %v", err)
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

func TestLoadSyntaxErrorAndNilReader(t *testing.T) {
	if _, _, err := Load(strings.NewReader("source: [unterminated")); err == nil {
		t.Fatal("Load accepted malformed YAML")
	}
	if _, _, err := Load(nil); err == nil {
		t.Fatal("Load accepted a nil reader")
	}
}
