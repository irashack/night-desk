package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/editorial"
)

// editPaper applies a merge patch to the paper's effective editorial fields: a
// present field replaces the current value (sections as a whole ordered list,
// boxes field by field) and null clears an optional one.
func (s *Server) editPaper(w http.ResponseWriter, r *http.Request) {
	p := current(r)
	body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if e != nil {
		fail(w, 413, "body must be at most 256 KB")
		return
	}
	// The principal's paper is effective as of authentication; edits build on it.
	ed, problems := applyPatch(p.paper.Editorial(), body)
	if len(problems) == 0 {
		_, problems, e = s.Editorial.Save(r.Context(), p.scope, ed)
		if e != nil {
			s.storageFailed(w)
			return
		}
		if len(problems) == 0 {
			jsonResponse(w, 200, s.paperView(p.paper.WithEditorial(ed)))
			return
		}
	}
	jsonResponse(w, 422, map[string]any{"errors": problemList(problems)})
}

func (s *Server) paperRevisions(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if q := r.URL.Query(); q.Has("limit") {
		n, e := strconv.Atoi(q.Get("limit"))
		if e != nil || n < 1 || n > 50 {
			fail(w, 400, "limit must be 1..50")
			return
		}
		limit = n
	}
	revs, e := s.Editorial.Revisions(r.Context(), current(r).scope, limit)
	if e != nil {
		s.storageFailed(w)
		return
	}
	jsonResponse(w, 200, revisionsResponse{Revisions: revs})
}

func (s *Server) restorePaper(w http.ResponseWriter, r *http.Request) {
	p := current(r)
	var req restoreRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || req.Revision == nil || *req.Revision < 0 {
		jsonResponse(w, 422, map[string]any{"errors": []problem{{"revision", "must be a revision number, or 0 for the declared paper"}}})
		return
	}
	if _, e := s.Editorial.Restore(r.Context(), p.scope, *req.Revision); errors.Is(e, editorial.ErrNoRevision) {
		fail(w, 404, "no such revision")
		return
	} else if e != nil && strings.Contains(e.Error(), "no longer validates") {
		jsonResponse(w, 422, map[string]any{"errors": []problem{{"revision", e.Error()}}})
		return
	} else if e != nil {
		s.storageFailed(w)
		return
	}
	paper, ok := s.Config().Paper(p.scope.PaperID())
	if !ok {
		fail(w, 401, "unauthorized")
		return
	}
	jsonResponse(w, 200, s.paperView(paper))
}

func (s *Server) storageFailed(w http.ResponseWriter) {
	if s.Log != nil {
		s.Log.Error("paper editorial storage failed")
	}
	fail(w, 500, "storage failed")
}

func problemList(problems []string) []problem {
	out := make([]problem, 0, len(problems))
	for _, p := range problems {
		path, rule, ok := strings.Cut(p, ": ")
		if !ok || path == "" {
			path, rule = "$", p
		}
		out = append(out, problem{path, rule})
	}
	return out
}

func strict(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if e := dec.Decode(v); e != nil {
		return e
	}
	if dec.More() {
		return errors.New("trailing data")
	}
	return nil
}

func applyPatch(ed config.Editorial, body []byte) (config.Editorial, []string) {
	var patch map[string]json.RawMessage
	if strict(body, &patch) != nil || patch == nil {
		return ed, []string{"$: must be a JSON object of paper fields"}
	}
	var problems []string
	bad := func(path, rule string) { problems = append(problems, path+": "+rule) }
	isNull := func(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }
	optional := func(key string, raw json.RawMessage) *string {
		if isNull(raw) {
			return nil
		}
		var v string
		if strict(raw, &v) != nil {
			bad(key, "must be a string or null")
			return nil
		}
		return &v
	}
	for key, raw := range patch {
		switch key {
		case "masthead":
			if isNull(raw) || strict(raw, &ed.Masthead) != nil {
				bad(key, "must be a string")
			}
		case "place":
			ed.Place = ""
			if v := optional(key, raw); v != nil {
				ed.Place = *v
			}
		case "brief":
			ed.Brief = optional(key, raw)
		case "deadline":
			ed.Deadline = optional(key, raw)
		case "motto":
			ed.Motto = nil
			if !isNull(raw) {
				var m config.Motto
				if strict(raw, &m) != nil {
					bad(key, "must be an object with guidance, or null")
				}
				ed.Motto = &m
			}
		case "sections":
			if shape := sectionLeanShape(raw); shape != nil {
				bad(shape.Path, shape.Rule)
				continue
			}
			var sections []config.Section
			if strict(raw, &sections) != nil {
				bad(key, "must be an array of {key, title, required, brief}")
			}
			ed.Sections = sections
		case "boxes":
			var boxes map[string]*bool
			if strict(raw, &boxes) != nil || boxes == nil {
				bad(key, "must be an object of booleans")
				continue
			}
			for name, v := range boxes {
				target := map[string]*bool{"weather": &ed.Boxes.Weather, "sun": &ed.Boxes.Sun,
					"solunar": &ed.Boxes.Solunar, "markets": &ed.Boxes.Markets}[name]
				if target == nil || v == nil {
					bad("boxes."+name, "must be one of weather, sun, solunar, markets with a boolean")
					continue
				}
				*target = *v
			}
		default:
			bad(key, "is not an editable paper field (masthead, place, brief, deadline, motto, sections, boxes)")
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return ed, problems
	}
	return ed, ed.Problems()
}
