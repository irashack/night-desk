package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/store"
)

// maxFoundPerReport bounds one discovery report; the store keeps at most
// store.MaxFoundSources per section.
const maxFoundPerReport = 8

// postFoundSources records a discovery attempt for a section that finds its own
// sources. The worker verified each feed by fetching and parsing it.
func (s *Server) postFoundSources(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	p := current(r)
	var section *config.Section
	for i := range p.paper.Sections {
		if p.paper.Sections[i].Key == key {
			section = &p.paper.Sections[i]
		}
	}
	if section == nil || !section.Finds() {
		fail(w, 404, "no section that finds its own sources")
		return
	}
	obj, ok := objectRequest(w, r)
	if !ok {
		return
	}
	problems := unknownFields(obj, "sources")
	var raw []map[string]json.RawMessage
	if json.Unmarshal(obj["sources"], &raw) != nil || raw == nil {
		problems = append(problems, problem{"sources", "must be an array of feeds"})
	} else if len(raw) > maxFoundPerReport {
		problems = append(problems, problem{"sources", fmt.Sprintf("must contain at most %d feeds", maxFoundPerReport)})
	}
	found := make([]store.FoundSource, 0, len(raw))
	seen := map[string]bool{}
	for i, item := range raw {
		base := fmt.Sprintf("sources[%d].", i)
		for _, p := range unknownFields(item, "feed", "outlet") {
			problems = append(problems, problem{base + p.Path, p.Rule})
		}
		var f store.FoundSource
		if json.Unmarshal(item["feed"], &f.Feed) != nil || !config.ValidFeed(f.Feed) {
			problems = append(problems, problem{base + "feed", "must be an absolute http or https URL without credentials or fragment"})
		} else if seen[f.Feed] {
			problems = append(problems, problem{base + "feed", "must be unique"})
		}
		seen[f.Feed] = true
		if json.Unmarshal(item["outlet"], &f.Outlet) != nil || f.Outlet == "" || strings.TrimSpace(f.Outlet) != f.Outlet || len(f.Outlet) > 200 || strings.ContainsFunc(f.Outlet, unicode.IsControl) {
			problems = append(problems, problem{base + "outlet", "must be trimmed single-line text of 1..200 bytes"})
		}
		found = append(found, f)
	}
	if len(problems) > 0 {
		jsonResponse(w, 422, map[string]any{"errors": problems})
		return
	}
	added, err := s.Store.AddFoundSources(r.Context(), p.scope, key, found, time.Now())
	if err != nil {
		s.storageFailed(w)
		return
	}
	jsonResponse(w, 200, acceptedResponse{Accepted: added})
}
