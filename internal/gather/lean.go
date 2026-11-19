package gather

import (
	"net/url"
	"strings"

	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/lean"
)

// enoughFound is how many verified feeds retire a section's topic search feed.
const enoughFound = 3

func sectionSources(p Paper, section config.Section, outlets []lean.Outlet) []config.Source {
	sources := section.Sources
	if sources == nil {
		sources = p.Sources
	}
	if section.Finds() {
		// The paper's feeds, the feeds found for this section, and until enough
		// are found a Google News search on the section's title.
		sources = append([]config.Source{}, sources...)
		found := 0
		for _, ss := range p.FoundSources {
			if ss.Key == section.Key {
				for _, f := range ss.Sources {
					sources = append(sources, config.Source{Feed: f.Feed})
				}
				found = len(ss.Sources)
			}
		}
		if found < enoughFound {
			sources = append(sources, config.Source{GoogleNews: &config.GoogleNews{Query: section.Title}})
		}
	}
	if !section.Lean || len(outlets) == 0 {
		return sources
	}
	// Copy before appending: effective configuration remains immutable.
	sources = append([]config.Source{}, sources...)
	hosts := []string{}
	for _, source := range sources {
		if source.GoogleNews != nil {
			hosts = append(hosts, source.GoogleNews.Site)
		} else if u, err := url.Parse(source.Feed); err == nil {
			hosts = append(hosts, u.Hostname())
		}
	}
	counts := map[string]int{}
	identity := func(outlet lean.Outlet) string {
		if outlet.Outlet != "" {
			return outlet.Outlet
		}
		return strings.ToLower(outlet.Domain)
	}
	monitored := map[string]bool{}
	for _, outlet := range outlets {
		domain := strings.TrimPrefix(strings.ToLower(strings.TrimSuffix(outlet.Domain, ".")), "www.")
		for _, host := range hosts {
			host = strings.TrimPrefix(strings.ToLower(strings.TrimSuffix(host, ".")), "www.")
			if host != "" && (host == domain || strings.HasSuffix(host, "."+domain) || strings.HasSuffix(domain, "."+host)) {
				monitored[identity(outlet)] = true
				break
			}
		}
	}
	for _, outlet := range outlets {
		side := string(outlet.Lean)
		switch outlet.Lean {
		case lean.Left, lean.LeanLeft:
			side = "left"
		case lean.Right, lean.LeanRight:
			side = "right"
		case lean.Center:
		default:
			continue
		}
		if counts[side] >= 3 || monitored[identity(outlet)] {
			continue
		}
		sources = append(sources, config.Source{GoogleNews: &config.GoogleNews{Site: outlet.Domain, Query: section.Title}})
		monitored[identity(outlet)] = true
		counts[side]++
	}
	return sources
}
