package scheduler_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/api"
	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/keys"
	"github.com/irashack/night-desk/internal/store"
)

func TestLateEditionAfterMissedDeadline(t *testing.T) {
	for _, role := range []string{"none", "reviewer"} {
		t.Run(role, func(t *testing.T) {
			db := database(t, t.TempDir())
			defer db.Close()
			s := scope(t, db)
			p := paper()
			p.AgentRole = role
			// The deadline (06:30 Chicago, 11:30 UTC) passes with nothing drafted.
			now := at("2026-10-30T12:00:00Z")
			sched := runner(db, p, &now)
			sched.Tick(ctx)
			run, err := db.EnsureRun(ctx, p.ID, "2026-10-30")
			if err != nil || run.State != "missed" {
				t.Fatal(run, err)
			}
			limits := store.DraftBudget{MaxCalls: 3, MaxTokens: 1000}
			if _, err = db.ClaimDraftRun(ctx, s, "2026-10-30", limits, now); !errors.Is(err, store.ErrRunConflict) {
				t.Fatal("an ordinary claim must not reopen a missed run", err)
			}
			lease, err := db.ClaimLateDraftRun(ctx, s, "2026-10-30", limits, now)
			if err != nil {
				t.Fatal(err)
			}
			// While the worker drafts, the scheduler waits rather than missing it again.
			now = now.Add(time.Minute)
			sched.Tick(ctx)
			if run, _ = db.EnsureRun(ctx, p.ID, "2026-10-30"); run.State != "drafting" {
				t.Fatal("a live lease must not be marked missed", run)
			}
			if _, err = db.ReportDraftRun(ctx, s, "2026-10-30", lease.ID, "worker", "submitted", store.DraftSpend{Calls: 3, Tokens: 900}, 0, body(), now); err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Minute)
			sched.Tick(ctx)
			_, editionErr := db.Edition(ctx, s, "2026-10-30")
			d, err := db.Draft(ctx, s, "2026-10-30")
			if err != nil {
				t.Fatal(err)
			}
			switch role {
			case "none":
				// A paper without an agent publishes its late edition at once.
				if editionErr != nil || d.State != "published" {
					t.Fatal(d.State, editionErr)
				}
			case "reviewer":
				// The default hold policy keeps an unreviewed late draft for the reviewer.
				if editionErr == nil || d.State != "held" {
					t.Fatal(d.State, editionErr)
				}
			}
		})
	}
}

func TestMissedRunStaysQuietWithoutDraft(t *testing.T) {
	db := database(t, t.TempDir())
	defer db.Close()
	p := paper()
	now := at("2026-10-30T12:00:00Z")
	sched := runner(db, p, &now)
	for range 5 {
		sched.Tick(ctx)
		now = now.Add(time.Minute)
	}
	run, err := db.EnsureRun(ctx, p.ID, "2026-10-30")
	if err != nil || run.State != "missed" || run.Attempts != 1 {
		t.Fatal("a missed run without a draft must not use further attempts", run, err)
	}
}

func TestLateClaimRules(t *testing.T) {
	db := database(t, t.TempDir())
	defer db.Close()
	_, key, err := keys.Issue(ctx, db, "daily", "worker", auth.Read, auth.Draft)
	if err != nil {
		t.Fatal(err)
	}
	p := paper()
	now := at("2026-10-30T12:00:00Z")
	c := &config.Config{Papers: []config.Paper{p}}
	server := httptest.NewServer((&api.Server{Store: db, Config: func() *config.Config { return c }, Now: func() time.Time { return now }}).Handler())
	defer server.Close()
	claim := func(date string) int {
		req, _ := http.NewRequest("POST", server.URL+"/api/v1/runs/"+date+"/claim", strings.NewReader(`{"max_calls":3,"max_tokens":1000}`))
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	// After the deadline, before the scheduler marks the run missed: refused.
	if code := claim("2026-10-30"); code != http.StatusConflict {
		t.Fatalf("claim after deadline of an unmissed run: %d", code)
	}
	for _, date := range []string{"2026-10-29", "2026-10-30"} {
		if _, err = db.EnsureRun(ctx, p.ID, date); err != nil {
			t.Fatal(err)
		}
		if err = db.FinishRun(ctx, p.ID, date, "missed"); err != nil {
			t.Fatal(err)
		}
	}
	if code := claim("2026-10-29"); code != http.StatusConflict {
		t.Fatalf("an earlier missed date must stay missed: %d", code)
	}
	if code := claim("2026-10-30"); code != http.StatusOK {
		t.Fatalf("late claim for today's missed run: %d", code)
	}
	// Tomorrow's edition is claimed normally inside its lead window.
	now = at("2026-10-31T10:30:00Z")
	if code := claim("2026-10-31"); code != http.StatusOK {
		t.Fatalf("ordinary claim before the deadline: %d", code)
	}
}
