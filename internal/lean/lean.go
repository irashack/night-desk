// Package lean implements Night Desk's coverage-lean ratings format, its CSV
// importer, and the per-story coverage bar.
//
// Night Desk ships the format and the importer, never third-party ratings: an
// operator supplies a ratings file with its source and attribution. Every
// coverage bar carries that attribution so a displayed rating always says where
// it came from.
package lean

import (
	"net/url"
	"strings"
)

// Source carries both uses of a source in this package. A ratings file's
// source uses Name, URL, License and Attribution; all four are required there.
// A story's distinct source uses Outlet, URL, Opinion and WireGroup.
//
// The two uses share a type because both are named by the public API: Load and
// ImportCSV consume the file source, and Coverage consumes story sources.
type Source struct {
	// Ratings-file source.
	Name        string `json:"name,omitempty"`
	URL         string `json:"url,omitempty"`
	License     string `json:"license,omitempty"`
	Attribution string `json:"attribution,omitempty"`

	// Story source.
	Outlet    string `json:"outlet,omitempty"`
	Opinion   bool   `json:"opinion,omitempty"`
	WireGroup int    `json:"wire_group,omitempty"`
}

// Lean is an outlet's position on Night Desk's five-point scale.
type Lean string

const (
	Left      Lean = "left"
	LeanLeft  Lean = "lean-left"
	Center    Lean = "center"
	LeanRight Lean = "lean-right"
	Right     Lean = "right"
)

// Valid reports whether l is one of the five scale values.
func (l Lean) Valid() bool {
	switch l {
	case Left, LeanLeft, Center, LeanRight, Right:
		return true
	}
	return false
}

// Factuality is an optional factuality rating. The zero value means the source
// did not supply one.
type Factuality string

const (
	VeryLow       Factuality = "very-low"
	Low           Factuality = "low"
	Mixed         Factuality = "mixed"
	MostlyFactual Factuality = "mostly-factual"
	High          Factuality = "high"
	VeryHigh      Factuality = "very-high"
)

// Valid reports whether f is a factuality value. The empty value is not valid;
// an absent factuality is represented by the field being omitted.
func (f Factuality) Valid() bool {
	switch f {
	case VeryLow, Low, Mixed, MostlyFactual, High, VeryHigh:
		return true
	}
	return false
}

// Rating is one outlet's entry in a ratings file.
type Rating struct {
	Outlet     string     `json:"outlet"`
	Domains    []string   `json:"domains"`
	Lean       Lean       `json:"lean"`
	Factuality Factuality `json:"factuality,omitempty"`
}

// Ratings is a ratings file: a required source with its attribution, and the
// outlet ratings themselves.
type Ratings struct {
	Source  Source   `json:"source"`
	Ratings []Rating `json:"ratings"`
}

// Problem is one strict-validation failure. Path is the location in the file,
// using "$" for the document root; Rule says what is wrong.
type Problem struct {
	Path string `json:"path"`
	Rule string `json:"rule"`
}

func (p Problem) String() string { return p.Path + ": " + p.Rule }

const leanRule = "must be one of left, lean-left, center, lean-right, right"
const factualityRule = "must be one of very-low, low, mixed, mostly-factual, high, very-high"

// normalizeOutlet makes outlet identity case-insensitive and ignores repeated
// whitespace, so one outlet is counted once.
func normalizeOutlet(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// normalizeDomain lowercases a domain, drops a trailing root dot, and removes a
// leading "www." so the registrable form and its subdomains compare together.
func normalizeDomain(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimSuffix(s, ".")
	for strings.HasPrefix(s, "www.") && strings.Contains(strings.TrimPrefix(s, "www."), ".") {
		s = strings.TrimPrefix(s, "www.")
	}
	return s
}

// domainMatches reports whether host is domain or a subdomain of it on a dot
// boundary. Both values are expected to be normalized.
func domainMatches(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// hostOf extracts a normalized host from a URL. It also tolerates a bare host
// or host/path, which keeps matching useful for hand-built Source values.
func hostOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
		return normalizeDomain(u.Hostname())
	}
	if i := strings.IndexAny(raw, "/?#"); i >= 0 {
		raw = raw[:i]
	}
	raw = strings.TrimPrefix(raw, "//")
	if i := strings.LastIndex(raw, "@"); i >= 0 {
		raw = raw[i+1:]
	}
	if i := strings.LastIndex(raw, ":"); i >= 0 && !strings.Contains(raw, "]") {
		raw = raw[:i]
	}
	return normalizeDomain(raw)
}

// lookup finds the rating for a source. A source URL is authoritative: the
// longest listed domain that suffixes its host is used, so a more specific
// domain (news.example.com) wins over a broader one (example.com). The outlet
// display name is only consulted for hand-built sources without a URL, where it
// is the only identity available.
func (r *Ratings) lookup(outlet, rawURL string) (Rating, bool) {
	if r == nil {
		return Rating{}, false
	}
	if host := hostOf(rawURL); host != "" {
		return r.lookupDomain(host)
	}
	return r.lookupOutlet(outlet)
}

// Lookup is lookup for callers outside the package, such as the reader
// labelling an opinion column's outlet.
func (r *Ratings) Lookup(outlet, rawURL string) (Rating, bool) { return r.lookup(outlet, rawURL) }

func (r *Ratings) lookupOutlet(outlet string) (Rating, bool) {
	if r == nil {
		return Rating{}, false
	}
	key := normalizeOutlet(outlet)
	if key == "" {
		return Rating{}, false
	}
	for i := range r.Ratings {
		if normalizeOutlet(r.Ratings[i].Outlet) == key {
			return r.Ratings[i], true
		}
	}
	return Rating{}, false
}

func (r *Ratings) lookupDomain(host string) (Rating, bool) {
	if r == nil || host == "" {
		return Rating{}, false
	}
	best, bestLen := Rating{}, -1
	for i := range r.Ratings {
		for _, d := range r.Ratings[i].Domains {
			d = normalizeDomain(d)
			if d == "" || !domainMatches(host, d) {
				continue
			}
			if len(d) > bestLen {
				best, bestLen = r.Ratings[i], len(d)
			}
		}
	}
	return best, bestLen >= 0
}

// validDomain checks a domain as written in a ratings file. It deliberately
// does not consult a public suffix list: a listed domain is a suffix to match,
// not a registrable domain to derive.
func validDomain(s string) bool {
	if s == "" || strings.TrimSpace(s) != s {
		return false
	}
	s = strings.TrimSuffix(s, ".")
	if s == "" || len(s) > 253 || strings.HasPrefix(s, ".") || strings.Contains(s, "..") {
		return false
	}
	labels := strings.Split(s, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' {
				continue
			}
			return false
		}
	}
	return true
}
