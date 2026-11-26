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
//
// The patch is merged onto the paper's newest revision and saved only if that
// revision is still current when the save commits; a client may also name the
// revision it read (If-Match or expected_revision) and is refused with 409 when
// the paper has moved on since.
func (s *Server) editPaper(w http.ResponseWriter, r *http.Request) {
	p := current(r)
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if e != nil {
		fail(w, 413, "body must be at most 256 KB")
		return
	}
	patch, problems := decodePatch(body)
	if len(problems) == 0 {
		if raw, ok := patch["expected_revision"]; ok {
			delete(patch, "expected_revision")
			var n int64
			if strict(raw, &n) != nil || n < 0 {
				problems = []string{"expected_revision: must be the revision number read from the paper, or 0 for a never-edited paper"}
			} else if expected != editorial.AnyRevision && expected != n {
				conflict(w, n)
				return
			} else {
				expected = n
			}
		}
	}
	if len(problems) > 0 {
		jsonResponse(w, 422, map[string]any{"errors": problemList(problems)})
		return
	}
	// Edits build on the newest revision, not the paper as of authentication.
	paper, rev, ok := s.Editorial.Current(p.scope.PaperID())
	if !ok {
		fail(w, 401, "unauthorized")
		return
	}
	if expected != editorial.AnyRevision && expected != rev {
		conflict(w, expected)
		return
	}
	ed, problems := applyPatch(paper.Editorial(), patch)
	if len(problems) == 0 {
		var saved int64
		saved, problems, e = s.Editorial.Save(r.Context(), p.scope, ed, rev)
		if errors.Is(e, editorial.ErrConflict) {
			conflict(w, rev)
			return
		}
		if e != nil {
			s.storageFailed(w)
			return
		}
		if len(problems) == 0 {
			setETag(w, saved)
			jsonResponse(w, 200, s.paperViewAt(paper.WithEditorial(ed), saved))
			return
		}
	}
	jsonResponse(w, 422, map[string]any{"errors": problemList(problems)})
}

// ifMatch reads the optional If-Match precondition: one paper ETag, such as
// "3", or * for any revision. Without one it returns AnyRevision.
func ifMatch(w http.ResponseWriter, r *http.Request) (int64, bool) {
	values := r.Header.Values("If-Match")
	if len(values) == 0 {
		return editorial.AnyRevision, true
	}
	v := strings.TrimSpace(strings.Join(values, ","))
	if v == "*" {
		return editorial.AnyRevision, true
	}
	if len(v) >= 3 && v[0] == '"' && v[len(v)-1] == '"' {
		if n, e := strconv.ParseInt(v[1:len(v)-1], 10, 64); e == nil && n >= 0 && strconv.FormatInt(n, 10) == v[1:len(v)-1] {
			return n, true
		}
	}
	fail(w, 400, `If-Match must be one ETag from GET /api/v1/paper, such as "3", or *`)
	return 0, false
}

func setETag(w http.ResponseWriter, revision int64) {
	w.Header().Set("ETag", `"`+strconv.FormatInt(revision, 10)+`"`)
}

func conflict(w http.ResponseWriter, expected int64) {
	since := "while the edit was saved"
	if expected >= 0 {
		since = "since revision " + strconv.FormatInt(expected, 10)
	}
	fail(w, http.StatusConflict, "the paper changed "+since+"; read it again (GET /api/v1/paper) and reapply the edit")
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
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req restoreRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || !atEOF(dec) || req.Revision == nil || *req.Revision < 0 {
		jsonResponse(w, 422, map[string]any{"errors": []problem{{"revision", "must be a revision number, or 0 for the declared paper"}}})
		return
	}
	if req.ExpectedRevision != nil {
		if *req.ExpectedRevision < 0 {
			jsonResponse(w, 422, map[string]any{"errors": []problem{{"expected_revision", "must be the revision number read from the paper, or 0 for a never-edited paper"}}})
			return
		}
		if expected != editorial.AnyRevision && expected != *req.ExpectedRevision {
			conflict(w, *req.ExpectedRevision)
			return
		}
		expected = *req.ExpectedRevision
	}
	_, e := s.Editorial.Restore(r.Context(), p.scope, *req.Revision, expected)
	if errors.Is(e, editorial.ErrNoRevision) {
		fail(w, 404, "no such revision")
		return
	} else if errors.Is(e, editorial.ErrConflict) {
		conflict(w, expected)
		return
	} else if e != nil && strings.Contains(e.Error(), "no longer validates") {
		jsonResponse(w, 422, map[string]any{"errors": []problem{{"revision", e.Error()}}})
		return
	} else if e != nil {
		s.storageFailed(w)
		return
	}
	paper, rev, ok := s.Editorial.Current(p.scope.PaperID())
	if !ok {
		fail(w, 401, "unauthorized")
		return
	}
	setETag(w, rev)
	jsonResponse(w, 200, s.paperViewAt(paper, rev))
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
	if _, e := dec.Token(); e != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

func decodePatch(body []byte) (map[string]json.RawMessage, []string) {
	var patch map[string]json.RawMessage
	if strict(body, &patch) != nil || patch == nil {
		return nil, []string{"$: must be a JSON object of paper fields"}
	}
	return patch, nil
}

func applyPatch(ed config.Editorial, patch map[string]json.RawMessage) (config.Editorial, []string) {
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

// atEOF reports whether nothing follows the decoded value.
func atEOF(dec *json.Decoder) bool {
	_, err := dec.Token()
	return err == io.EOF
}
