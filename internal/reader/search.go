package reader

import (
	"net/http"
	"unicode/utf8"

	"github.com/irashack/night-desk/internal/store"
)

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	v := s.base(r)
	v.Query = r.URL.Query().Get("q")
	saved := r.URL.Query().Get("saved")
	if saved != "" && saved != "1" {
		http.Error(w, "saved: must be 1 or omitted", http.StatusBadRequest)
		return
	}
	v.SavedOnly = saved == "1"
	in, ok := store.ParseSearchIn(r.URL.Query().Get("in"))
	if !ok {
		http.Error(w, "in: must be writer, stance, outlets or omitted", http.StatusBadRequest)
		return
	}
	v.SearchIn = string(in)
	if utf8.RuneCountInString(v.Query) > 500 {
		http.Error(w, "q: must be at most 500 characters", http.StatusBadRequest)
		return
	}
	var err error
	v.Results, err = s.Store.SearchFields(r.Context(), current(r).scope, v.Query, in, v.SavedOnly)
	if err != nil {
		http.Error(w, "Search unavailable", http.StatusInternalServerError)
		return
	}
	s.render(w, "search", v)
}
