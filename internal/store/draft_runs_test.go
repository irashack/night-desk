package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/api"
	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/candidate"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/gather"
	"github.com/irashack/night-desk/internal/keys"
	"github.com/irashack/night-desk/internal/model"
	"github.com/irashack/night-desk/internal/store"
)

func TestDraftRunBudgetCompletesWorkerPipeline(t *testing.T) {
	s, dir := open(t)
	_, key, err := keys.Issue(ctx, s, "daily", "worker", auth.Read, auth.Gather, auth.Draft)
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.Authenticate(ctx, s, key)
	if err != nil {
		t.Fatal(err)
	}
	c := candidate.Candidate{Section: "news", CanonicalURL: "https://example.com/story", Outlet: "Desk", Title: "Story", Text: "Facts.", State: "ok", FetchedAt: time.Now().UTC()}
	if err := s.UpsertCandidates(ctx, a, []candidate.Candidate{c}); err != nil {
		t.Fatal(err)
	}
	// Drafting runs in real time: lease expiry bounds the worker's model calls.
	// The deadline is 30 minutes away, inside the default 90-minute lead.
	at := time.Now().UTC()
	due := at.Add(30 * time.Minute)
	deadline, date := due.Format("15:04"), due.Format("2006-01-02")
	cfg := &config.Config{Papers: []config.Paper{{ID: "daily", AgentRole: "none", Timezone: "UTC", Deadline: &deadline, Sections: []config.Section{{Key: "news", Title: "News", Required: true}}}}}
	server := httptest.NewServer((&api.Server{Store: s, Config: func() *config.Config { return cfg }}).Handler())
	defer server.Close()
	contents := []string{
		`{"labels":[{"id":"1","section":"news","fit":true}]}`,
		`{"groups":[]}`,
		`{"schema":2,"lead":"one","sections":[{"key":"news","items":[{"id":"one","kind":"news","title":"A report.","summary":"A useful report.","source":"Desk","url":"https://example.com/story"}]}]}`,
	}
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls >= len(contents) {
			t.Error("unexpected model call")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		content := contents[calls]
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}, "finish_reason": "stop"}}, "usage": model.Usage{InputTokens: 1000, OutputTokens: 1000, TotalTokens: 2000}})
	}))
	defer provider.Close()
	worker, err := gather.New(server.URL, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	tier := model.TierConfig{BaseURL: provider.URL, Model: "test", Price: &model.Price{InputPerMTok: 4, OutputPerMTok: 20}}
	drafted, err := worker.DraftOnce(ctx, gather.DraftConfig{Models: &model.Config{Fast: tier, Writer: tier}, Limits: model.Limits{MaxCalls: 3, MaxTokens: 18000}}, model.Options{}, at)
	if err != nil || !drafted || calls != 3 {
		t.Fatalf("drafted=%v calls=%d err=%v", drafted, calls, err)
	}
	draft, err := s.Draft(ctx, a, date)
	if err != nil || draft.State != "drafted" || draft.Revision != 1 || draft.Body == nil || draft.Body.Lead != "one" {
		t.Fatal(draft, err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "nightdesk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var spentCalls, tokens, attempts int
	if err = db.QueryRow("SELECT draft_calls,draft_tokens,draft_attempts FROM runs WHERE paper_id='daily' AND date=?", date).Scan(&spentCalls, &tokens, &attempts); err != nil || spentCalls != 3 || tokens != 6000 || attempts != 1 {
		t.Fatal(spentCalls, tokens, attempts, err)
	}
	// Three calls of 1000 input and 1000 output tokens at $4 and $20 per million.
	data, err := s.Quality(ctx, "daily", date, date, nil)
	if err != nil || data.DraftRuns != 1 || data.DraftTokens != 6000 || data.DraftCostMicros != 72000 {
		t.Fatal(data.DraftRuns, data.DraftTokens, data.DraftCostMicros, err)
	}
}

func TestDraftRunLeaseExpirationSpendingAndRestart(t *testing.T) {
	s, dir := open(t)
	a, b := scope(t, s, "a"), scope(t, s, "b")
	at := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	limits := store.DraftBudget{MaxCalls: 12, MaxTokens: 9000}
	lease, err := s.ClaimDraftRun(ctx, a, "2026-10-30", limits, at)
	if err != nil || lease.Remaining != limits {
		t.Fatal(lease, err)
	}
	if _, err = s.ClaimDraftRun(ctx, a, "2026-10-30", limits, at); !errors.Is(err, store.ErrRunConflict) {
		t.Fatal(err)
	}
	if _, err = s.ReportDraftRun(ctx, b, "2026-10-30", lease.ID, "worker", "failed", store.DraftSpend{}, 0, nil, at); !errors.Is(err, store.ErrRunConflict) {
		t.Fatal("tenant leak", err)
	}
	// Reports refund the unused reservation exactly once. The remainder survives
	// a server restart and cannot be raised by a later worker.
	spent := store.DraftSpend{Calls: 2, Tokens: 1000}
	for range 2 {
		if _, err = s.ReportDraftRun(ctx, a, "2026-10-30", lease.ID, "worker", "failed", spent, 0, nil, at); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	later := at.Add(store.DraftLeaseDuration)
	second, err := s.ClaimDraftRun(ctx, a, "2026-10-30", store.DraftBudget{MaxCalls: 120, MaxTokens: 90000}, later)
	if err != nil || second.ID == lease.ID || second.Remaining != (store.DraftBudget{MaxCalls: 10, MaxTokens: 8000}) {
		t.Fatal(second, err)
	}
	// A lost report forfeits the full remaining budget, even after expiration.
	later = later.Add(store.DraftLeaseDuration)
	if _, err = s.ReportDraftRun(ctx, a, "2026-10-30", second.ID, "worker", "failed", store.DraftSpend{}, 0, nil, later); !errors.Is(err, store.ErrRunConflict) {
		t.Fatal("stale report", err)
	}
	if _, err = s.ClaimDraftRun(ctx, a, "2026-10-30", limits, later); !errors.Is(err, store.ErrRunConflict) {
		t.Fatal("expired reservation was refunded", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "nightdesk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var calls, tokens, attempts int
	if err = db.QueryRow("SELECT draft_calls,draft_tokens,draft_attempts FROM runs WHERE paper_id='a' AND date='2026-10-30'").Scan(&calls, &tokens, &attempts); err != nil || calls != 12 || tokens != 9000 || attempts != 2 {
		t.Fatal(calls, tokens, attempts, err)
	}
	if _, err = s.ClaimDraftRun(ctx, a, "2026-10-31", limits, later); err != nil {
		t.Fatal("new date", err)
	}
	if _, err = s.ClaimDraftRun(ctx, b, "2026-10-30", limits, later); err != nil {
		t.Fatal("other paper", err)
	}
}

func TestDraftRunReportedFailuresLeaveBoundedRetries(t *testing.T) {
	s, _ := open(t)
	a := scope(t, s, "a")
	at := time.Now()
	limits := store.DraftBudget{MaxCalls: 12, MaxTokens: 9000}
	for attempt := 0; attempt < store.MaxRunAttempts; attempt++ {
		lease, err := s.ClaimDraftRun(ctx, a, "2026-10-30", limits, at)
		want := store.DraftBudget{MaxCalls: 12 - attempt, MaxTokens: 9000 - 1000*attempt}
		if err != nil || lease.Remaining != want {
			t.Fatal(lease, want, err)
		}
		if _, err := s.ReportDraftRun(ctx, a, "2026-10-30", lease.ID, "worker", "failed", store.DraftSpend{Calls: 1, Tokens: 1000}, 0, nil, at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ClaimDraftRun(ctx, a, "2026-10-30", limits, at); !errors.Is(err, store.ErrRunConflict) {
		t.Fatal("unbounded attempts", err)
	}
}

func TestDraftRunConcurrentClaims(t *testing.T) {
	s, dir := open(t)
	a := scope(t, s, "a")
	second, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, db := range []*store.Store{s, second} {
		workers.Add(1)
		go func(db *store.Store) {
			defer workers.Done()
			<-start
			_, err := db.ClaimDraftRun(context.Background(), a, "2026-10-30", store.DraftBudget{MaxCalls: 12, MaxTokens: 9000}, time.Now())
			results <- err
		}(db)
	}
	close(start)
	workers.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, store.ErrRunConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal(successes, conflicts)
	}
}

func TestDraftRunCannotSubmitAfterSchedulerHoldsDate(t *testing.T) {
	s, _ := open(t)
	a := scope(t, s, "a")
	at := time.Now()
	lease, err := s.ClaimDraftRun(ctx, a, "2026-10-30", store.DraftBudget{MaxCalls: 3, MaxTokens: 100000}, at)
	if err != nil || lease.Remaining.MaxCalls != 3 {
		t.Fatal(lease, err)
	}
	if err = s.FinishRun(ctx, "a", "2026-10-30", "held"); err != nil {
		t.Fatal(err)
	}
	_, err = s.ReportDraftRun(ctx, a, "2026-10-30", lease.ID, "worker", "submitted", store.DraftSpend{Calls: 3, Tokens: 100}, 0, &edition.Edition{Schema: 2}, at)
	if !errors.Is(err, store.ErrRunConflict) {
		t.Fatal(err)
	}
	// Spending still settles without reopening the held date.
	if _, err = s.ReportDraftRun(ctx, a, "2026-10-30", lease.ID, "worker", "failed", store.DraftSpend{Calls: 3, Tokens: 100}, 0, nil, at); err != nil {
		t.Fatal(err)
	}
	run, err := s.EnsureRun(ctx, "a", "2026-10-30")
	if err != nil || run.State != "held" {
		t.Fatal(run, err)
	}
}
