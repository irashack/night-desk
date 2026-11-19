// Package conformance serves deterministic, stateful fixtures for agent adapters.
// It opens no database and must only be exposed on loopback.
package conformance

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/irashack/night-desk/internal/api"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/store"
)

const Date = "2026-10-30"
const QuietPeriod = 1200 * time.Millisecond

// Scenarios returns the supported script names, in a stable order.
func Scenarios() []string { return []string{"validation", "retry", "review", "webhooks", "forbidden"} }

type Check struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}
type Report struct {
	Scenario string  `json:"scenario"`
	Passed   bool    `json:"passed"`
	Checks   []Check `json:"checks"`
}

// Server registers protocol paths from api.Routes. Done closes after a completed
// report is fetched, allowing a CLI to shut down after delivering that report.
// Reports describe observable HTTP behavior; adapters must assert surfaced errors
// in their own tests. Silence is observed for QuietPeriod, not indefinitely.
type Server struct {
	mu                   sync.Mutex
	scenario, key        string
	mux                  *http.ServeMux
	checks               []Check
	failed               map[string]bool
	done                 chan struct{}
	once                 sync.Once
	attempts             int
	target               string
	body                 []byte
	last                 time.Time
	terminal             bool
	revision             int64
	readRevision         int64
	conflicted, approved bool
	published            bool
}

func New(scenario string, key []byte) (*Server, error) {
	known := false
	for _, name := range Scenarios() {
		known = known || name == scenario
	}
	if !known {
		return nil, errors.New("unknown conformance scenario")
	}
	if len(key) == 0 {
		return nil, errors.New("key must be non-empty printable ASCII without whitespace")
	}
	for _, b := range key {
		if b < 33 || b > 126 {
			return nil, errors.New("key must be non-empty printable ASCII without whitespace")
		}
	}
	s := &Server{scenario: scenario, key: string(key), mux: http.NewServeMux(), failed: map[string]bool{}, done: make(chan struct{}), revision: 1}
	names := map[string][]string{
		"validation": {"validation_returned", "corrected_resubmission"},
		"retry":      {"retry_sequence", "backoff", "retry_after", "idempotent_publish"},
		"review":     {"read_current_revision", "stale_conflict", "reread_after_conflict", "approved", "published"},
		"forbidden":  {"forbidden_returned", "not_retried"},
		"webhooks":   {"draft.ready", "review.requested", "bad_signature_rejected", "stale_timestamp_rejected"},
	}
	for _, name := range names[scenario] {
		s.checks = append(s.checks, Check{name, false, "pending"})
	}
	for _, route := range api.Routes() {
		tool := route.Tool
		s.mux.HandleFunc(route.Method+" "+route.Path, func(w http.ResponseWriter, r *http.Request) { s.protocol(w, r, tool) })
	}
	s.mux.HandleFunc("GET /conformance/report", s.reportHTTP)
	return s, nil
}

func (s *Server) Done() <-chan struct{}                            { return s.done }
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }
func (s *Server) check(name string, passed bool, detail string) {
	for i := range s.checks {
		if s.checks[i].Name == name {
			if s.failed[name] {
				return
			}
			s.checks[i] = Check{name, passed, detail}
			if !passed {
				s.failed[name] = true
			}
			return
		}
	}
}
func (s *Server) finished() bool {
	if !s.terminal {
		return false
	}
	if s.scenario == "forbidden" || s.scenario == "retry" {
		return time.Since(s.last) >= QuietPeriod
	}
	return true
}
func (s *Server) report() Report {
	if s.scenario == "forbidden" && s.finished() {
		s.check("not_retried", s.attempts == 1, "observed one forbidden write during the quiet period")
	}
	result := Report{Scenario: s.scenario, Passed: s.finished(), Checks: append([]Check{}, s.checks...)}
	for _, c := range result.Checks {
		result.Passed = result.Passed && c.Passed
	}
	return result
}

// Finished reports whether the script and any quiet observation period ended.
func (s *Server) Finished() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.finished() }

