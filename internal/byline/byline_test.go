package byline

import (
	"reflect"
	"strings"
	"testing"
)

func TestClean(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		outlet      string
		outletNames []string
		want        string
	}{
		{"empty", "", "", nil, ""},
		{"by prefix", "By Jane Doe", "", nil, "Jane Doe"},
		{"written by prefix", "Written by Jane Doe", "", nil, "Jane Doe"},
		{"rss email form", "jdoe@example.com (Jane Doe)", "", nil, "Jane Doe"},
		{"email alone", "jdoe@example.com", "", nil, ""},
		{"email plus name", "jdoe@example.com Jane Doe", "", nil, "Jane Doe"},
		{"co-authors kept", "Jane Doe and John Roe", "", nil, "Jane Doe and John Roe"},
		{"wire suffix", "Jane Doe, CNN", "", nil, "Jane Doe"},
		{"pipe wire suffix", "Jane Doe | Reuters", "", nil, "Jane Doe"},
		{"several wire suffixes", "Jane Doe, CNN, Reuters", "", nil, "Jane Doe"},
		{"own outlet suffix", "Jane Doe - The Hill", "The Hill", nil, "Jane Doe"},
		{"other outlet name", "The Washington Post", "", []string{"the washington post"}, ""},
		{"outlet name alone", "Reuters", "", nil, ""},
		{"staff", "Staff", "", nil, ""},
		{"staff writer", "Staff Writer", "", nil, ""},
		{"news desk", "News Desk", "", nil, ""},
		{"role suffix", "Jane Doe, opinion contributor", "", nil, "Jane Doe"},
		{"long role suffix", "Jane Doe, senior political correspondent", "", nil, "Jane Doe"},
		{"place and agencies", "Jane Doe in Nairobi and agencies", "", nil, "Jane Doe"},
		{"place between authors", "Jane Doe in New York and John Roe", "", nil, "Jane Doe and John Roe"},
		{"html fragment", "<strong>Jane Doe</strong>", "", nil, "Jane Doe"},
		{"html entities", "<span>Jane Doe &amp; John Roe</span>", "", nil, "Jane Doe & John Roe"},
		{"url", "https://example.com/jane", "", nil, ""},
		{"bare www", "www.example.com/jane", "", nil, ""},
		{"digits", "Jane Doe 2", "", nil, ""},
		{"no letters", "---", "", nil, ""},
		{"too long", strings.Repeat("A", maxBylineChars+1), "", nil, ""},
		{"limit", strings.Repeat("A", maxBylineChars), "", nil, strings.Repeat("A", maxBylineChars)},
		{"case preserved", "JANE DOE", "", nil, "JANE DOE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Clean(test.raw, test.outlet, test.outletNames); got != test.want {
				t.Fatalf("Clean(%q, %q, %v) = %q, want %q", test.raw, test.outlet, test.outletNames, got, test.want)
			}
		})
	}
}

func TestFromHTMLJSONLD(t *testing.T) {
	page := []byte(`<!doctype html><html><head>
		<script type="application/ld+json">
		{"@context":"https://schema.org","@type":"NewsArticle",
		 "author":[{"@type":"Person","name":"Jane Doe"},
		           {"@type":"Person","name":"By John Roe"}]}
		</script>
		<meta name="author" content="Meta Person">
	</head></html>`)
	want := []string{"Jane Doe", "John Roe"}
	if got := FromHTML(page); !reflect.DeepEqual(got, want) {
		t.Fatalf("FromHTML = %#v, want %#v", got, want)
	}
}

