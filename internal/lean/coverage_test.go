package lean

import "testing"

func testRatings() *Ratings {
	return &Ratings{
		Source: Source{Attribution: "Example Ratings Cooperative"},
		Ratings: []Rating{
			{Outlet: "Left Example", Domains: []string{"left.example"}, Lean: Left},
			{Outlet: "Lean Left Example", Domains: []string{"leanleft.example"}, Lean: LeanLeft},
			{Outlet: "Center Example", Domains: []string{"center.example"}, Lean: Center},
			{Outlet: "Lean Right Example", Domains: []string{"leanright.example"}, Lean: LeanRight},
			{Outlet: "Right Example", Domains: []string{"right.example"}, Lean: Right},
		},
	}
}

func TestCoverageCounts(t *testing.T) {
	sources := []Source{
		{Outlet: "Left Example", URL: "https://left.example/a"},
		{Outlet: "Lean Left Example", URL: "https://leanleft.example/a"},
		{Outlet: "Center Example", URL: "https://center.example/a"},
		{Outlet: "Right Example", URL: "https://right.example/a"},
		{Outlet: "Lean Right Example", URL: "https://leanright.example/a"},
		{Outlet: "Unrated Example", URL: "https://unrated.example/a"},
		{Outlet: "Opinion Example", URL: "https://opinion.example/a", Opinion: true},
	}
	got := Coverage(testRatings(), sources, 2)
	want := Bar{
		Left: 2, Center: 1, Right: 2, Unrated: 1, Opinion: 1, FetchFailed: 2,
		Attribution: "Example Ratings Cooperative",
	}
	if got != want {
		t.Fatalf("Coverage = %+v, want %+v", got, want)
	}
	if got.Label() != "Coverage found among monitored sources: 2 left · 1 center · 2 right" {
		t.Fatalf("Label = %q", got.Label())
	}
}

func TestCoverageOneCountPerOutlet(t *testing.T) {
	sources := []Source{
		{Outlet: "Center Example", URL: "https://center.example/a"},
		{Outlet: "  center   example ", URL: "https://center.example/b"},
		{Outlet: "CENTER EXAMPLE", URL: "https://center.example/c"},
	}
	got := Coverage(testRatings(), sources, 0)
	if got.Left != 0 || got.Center != 1 || got.Right != 0 || got.Unrated != 0 {
		t.Fatalf("Coverage = %+v, want one center outlet", got)
	}
}

func TestCoverageFiveLabelsOneRatedOutlet(t *testing.T) {
	ratings := &Ratings{
		Source: Source{Attribution: "Example Ratings"},
		Ratings: []Rating{
			{Outlet: "Rated Right", Domains: []string{"right.example"}, Lean: Right},
		},
	}
	sources := []Source{
		{Outlet: "Alpha", URL: "https://right.example/a"},
		{Outlet: "Beta", URL: "https://www.right.example/b"},
		{Outlet: "Gamma", URL: "https://news.right.example/c"},
		{Outlet: "Delta", URL: "https://right.example/d"},
		{Outlet: "Epsilon", URL: "https://m.right.example/e"},
	}
	got := Coverage(ratings, sources, 0)
	want := Bar{Right: 1, Attribution: "Example Ratings"}
	if got != want {
		t.Fatalf("Coverage = %+v, want %+v", got, want)
	}
	if got.Blindspot != "" {
		t.Fatalf("Blindspot = %q, want no blindspot from repeated labels", got.Blindspot)
	}
}

func TestCoverageFiveLabelsOneUnratedDomain(t *testing.T) {
	sources := []Source{
		{Outlet: "Alpha", URL: "https://unrated.example/a"},
		{Outlet: "Beta", URL: "https://www.unrated.example/b"},
		{Outlet: "Gamma", URL: "https://news.unrated.example/c"},
		{Outlet: "Delta", URL: "https://unrated.example/d"},
		{Outlet: "Epsilon", URL: "https://m.unrated.example/e"},
	}
	got := Coverage(nil, sources, 0)
	if got != (Bar{Unrated: 1}) {
		t.Fatalf("Coverage = %+v, want one unrated domain", got)
	}
}

