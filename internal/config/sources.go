package config

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

const (
	MaxSources   = 1024
	MaxOPMLBytes = 1 << 20
)

// Source names exactly one feed, local OPML import, or Google News search.
// OPML imports are expanded to feed sources by Load, never by an API edit.
type Source struct {
	Feed       string      `json:"feed,omitempty"`
	OPML       string      `json:"opml,omitempty"`
	GoogleNews *GoogleNews `json:"google_news,omitempty"`
	fields     uint8
	decoded    bool
}

type GoogleNews struct {
	Site  string `json:"site"`
	Query string `json:"query"`
}

type Gather struct {
	AllowPrivate []string `json:"allow_private"`
}

// Filters are a paper's ingestion filters: items from blocked outlets (feed
// name, or URL host and its subdomains) or with a blocked headline phrase are
// dropped before their pages are fetched, as are items matching a built-in rule
// not listed in Disabled.
type Filters struct {
	BlockedOutlets  []string `json:"blocked_outlets,omitempty"`
	BlockedKeywords []string `json:"blocked_keywords,omitempty"`
	Disabled        []string `json:"disabled,omitempty"`
}

// IngestFilters names the built-in ingestion rules, in the order gathering
// applies them after blocked outlets and keywords.
var IngestFilters = []string{"betting", "fantasy", "roundup", "cartoon"}

func (f Filters) check(add func(string, string), base string) {
	for _, list := range []struct {
		name   string
		values []string
	}{{"blocked_outlets", f.BlockedOutlets}, {"blocked_keywords", f.BlockedKeywords}} {
		if len(list.values) > 200 {
			add(base+list.name, "must contain at most 200 entries")
		}
		seen := map[string]bool{}
		for i, v := range list.values {
			path := fmt.Sprintf("%s%s[%d]", base, list.name, i)
			if v == "" || strings.TrimSpace(v) != v || len(v) > 200 || strings.ContainsAny(v, "\r\n\t") {
				add(path, "must be trimmed single-line text of 1..200 bytes")
			}
			if seen[strings.ToLower(v)] {
				add(path, "must be unique")
			}
			seen[strings.ToLower(v)] = true
		}
	}
	disabled := map[string]bool{}
	for i, name := range f.Disabled {
		path := fmt.Sprintf("%sdisabled[%d]", base, i)
		if !slices.Contains(IngestFilters, name) {
			add(path, "must be one of "+strings.Join(IngestFilters, ", "))
		}
		if disabled[name] {
			add(path, "must be unique")
		}
		disabled[name] = true
	}
}

// UnmarshalJSON remembers even empty discriminators so validation cannot accept
// an ambiguous source merely because one of its fields was empty.
func (s *Source) UnmarshalJSON(data []byte) error {
	type plain Source
	var p plain
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	p.decoded = true
	for key := range fields {
		switch key {
		case "feed":
			p.fields |= 1
		case "opml":
			p.fields |= 2
		case "google_news":
			p.fields |= 4
		}
	}
	*s = Source(p)
	return nil
}

func (g Gather) check(add func(string, string)) {
	seen := map[string]bool{}
	for i, address := range g.AllowPrivate {
		path := fmt.Sprintf("gather.allow_private[%d]", i)
		host, port, err := net.SplitHostPort(address)
		n, pe := strconv.Atoi(port)
		if err != nil || pe != nil || n < 1 || n > 65535 || !validHost(host) || address != strings.TrimSpace(address) {
			add(path, "must be host:port with port 1..65535")
		}
		key := strings.ToLower(address)
		if seen[key] {
			add(path, "must be unique")
		}
		seen[key] = true
	}
}

func validHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

func checkSources(add func(string, string), path string, sources []Source, allowOPML bool) {
	if len(sources) > MaxSources {
		add(path, fmt.Sprintf("must contain at most %d sources", MaxSources))
	}
	for i, s := range sources {
		p := fmt.Sprintf("%s[%d]", path, i)
		fields := s.fields
		if !s.decoded {
			if s.Feed != "" {
				fields |= 1
			}
			if s.OPML != "" {
				fields |= 2
			}
			if s.GoogleNews != nil {
				fields |= 4
			}
		}
		switch fields {
		case 1:
			if !validFeed(s.Feed) {
				add(p+".feed", "must be an absolute http or https URL without credentials or fragment")
			}
		case 2:
			if !allowOPML {
				add(p+".opml", "is only allowed in the operator configuration")
				continue
			}
			checkText(add, p+".opml", s.OPML)
		case 4:
			if s.GoogleNews == nil {
				add(p+".google_news", "must be an object")
				continue
			}
			if !validHost(s.GoogleNews.Site) {
				add(p+".google_news.site", "must be a hostname")
			}
			checkText(add, p+".google_news.query", s.GoogleNews.Query)
			if len(s.GoogleNews.Query) > 2000 {
				add(p+".google_news.query", "must be at most 2000 bytes")
			}
		default:
			add(p, "must contain exactly one of feed, opml or google_news")
		}
	}
}