func TestFromHTMLGraphAndStringAuthor(t *testing.T) {
	page := []byte(`<html><head>
		<script type="application/ld+json">
		{"@context":"https://schema.org","@graph":[
			{"@type":"WebSite","name":"Example"},
			{"@type":"NewsArticle","author":{"@type":"Person","name":"Jane Doe"}}
		]}
		</script>
	</head></html>`)
	want := []string{"Jane Doe"}
	if got := FromHTML(page); !reflect.DeepEqual(got, want) {
		t.Fatalf("FromHTML graph = %#v, want %#v", got, want)
	}

	if got := FromHTML([]byte(`<script type="application/ld+json">{"author":"Jane Doe"}</script>`)); !reflect.DeepEqual(got, want) {
		t.Fatalf("FromHTML string author = %#v, want %#v", got, want)
	}
}

func TestFromHTMLSkipsMalformedAndInvalidJSONLD(t *testing.T) {
	page := []byte(`<html><head>
		<script type="application/ld+json">{not json</script>
		<script type="application/ld+json">[{"author":{"name":"Staff"}},{"author":{"name":"Jane Doe"}}]</script>
		<meta name="author" content="Fallback Person">
	</head></html>`)
	want := []string{"Jane Doe"}
	if got := FromHTML(page); !reflect.DeepEqual(got, want) {
		t.Fatalf("FromHTML malformed first = %#v, want %#v", got, want)
	}
}

func TestFromHTMLFallsBackToMeta(t *testing.T) {
	page := []byte(`<html><head>
		<meta property="article:author" content="By John Roe">
		<meta name="author" content="Jane Doe">
		<meta property="article:author" content="Jane Doe">
		<meta name="byl" content="Byl Person">
		<meta name="parsely-author" content="Parsely Person">
	</head></html>`)
	want := []string{"Jane Doe", "John Roe", "Byl Person", "Parsely Person"}
	if got := FromHTML(page); !reflect.DeepEqual(got, want) {
		t.Fatalf("FromHTML meta = %#v, want %#v", got, want)
	}
}

func TestFromHTMLFallsBackWhenJSONLDHasNoPerson(t *testing.T) {
	page := []byte(`<html><head>
		<script type="application/ld+json">{"author":{"name":"Staff"}}</script>
		<meta name="author" content="Jane Doe">
	</head></html>`)
	want := []string{"Jane Doe"}
	if got := FromHTML(page); !reflect.DeepEqual(got, want) {
		t.Fatalf("FromHTML staff fallback = %#v, want %#v", got, want)
	}
}

func TestFromHTMLDeduplicates(t *testing.T) {
	page := []byte(`<html><head>
		<script type="application/ld+json">
		{"author":[{"name":"Jane Doe"},{"name":"Jane Doe"},{"name":"John Roe"}]}
		</script>
	</head></html>`)
	want := []string{"Jane Doe", "John Roe"}
	if got := FromHTML(page); !reflect.DeepEqual(got, want) {
		t.Fatalf("FromHTML duplicates = %#v, want %#v", got, want)
	}
}

func TestFromHTMLNoByline(t *testing.T) {
	for _, page := range [][]byte{
		nil,
		[]byte(``),
		[]byte(`<html><head><title>No author</title></head></html>`),
		[]byte(`<meta name="author" content="Staff">`),
		[]byte(`<script type="application/ld+json">not json</script>`),
	} {
		if got := FromHTML(page); got != nil {
			t.Fatalf("FromHTML(%q) = %#v, want nil", page, got)
		}
	}
}

func TestJoin(t *testing.T) {
	tests := []struct {
		names []string
		want  string
	}{
		{nil, ""},
		{[]string{}, ""},
		{[]string{"Jane Doe"}, "Jane Doe"},
		{[]string{" Jane Doe "}, "Jane Doe"},
		{[]string{"Jane Doe", "John Roe"}, "Jane Doe and John Roe"},
		{[]string{"Jane Doe", "John Roe", "Mary Major"}, "Jane Doe, John Roe and Mary Major"},
		{[]string{"Jane Doe", "", "Jane Doe", "John Roe"}, "Jane Doe and John Roe"},
	}
	for _, test := range tests {
		if got := Join(test.names); got != test.want {
			t.Fatalf("Join(%#v) = %q, want %q", test.names, got, test.want)
		}
	}
}
