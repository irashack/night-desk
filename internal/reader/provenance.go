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
	// Status is checked (nothing to report), notes (findings) or unchecked
	// (no stored copy of the source to check against).
	Status  string
	Summary string
	Notes   []string
	Checked string
	// Compared of Cited source pages had a stored copy to compare with.
	Compared, Cited int
}

var warningPath = regexp.MustCompile(`^sections\[(\d+)\]\.items\[(\d+)\]\.(title|summary|why|url|sources\[(\d+)\]\.url)$`)

var fieldNames = map[string]string{"title": "headline", "summary": "summary", "why": "“For you” line"}

// note turns one stored source-check warning into a sentence for readers, or
// "" for a warning that is not about the sources.
func note(it edition.Item, field, source, rule string) (string, bool) {
	if field == "url" || strings.HasPrefix(field, "sources[") {
		outlet := it.Source
		if strings.HasPrefix(field, "sources[") {
			outlet = "another outlet"
			if k, err := strconv.Atoi(source); err == nil && k < len(it.Sources) {
				outlet = it.Sources[k].Outlet
				if outlet == "" {
					if u, err := url.Parse(it.Sources[k].URL); err == nil {
						outlet = u.Hostname()
					}
				}
			}
		}
		switch {
		case strings.Contains(rule, "no stored snapshot"):
			return "Not checked against " + outlet + ": Night Desk kept no copy of that page.", true
		case strings.Contains(rule, "fetch failed"):
			return "Not checked against " + outlet + ": no text could be read from the page.", true
		}
		return "", false
	}
	name := fieldNames[field]
	code, value, _ := strings.Cut(rule, ": ")
	switch code {
	case "unsupported-number":
		return fmt.Sprintf("The figure “%s” in the %s was not found in the source.", value, name), false
	case "unsupported-name":
		return fmt.Sprintf("The name “%s” in the %s was not found in the source.", value, name), false
	case "unsupported-former-title":
		return fmt.Sprintf("The title “%s” in the %s was not found in the source.", value, name), false
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
		// unchecked lists each reason a source went unchecked, in order, and
		// how many cited pages it covers.
		unchecked []string
		pages     map[string]int
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
		text, unverified := note(it, strings.SplitN(m[3], ".", 2)[0], m[4], w.Rule)
		if text == "" {
			continue
		}
		f := byItem[[2]int{i, j}]
		if f == nil {
			f = &found{pages: map[string]int{}, unverified: map[string]bool{}}
			byItem[[2]int{i, j}] = f
		}
		switch {
		case unverified:
			link := citedURL(it, m[4])
			if f.unverified[link] {
				continue
			}
			f.unverified[link] = true
			if f.pages[text] == 0 {
				f.unchecked = append(f.unchecked, text)
			}
			f.pages[text]++
		case !contains(f.notes, text):
			f.notes = append(f.notes, text)
		}
	}
	checked := at.Format("Jan 2, 3:04 pm MST")
	out := map[string]*provenanceView{}
	for i, sec := range ed.Sections {
		for j, it := range sec.Items {
			cited := len(citedURLs(it))
			v := &provenanceView{Status: "checked", Summary: "Checked against the source", Checked: checked, Compared: cited, Cited: cited}
			if f := byItem[[2]int{i, j}]; f != nil {
				v.Notes = f.notes
				for _, text := range f.unchecked {
					if n := f.pages[text]; n > 1 {
						text = fmt.Sprintf("%s (%d pages)", text, n)
					}
					v.Notes = append(v.Notes, text)
				}
				v.Compared = max(cited-len(f.unverified), 0)
				var parts []string
				switch len(f.notes) {
				case 0:
				case 1:
					parts = append(parts, "1 note from the source check")
				default:
					parts = append(parts, fmt.Sprintf("%d notes from the source check", len(f.notes)))
				}
				switch {
				case v.Compared == 0 && len(f.notes) == 0:
					v.Status, parts = "unchecked", []string{"Not checked against the source"}
				case v.Compared > 0 && v.Compared < cited:
					v.Status, parts = "notes", append(parts, fmt.Sprintf("checked against %d of %d sources", v.Compared, cited))
				default:
					v.Status = "notes"
				}
				if len(parts) == 0 {
					v.Status, parts = "checked", []string{"Checked against the source"}
				}
				v.Summary = strings.Join(parts, " · ")
				v.Summary = strings.ToUpper(v.Summary[:1]) + v.Summary[1:]
			}
			out[it.ID] = v
		}
	}
	return out
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
