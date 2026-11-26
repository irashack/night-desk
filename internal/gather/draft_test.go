package gather

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/candidate"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/model"
	"github.com/irashack/night-desk/internal/store"
)

func TestWorkerDraftWindowAndRoles(t *testing.T) {
	for _, tc := range []struct {
		name, role, at, lead, deadline, date string
		exists                               bool
		sendBack                             bool
		refuse                               bool // the server refuses the claim: the run is not missed
		status                               int
		want                                 bool
		wantErr                              bool
	}{
		{name: "midnight", role: "none", at: "2026-10-31T04:00:00Z", deadline: "00:00", date: "2026-10-31", want: true},
		{name: "half past midnight", role: "none", at: "2026-10-31T04:00:00Z", deadline: "00:30", date: "2026-10-31", want: true},
		{name: "spring gap", role: "none", at: "2026-03-08T07:00:00Z", deadline: "02:30", date: "2026-03-08", want: true},
		{name: "previous local date", role: "none", at: "2026-10-30T04:00:00Z", deadline: "23:30", date: "2026-10-29", want: true},
		{name: "reviewer at start", role: "reviewer", at: "2026-10-30T10:00:00Z", want: true},
		{name: "none within window", role: "none", at: "2026-10-30T11:00:00Z", want: true},
		{name: "too early", role: "reviewer", at: "2026-10-30T09:59:59Z"},
		{name: "at deadline", role: "none", at: "2026-10-30T11:30:00Z", refuse: true},
		{name: "after deadline", role: "reviewer", at: "2026-10-30T11:30:01Z", refuse: true},
		{name: "late edition after a missed deadline", role: "none", at: "2026-10-30T20:00:00Z", want: true},
		{name: "author", role: "author", at: "2026-10-30T11:00:00Z"},
		{name: "default author", at: "2026-10-30T11:00:00Z"},
		{name: "send back", role: "reviewer", at: "2026-10-30T11:00:00Z", sendBack: true, want: true},
		{name: "existing draft", role: "reviewer", at: "2026-10-30T11:00:00Z", exists: true},
		{name: "custom lead early", role: "none", at: "2026-10-30T11:00:00Z", lead: "15m"},
		{name: "custom lead starts", role: "none", at: "2026-10-30T11:15:00Z", lead: "15m", want: true},
		{name: "invalid lead", role: "reviewer", at: "2026-10-30T11:00:00Z", lead: "-1m", wantErr: true},
		{name: "lookup forbidden", role: "none", at: "2026-10-30T11:00:00Z", status: 403, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, submitted, poolReads := 0, 0, 0
			date := tc.date
			if date == "" {
				date = "2026-10-30"
			}
			modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "" {
					t.Error("wrong provider request or leaked worker credential")
				}
				var req struct {
					Messages []model.Message `json:"messages"`
				}
				if json.NewDecoder(r.Body).Decode(&req) != nil {
					t.Fatal("invalid provider request")
				}
				if len(req.Messages) < 2 || !strings.Contains(req.Messages[1].Content, `"wire_group":"wire-1"`) || !strings.Contains(req.Messages[1].Content, `"byline":"Jane Reporter"`) || !strings.Contains(req.Messages[1].Content, "Stored source text.") || !strings.Contains(req.Messages[1].Content, date) {
					t.Error("candidate guidance missing from model input")
				}
				content := ""
				switch calls {
				case 0:
					content = `{"labels":[{"id":"1","section":"news","fit":true}]}`
				case 1:
					content = `{"groups":[]}`
				case 2:
					wantMessages := 3
					if tc.sendBack {
						wantMessages = 4
					}
					if len(req.Messages) != wantMessages || !strings.Contains(req.Messages[2].Content, "Prefer local reporting.") {
						t.Error("reader notes missing")
					}
					if tc.sendBack && (!strings.Contains(req.Messages[3].Content, "BEGIN EDITOR_NOTE_JSON") || !strings.Contains(req.Messages[3].Content, "Explain the local effect.")) {
						t.Error("editor request missing")
					}
					content = `{"schema":1,"lead":"one","sections":[{"key":"news","items":[{"id":"one","kind":"news","title":"A report.","summary":"A useful report.","source":"Desk","url":"https://example.com/story"}]}]}`
				default:
					t.Error("unexpected model call")
				}
				calls++
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}, "finish_reason": "stop"}}, "usage": model.Usage{InputTokens: 10, OutputTokens: 10, TotalTokens: 20}})
			}))
			defer modelServer.Close()
			deadline := tc.deadline
			if deadline == "" {
				deadline = "06:30"
			}
			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer worker-key" {
					t.Error("worker key missing")
				}
				switch r.Method + " " + r.URL.Path {
				case "GET /api/v1/paper":
					json.NewEncoder(w).Encode(map[string]any{"agent_role": tc.role, "deadline": deadline, "timezone": "America/Chicago", "draft_lead": tc.lead, "reader_notes": "Prefer local reporting.", "masthead": "Daily", "sections": []config.Section{{Key: "news", Title: "News", Required: true}}})
				case "GET /api/v1/drafts/" + date:
					if tc.status != 0 {
						w.WriteHeader(tc.status)
						return
					}
					if tc.sendBack {
						fmt.Fprint(w, `{"state":"drafted","revision":3,"body":{"schema":1},"actions":[{"revision":2,"action":"submit"},{"revision":3,"action":"send_back","note":"Explain the local effect."}]}`)
					} else if tc.exists {
						fmt.Fprint(w, `{"state":"drafted","body":{"schema":1}}`)
					} else {
						w.WriteHeader(404)
					}
				case "GET /api/v1/candidates":
					poolReads++
					group := "wire-1"
					json.NewEncoder(w).Encode(map[string]any{"candidates": []candidate.Candidate{{ID: 1, Section: "news", CanonicalURL: "https://example.com/story", Outlet: "Desk", Title: "Story", Text: "Stored source text.", State: "ok", Byline: []string{"Jane Reporter"}, WireGroup: &group}, {ID: 2, Section: "news", State: "unavailable"}, {ID: 3, Section: "news", State: "ok", Repeat: true}, {ID: 4, Section: "removed", State: "ok", CanonicalURL: "https://example.com/old"}, {ID: 5, Section: "news", State: "ok", CanonicalURL: "http://example.com/plain-http"}}})
				case "POST /api/v1/runs/" + date + "/claim":
					if tc.refuse {
						w.WriteHeader(http.StatusConflict)
						return
					}
					json.NewEncoder(w).Encode(map[string]any{"lease_id": "lease", "expires_at": time.Now().Add(time.Hour), "remaining": model.Limits{MaxCalls: 4, MaxTokens: 33334}})
				case "POST /api/v1/runs/" + date + "/report":
					var report struct {
						Outcome  string                      `json:"outcome"`
						Draft    edition.Edition             `json:"draft"`
						Revision int64                       `json:"expected_revision"`
						Spend    struct{ Calls, Tokens int } `json:"spend"`
					}
					if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
						t.Error(err)
					}
					if report.Outcome != "submitted" || report.Draft.Lead != "one" || report.Draft.Schema != 2 || report.Spend.Calls != 3 || report.Spend.Tokens != 60 {
						t.Errorf("unexpected report: %#v", report)
					}
					expected := int64(0)
					if tc.sendBack {
						expected = 3
					}
					if report.Revision != expected {
						t.Error("wrong expected revision", report.Revision)
					}
					submitted++
					fmt.Fprint(w, `{"revision":1,"warnings":[]}`)
				default:
					t.Errorf("unexpected worker request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer apiServer.Close()
			worker, err := New(apiServer.URL, "worker-key", nil)
			if err != nil {
				t.Fatal(err)
			}
			tier := model.TierConfig{BaseURL: modelServer.URL + "/v1", Model: "test"}
			cfg := DraftConfig{Models: &model.Config{Fast: tier, Writer: tier}}
			now, err := time.Parse(time.RFC3339, tc.at)
			if err != nil {
				t.Fatal(err)
			}
			drafted, err := worker.DraftOnce(context.Background(), cfg, model.Options{}, now)
			if (err != nil) != tc.wantErr || drafted != tc.want {
				t.Fatal(drafted, err)
			}
			wantCalls, wantSubmits := 0, 0
			if tc.want {
				wantCalls, wantSubmits = 3, 1
			}
			if calls != wantCalls || submitted != wantSubmits || poolReads != wantSubmits {
				t.Fatal(calls, submitted, poolReads)
			}
		})
	}
}

func TestDraftConfigAndRelativePaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yaml")
	key := filepath.Join(dir, "provider-key")
	if err := os.WriteFile(key, []byte("private-provider-key"), 0600); err != nil {
		t.Fatal(err)
	}
	valid := `models:
  fast:
    base_url: http://localhost:11434/v1
    model: fast
    key_file: provider-key
  writer:
    base_url: http://localhost:11434/v1
    model: writer
    key_file: provider-key
prompts:
  directory: prompts
tracing:
  record_content: true
limits:
  max_calls: 4
  max_tokens: 20000
`
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadDraftConfig(path)
	if err != nil || c.Models.Fast.KeyFile != key || c.Models.Writer.KeyFile != key || c.Prompts.Directory != filepath.Join(dir, "prompts") || !c.Tracing.RecordContent || c.Limits.MaxCalls != 4 {
		t.Fatal(c, err)
	}
	for _, tc := range []struct{ body, path string }{
		{`{"models":null}`, "models"},
		{`{"models":{"fast":{"base_url":1,"model":"fast"},"writer":{"base_url":"http://localhost/v1","model":"writer"}}}`, "models.fast.base_url"},
		{`{"models":{"fast":{"base_url":"http://localhost/v1","model":"fast","api_key":"private-provider-key"},"writer":{"base_url":"http://localhost/v1","model":"writer"}}}`, "models.fast.api_key"},
		{`{"limits":{"max_calls":0,"max_tokens":1}}`, "limits.max_calls"},
		{`{"limits":{"max_calls":0,"max_tokens":0}}`, "limits.max_calls"},
		{`{"models":{"fast":{"base_url":"invalid","model":"fast"},"writer":{"base_url":"http://localhost/v1","model":"writer"}}}`, "models.fast.base_url"},
		{`{"prompts":{"directory":false}}`, "prompts.directory"},
	} {
		if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadDraftConfig(path); err == nil || !strings.Contains(err.Error(), tc.path+":") || strings.Contains(err.Error(), "private-provider-key") {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err = LoadDraftConfig(path)
	if err != nil || c.Models != nil {
		t.Fatal(c, err)
	}

}

func TestDraftCandidatesBoundedPagesAndSections(t *testing.T) {
	p := config.Paper{}
	for i := 0; i < 16; i++ {
		p.Sections = append(p.Sections, config.Section{Key: fmt.Sprintf("section-%d", i)})
	}
	reads := map[string]int{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		section := r.URL.Query().Get("section")
		if r.URL.Path != "/api/v1/candidates" || r.URL.Query().Get("limit") != "10" {
			t.Error(r.URL)
		}
		reads[section]++
		if reads[section] == 1 {
			json.NewEncoder(w).Encode(map[string]any{"candidates": []candidate.Candidate{{ID: 1, Section: section, State: "unavailable"}}, "next_cursor": "page-two"})
			return
		}
		if r.URL.Query().Get("cursor") != "page-two" {
			t.Error("missing cursor", r.URL)
		}
		list := []candidate.Candidate{}
		for i := 10; i > 0; i-- {
			list = append(list, candidate.Candidate{ID: int64(i), Section: section, State: "ok", CanonicalURL: fmt.Sprintf("https://example.com/%s/%d", section, i), Outlet: "Example", Text: strings.Repeat("é", 10000)})
		}
		json.NewEncoder(w).Encode(map[string]any{"candidates": list, "next_cursor": "older"})
	}))
	defer api.Close()
	worker, _ := New(api.URL, "key", nil)
	selected, err := worker.draftCandidates(context.Background(), p, false)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	textBytes := 0
	for _, c := range selected {
		counts[c.Section]++
		textBytes += len(c.Text)
		// Newest first: IDs 10, 9, 8 in each section.
		if want := strconv.Itoa(11 - counts[c.Section]); c.ID != want || !strings.HasSuffix(c.URL, "/"+want) {
			t.Fatal("did not prefer newest", c.ID, c.URL)
		}
		// Every section inherits the paper's sources, so section-fit may move
		// a candidate to any of the others.
		if len(c.Sections) != 15 || slices.Contains(c.Sections, c.Section) {
			t.Fatal("alternatives", c.Section, c.Sections)
		}
	}
	if len(selected) > maxDraftCandidates || textBytes > maxDraftTextBytes {
		t.Fatal(len(selected), textBytes)
	}
	// 16 sections share 48 candidates and 16 KiB: three stories of about 340
	// bytes each, not one story of 1 KiB.
	for _, s := range p.Sections {
		if counts[s.Key] != 3 || reads[s.Key] != 2 {
			t.Fatal(s.Key, counts[s.Key], reads[s.Key])
		}
	}
}