func TestCoverageURLSelectsRatingNotOutletLabel(t *testing.T) {
	ratings := &Ratings{
		Source: Source{Attribution: "Example Ratings"},
		Ratings: []Rating{
			{Outlet: "Left Example", Domains: []string{"left.example"}, Lean: Left},
			{Outlet: "Right Example", Domains: []string{"right.example"}, Lean: Right},
		},
	}
	sources := []Source{
		{Outlet: "Left Example", URL: "https://right.example/a"},
		{Outlet: "Left Example", URL: "https://right.example/b"},
		{Outlet: "Left Example", URL: "https://right.example/c"},
		{Outlet: "Left Example", URL: "https://right.example/d"},
		{Outlet: "Left Example", URL: "https://right.example/e"},
	}
	got := Coverage(ratings, sources, 0)
	if got.Left != 0 || got.Right != 1 || got.Blindspot != "" {
		t.Fatalf("Coverage = %+v, want one right outlet from the URL", got)
	}
}

func TestCoverageWireGroupCountsFirstOnly(t *testing.T) {
	sources := []Source{
		{Outlet: "Wire Left", URL: "https://wire.example/a", WireGroup: 7},
		{Outlet: "Wire Right", URL: "https://wire2.example/a", WireGroup: 7},
		{Outlet: "Wire Center", URL: "https://wire3.example/a", WireGroup: 7},
		{Outlet: "Center Example", URL: "https://center.example/a"},
	}
	got := Coverage(testRatings(), sources, 0)
	if got.Left != 0 || got.Center != 1 || got.Right != 0 || got.Unrated != 1 {
		t.Fatalf("Coverage = %+v, want only the first wire copy plus center", got)
	}

	// The first copy is unrated and later copies are rated: the group still
	// counts once, as its first source.
	sources = []Source{
		{Outlet: "Unrated Wire", URL: "https://unrated.example/a", WireGroup: 3},
		{Outlet: "Left Example", URL: "https://left.example/a", WireGroup: 3},
	}
	got = Coverage(testRatings(), sources, 0)
	if got.Left != 0 || got.Unrated != 1 {
		t.Fatalf("Coverage = %+v, want the first wire copy only", got)
	}
}

func TestCoverageOpinionAndUnrated(t *testing.T) {
	sources := []Source{
		{Outlet: "Left Example", URL: "https://left.example/a", Opinion: true},
		{Outlet: "Opinion Example", URL: "https://opinion.example/a", Opinion: true},
		{Outlet: "Unrated Example", URL: "https://unrated.example/a"},
	}
	got := Coverage(testRatings(), sources, 1)
	if got.Left != 0 || got.Center != 0 || got.Right != 0 {
		t.Fatalf("opinion or unrated leaked into rated counts: %+v", got)
	}
	if got.Opinion != 2 || got.Unrated != 1 || got.FetchFailed != 1 {
		t.Fatalf("Coverage = %+v, want 2 opinion and 1 unrated", got)
	}
}

func TestCoverageDomainMatching(t *testing.T) {
	ratings := &Ratings{
		Source: Source{Attribution: "Example Ratings"},
		Ratings: []Rating{
			{Outlet: "Broad", Domains: []string{"example.com"}, Lean: Left},
			{Outlet: "Specific", Domains: []string{"news.example.com"}, Lean: Right},
			{Outlet: "Case", Domains: []string{"Case.Example"}, Lean: Center},
		},
	}
	sources := []Source{
		{Outlet: "Unknown A", URL: "https://www.example.com/a"},
		{Outlet: "Unknown B", URL: "https://news.example.com/a"},
		{Outlet: "Unknown C", URL: "https://foo.news.example.com/a"},
		{Outlet: "Unknown D", URL: "https://notexample.com/a"},
		{Outlet: "Unknown E", URL: "https://case.example/a"},
		{Outlet: "Unknown F", URL: "https://www.case.example/a"},
		{Outlet: "Unknown G", URL: "https://example.com.evil.test/a"},
	}
	got := Coverage(ratings, sources, 0)
	// B and C match the same specific rating, so they are one outlet; likewise
	// E and F. Domain matching still picks the longest listed suffix.
	want := Bar{Left: 1, Center: 1, Right: 1, Unrated: 2, Attribution: "Example Ratings"}
	if got != want {
		t.Fatalf("Coverage = %+v, want %+v", got, want)
	}
}

func TestCoverageDomainWWWPrefixDoesNotCreateBareSuffix(t *testing.T) {
	ratings := &Ratings{
		Source: Source{Attribution: "Example Ratings"},
		Ratings: []Rating{
			{Outlet: "WWW Example", Domains: []string{"www.example"}, Lean: Left},
		},
	}
	got := Coverage(ratings, []Source{
		{Outlet: "One", URL: "https://foo.example/a"},
		{Outlet: "Two", URL: "https://www.example/a"},
	}, 0)
	if got.Left != 1 || got.Unrated != 1 {
		t.Fatalf("Coverage = %+v, want only the listed www host to match", got)
	}
}

