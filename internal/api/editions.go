package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/irashack/night-desk/internal/edition"
)

func (s *Server) editions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, limit := q.Get("before"), 20
	if q.Has("before") {
		if _, e := edition.ParseDate(before); e != nil {
			fail(w, 400, "before must be YYYY-MM-DD")
			return
		}
	}
	if q.Has("limit") {
		n, e := strconv.Atoi(q.Get("limit"))
		if e != nil || n < 1 || n > 50 {
			fail(w, 400, "limit must be 1..50")
			return
		}
		limit = n
	}
	editions, e := s.Store.Editions(r.Context(), current(r).scope, before, limit)
	if e != nil {
		fail(w, 500, "storage failed")
		return
	}
	jsonResponse(w, 200, editionsResponse{Editions: editions})
}

func (s *Server) readEdition(w http.ResponseWriter, r *http.Request) {
	date := r.PathValue("date")
	if _, e := edition.ParseDate(date); e != nil {
		fail(w, 400, "date must be YYYY-MM-DD")
		return
	}
	body, publishedAt, e := s.Store.PublishedEdition(r.Context(), current(r).scope, date)
	if errors.Is(e, sql.ErrNoRows) {
		fail(w, 404, "no such edition")
		return
	}
	if e != nil {
		fail(w, 500, "storage failed")
		return
	}
	var document map[string]json.RawMessage
	if json.Unmarshal(body, &document) != nil || document == nil {
		fail(w, 500, "storage failed")
		return
	}
	document["published_at"], _ = json.Marshal(publishedAt)
	jsonResponse(w, 200, document)
}