func TestDraftCandidatesShareSectionBudget(t *testing.T) {
	for _, tc := range []struct{ sections, perSection, itemBytes int }{
		{1, 8, 2048}, // one section: as before, eight stories of up to 2 KiB
		{4, 8, 512},
		{8, 6, 341},
	} {
		p := config.Paper{}
		for i := 0; i < tc.sections; i++ {
			p.Sections = append(p.Sections, config.Section{Key: fmt.Sprintf("s%d", i)})
		}
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			section := r.URL.Query().Get("section")
			list := []candidate.Candidate{}
			for i := 10; i > 0; i-- {
				list = append(list, candidate.Candidate{ID: int64(i), Section: section, State: "ok", CanonicalURL: fmt.Sprintf("https://example.com/%s/%d", section, i), Outlet: "Example", Text: strings.Repeat("word ", 1000)})
			}
			json.NewEncoder(w).Encode(map[string]any{"candidates": list})
		}))
		worker, _ := New(api.URL, "key", nil)
		selected, err := worker.draftCandidates(context.Background(), p, false)
		api.Close()
		if err != nil {
			t.Fatal(err)
		}
		counts := map[string]int{}
		for _, c := range selected {
			counts[c.Section]++
			if len(c.Text) > tc.itemBytes {
				t.Fatalf("%d sections: %d bytes, want at most %d", tc.sections, len(c.Text), tc.itemBytes)
			}
		}
		for _, s := range p.Sections {
			if counts[s.Key] != tc.perSection {
				t.Fatalf("%d sections: %s has %d stories, want %d", tc.sections, s.Key, counts[s.Key], tc.perSection)
			}
		}
	}
}

