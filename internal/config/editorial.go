package config

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxSections bounds a paper's sections: the reader pages and the writing agent's
// scouting both assume a paper a person reads in one sitting.
const MaxSections = 16

// Editorial is the part of a paper its writing agent may change through the agent
// API. Identity, place coordinates, timezone, look and readers stay declared.
type Editorial struct {
	Masthead string    `json:"masthead"`
	Place    string    `json:"place,omitempty"`
	Brief    *string   `json:"brief,omitempty"`
	Deadline *string   `json:"deadline,omitempty"`
	Motto    *Motto    `json:"motto,omitempty"`
	Sections []Section `json:"sections"`
	Boxes    Boxes     `json:"boxes"`
}

func (p Paper) Editorial() Editorial {
	return Editorial{p.Masthead, p.Place, p.Brief, p.Deadline, p.Motto, p.Sections, p.Boxes}
}

// WithEditorial returns a copy of p carrying e's editorial fields.
func (p Paper) WithEditorial(e Editorial) Paper {
	p.Masthead, p.Place, p.Brief, p.Deadline, p.Motto, p.Sections, p.Boxes =
		e.Masthead, e.Place, e.Brief, e.Deadline, e.Motto, e.Sections, e.Boxes
	return p
}

// Problems lists e's violations as "path: rule", paths relative to the paper.
func (e Editorial) Problems() []string {
	var out []string
	e.check(func(p, r string) { out = append(out, p+": "+r) }, "", false)
	return out
}

func (e Editorial) check(add func(p, r string), b string, allowOPML bool) {
	guidance := func(p, v string, cap int) {
		checkText(add, p, v)
		if htmlTag.MatchString(v) || strings.Contains(v, "](") {
			add(p, "must be plain text (no HTML tags or Markdown links)")
		}
		if utf8.RuneCountInString(v) > cap {
			add(p, fmt.Sprintf("must be at most %d characters", cap))
		}
	}
	checkText(add, b+"masthead", e.Masthead)
	if e.Place != "" {
		checkText(add, b+"place", e.Place)
	}
	if e.Brief != nil {
		guidance(b+"brief", *e.Brief, 2000)
	}
	if e.Deadline != nil && !deadline.MatchString(*e.Deadline) {
		add(b+"deadline", "must be HH:MM (00:00..23:59) in the paper's timezone")
	}
	if e.Motto != nil {
		guidance(b+"motto.guidance", e.Motto.Guidance, 2000)
	}
	if len(e.Sections) == 0 {
		add(b+"sections", "must contain at least one section")
	}
	if len(e.Sections) > MaxSections {
		add(b+"sections", fmt.Sprintf("must contain at most %d sections", MaxSections))
	}
	keys := map[string]bool{}
	for j, s := range e.Sections {
		path := fmt.Sprintf("%ssections[%d].", b, j)
		if !slug.MatchString(s.Key) || keys[s.Key] {
			add(path+"key", "must be a unique slug")
		}
		keys[s.Key] = true
		checkText(add, path+"title", s.Title)
		checkSources(add, path+"sources", s.Sources, allowOPML)
		if s.Brief != nil {
			guidance(path+"brief", *s.Brief, 600)
		}
	}
}

func checkText(add func(p, r string), p, v string) {
	if strings.TrimSpace(v) == "" || strings.TrimSpace(v) != v || strings.ContainsFunc(v, unicode.IsControl) {
		add(p, "must be trimmed non-empty text")
	}
}

// WithEditorials returns a copy of c in which each named paper carries its
// editorial revision; c itself, a published snapshot, is never modified.
func (c *Config) WithEditorials(edits map[string]Editorial) *Config {
	if len(edits) == 0 {
		return c
	}
	out := *c
	out.Papers = make([]Paper, len(c.Papers))
	for i, p := range c.Papers {
		if e, ok := edits[p.ID]; ok {
			p = p.WithEditorial(e)
		}
		out.Papers[i] = p
	}
	return &out
}
