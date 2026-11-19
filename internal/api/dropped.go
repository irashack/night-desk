package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/irashack/night-desk/internal/config"
)

// postDropped records counts only; the worker never sends what it dropped.
func (s *Server) postDropped(w http.ResponseWriter, r *http.Request) {
	obj, ok := objectRequest(w, r)
	if !ok {
		return
	}
	problems := unknownFields(obj, "dropped")
	var dropped []droppedCount
	if json.Unmarshal(obj["dropped"], &dropped) != nil || dropped == nil {
		problems = append(problems, problem{"dropped", "must be an array of rule counts"})
	}
	rules := append([]string{"blocked_outlet", "blocked_keyword"}, config.IngestFilters...)
	counts := map[string]int{}
	total := 0
	for i, d := range dropped {
		base := fmt.Sprintf("dropped[%d].", i)
		if !slices.Contains(rules, d.Rule) {
			problems = append(problems, problem{base + "rule", "must be a known filter rule"})
		} else if _, seen := counts[d.Rule]; seen {
			problems = append(problems, problem{base + "rule", "must be unique"})
		}
		if d.Count < 1 || d.Count > 100000 {
			problems = append(problems, problem{base + "count", "must be between 1 and 100000"})
		}
		counts[d.Rule] = d.Count
		total += d.Count
	}
	if len(problems) > 0 {
		jsonResponse(w, 422, map[string]any{"errors": problems})
		return
	}
	p := current(r)
	loc, err := time.LoadLocation(p.paper.Timezone)
	if err != nil {
		s.storageFailed(w)
		return
	}
	if err = s.Store.AddGatherDrops(r.Context(), p.scope, time.Now().In(loc).Format("2006-01-02"), counts); err != nil {
		s.storageFailed(w)
		return
	}
	jsonResponse(w, 200, acceptedResponse{Accepted: total})
}
