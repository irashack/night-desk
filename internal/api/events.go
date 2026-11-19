package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/irashack/night-desk/internal/store"
)

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 100
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil || n < 1 || n > 500 {
			jsonResponse(w, 400, map[string]any{"errors": []problem{{"limit", "must be 1..500"}}})
			return
		}
		limit = n
	}
	events, next, err := s.Store.Events(r.Context(), current(r).scope, q.Get("after"), limit)
	if errors.Is(err, store.ErrEventCursor) {
		jsonResponse(w, 400, map[string]any{"errors": []problem{{"after", "must be an event ID belonging to this paper"}}})
		return
	}
	if err != nil {
		s.storageFailed(w)
		return
	}
	jsonResponse(w, 200, map[string]any{"events": events, "next_cursor": next})
}
