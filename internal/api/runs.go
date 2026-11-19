package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"time"

	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/scheduler"
	"github.com/irashack/night-desk/internal/store"
)

type runReportRequest struct {
	LeaseID          string           `json:"lease_id"`
	Outcome          string           `json:"outcome" enum:"submitted,failed"`
	Spend            store.DraftSpend `json:"spend"`
	ExpectedRevision int64            `json:"expected_revision,omitempty" minimum:"0"`
	Draft            *edition.Edition `json:"draft,omitempty"`
}

func runFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrRunConflict) || errors.Is(err, store.ErrDraftConflict) {
		fail(w, 409, "draft run lease, budget or revision conflict")
		return
	}
	fail(w, 500, "storage failed")
}
func integerField(obj map[string]json.RawMessage, key, path string, minimum int) (int, []problem) {
	var n *int
	if json.Unmarshal(obj[key], &n) != nil || n == nil || *n < minimum {
		return 0, []problem{{path, "must be an integer >= " + strconv.Itoa(minimum)}}
	}
	return *n, nil
}

// Reuse the candidate wire-shape checker for duplicate, unknown and null
// fields, retaining indexed paths before decoding the action-specific object.
func runObject(w http.ResponseWriter, r *http.Request, t reflect.Type) (map[string]json.RawMessage, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		fail(w, 413, "body must be at most 1 MB")
		return nil, false
	}
	if p := candidateShape(raw, t); p != nil {
		jsonResponse(w, 422, validationResponse{Errors: []problem{*p}})
		return nil, false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		jsonResponse(w, 422, validationResponse{Errors: []problem{{"$", "must be a JSON object"}}})
		return nil, false
	}
	return obj, true
}

func (s *Server) claimRun(w http.ResponseWriter, r *http.Request) {
	date, ok := draftDate(w, r)
	if !ok {
		return
	}
	obj, ok := runObject(w, r, reflect.TypeFor[store.DraftBudget]())
	if !ok {
		return
	}
	problems := unknownFields(obj, "max_calls", "max_tokens")
	calls, errs := integerField(obj, "max_calls", "max_calls", 1)
	problems = append(problems, errs...)
	tokens, errs := integerField(obj, "max_tokens", "max_tokens", 1)
	problems = append(problems, errs...)
	if len(problems) > 0 {
		jsonResponse(w, 422, validationResponse{Errors: problems})
		return
	}
	// After the deadline, only a run missed at its deadline may be claimed, for a
	// late edition on the paper's current local date.
	claim := s.Store.ClaimDraftRun
	if p := current(r).paper; p.Deadline != nil {
		if loc, err := time.LoadLocation(p.Timezone); err == nil {
			day, _ := time.ParseInLocation("2006-01-02", date, loc)
			deadline, err := scheduler.Deadline(p, day.Add(12*time.Hour))
			if now := s.now(); err == nil && !now.Before(deadline) {
				if now.In(loc).Format("2006-01-02") != date {
					runFailure(w, store.ErrRunConflict)
					return
				}
				claim = s.Store.ClaimLateDraftRun
			}
		}
	}
	lease, err := claim(r.Context(), current(r).scope, date, store.DraftBudget{MaxCalls: calls, MaxTokens: tokens}, s.now())
	if err != nil {
		runFailure(w, err)
		return
	}
	jsonResponse(w, 200, lease)
}
func (s *Server) reportRun(w http.ResponseWriter, r *http.Request) {
	date, ok := draftDate(w, r)
	if !ok {
		return
	}
	obj, ok := runObject(w, r, reflect.TypeFor[runReportRequest]())
	if !ok {
		return
	}
	problems := unknownFields(obj, "lease_id", "outcome", "spend", "expected_revision", "draft")
	var lease, outcome string
	if json.Unmarshal(obj["lease_id"], &lease) != nil || lease == "" {
		problems = append(problems, problem{"lease_id", "must be a non-empty string"})
	}
	if json.Unmarshal(obj["outcome"], &outcome) != nil || (outcome != "submitted" && outcome != "failed") {
		problems = append(problems, problem{"outcome", "must be submitted or failed"})
	}
	var spend map[string]json.RawMessage
	if json.Unmarshal(obj["spend"], &spend) != nil || spend == nil {
		problems = append(problems, problem{"spend", "must be an object"})
	}
	for _, p := range unknownFields(spend, "calls", "tokens", "cost_micros") {
		p.Path = "spend." + p.Path
		problems = append(problems, p)
	}
	calls, errs := integerField(spend, "calls", "spend.calls", 0)
	problems = append(problems, errs...)
	tokens, errs := integerField(spend, "tokens", "spend.tokens", 0)
	problems = append(problems, errs...)
	cost := 0
	if _, present := spend["cost_micros"]; present {
		cost, errs = integerField(spend, "cost_micros", "spend.cost_micros", 0)
		problems = append(problems, errs...)
	}
	expected := 0
	if _, present := obj["expected_revision"]; present || outcome == "submitted" {
		expected, errs = integerField(obj, "expected_revision", "expected_revision", 0)
		problems = append(problems, errs...)
	}
	var ed *edition.Edition
	warnings := []problem{}
	if outcome == "submitted" {
		var err error
		ed, err = edition.Decode(bytes.NewReader(obj["draft"]))
		if err != nil {
			for _, p := range problemList([]string{err.Error()}) {
				if p.Path == "$" {
					p.Path = "draft"
				} else {
					p.Path = "draft." + p.Path
				}
				problems = append(problems, p)
			}
		} else {
			ps, ws := validation(ed, current(r).paper)
			warnings = ws
			for _, p := range ps {
				p.Path = "draft." + p.Path
				problems = append(problems, p)
			}
		}
	} else if _, present := obj["draft"]; present {
		problems = append(problems, problem{"draft", "must be omitted for failed attempts"})
	}
	if len(problems) > 0 {
		jsonResponse(w, 422, validationResponse{Errors: problems, Warnings: warnings})
		return
	}
	p := current(r)
	if ed != nil {
		warnings = append(warnings, s.sourceWarnings(r.Context(), p.scope, p.paper, ed)...)
	}
	revision, err := s.Store.ReportDraftRun(r.Context(), p.scope, date, lease, p.keyID, outcome, store.DraftSpend{Calls: calls, Tokens: tokens, CostMicros: int64(cost)}, int64(expected), ed, time.Now())
	if err != nil {
		runFailure(w, err)
		return
	}
	jsonResponse(w, 200, draftSubmitted{Revision: revision, Warnings: warnings})
}
