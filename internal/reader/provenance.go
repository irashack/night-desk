package reader

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/store"
)

// provenanceView tells a reader what the source checks found for one story:
// the advisory checks code ran against Night Desk's stored copy of each source
// when the edition was published.
type provenanceView struct {
	// Status is checked (nothing to report) or notes (findings). Stories
	// with no stored source page to compare get no view at all.
	Status  string
	Summary string
	Notes   []string
	Checked string
	// Compared of Cited source pages had a stored copy to compare with.
	Compared, Cited int
}

var warningPath = regexp.MustCompile(`^sections\[(\d+)\]\.items\[(\d+)\]\.(title|summary|why|url|sources\[(\d+)\]\.url)$`)

var fieldNames = map[string]string{"title": "headline", "summary": "summary", "why": "“For you” line"}

// note turns one stored source-check warning into a sentence for readers. For
// a cited page the check could not compare it reports true; it
// returns "" for a warning that is not about the sources.
func note(field, rule string) (string, bool) {
	if field == "url" || strings.HasPrefix(field, "sources[") {
		if strings.Contains(rule, "no stored snapshot") || strings.Contains(rule, "fetch failed") {
			return "unverified", true
		}
		return "", false
	}
	name := fieldNames[field]
	code, value, _ := strings.Cut(rule, ": ")
	switch code {
	case "unsupported-number":
		return fmt.Sprintf("The figure “%s” in the %s is not in the source.", value, name), false
	case "unsupported-name":
		return fmt.Sprintf("The name “%s” in the %s is not in the source.", value, name), false
	case "unsupported-former-title":
		return fmt.Sprintf("The title “%s” in the %s is not in the source.", value, name), false
	case "truncated":
		return fmt.Sprintf("The %s may be cut off.", name), false
	}
	return "", false
}

// provenance builds each story's source-check summary from the record kept
// when date's edition was published. Stories get none when no record remains
// (it is kept for the paper's retention_days).
func (s *Server) provenance(ctx context.Context, scope auth.PaperScope, date string, ed *edition.Edition, zone *time.Location, retentionDays int) (map[string]*provenanceView, error) {
	warnings, at, ok, err := s.Store.PublishedChecks(ctx, scope, date, retentionDays)
	if err != nil || !ok {
		return nil, err
	}
	return provenanceFrom(ed, warnings, at.In(zone)), nil
}

func provenanceFrom(ed *edition.Edition, warnings []store.DiagnosticProblem, at time.Time) map[string]*provenanceView {
	type found struct {
		// notes lists what the check could not find.
		notes []string
		// unverified holds the cited addresses the check had no copy of.
		unverified map[string]bool
	}
	byItem := map[[2]int]*found{}
	for _, w := range warnings {
		m := warningPath.FindStringSubmatch(w.Path)
		if m == nil {
			continue
		}
		i, _ := strconv.Atoi(m[1])
		j, _ := strconv.Atoi(m[2])
		if i >= len(ed.Sections) || j >= len(ed.Sections[i].Items) {
			continue
		}
		it := ed.Sections[i].Items[j]
		text, unverified := note(strings.SplitN(m[3], ".", 2)[0], w.Rule)
		if text == "" {
			continue
		}
		f := byItem[[2]int{i, j}]
		if f == nil {
			f = &found{unverified: map[string]bool{}}
			byItem[[2]int{i, j}] = f
		}
		switch {
		case unverified:
			link := citedURL(it, m[4])
			if f.unverified[link] {
				continue
			}
			f.unverified[link] = true
		case !contains(f.notes, text):
			f.notes = append(f.notes, text)
		}
	}
	checked := at.Format("Jan 2, 3:04 pm MST")
	out := map[string]*provenanceView{}
	for i, sec := range ed.Sections {
		for j, it := range sec.Items {
			cited := citedURLs(it)
			v := &provenanceView{Status: "checked", Checked: checked, Compared: len(cited), Cited: len(cited)}
			unverified := map[string]bool{}
			if f := byItem[[2]int{i, j}]; f != nil {
				// Only findings reach the reader: a page Night Desk kept no
				// copy of is the usual case (link-only papers) and nothing
				// the reader can act on.
				v.Notes = f.notes
				unverified = f.unverified
				v.Compared = max(len(cited)-len(f.unverified), 0)
			}
			switch {
			case len(v.Notes) == 1:
				v.Status, v.Summary = "notes", "1 detail to double-check"
			case len(v.Notes) > 1:
				v.Status, v.Summary = "notes", fmt.Sprintf("%d details to double-check", len(v.Notes))
			case v.Compared == 0:
				// Nothing was compared: say nothing rather than warn.
				continue
			default:
				v.Summary = "Checked against " + comparedOutlets(it, unverified)
			}
			out[it.ID] = v
		}
	}
	return out
}

// comparedOutlets names the outlets whose pages the check compared a story
// with: "Wire", "Wire and Herald", or "Wire and 2 other sources".
func comparedOutlets(it edition.Item, unverified map[string]bool) string {
	var names []string
	add := func(link, outlet string) {
		if link == "" || unverified[link] {
			return
		}
		if outlet == "" {
			if u, err := url.Parse(link); err == nil && u.Hostname() != "" {
				outlet = strings.TrimPrefix(u.Hostname(), "www.")
			} else {
				outlet = "the source"
			}
		}
		if !contains(names, outlet) {
			names = append(names, outlet)
		}
	}
	add(it.URL, it.Source)
	for _, s := range it.Sources {
		add(s.URL, s.Outlet)
	}
	switch len(names) {
	case 0:
		return "the source"
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	}
	return fmt.Sprintf("%s and %d other sources", names[0], len(names)-1)
}

// citedURL is the address a source-check path names: the story's own link, or
// the source at index (a number) in its other coverage.
func citedURL(it edition.Item, index string) string {
	if k, err := strconv.Atoi(index); err == nil && k >= 0 && k < len(it.Sources) {
		return it.Sources[k].URL
	}
	return it.URL
}

// citedURLs are the distinct addresses a story cites, as the source checks
// count them.
func citedURLs(it edition.Item) map[string]bool {
	out := map[string]bool{}
	if it.URL != "" {
		out[it.URL] = true
	}
	for _, s := range it.Sources {
		if s.URL != "" {
			out[s.URL] = true
		}
	}
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// paperZone is the paper's time zone, or UTC when it names none the system knows.
func paperZone(name string) *time.Location {
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	return time.UTC
}