func TestCoverageURLDomainWinsOverOutletLabel(t *testing.T) {
	ratings := &Ratings{
		Source: Source{Attribution: "Example Ratings"},
		Ratings: []Rating{
			{Outlet: "Broad", Domains: []string{"example.com"}, Lean: Left},
			{Outlet: "Specific", Domains: []string{"news.example.com"}, Lean: Right},
		},
	}
	got := Coverage(ratings, []Source{
		{Outlet: "Broad", URL: "https://news.example.com/a"},
	}, 0)
	if got.Left != 0 || got.Right != 1 {
		t.Fatalf("Coverage = %+v, want the URL's more specific outlet", got)
	}
}

func TestCoverageBlankOutletUsesDomain(t *testing.T) {
	ratings := &Ratings{
		Source: Source{Attribution: "Example Ratings"},
		Ratings: []Rating{
			{Outlet: "Broad", Domains: []string{"example.com"}, Lean: Left},
		},
	}
	got := Coverage(ratings, []Source{
		{URL: "https://www.example.com/a"},
		{URL: "https://www.example.com/b"},
	}, 0)
	if got.Left != 1 || got.Unrated != 0 {
		t.Fatalf("Coverage = %+v, want one matched outlet", got)
	}
}

func TestCoverageNilRatings(t *testing.T) {
	got := Coverage(nil, []Source{
		{Outlet: "One", URL: "https://one.example/a"},
		{Outlet: "Two", URL: "https://two.example/a", Opinion: true},
	}, 0)
	if got.Unrated != 1 || got.Opinion != 1 || got.Attribution != "" {
		t.Fatalf("Coverage = %+v", got)
	}
}

func TestCoverageEmpty(t *testing.T) {
	got := Coverage(testRatings(), nil, 0)
	if got != (Bar{Attribution: "Example Ratings Cooperative"}) {
		t.Fatalf("Coverage = %+v", got)
	}
	if got.Label() != "Coverage found among monitored sources: 0 left · 0 center · 0 right" {
		t.Fatalf("Label = %q", got.Label())
	}
}

func TestCoverageFetchFailedIsNotSilence(t *testing.T) {
	ratings := &Ratings{
		Source: Source{Attribution: "Example Ratings"},
		Ratings: []Rating{
			{Outlet: "One", Lean: Right},
			{Outlet: "Two", Lean: Right},
			{Outlet: "Three", Lean: Right},
			{Outlet: "Four", Lean: Right},
			{Outlet: "Five", Lean: Right},
		},
	}
	sources := []Source{
		{Outlet: "One"}, {Outlet: "Two"}, {Outlet: "Three"}, {Outlet: "Four"}, {Outlet: "Five"},
	}
	got := Coverage(ratings, sources, 40)
	if got.Left != 0 || got.Right != 5 || got.FetchFailed != 40 {
		t.Fatalf("Coverage = %+v", got)
	}
	if got.Blindspot != "left" {
		t.Fatalf("Blindspot = %q, want left", got.Blindspot)
	}
}

func TestBlindspotThresholds(t *testing.T) {
	tests := []struct {
		name string
		b    Bar
		want string
	}{
		{"too few rated", Bar{Left: 0, Right: 4}, ""},
		{"left missing", Bar{Left: 0, Center: 0, Right: 5}, "left"},
		{"right missing", Bar{Left: 5, Right: 0}, "right"},
		{"left at ten percent", Bar{Left: 1, Center: 0, Right: 9}, "left"},
		{"left above ten percent", Bar{Left: 2, Center: 0, Right: 8}, ""},
		{"right at ten percent", Bar{Left: 9, Right: 1}, "right"},
		{"right above ten percent", Bar{Left: 8, Right: 2}, ""},
		{"left missing with center", Bar{Left: 0, Center: 5, Right: 5}, "left"},
		{"both sides low", Bar{Center: 10}, ""},
		{"no side at half", Bar{Left: 1, Center: 10, Right: 9}, ""},
		{"exact half", Bar{Left: 0, Center: 3, Right: 3}, "left"},
		{"both sides present", Bar{Left: 3, Center: 2, Right: 3}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := blindspot(tt.b); got != tt.want {
				t.Fatalf("blindspot(%+v) = %q, want %q", tt.b, got, tt.want)
			}
		})
	}
}
