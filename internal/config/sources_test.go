package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func loadSourcesConfig(t *testing.T, paper, top string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "paper.yaml")
	write(t, path, top+strings.Replace(valid, "  masthead: The Daily", "  masthead: The Daily\n"+paper, 1))
	return Load(path)
}

func TestSourcesLoad(t *testing.T) {
	c, err := loadSourcesConfig(t, `  sources:
  - feed: https://news.example/feed.xml
  - google_news: {site: news.example, query: city council}
`, "gather:\n  allow_private: ['miniflux.internal:8080', '[::1]:8081']\n")
	if err != nil {
		t.Fatal(err)
	}
	p := c.Papers[0]
	if p.RetentionDays != 14 || len(p.Sources) != 2 || p.Sources[1].GoogleNews.Query != "city council" || len(c.Gather.AllowPrivate) != 2 {
		t.Fatalf("unexpected sources config: %#v", c)
	}
	if !reflect.DeepEqual(p.SourcesFor(p.Sections[0]), p.Sources) {
		t.Fatal("omitted section sources did not inherit")
	}
	p.Sections[0].Sources = []Source{}
	if got := p.SourcesFor(p.Sections[0]); got == nil || len(got) != 0 {
		t.Fatal("explicit empty sources did not disable gathering")
	}
	p.Sections[0].Sources = []Source{{Feed: "https://other.example/rss"}}
	if got := p.SourcesFor(p.Sections[0]); len(got) != 1 || got[0].Feed != "https://other.example/rss" {
		t.Fatal("override did not replace paper sources")
	}
}

func TestFiltersLoad(t *testing.T) {
	c, err := loadSourcesConfig(t, "  filters:\n    blocked_outlets: [tabloid.example, Daily Picks]\n    blocked_keywords: [horoscope]\n    disabled: [roundup]\n", "")
	if err != nil {
		t.Fatal(err)
	}
	if f := c.Papers[0].Filters; f == nil || len(f.BlockedOutlets) != 2 || f.BlockedKeywords[0] != "horoscope" || f.Disabled[0] != "roundup" {
		t.Fatalf("unexpected filters: %#v", c.Papers[0].Filters)
	}
}

func TestSourcesValidation(t *testing.T) {
	for _, tt := range []struct{ name, body, top, want string }{
		{"retention zero", "  retention_days: 0\n", "", "papers[0].retention_days: must be between 1 and 90"},
		{"retention high", "  retention_days: 91\n", "", "papers[0].retention_days: must be between 1 and 90"},
		{"retention fractional", "  retention_days: 1.5\n", "", "papers[0].retention_days: must be an integer"},
		{"retention type", "  retention_days: '14'\n", "", "papers[0].retention_days: must be an integer"},
		{"empty source", "  sources: [{}]\n", "", "papers[0].sources[0]: must contain exactly one"},
		{"two kinds", "  sources: [{feed: '', opml: file.opml}]\n", "", "papers[0].sources[0]: must contain exactly one"},
		{"bad protocol", "  sources: [{feed: file:///etc/passwd}]\n", "", "papers[0].sources[0].feed: must be an absolute"},
		{"credentials", "  sources: [{feed: 'https://secret:password@news.example/feed'}]\n", "", "papers[0].sources[0].feed: must be an absolute"},
		{"fragment", "  sources: [{feed: 'https://news.example/feed#x'}]\n", "", "papers[0].sources[0].feed: must be an absolute"},
		{"empty port", "  sources: [{feed: 'https://news.example:/feed'}]\n", "", "papers[0].sources[0].feed: must be an absolute"},
		{"empty feed", "  sources: [{feed: ''}]\n", "", "papers[0].sources[0].feed: must be an absolute"},
		{"unknown field", "  sources: [{feed: 'https://news.example/feed', typo: true}]\n", "", "papers[0].sources[0].typo: unknown field"},
		{"unknown search field", "  sources: [{google_news: {site: news.example, query: abc, lang: en}}]\n", "", "papers[0].sources[0].google_news.lang: unknown field"},
		{"search site", "  sources: [{google_news: {site: 'https://news.example', query: abc}}]\n", "", "papers[0].sources[0].google_news.site: must be a hostname"},
		{"search query", "  sources: [{google_news: {site: news.example, query: ''}}]\n", "", "papers[0].sources[0].google_news.query: must be trimmed"},
		{"allow url", "", "gather:\n  allow_private: ['http://localhost:8080']\n", "gather.allow_private[0]: must be host:port"},
		{"allow no port", "", "gather:\n  allow_private: ['localhost']\n", "gather.allow_private[0]: must be host:port"},
		{"allow zero port", "", "gather:\n  allow_private: ['localhost:0']\n", "gather.allow_private[0]: must be host:port"},
		{"allow high port", "", "gather:\n  allow_private: ['localhost:65536']\n", "gather.allow_private[0]: must be host:port"},
		{"allow wildcard", "", "gather:\n  allow_private: ['*.internal:8080']\n", "gather.allow_private[0]: must be host:port"},
		{"allow duplicate", "", "gather:\n  allow_private: ['localhost:8080', 'LOCALHOST:8080']\n", "gather.allow_private[1]: must be unique"},
		{"filter outlet blank", "  filters: {blocked_outlets: [' x']}\n", "", "papers[0].filters.blocked_outlets[0]: must be trimmed single-line text"},
		{"filter keyword duplicate", "  filters: {blocked_keywords: [horoscope, Horoscope]}\n", "", "papers[0].filters.blocked_keywords[1]: must be unique"},
		{"filter unknown rule", "  filters: {disabled: [weather]}\n", "", "papers[0].filters.disabled[0]: must be one of betting, fantasy, roundup, cartoon"},
		{"filter duplicate rule", "  filters: {disabled: [roundup, roundup]}\n", "", "papers[0].filters.disabled[1]: must be unique"},
		{"filter unknown field", "  filters: {blocked: [x]}\n", "", "papers[0].filters.blocked: unknown field"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadSourcesConfig(t, tt.body, tt.top)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
			if strings.Contains(fmt.Sprint(err), "password") {
				t.Fatal("validation leaked credentials")
			}
		})
	}
}

