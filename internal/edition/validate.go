package edition

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
)

type Problem struct {
	Path     string
	Rule     string
	Severity Severity
}

func (p Problem) String() string { return p.Path + ": " + p.Rule }

type SectionRule struct {
	Key      string
	Required bool
}

type Limits struct {
	ItemsPerSection int
	ItemsPerEdition int
}

// Zero limits use the v1 ceilings. Positive limits may tighten, not relax, them.
type PaperRules struct {
	Sections []SectionRule
	Limits   Limits
}

var itemID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
var htmlTag = regexp.MustCompile(`<[/!?]?[a-zA-Z]`)

// time.Parse also accepts single-digit hours, comma fractions, and overflowing
// zone offsets; these are not RFC 3339 timestamps.
var timestamp = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](\.[0-9]+)?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

func Validate(e *Edition, p PaperRules) []Problem {
	var problems []Problem
	add := func(path, rule string) { problems = append(problems, Problem{path, rule, Error}) }
	if e == nil {
		add("$", "must be an edition object")
		return problems
	}
	text := func(path, value string, cap int) {
		if value == "" || strings.TrimSpace(value) != value {
			add(path, "must be trimmed non-empty text")
		}
		if strings.ContainsFunc(value, unicode.IsControl) {
			add(path, "must not contain control characters")
		}
		if htmlTag.MatchString(value) || strings.Contains(value, "](") {
			add(path, "must be plain text (no HTML tags or Markdown links)")
		}
		if cap > 0 && utf8.RuneCountInString(value) > cap {
			add(path, fmt.Sprintf("must be at most %d characters", cap))
		}
	}
	enum := func(path, value string, allowed ...string) {
		if !slices.Contains(allowed, value) {
			add(path, "must be one of "+strings.Join(allowed, ", "))
		}
	}
	https := func(path, value string) {
		u, err := url.Parse(value)
		if err != nil {
			add(path, "must be an absolute https URL without userinfo")
			return
		}
		if u.Scheme != "https" {
			add(path, "must be https")
		}
		if !u.IsAbs() || u.Hostname() == "" || u.Opaque != "" || strings.ContainsAny(value, " \t\r\n") {
			add(path, "must be an absolute URL with a host")
		}
		if u.User != nil {
			add(path, "must not contain userinfo")
		}
	}
	if e.Schema != 1 && e.Schema != 2 {
		add("schema", "must be 1 or 2")
	}
	if e.Motto != nil {
		enum("motto.kind", e.Motto.Kind, "scripture", "quotation")
		text("motto.text", e.Motto.Text, 400)
		text("motto.source", e.Motto.Source, 120)
	}
	if e.Sections == nil {
		add("sections", "must be an array")
	}
	order := make(map[string]int, len(p.Sections))
	for i, s := range p.Sections {
		order[s.Key] = i
	}
	seenSections := make(map[string]bool)
	ids := make(map[string]bool)
	kinds := make(map[string]string)
	type about struct{ path, id string }
	var abouts []about
	last, total := -1, 0
	sectionLimit, editionLimit := 6, 80
	if n := p.Limits.ItemsPerSection; n > 0 && n < sectionLimit {
		sectionLimit = n
	}
	if n := p.Limits.ItemsPerEdition; n > 0 && n < editionLimit {
		editionLimit = n
	}
	for i, section := range e.Sections {
		path := fmt.Sprintf("sections[%d]", i)
		text(path+".key", section.Key, 0)
		index, known := order[section.Key]
		if !known {
			add(path+".key", "must be a paper section key")
		}
		if seenSections[section.Key] {
			add(path+".key", "must be unique")
		}
		seenSections[section.Key] = true
		if known {
			if index < last {
				problems = append(problems, Problem{path + ".key", "should follow the paper's section order", Warning})
			}
			last = max(last, index)
			if p.Sections[index].Required && len(section.Items) == 0 {
				add(path+".items", "required section must be non-empty")
			}
		}
		if section.Items == nil {
			add(path+".items", "must be an array")
		}
		if len(section.Items) > sectionLimit {
			add(path+".items", fmt.Sprintf("must contain at most %d items", sectionLimit))
		}
		total += len(section.Items)
		for j, item := range section.Items {
			base := fmt.Sprintf("%s.items[%d].", path, j)
			if !itemID.MatchString(item.ID) {
				add(base+"id", "must match ^[a-z0-9][a-z0-9-]{0,63}$")
			}
			if ids[item.ID] {
				add(base+"id", "must be unique across the edition")
			}
			ids[item.ID] = true
			kinds[item.ID] = item.Kind
			if e.Schema == 2 {
				enum(base+"kind", item.Kind, "news", "seasonal", "evergreen", "brief", "opinion")
			} else {
				enum(base+"kind", item.Kind, "news", "seasonal", "evergreen", "brief")
			}
			if item.Kind == "opinion" && item.ID == e.Lead {
				add("lead", "must not reference an opinion item")
			}
			text(base+"title", item.Title, 160)
			text(base+"summary", item.Summary, 600)
			if item.Why != nil {
				text(base+"why", *item.Why, 300)
			}
			text(base+"source", item.Source, 80)
			https(base+"url", item.URL)
			if e.Schema == 1 {
				if item.Byline != nil {
					add(base+"byline", "requires schema 2")
				}
				if item.Sources != nil {
					add(base+"sources", "requires schema 2")
				}
				if item.About != nil {
					add(base+"about", "requires schema 2")
				}
				if item.Stance != nil {
					add(base+"stance", "requires schema 2")
				}
			} else {
				if item.Byline != nil {
					text(base+"byline", *item.Byline, 120)
				}
				if item.Stance != nil {
					text(base+"stance", *item.Stance, 140)
					if item.Kind != "opinion" {
						add(base+"stance", "is only for opinion items")
					}
				}
				if item.About != nil {
					if item.Kind != "opinion" {
						add(base+"about", "is only for opinion items")
					}
					abouts = append(abouts, about{base + "about", *item.About})
				}
				if len(item.Sources) > 12 {
					add(base+"sources", "must contain at most 12 sources")
				}
				urls := make(map[string]bool, len(item.Sources))
				for k, source := range item.Sources {
					path := fmt.Sprintf("%ssources[%d].", base, k)
					text(path+"outlet", source.Outlet, 80)
					https(path+"url", source.URL)
					if urls[source.URL] {
						add(path+"url", "must be unique within the item sources")
					}
					if source.URL == item.URL {
						add(path+"url", "must not repeat the item URL")
					}
					urls[source.URL] = true
					if source.Byline != nil {
						text(path+"byline", *source.Byline, 120)
					}
				}
			}
			if item.Minutes != nil && (*item.Minutes < 1 || *item.Minutes > 240) {
				add(base+"minutes", "must be between 1 and 240")
			}
			if item.ImageURL != nil {
				https(base+"image_url", *item.ImageURL)
			}
		}
	}
	for _, s := range p.Sections {
		if s.Required && !seenSections[s.Key] {
			add("sections", fmt.Sprintf("required section %q must be present", s.Key))
		}
	}
	if total > editionLimit {
		add("sections", fmt.Sprintf("must contain at most %d items in total", editionLimit))
	}
	if !ids[e.Lead] || e.Lead == "" {
		add("lead", "must reference an existing item id")
	}
	// An opinion names a story in the same edition, never another opinion.
	for _, a := range abouts {
		if kind, ok := kinds[a.id]; !ok {
			add(a.path, "must reference an item id in this edition")
		} else if kind == "opinion" {
			add(a.path, "must reference a story, not an opinion item")
		}
	}
	if e.Boxes != nil && e.Boxes.Markets != nil {
		m := e.Boxes.Markets
		text("boxes.markets.source", m.Source, 80)
		if _, err := time.Parse(time.RFC3339, m.AsOf); err != nil || !timestamp.MatchString(m.AsOf) {
			add("boxes.markets.as_of", "must be RFC 3339")
		}
		if m.Quotes == nil {
			add("boxes.markets.quotes", "must be an array")
		}
		if len(m.Quotes) > 12 {
			add("boxes.markets.quotes", "must contain at most 12 quotes")
		}
		for i, q := range m.Quotes {
			path := fmt.Sprintf("boxes.markets.quotes[%d].", i)
			text(path+"symbol", q.Symbol, 16)
			text(path+"label", q.Label, 40)
			text(path+"value", q.Value, 24)
			text(path+"change", q.Change, 24)
			enum(path+"direction", q.Direction, "up", "down", "flat")
		}
	}
	return problems
}
