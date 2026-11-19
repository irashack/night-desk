package reader

import (
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/store"
)

// editionSections uses the publication order when available, then appends any
// unlisted edition sections in payload order. Older editions use today's layout.
func editionSections(p config.Paper, ed *edition.Edition, layout *store.EditionLayout) []store.LayoutSection {
	titles := map[string]string{}
	for _, s := range p.Sections {
		titles[s.Key] = s.Title
	}
	var sections []store.LayoutSection
	if layout != nil {
		sections = append(sections, layout.Sections...)
	} else {
		for _, s := range p.Sections {
			sections = append(sections, store.LayoutSection{Key: s.Key, Title: s.Title})
		}
	}
	seen := map[string]bool{}
	var out []store.LayoutSection
	add := func(s store.LayoutSection) {
		if seen[s.Key] {
			return
		}
		seen[s.Key] = true
		if s.Title == "" {
			s.Title = titles[s.Key]
		}
		if s.Title == "" {
			s.Title = s.Key
		}
		out = append(out, s)
	}
	for _, s := range sections {
		add(s)
	}
	for _, s := range ed.Sections {
		add(store.LayoutSection{Key: s.Key})
	}
	return out
}