func TestSourcesOPMLExpansion(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "feeds.opml"), `<?xml version="1.0"?><opml version="2.0"><body><outline text="Folder"><outline xmlUrl="https://news.example/feed?a=1&amp;b=2"/><outline xmlUrl="http://other.example/rss"/><outline xmlUrl="https://news.example/feed?a=1&amp;b=2"/></outline></body></opml>`)
	path := filepath.Join(dir, "paper.yaml")
	declaration := strings.Replace(valid, "  look: broadsheet", "  sources: [{opml: feeds.opml}]\n  look: broadsheet", 1)
	declaration = strings.Replace(declaration, "    required: true", "    sources: [{opml: feeds.opml}]\n    required: true", 1)
	write(t, path, declaration)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p := c.Papers[0]
	if len(p.Sources) != 2 || p.Sources[0].Feed != "https://news.example/feed?a=1&b=2" || p.Sources[0].OPML != "" || !reflect.DeepEqual(p.Sources, p.Sections[0].Sources) {
		t.Fatalf("OPML expansion: %#v", p.Sources)
	}
	if got := p.Editorial().Problems(); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestSourcesOPMLInvalid(t *testing.T) {
	for _, tt := range []struct{ name, content, want string }{
		{"malformed", `<opml><body>`, "valid OPML"},
		{"wrong root", `<rss><outline xmlUrl="https://news.example/feed"/></rss>`, "opml root"},
		{"empty", `<opml><body/></opml>`, "at least one feed"},
		{"unsafe URL", `<opml><outline xmlUrl="file:///etc/passwd"/></opml>`, "outline xmlUrl"},
		{"credentials", `<opml><outline xmlUrl="https://hidden:password@news.example/feed"/></opml>`, "outline xmlUrl"},
		{"too large", strings.Repeat(" ", MaxOPMLBytes+1), "at most 1048576 bytes"},
		{"too deep", `<opml>` + strings.Repeat(`<outline>`, 64) + strings.Repeat(`</outline>`, 64) + `</opml>`, "64 nested elements"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, "feeds.opml"), tt.content)
			path := filepath.Join(dir, "paper.yaml")
			write(t, path, strings.Replace(valid, "  look: broadsheet", "  sources: [{opml: feeds.opml}]\n  look: broadsheet", 1))
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), "papers[0].sources[0].opml: ") || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
			if strings.Contains(fmt.Sprint(err), "password") {
				t.Fatal("OPML validation leaked credentials")
			}
		})
	}
	if _, err := readOPML(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing OPML accepted")
	}
}

func TestSourcesEditorialBoundaries(t *testing.T) {
	c, err := loadSourcesConfig(t, "  sources: [{feed: https://paper.example/feed}]\n", "")
	if err != nil {
		t.Fatal(err)
	}
	p := c.Papers[0]
	for _, body := range []string{`[{"opml":"/etc/passwd"}]`, `[{"feed":"","google_news":{"site":"example.com","query":"abc"}}]`, `[{}]`} {
		ed := p.Editorial()
		if err := json.Unmarshal([]byte(body), &ed.Sections[0].Sources); err != nil {
			t.Fatal(err)
		}
		if problems := ed.Problems(); len(problems) == 0 || !strings.HasPrefix(problems[0], "sections[0].sources[0]") {
			t.Fatalf("missing source validation: %v", problems)
		}
	}
	// Revisions preserve the distinct inheritance and disable states.
	for _, override := range [][]Source{nil, {}} {
		ed := p.Editorial()
		ed.Sections[0].Sources = override
		data, err := json.Marshal(ed)
		if err != nil {
			t.Fatal(err)
		}
		var restored Editorial
		if err := json.Unmarshal(data, &restored); err != nil {
			t.Fatal(err)
		}
		if (restored.Sections[0].Sources == nil) != (override == nil) {
			t.Fatal("source inheritance changed in revision round trip")
		}
		out := p.WithEditorial(restored)
		if len(out.Sources) != 1 || out.RetentionDays != 14 {
			t.Fatal("editorial overlay changed operator settings")
		}
	}
}

func TestSourcesOPMLFeedLimit(t *testing.T) {
	var data strings.Builder
	data.WriteString(`<opml><body>`)
	for i := 0; i <= MaxSources; i++ {
		fmt.Fprintf(&data, `<outline xmlUrl="https://news.example/%d"/>`, i)
	}
	data.WriteString(`</body></opml>`)
	file := filepath.Join(t.TempDir(), "large.opml")
	if err := os.WriteFile(file, []byte(data.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readOPML(file); err == nil || !strings.Contains(err.Error(), "1024 feeds") {
		t.Fatal("feed limit not enforced", err)
	}
}

func TestSourcesOPMLTotalLimit(t *testing.T) {
	var data strings.Builder
	data.WriteString(`<opml><body>`)
	for i := 0; i < MaxSources; i++ {
		fmt.Fprintf(&data, `<outline xmlUrl="https://news.example/%d"/>`, i)
	}
	data.WriteString(`</body></opml>`)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "feeds.opml"), data.String())
	_, err := expandSources(dir, "papers[0].sources", []Source{{OPML: "feeds.opml"}, {Feed: "https://other.example/rss"}})
	if err == nil || !strings.Contains(err.Error(), "papers[0].sources: must contain at most 1024 sources") {
		t.Fatal("combined feed limit not enforced", err)
	}
}
