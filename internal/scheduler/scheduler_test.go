package scheduler_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/api"
	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/candidate"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/keys"
	"github.com/irashack/night-desk/internal/scheduler"
	"github.com/irashack/night-desk/internal/store"
)

var ctx = context.Background()

func paper() config.Paper {
	deadline := "06:30"
	return config.Paper{ID: "daily", AgentRole: "reviewer", Timezone: "America/Chicago", Deadline: &deadline, Sections: []config.Section{{Key: "news", Title: "News", Required: true}}}
}
func body() *edition.Edition {
	return &edition.Edition{Schema: 1, Lead: "one", Sections: []edition.Section{{Key: "news", Items: []edition.Item{{ID: "one", Kind: "news", Title: "Headline", Summary: "A summary.", Source: "Desk", URL: "https://example.com/story"}}}}}
}
func database(t *testing.T, dir string) *store.Store {
	t.Helper()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}
func scope(t *testing.T, db *store.Store) auth.PaperScope {
	t.Helper()
	_, key, err := keys.Issue(ctx, db, "daily", "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := auth.Authenticate(ctx, db, key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}
func runner(db *store.Store, p config.Paper, now *time.Time) *scheduler.Scheduler {
	c := &config.Config{Papers: []config.Paper{p}}
	return &scheduler.Scheduler{Store: db, Config: func() *config.Config { return c }, Publisher: &api.Server{Store: db}, Now: func() time.Time { return *now }}
}
func TestDeadlinePoliciesAndRestartCatchUp(t *testing.T) {
	for _, policy := range []string{"hold", "publish-unreviewed"} {
		t.Run(policy, func(t *testing.T) {
			dir := t.TempDir()
			db := database(t, dir)
			s := scope(t, db)
			p := paper()
			p.DeadlinePolicy = policy
			if _, err := db.SubmitDraft(ctx, s, "2026-10-29", "author", body()); err != nil {
				t.Fatal(err)
			}
			if _, err := db.SubmitDraft(ctx, s, "2026-10-30", "author", body()); err != nil {
				t.Fatal(err)
			}
			now := at("2026-10-30T11:29:59Z") // 06:29:59 CDT
			runner(db, p, &now).Tick(ctx)
			run, err := db.EnsureRun(ctx, p.ID, "2026-10-30")
			if err != nil || run.Attempts != 0 {
				t.Fatal(run, err)
			}
			// Restart while the deadline passes; only today's draft is caught up.
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			db = database(t, dir)
			defer db.Close()
			now = at("2026-10-30T12:00:00Z")
			runner(db, p, &now).Tick(ctx)
			d, err := db.Draft(ctx, s, "2026-10-30")
			if err != nil || d.Revision != 2 {
				t.Fatal(d, err)
			}
			old, err := db.Draft(ctx, s, "2026-10-29")
			if err != nil || old.State != "drafted" {
				t.Fatal(old, err)
			}
			want := "held"
			if policy == "publish-unreviewed" {
				want = "published"
			}
			if d.State != want || d.Unreviewed != (want == "published") {
				t.Fatal(d)
			}
			run, err = db.EnsureRun(ctx, p.ID, "2026-10-30")
			if err != nil || run.State != want || run.Attempts != 1 {
				t.Fatal(run, err)
			}
			published, err := db.RunPublished(ctx, p.ID, "2026-10-30")
			if err != nil || published != (want == "published") {
				t.Fatal(published, err)
			}
			if published {
				flag, err := db.EditionUnreviewed(ctx, s, "2026-10-30")
				if err != nil || !flag {
					t.Fatal(flag, err)
				}
			}
			// Reconstructing the scheduler after completion cannot rerun the policy.
			runner(db, p, &now).Tick(ctx)
			d, err = db.Draft(ctx, s, "2026-10-30")
			if err != nil || d.Revision != 2 || len(d.Actions) != 2 {
				t.Fatal(d, err)
			}
		})
	}
}
func TestDSTDeadline(t *testing.T) {
	tests := []struct{ date, deadline, want string }{
		{"2026-03-08T12:00:00Z", "02:30", "2026-03-08T08:00:00Z"},
		{"2026-03-08T12:00:00Z", "06:30", "2026-03-08T11:30:00Z"},
		{"2026-11-01T12:00:00Z", "01:30", "2026-11-01T06:30:00Z"},
		{"2026-11-01T12:00:00Z", "06:30", "2026-11-01T12:30:00Z"},
	}
	for _, tc := range tests {
		p := paper()
		p.Deadline = &tc.deadline
		got, err := scheduler.Deadline(p, at(tc.date))
		if err != nil || !got.Equal(at(tc.want)) {
			t.Fatal(tc, got, err)
		}
	}
	db := database(t, t.TempDir())
	defer db.Close()
	s := scope(t, db)
	p := paper()
	deadline := "01:30"
	p.Deadline = &deadline
	if _, err := db.SubmitDraft(ctx, s, "2026-11-01", "author", body()); err != nil {
		t.Fatal(err)
	}
	now := at("2026-11-01T06:29:00Z")
	r := runner(db, p, &now)
	r.Tick(ctx)
	d, _ := db.Draft(ctx, s, "2026-11-01")
	if d.State != "drafted" {
		t.Fatal(d)
	}
	now = at("2026-11-01T06:30:00Z")
	r.Tick(ctx)
	now = at("2026-11-01T07:30:00Z")
	r.Tick(ctx) // Second 01:30 local time.
	d, _ = db.Draft(ctx, s, "2026-11-01")
	if d.State != "held" || d.Revision != 2 {
		t.Fatal(d)
	}
}

type policyFunc func(context.Context, config.Paper, string) error

func (f policyFunc) ApplyDeadline(ctx context.Context, p config.Paper, date string) error {
	return f(ctx, p, date)
}
func TestWorkerOwnsDraftingAndPolicyRetryLimits(t *testing.T) {
	db := database(t, t.TempDir())
	defer db.Close()
	p := paper()
	now := at("2026-10-30T10:00:00Z")
	for _, role := range []string{"author", "reviewer", "none"} {
		p.AgentRole = role
		r := runner(db, p, &now)
		r.Tick(ctx)
		r.Tick(ctx)
		run, err := db.EnsureRun(ctx, p.ID, "2026-10-30")
		if err != nil || run.DraftAttempts != 0 || run.State != "pending" {
			t.Fatal(run, err)
		}
	}
	// Deadline-policy retries remain bounded across ticks.
	now = at("2026-10-30T12:00:00Z")
	calls := 0
	for i := 0; i < 5; i++ {
		r := runner(db, p, &now)
		r.Publisher = policyFunc(func(context.Context, config.Paper, string) error { calls++; return errors.New("temporary failure") })
		r.Tick(ctx)
	}
	run, err := db.EnsureRun(ctx, p.ID, "2026-10-30")
	if err != nil || calls != 3 || run.Attempts != 3 || run.State != "failed" {
		t.Fatal(run, calls, err)
	}
}
func TestDeadlineDoesNotOverwriteAndInvalidDraftHeld(t *testing.T) {
	db := database(t, t.TempDir())
	defer db.Close()
	s := scope(t, db)
	p := paper()
	p.DeadlinePolicy = "publish-unreviewed"
	ed := body()
	if _, err := db.SubmitDraft(ctx, s, "2026-10-30", "author", ed); err != nil {
		t.Fatal(err)
	}
	ed.Sections[0].Items[0].Title = "Published first"
	if err := db.Publish(ctx, s, "2026-10-30", ed, nil); err != nil {
		t.Fatal(err)
	}
	now := at("2026-10-30T12:00:00Z")
	runner(db, p, &now).Tick(ctx)
	got, err := db.Edition(ctx, s, "2026-10-30")
	if err != nil || got.Sections[0].Items[0].Title != "Published first" {
		t.Fatal(got, err)
	}
	if _, err := db.SubmitDraft(ctx, s, "2026-10-31", "author", body()); err != nil {
		t.Fatal(err)
	}
	// A config reload makes the previously valid draft invalid for the current paper.
	p.Sections = append(p.Sections, config.Section{Key: "local", Title: "Local", Required: true})
	now = at("2026-10-31T12:00:00Z")
	runner(db, p, &now).Tick(ctx)
	d, err := db.Draft(ctx, s, "2026-10-31")
	if err != nil || d.State != "held" {
		t.Fatal(d, err)
	}
	now = at("2026-11-01T13:00:00Z")
	runner(db, p, &now).Tick(ctx)
	// Nothing was drafted: the run is missed, open to a late edition that day.
	run, err := db.EnsureRun(ctx, p.ID, "2026-11-01")
	if err != nil || run.State != "missed" {
		t.Fatal(run, err)
	}
}
func TestSchedulerStopsWithContext(t *testing.T) {
	db := database(t, t.TempDir())
	defer db.Close()
	p := paper()
	now := at("2026-10-30T12:00:00Z")
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	done := make(chan struct{})
	go func() { runner(db, p, &now).Run(canceled); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
}

func TestInterruptedDeadlineRunResumes(t *testing.T) {
	dir := t.TempDir()
	db := database(t, dir)
	scope := scope(t, db)
	p := paper()
	if _, err := db.SubmitDraft(ctx, scope, "2026-10-30", "author", body()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnsureRun(ctx, p.ID, "2026-10-30"); err != nil {
		t.Fatal(err)
	}
	if claimed, err := db.BeginRunAttempt(ctx, p.ID, "2026-10-30", false); err != nil || !claimed {
		t.Fatal(claimed, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = database(t, dir)
	defer db.Close()
	now := at("2026-10-30T12:00:00Z")
	runner(db, p, &now).Tick(ctx)
	run, err := db.EnsureRun(ctx, p.ID, "2026-10-30")
	if err != nil || run.State != "held" || run.Attempts != 2 {
		t.Fatal(run, err)
	}
	d, err := db.Draft(ctx, scope, "2026-10-30")
	if err != nil || d.State != "held" || len(d.Actions) != 2 {
		t.Fatal(d, err)
	}
}

func TestNoneDeadlinePublishesLatestWithoutReviewMark(t *testing.T) {
	for _, policy := range []string{"hold", "publish-unreviewed"} {
		t.Run(policy, func(t *testing.T) {
			db := database(t, t.TempDir())
			defer db.Close()
			scope := scope(t, db)
			p := paper()
			p.AgentRole = "none"
			p.DeadlinePolicy = policy
			if _, err := db.SubmitDraft(ctx, scope, "2026-10-30", "worker", body()); err != nil {
				t.Fatal(err)
			}
			latest := body()
			latest.Sections[0].Items[0].Title = "Latest valid draft"
			if _, err := db.SubmitDraft(ctx, scope, "2026-10-30", "worker", latest); err != nil {
				t.Fatal(err)
			}
			now := at("2026-10-30T11:29:59Z")
			runner(db, p, &now).Tick(ctx)
			if yes, err := db.RunPublished(ctx, p.ID, "2026-10-30"); err != nil || yes {
				t.Fatal(yes, err)
			}
			now = now.Add(time.Second)
			runner(db, p, &now).Tick(ctx)
			draft, err := db.Draft(ctx, scope, "2026-10-30")
			if err != nil || draft.State != "published" || draft.Unreviewed || draft.ApprovedRevision != nil || draft.Actions[len(draft.Actions)-1].Action != "publish" {
				t.Fatal(draft, err)
			}
			flag, err := db.EditionUnreviewed(ctx, scope, "2026-10-30")
			if err != nil || flag {
				t.Fatal(flag, err)
			}
			ed, err := db.Edition(ctx, scope, "2026-10-30")
			if err != nil || ed.Sections[0].Items[0].Title != "Latest valid draft" {
				t.Fatal(ed, err)
			}
			runner(db, p, &now).Tick(ctx)
			again, err := db.Draft(ctx, scope, "2026-10-30")
			if err != nil || again.Revision != draft.Revision {
				t.Fatal(again, err)
			}
		})
	}
}

func TestNoneDeadlineHoldsInvalidDraft(t *testing.T) {
	db := database(t, t.TempDir())
	defer db.Close()
	scope := scope(t, db)
	p := paper()
	p.AgentRole = "none"
	if _, err := db.SubmitDraft(ctx, scope, "2026-10-30", "worker", body()); err != nil {
		t.Fatal(err)
	}
	p.Sections = append(p.Sections, config.Section{Key: "local", Required: true})
	now := at("2026-10-30T11:30:00Z")
	runner(db, p, &now).Tick(ctx)
	d, err := db.Draft(ctx, scope, "2026-10-30")
	if err != nil || d.State != "held" {
		t.Fatal(d, err)
	}
}

func TestTickPrunesExpiredSnapshotsWithoutWorker(t *testing.T) {
	db := database(t, t.TempDir())
	defer db.Close()
	p := paper()
	p.Deadline, p.RetentionDays = nil, 7
	s := scope(t, db)
	snapshot := func(link string, age time.Duration) candidate.Candidate {
		return candidate.Candidate{Section: "news", CanonicalURL: link, Outlet: "Desk", Title: "Story", FetchedAt: time.Now().UTC().Add(-age), Text: "Stored text.", State: "ok"}
	}
	if err := db.UpsertCandidates(ctx, s, []candidate.Candidate{snapshot("https://example.com/old", 8*24*time.Hour), snapshot("https://example.com/new", time.Hour)}); err != nil {
		t.Fatal(err)
	}
	now := at("2026-11-20T12:00:00Z")
	r := runner(db, p, &now)
	r.Tick(ctx)
	kept := func() []string {
		t.Helper()
		pool, err := db.Candidates(ctx, s, "", time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		links := []string{}
		for _, c := range pool {
			links = append(links, c.CanonicalURL)
		}
		return links
	}
	if got := kept(); len(got) != 1 || got[0] != "https://example.com/new" {
		t.Fatalf("after first tick: %v", got)
	}
	if err := db.UpsertCandidates(ctx, s, []candidate.Candidate{snapshot("https://example.com/later", 9*24*time.Hour)}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Minute)
	r.Tick(ctx)
	if got := kept(); len(got) != 2 {
		t.Fatalf("pruned again within the hour: %v", got)
	}
	now = now.Add(time.Hour)
	r.Tick(ctx)
	if got := kept(); len(got) != 1 {
		t.Fatalf("after an hour: %v", got)
	}
}