func TestPendingSendBack(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		actions     []store.DraftAction
		approved    *int64
		want        string
	}{
		{name: "pending", state: "drafted", actions: []store.DraftAction{{Revision: 2, Action: "send_back", Note: "Revise."}}, want: "Revise."},
		{name: "later note", state: "in_review", actions: []store.DraftAction{{Revision: 2, Action: "send_back", Note: "Revise."}, {Revision: 3, Action: "note", Note: "Another note."}}, want: "Revise."},
		{name: "resubmitted", state: "drafted", actions: []store.DraftAction{{Revision: 2, Action: "send_back", Note: "Revise."}, {Revision: 3, Action: "submit"}}},
		{name: "held", state: "held", actions: []store.DraftAction{{Revision: 2, Action: "send_back", Note: "Revise."}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pendingSendBack(store.Draft{State: tc.state, Actions: tc.actions, ApprovedRevision: tc.approved}); got != tc.want {
				t.Fatal(got)
			}
		})
	}
}

func TestWorkerReportsModelAndSubmissionFailures(t *testing.T) {
	for _, failure := range []string{"model", "submit", "leased"} {
		t.Run(failure, func(t *testing.T) {
			calls, reports := 0, []string{}
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if failure == "model" {
					w.WriteHeader(503)
					return
				}
				content := `{"labels":[{"id":"1","section":"news","fit":true}]}`
				if calls == 2 {
					content = `{"groups":[]}`
				}
				if calls == 3 {
					content = `{"schema":2,"lead":"one","sections":[{"key":"news","items":[{"id":"one","kind":"news","title":"A report","summary":"A report.","source":"Example","url":"https://example.com/a"}]}]}`
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}, "finish_reason": "stop"}}, "usage": model.Usage{InputTokens: 10, OutputTokens: 10, TotalTokens: 20}})
			}))
			defer provider.Close()
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/paper":
					fmt.Fprint(w, `{"agent_role":"none","timezone":"UTC","deadline":"06:30","sections":[{"key":"news","title":"News"}]}`)
				case "/api/v1/drafts/2026-10-30":
					w.WriteHeader(404)
				case "/api/v1/candidates":
					json.NewEncoder(w).Encode(map[string]any{"candidates": []candidate.Candidate{{ID: 1, Section: "news", CanonicalURL: "https://example.com/a", State: "ok", Outlet: "Example", Text: "Facts."}}})
				case "/api/v1/runs/2026-10-30/claim":
					if failure == "leased" {
						w.WriteHeader(409)
						return
					}
					json.NewEncoder(w).Encode(store.DraftLease{ID: "lease", ExpiresAt: time.Now().Add(time.Minute), Remaining: store.DraftBudget{MaxCalls: 4, MaxTokens: 33334}})
				case "/api/v1/runs/2026-10-30/report":
					var report struct {
						Outcome string           `json:"outcome"`
						Spend   store.DraftSpend `json:"spend"`
					}
					if json.NewDecoder(r.Body).Decode(&report) != nil {
						t.Error("invalid report")
					}
					reports = append(reports, report.Outcome)
					if report.Spend.Calls != calls || report.Spend.Tokens <= 0 {
						t.Error(report.Spend, calls)
					}
					if report.Outcome == "submitted" {
						w.WriteHeader(422)
						return
					}
					fmt.Fprint(w, `{}`)
				default:
					t.Error(r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer api.Close()
			worker, _ := New(api.URL, "key", nil)
			tier := model.TierConfig{BaseURL: provider.URL, Model: "test"}
			drafted, err := worker.DraftOnce(context.Background(), DraftConfig{Models: &model.Config{Fast: tier, Writer: tier}}, model.Options{}, time.Date(2026, 10, 30, 6, 0, 0, 0, time.UTC))
			if drafted || (err == nil) != (failure == "leased") {
				t.Fatal(drafted, err)
			}
			switch failure {
			case "model":
				if calls != 1 || fmt.Sprint(reports) != "[failed]" {
					t.Fatal(calls, reports)
				}
			case "submit":
				if calls != 3 || fmt.Sprint(reports) != "[submitted failed]" {
					t.Fatal(calls, reports)
				}
			case "leased":
				if calls != 0 || len(reports) != 0 {
					t.Fatal(calls, reports)
				}
			}
		})
	}
}

