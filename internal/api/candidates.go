package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/irashack/night-desk/internal/candidate"
)

const candidateBodyLimit = 8 << 20

func (s *Server) postCandidates(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, candidateBodyLimit))
	if err != nil {
		fail(w, 413, "body must be at most 8 MB")
		return
	}
	var batch candidateBatch
	if shape := candidateShape(body, reflect.TypeOf(batch)); shape != nil {
		jsonResponse(w, 422, map[string]any{"errors": []problem{*shape}})
		return
	}
	if err = json.Unmarshal(body, &batch); err != nil {
		jsonResponse(w, 422, map[string]any{"errors": []problem{{"$", "must be a candidate batch object"}}})
		return
	}
	problems := []problem{}
	if len(batch.Candidates) < 1 || len(batch.Candidates) > 100 {
		problems = append(problems, problem{"candidates", "must contain 1..100 candidates"})
	}
	p := current(r)
	for i, c := range batch.Candidates {
		base := fmt.Sprintf("candidates[%d].", i)
		for _, v := range candidate.Validate(c) {
			problems = append(problems, problem{base + v.Path, v.Rule})
		}
		found := false
		for _, sec := range p.paper.Sections {
			if sec.Key == c.Section {
				found = true
			}
		}
		if !found {
			problems = append(problems, problem{base + "section", "must be a configured section"})
		}
	}
	if len(problems) > 0 {
		jsonResponse(w, 422, map[string]any{"errors": problems})
		return
	}
	if err = s.Store.UpsertCandidates(r.Context(), p.scope, batch.Candidates); err != nil {
		s.storageFailed(w)
		return
	}
	days := p.paper.RetentionDays
	if days == 0 {
		days = 14
	}
	if err = s.Store.PruneCandidates(r.Context(), p.scope, days); err != nil {
		s.storageFailed(w)
		return
	}
	jsonResponse(w, 200, acceptedResponse{Accepted: len(batch.Candidates)})
}

func (s *Server) getCandidates(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for k, v := range q {
		if (k != "section" && k != "since" && k != "cursor" && k != "limit") || len(v) != 1 {
			jsonResponse(w, 400, map[string]any{"errors": []problem{{k, "unknown or repeated query parameter"}}})
			return
		}
	}
	var since, before time.Time
	var beforeID int64
	limit := 10
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil || n < 1 || n > 20 {
			jsonResponse(w, 400, validationResponse{Errors: []problem{{"limit", "must be an integer between 1 and 20"}}})
			return
		}
		limit = n
	}
	if q.Has("cursor") {
		raw, err := base64.RawURLEncoding.DecodeString(q.Get("cursor"))
		timestamp, id, ok := strings.Cut(string(raw), "|")
		parsedBefore, errTime := time.Parse(time.RFC3339Nano, timestamp)
		parsedID, errID := strconv.ParseInt(id, 10, 64)
		if err != nil || !ok || errTime != nil || errID != nil || parsedID < 1 {
			jsonResponse(w, 400, validationResponse{Errors: []problem{{"cursor", "must be a candidate cursor"}}})
			return
		}
		before = parsedBefore
		beforeID = parsedID
	}

	if q.Has("since") {
		var err error
		since, err = time.Parse(time.RFC3339Nano, q.Get("since"))
		if err != nil {
			jsonResponse(w, 400, map[string]any{"errors": []problem{{"since", "must be RFC3339"}}})
			return
		}
	}
	p := current(r)
	if q.Has("section") {
		found := false
		for _, sec := range p.paper.Sections {
			if sec.Key == q.Get("section") {
				found = true
			}
		}
		if !found {
			jsonResponse(w, 400, map[string]any{"errors": []problem{{"section", "must be a configured section"}}})
			return
		}
	}
	// Reading never deletes: pruning happens when the worker posts (gather).
	list, err := s.Store.CandidatePage(r.Context(), p.scope, q.Get("section"), since, before, beforeID, limit+1)
	if err != nil {
		s.storageFailed(w)
		return
	}
	// Account for JSON escaping too: even maximum article text and aliases must
	// fit under the worker's response cap.
	count, bytes := 0, 0
	for count < len(list) && count < limit {
		encoded, _ := json.Marshal(list[count])
		if count > 0 && bytes+len(encoded) > 7<<20 {
			break
		}
		bytes += len(encoded)
		count++
	}
	next := ""
	if count < len(list) && count > 0 {
		last := list[count-1]
		next = base64.RawURLEncoding.EncodeToString([]byte(last.FetchedAt.UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(last.ID, 10)))
	}
	jsonResponse(w, 200, candidatePage{Candidates: list[:count], NextCursor: next})
}

// candidateBatch remains the ingest contract; pagination metadata is read-only.
type candidatePage struct {
	Candidates []candidate.Candidate `json:"candidates" nullable:"false"`
	NextCursor string                `json:"next_cursor"`
}
