package config

import (
	"path/filepath"
	"strings"
	"testing"
)

const ratingsYAML = `source:
  name: Test ratings
  url: https://ratings.example
  license: Public domain
  attribution: Test ratings attribution
ratings:
- outlet: Example
  domains: [example.com]
  lean: left
`

func TestLeanConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	write(t, filepath.Join(dir, "ratings.yaml"), ratingsYAML)
	decl := strings.Replace(valid, "    required: true", "    lean: true\n    required: true", 1)
	write(t, path, "lean: {ratings_file: ratings.yaml}\n"+decl)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Papers[0].Sections[0].Lean || c.Lean.Ratings == nil || c.Lean.Ratings.Source.Attribution != "Test ratings attribution" || c.Lean.RatingsFile != filepath.Join(dir, "ratings.yaml") {
		t.Fatalf("bad lean config: %+v", c.Lean)
	}
	// Enabled sections are valid without ratings and default to false otherwise.
	for _, declaration := range []string{decl, valid} {
		write(t, path, declaration)
		c, err = Load(path)
		if err != nil || c.Lean.Ratings != nil || c.Papers[0].Sections[0].Lean != (declaration == decl) {
			t.Fatalf("optional lean: %+v %v", c, err)
		}
	}
}

func TestLeanConfigValidationPaths(t *testing.T) {
	for _, tt := range []struct{ name, top, ratings, replacement, want string }{
		{"missing file", "lean: {ratings_file: missing}\n", "", "", "lean.ratings_file: must name a readable ratings file"},
		{"empty path", "lean: {ratings_file: ''}\n", "", "", "lean.ratings_file: must be trimmed non-empty text"},
		{"unknown field", "lean: {rating_file: missing}\n", "", "", "lean.rating_file: unknown field"},
		{"runtime field", "lean: {Ratings: {}}\n", "", "", "lean.Ratings: unknown field"},
		{"bad enum", "lean: {ratings_file: ratings.yaml}\n", strings.Replace(ratingsYAML, "lean: left", "lean: sideways", 1), "", "lean.ratings_file.ratings[0].lean: must be one of"},
		{"missing attribution", "lean: {ratings_file: ratings.yaml}\n", strings.Replace(ratingsYAML, "  attribution: Test ratings attribution\n", "", 1), "", "lean.ratings_file.source.attribution: must be trimmed non-empty text"},
		{"malformed ratings", "lean: {ratings_file: ratings.yaml}\n", "[broken", "", "lean.ratings_file: must contain valid YAML or JSON ratings"},
		{"null switch", "", "", "    lean: null\n", "papers[0].sections[0].lean: must be a boolean"},
		{"string switch", "", "", "    lean: 'true'\n", "papers[0].sections[0].lean: must be a boolean"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			write(t, filepath.Join(dir, "ratings.yaml"), tt.ratings)
			write(t, path, tt.top+strings.Replace(valid, "    required: true", tt.replacement+"    required: true", 1))
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q; got %v", tt.want, err)
			}
		})
	}
}

func TestLeanReloadRejectsInvalidRatings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	file := filepath.Join(dir, "ratings.yaml")
	write(t, file, ratingsYAML)
	write(t, path, "lean: {ratings_file: ratings.yaml}\n"+valid)
	m, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	old := m.Current()
	write(t, file, "{}")
	if err = m.Reload(true); err == nil || m.Current() != old {
		t.Fatalf("invalid ratings replaced snapshot: %v", err)
	}
	write(t, file, strings.Replace(ratingsYAML, "lean: left", "lean: right", 1))
	if err = m.Reload(true); err != nil || m.Current().Lean.Ratings.Ratings[0].Lean != "right" || old.Lean.Ratings.Ratings[0].Lean != "left" {
		t.Fatalf("ratings reload changed old snapshot: %v", err)
	}
}
