// Package candidate defines immutable article snapshots gathered for one paper.
package candidate

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxTextBytes = 200 * 1024

// Candidate contains source data, never instructions. Paper scope comes from the
// authenticated caller and is deliberately absent from this transport object.
type Candidate struct {
	ID           int64      `json:"id,omitempty"`
	Section      string     `json:"section"`
	CanonicalURL string     `json:"canonical_url"`
	Aliases      []string   `json:"aliases"`
	Outlet       string     `json:"outlet"`
	Title        string     `json:"title"`
	PublishedAt  *time.Time `json:"published_at,omitempty"`
	FetchedAt    time.Time  `json:"fetched_at"`
	Byline       []string   `json:"byline"`
	Text         string     `json:"text"`
	State        string     `json:"state"`
	WireGroup    *string    `json:"wire_group,omitempty"`
	Repeat       bool       `json:"repeat"`
}

type Problem struct {
	Path string `json:"path"`
	Rule string `json:"rule"`
}

func (p Problem) String() string { return p.Path + ": " + p.Rule }

var slug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
var htmlTag = regexp.MustCompile(`<[/!?]?[a-zA-Z]`)

// Validate checks a snapshot before it enters the candidate pool. Extracted text
// may contain paragraph breaks and tabs; metadata remains single-line text.
func Validate(c Candidate) []Problem {
	var problems []Problem
	add := func(path, rule string) { problems = append(problems, Problem{path, rule}) }
	plain := func(path, value string, limit int, required, multiline bool) {
		if (required && value == "") || strings.TrimSpace(value) != value {
			add(path, "must be trimmed non-empty text")
		}
		if !utf8.ValidString(value) {
			add(path, "must be valid UTF-8")
		}
		if strings.ContainsFunc(value, func(r rune) bool {
			return unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\r' || r == '\t'))
		}) {
			add(path, "must not contain control characters")
		}
		if htmlTag.MatchString(value) || strings.Contains(value, "](") {
			add(path, "must be plain text (no HTML tags or Markdown links)")
		}
		if limit > 0 && utf8.RuneCountInString(value) > limit {
			add(path, fmt.Sprintf("must be at most %d characters", limit))
		}
	}
	webURL := func(path, value string) {
		u, err := url.Parse(value)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.Opaque != "" || strings.ContainsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) || len(value) > 8192 {
			add(path, "must be an absolute http or https URL without userinfo, at most 8192 bytes")
		}
	}
	if c.ID < 0 {
		add("id", "must not be negative")
	}
	if !slug.MatchString(c.Section) {
		add("section", "must match ^[a-z0-9][a-z0-9-]{0,63}$")
	}
	webURL("canonical_url", c.CanonicalURL)
	if len(c.Aliases) > 100 {
		add("aliases", "must contain at most 100 URLs")
	}
	for i, alias := range c.Aliases {
		webURL(fmt.Sprintf("aliases[%d]", i), alias)
	}
	plain("outlet", c.Outlet, 200, true, false)
	plain("title", c.Title, 1000, true, false)
	if c.FetchedAt.IsZero() || c.FetchedAt.Year() < 1 || c.FetchedAt.Year() > 9999 {
		add("fetched_at", "must be a non-zero RFC 3339 timestamp")
	}
	if c.PublishedAt != nil && (c.PublishedAt.IsZero() || c.PublishedAt.Year() < 1 || c.PublishedAt.Year() > 9999) {
		add("published_at", "must be a non-zero RFC 3339 timestamp")
	}
	if len(c.Byline) > 32 {
		add("byline", "must contain at most 32 authors")
	}
	for i, author := range c.Byline {
		plain(fmt.Sprintf("byline[%d]", i), author, 200, true, false)
	}
	plain("text", c.Text, 0, c.State == "ok", true)
	if len(c.Text) > MaxTextBytes {
		add("text", "must be at most 204800 bytes")
	}
	if c.State != "ok" && c.State != "unavailable" {
		add("state", "must be one of ok, unavailable")
	}
	if c.State == "unavailable" && c.Text != "" {
		add("text", "must be empty when state is unavailable")
	}
	if c.WireGroup != nil {
		plain("wire_group", *c.WireGroup, 128, true, false)
	}
	return problems
}