func TestDraftOnceWithoutModels(t *testing.T) {
	for _, tc := range []struct {
		name, role string
		missing    bool
		forbidden  bool // a gather-only key, without the draft capability
	}{
		{name: "none", role: "none"},
		{name: "reviewer", role: "reviewer"},
		{name: "required section empty", role: "none", missing: true},
		{name: "gather-only key", role: "none", forbidden: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const date = "2026-10-30"
			var claimed, reported bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET /api/v1/paper":
					sections := []config.Section{{Key: "local", Required: true}}
					if tc.missing {
						sections = append(sections, config.Section{Key: "world", Required: true})
					}
					json.NewEncoder(w).Encode(map[string]any{
						"agent_role": tc.role, "deadline": "06:30", "timezone": "UTC", "sections": sections,
					})
				case "GET /api/v1/drafts/" + date:
					w.WriteHeader(http.StatusNotFound)
				case "GET /api/v1/candidates":
					var pool []candidate.Candidate
					if r.URL.Query().Get("section") == "local" {
						pool = []candidate.Candidate{{
							ID: 1, Section: "local", State: "ok", Title: "A local report", Text: "The library reopened. Readers returned.",
							Outlet: "Desk", CanonicalURL: "https://example.com/report",
						}}
					}
					json.NewEncoder(w).Encode(map[string]any{"candidates": pool})
				case "POST /api/v1/runs/" + date + "/claim":
					if tc.forbidden {
						w.WriteHeader(http.StatusForbidden)
						fmt.Fprint(w, `{"error":"forbidden"}`)
						return
					}
					var budget store.DraftBudget
					if err := json.NewDecoder(r.Body).Decode(&budget); err != nil || budget != (store.DraftBudget{MaxCalls: 1, MaxTokens: 1}) {
						t.Errorf("unexpected claim: %+v, %v", budget, err)
					}
					claimed = true
					json.NewEncoder(w).Encode(store.DraftLease{ID: "lease", ExpiresAt: time.Now().Add(time.Hour), Remaining: budget})
				case "POST /api/v1/runs/" + date + "/report":
					var report struct {
						LeaseID string           `json:"lease_id"`
						Outcome string           `json:"outcome"`
						Draft   *edition.Edition `json:"draft"`
						Spend   store.DraftSpend `json:"spend"`
					}
					if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
						t.Error(err)
					}
					if !claimed || reported || report.LeaseID != "lease" || report.Spend != (store.DraftSpend{}) {
						t.Errorf("unexpected report: %+v", report)
					}
					reported = true
					if tc.missing {
						if report.Outcome != "failed" || report.Draft != nil {
							t.Errorf("unexpected failure report: %+v", report)
						}
					} else if report.Outcome != "submitted" || report.Draft == nil || len(report.Draft.Sections) != 1 || len(report.Draft.Sections[0].Items) != 1 {
						t.Errorf("unexpected submission: %+v", report)
					} else if item := report.Draft.Sections[0].Items[0]; item.ID != report.Draft.Lead || item.Title != "A local report" ||
						item.Summary != "The library reopened. Readers returned." || item.Source != "Desk" || item.URL != "https://example.com/report" {
						t.Errorf("unexpected item: %+v", item)
					}
					fmt.Fprint(w, `{"revision":1}`)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			worker, err := New(server.URL, "worker-key", nil)
			if err != nil {
				t.Fatal(err)
			}
			drafted, err := worker.DraftOnce(context.Background(), DraftConfig{}, model.Options{}, time.Date(2026, 10, 30, 6, 0, 0, 0, time.UTC))
			if tc.forbidden {
				if drafted || err != nil || reported {
					t.Fatalf("gather-only key: drafted = %v, error = %v, reported = %v", drafted, err, reported)
				}
				return
			}
			if drafted == tc.missing || (err != nil) != tc.missing || !reported {
				t.Fatalf("drafted = %v, error = %v, reported = %v", drafted, err, reported)
			}
		})
	}
}