// ValidFeed reports whether value is an absolute http(s) feed URL without
// credentials or fragment, as configured feeds must be.
func ValidFeed(value string) bool { return validFeed(value) }

// Finds reports whether gathering finds this section's sources: it asks to and
// declares none of its own.
func (s Section) Finds() bool { return s.FindSources && s.Sources == nil }

func validFeed(value string) bool {
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !validHost(u.Hostname()) || u.User != nil || u.Fragment != "" || strings.HasSuffix(u.Host, ":") || strings.ContainsFunc(value, unicode.IsControl) {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}

// SourcesFor returns a section's replacement source list. An omitted (nil) list
// inherits paper sources; an explicit empty list disables gathering there.
// Replacing the whole ordered section list also replaces its source overrides.
func (p Paper) SourcesFor(section Section) []Source {
	if section.Sources != nil {
		return section.Sources
	}
	return p.Sources
}

func (c *Config) expandSources(base string) error {
	for i := range c.Papers {
		p := &c.Papers[i]
		path := fmt.Sprintf("papers[%d]", i)
		var err error
		p.Sources, err = expandSources(base, path+".sources", p.Sources)
		if err != nil {
			return err
		}
		for j := range p.Sections {
			p.Sections[j].Sources, err = expandSources(base, fmt.Sprintf("%s.sections[%d].sources", path, j), p.Sections[j].Sources)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func expandSources(base, path string, sources []Source) ([]Source, error) {
	if sources == nil {
		return nil, nil
	}
	out := make([]Source, 0, len(sources))
	for i, source := range sources {
		if source.OPML == "" {
			out = append(out, source)
			continue
		}
		file := source.OPML
		if !filepath.IsAbs(file) {
			file = filepath.Join(base, file)
		}
		feeds, err := readOPML(file)
		if err != nil {
			return nil, fmt.Errorf("%s[%d].opml: %w", path, i, err)
		}
		for _, feed := range feeds {
			out = append(out, Source{Feed: feed})
		}
		if len(out) > MaxSources {
			return nil, fmt.Errorf("%s: must contain at most %d sources after OPML import", path, MaxSources)
		}
	}
	if len(out) > MaxSources {
		return nil, fmt.Errorf("%s: must contain at most %d sources after OPML import", path, MaxSources)
	}
	return out, nil
}

func readOPML(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("must name a readable OPML file")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxOPMLBytes+1))
	if err != nil {
		return nil, fmt.Errorf("must name a readable OPML file")
	}
	if len(data) > MaxOPMLBytes {
		return nil, fmt.Errorf("must be at most %d bytes", MaxOPMLBytes)
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	feeds := []string{}
	seen := map[string]bool{}
	depth, roots := 0, 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("must contain valid OPML")
		}
		switch t := token.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				roots++
				if roots != 1 || t.Name.Local != "opml" {
					return nil, fmt.Errorf("must contain an opml root element")
				}
			}
			if depth > 64 {
				return nil, fmt.Errorf("must contain at most 64 nested elements")
			}
			if t.Name.Local != "outline" {
				continue
			}
			for _, attr := range t.Attr {
				if attr.Name.Local != "xmlUrl" {
					continue
				}
				if !validFeed(attr.Value) {
					return nil, fmt.Errorf("outline xmlUrl must be an absolute http or https URL without credentials or fragment")
				}
				if !seen[attr.Value] {
					feeds = append(feeds, attr.Value)
					seen[attr.Value] = true
				}
				if len(feeds) > MaxSources {
					return nil, fmt.Errorf("must contain at most %d feeds", MaxSources)
				}
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(t)) != "" {
				return nil, fmt.Errorf("must contain valid OPML")
			}
		}
	}
	if roots != 1 || len(feeds) == 0 {
		return nil, fmt.Errorf("must contain at least one feed in an opml root element")
	}
	return feeds, nil
}