func (s *Server) Report() Report { s.mu.Lock(); defer s.mu.Unlock(); return s.report() }
func (s *Server) reportHTTP(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		reply(w, 403, map[string]string{"error": "loopback only"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	reply(w, 200, s.report())
	if s.finished() {
		s.once.Do(func() { close(s.done) })
	}
}
func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
func failure(w http.ResponseWriter, status int, message string) {
	reply(w, status, map[string]string{"error": message})
}

func (s *Server) protocol(w http.ResponseWriter, r *http.Request, tool string) {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.key)) != 1 {
		failure(w, 401, "unauthorized")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if date := r.PathValue("date"); date != "" {
		if _, err := edition.ParseDate(date); err != nil {
			failure(w, 400, "date must be YYYY-MM-DD")
			return
		}
	}
	switch tool {
	case "get_paper":
		role := "author"
		if s.scenario == "review" {
			role = "reviewer"
		}
		reply(w, 200, map[string]any{"masthead": "Conformance Daily", "timezone": "UTC", "agent_role": role, "deadline_policy": "hold", "reader_notes": "Prefer primary sources.", "retention_days": 14, "recent_urls": []string{}, "sections": []map[string]any{{"key": "news", "title": "News", "required": true}}, "boxes": map[string]bool{"weather": false, "sun": false, "solunar": false, "markets": false}, "revision": 0, "edited": false})
	case "get_feedback":
		reply(w, 200, map[string]any{"events": []any{}, "next_cursor": 0})
	case "get_candidates":
		reply(w, 200, map[string]any{"candidates": []any{}})
	case "list_drafts":
		drafts := []store.Draft{}
		if s.scenario == "review" {
			draft := s.draft(Date)
			if state := r.URL.Query().Get("state"); state == "" || state == draft.State {
				drafts = append(drafts, draft)
			}
		}
		reply(w, 200, map[string]any{"drafts": drafts})
	case "get_draft":
		if s.scenario != "review" {
			failure(w, 404, "no such draft")
			return
		}
		if !s.bind(r.URL.Path) {
			failure(w, 409, "use the same draft date")
			return
		}
		s.readRevision = s.revision
		s.check("read_current_revision", true, "read in-review draft")
		if s.conflicted {
			s.check("reread_after_conflict", true, "read the advanced revision after 409")
		}
		reply(w, 200, s.draft(r.PathValue("date")))
	case "publish_edition", "submit_draft":
		s.writeEdition(w, r, tool)
	case "review_draft", "publish_draft":
		s.review(w, r, tool)
	default:
		failure(w, 501, "route is not scripted by this scenario")
	}
}
func (s *Server) bind(path string) bool {
	// A review's read, action and publish paths share the same draft identity.
	path = strings.TrimSuffix(strings.TrimSuffix(path, "/review"), "/publish")
	if s.target == "" {
		s.target = path
	}
	return s.target == path
}
func Fixture() *edition.Edition {
	return &edition.Edition{Schema: 1, Lead: "news-1", Sections: []edition.Section{{Key: "news", Items: []edition.Item{{ID: "news-1", Kind: "news", Title: "Council opens a new library", Summary: "The new public library opened on Friday.", Source: "City Gazette", URL: "https://example.com/library"}}}}}
}
func (s *Server) draft(date string) store.Draft {
	state := "in_review"
	if s.published {
		state = "published"
	}
	d := store.Draft{Date: date, State: state, Revision: s.revision, Body: Fixture(), UpdatedAt: "2026-10-30T12:00:00Z", Actions: []store.DraftAction{}, Unreviewed: false}
	if s.approved {
		rev := s.revision
		d.ApprovedRevision = &rev
	}
	return d
}
func (s *Server) writeEdition(w http.ResponseWriter, r *http.Request, tool string) {
	if s.scenario == "forbidden" {
		s.attempts++
		s.last = time.Now()
		s.terminal = true
		s.check("forbidden_returned", true, "403: missing publish capability")
		if s.attempts > 1 {
			s.check("not_retried", false, "retried after permanent 403")
		}
		failure(w, 403, "forbidden")
		return
	}
	if s.scenario != "validation" && s.scenario != "retry" {
		failure(w, 409, "scenario requires the draft review flow")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		failure(w, 413, "body must be at most 1 MB")
		return
	}
	s.attempts++
	if !s.bind(r.URL.Path) {
		name := "idempotent_publish"
		if s.scenario == "validation" {
			name = "corrected_resubmission"
		}
		s.check(name, false, "changed target during resubmission")
		s.terminal = true
		failure(w, 409, "use the same method and target")
		return
	}
	if s.scenario == "validation" && s.attempts == 1 {
		s.body = bytes.Clone(raw)
		s.check("validation_returned", true, "returned an exact path and rule")
		reply(w, 422, map[string]any{"errors": []map[string]string{{"path": "sections[0].items[0].url", "rule": "must be https"}}})
		return
	}
	ed, err := edition.Decode(bytes.NewReader(raw))
	if err != nil {
		path, rule, ok := strings.Cut(err.Error(), ": ")
		if !ok || path == "" {
			path, rule = "$", err.Error()
		}
		reply(w, 422, map[string]any{"errors": []map[string]string{{"path": path, "rule": rule}}})
		return
	}
	problems := []map[string]string{}
	for _, p := range edition.Validate(ed, edition.PaperRules{Sections: []edition.SectionRule{{Key: "news", Required: true}}}) {
		if p.Severity == edition.Error {
			problems = append(problems, map[string]string{"path": p.Path, "rule": p.Rule})
		}
	}
	if len(problems) > 0 {
		reply(w, 422, map[string]any{"errors": problems})
		return
	}
	if s.scenario == "validation" {
		var before, after any
		_ = json.Unmarshal(s.body, &before)
		_ = json.Unmarshal(raw, &after)
		a, _ := json.Marshal(before)
		b, _ := json.Marshal(after)
		s.check("corrected_resubmission", !bytes.Equal(a, b), "resubmitted a changed, valid edition to the same target")
	} else {
		canonical, _ := json.Marshal(ed)
		if s.body == nil {
			s.body = canonical
		}
		s.check("idempotent_publish", bytes.Equal(s.body, canonical), "all attempts use one target and identical edition content")
		now := time.Now()
		switch s.attempts {
		case 1:
			s.last = now
			failure(w, 503, "temporarily unavailable")
			return
		case 2:
			s.check("backoff", now.Sub(s.last) >= 100*time.Millisecond, "waited at least 100ms after 503")
			s.last = now
			w.Header().Set("Retry-After", "1")
			failure(w, 429, "rate limited")
			return
		case 3:
			s.check("retry_after", now.Sub(s.last) >= time.Second, "waited at least Retry-After: 1")
			s.check("retry_sequence", true, "503 then 429 then success")
		}
		s.last = now
	}
	s.terminal = true
	if tool == "submit_draft" {
		reply(w, 200, map[string]any{"revision": 1, "warnings": []any{}})
	} else {
		reply(w, 200, map[string]any{"date": r.PathValue("date"), "warnings": []any{}})
	}
}
func (s *Server) review(w http.ResponseWriter, r *http.Request, tool string) {
	if s.scenario != "review" {
		failure(w, 409, "not a review scenario")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		failure(w, 413, "body must be at most 1 MB")
		return
	}
	var object map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	if dec.Decode(&object) != nil || object == nil {
		validationFailure(w, "$", "must be a JSON object")
		return
	}
	if dec.Decode(new(any)) != io.EOF {
		validationFailure(w, "$", "must contain one JSON object")
		return
	}
	var unknown []string
	for name := range object {
		if name != "revision" && (name != "action" || tool != "review_draft") {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		validationFailure(w, unknown[0], "unknown field")
		return
	}
	var body struct {
		Revision int64
		Action   string
	}
	if json.Unmarshal(object["revision"], &body.Revision) != nil || body.Revision < 1 {
		validationFailure(w, "revision", "must be a positive integer")
		return
	}
	if tool == "review_draft" && (json.Unmarshal(object["action"], &body.Action) != nil || body.Action != "approve") {
		validationFailure(w, "action", "this script expects approve")
		return
	}
	if !s.bind(r.URL.Path) || body.Revision != s.revision || s.readRevision != s.revision {
		failure(w, 409, "draft revision or state conflict")
		return
	}
	if tool == "review_draft" {

		if !s.conflicted {
			s.conflicted = true
			s.revision++
			s.check("stale_conflict", true, "advanced revision and returned 409 once")
			failure(w, 409, "draft revision or state conflict")
			return
		}
		s.revision++
		s.readRevision = s.revision
		s.approved = true
		s.check("approved", true, "approved the current revision after re-reading")
		reply(w, 200, map[string]int64{"revision": s.revision})
		return
	}
	if !s.approved || s.published {
		failure(w, 409, "draft revision or state conflict")
		return
	}
	s.published = true
	s.approved = false
	s.revision++
	s.terminal = true
	s.check("published", true, "published only the approved revision")
	reply(w, 200, map[string]any{"revision": s.revision, "date": r.PathValue("date"), "warnings": []any{}})
}

func validationFailure(w http.ResponseWriter, path, rule string) {
	reply(w, 422, map[string]any{"errors": []map[string]string{{"path": path, "rule": rule}}})
}
