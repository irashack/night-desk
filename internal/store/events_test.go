package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/webhook"
)

func eventStore(t *testing.T) (*Store, auth.PaperScope) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	key := "nd_" + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	if err = s.InsertKey(ctx, "key", "daily", "", sha256.Sum256([]byte(key))); err != nil {
		t.Fatal(err)
	}
	scope, err := auth.Authenticate(ctx, s, key)
	if err != nil {
		t.Fatal(err)
	}
	c := &config.Config{Papers: []config.Paper{{ID: "daily", AgentRole: "reviewer", Webhooks: []config.Webhook{{URL: "https://agent.example/all"}, {URL: "https://agent.example/feedback", Events: []string{webhook.EventFeedbackNew}}}}}}
	s.SetEventConfig(func() *config.Config { return c })
	return s, scope
}
func eventEdition() *edition.Edition {
	return &edition.Edition{Schema: 1, Lead: "one", Sections: []edition.Section{{Key: "news", Items: []edition.Item{{ID: "one", Kind: "news", Title: "Title", Summary: "Summary", Source: "Desk", URL: "https://example.com/news"}}}}}
}

func TestOutboxMutationEventsAndOrder(t *testing.T) {
	s, scope := eventStore(t)
	ctx := context.Background()
	date := "2026-10-30"
	rev, err := s.SubmitDraft(ctx, scope, date, "author", eventEdition())
	if err != nil {
		t.Fatal(err)
	}
	rev, err = s.ReviewDraft(ctx, scope, date, rev, "reviewer", "approve", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishDraft(ctx, scope, date, rev, false, "publisher", nil); err != nil {
		t.Fatal(err)
	}
	// Legacy replacement and deadline publication use the same outbox helper.
	// The replacement changes the edition: an identical repeat announces nothing.
	replacement := eventEdition()
	replacement.Sections[0].Items[0].Title = "Corrected title"
	if err = s.Publish(ctx, scope, date, replacement, nil); err != nil {
		t.Fatal(err)
	}
	rev, err = s.SubmitDraft(ctx, scope, "2026-10-31", "worker", eventEdition())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ApplyDeadline(ctx, "daily", "2026-10-31", rev, false, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.AddFeedback(ctx, scope, Feedback{Date: date, ItemID: "one", Kind: "up"}); err != nil {
		t.Fatal(err)
	}
	events, next, err := s.Events(ctx, scope, "", 500)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{webhook.EventDraftReady, webhook.EventReviewRequested, webhook.EventReviewRequested, webhook.EventEditionPublished, webhook.EventEditionPublished, webhook.EventDraftReady, webhook.EventReviewRequested, webhook.EventEditionPublished, webhook.EventFeedbackNew}
	if len(events) != len(want) {
		t.Fatalf("events=%v", events)
	}
	for i, e := range events {
		if e.Type != want[i] || len(e.ID) != 32 || e.Paper != "daily" || e.At.IsZero() {
			t.Fatal(i, e)
		}
	}
	if next != events[len(events)-1].ID {
		t.Fatal(next)
	}
	page, next, err := s.Events(ctx, scope, events[0].ID, 2)
	if err != nil || len(page) != 2 || page[0].ID != events[1].ID || next != events[2].ID {
		t.Fatal(page, next, err)
	}
	page, next, err = s.Events(ctx, scope, events[len(events)-1].ID, 2)
	if err != nil || len(page) != 0 || next != events[len(events)-1].ID {
		t.Fatal(page, next, err)
	}
	if _, _, err = s.Events(ctx, scope, strings.Repeat("0", 32), 10); !errors.Is(err, ErrEventCursor) {
		t.Fatal(err)
	}
	var deliveries int
	if err = s.db.QueryRow("SELECT count(*) FROM deliveries").Scan(&deliveries); err != nil || deliveries != len(want)+1 {
		t.Fatal(deliveries, err)
	}
	var feedback int
	if err = s.db.QueryRow("SELECT count(*) FROM feedback").Scan(&feedback); err != nil || feedback != 1 {
		t.Fatal(feedback, err)
	}
}

func TestOutboxFailureRollsBackMutation(t *testing.T) {
	for _, kind := range []string{webhook.EventDraftReady, webhook.EventReviewRequested, webhook.EventEditionPublished, webhook.EventFeedbackNew} {
		t.Run(kind, func(t *testing.T) {
			s, scope := eventStore(t)
			ctx := context.Background()
			if _, err := s.db.Exec(`CREATE TRIGGER reject_event BEFORE INSERT ON events WHEN NEW.type='` + kind + `' BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case webhook.EventDraftReady, webhook.EventReviewRequested:
				_, err = s.SubmitDraft(ctx, scope, "2026-10-30", "author", eventEdition())
			case webhook.EventEditionPublished:
				err = s.Publish(ctx, scope, "2026-10-30", eventEdition(), nil)
			case webhook.EventFeedbackNew:
				err = s.AddFeedback(ctx, scope, Feedback{Kind: "up"})
			}
			if err == nil {
				t.Fatal("expected rollback")
			}
			for _, table := range []string{"drafts", "draft_actions", "editions", "feedback", "events", "deliveries"} {
				var count int
				if err = s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatal(table, count, err)
				}
			}
		})
	}
}

func TestDeliveryClaimsAndCrashBound(t *testing.T) {
	s, scope := eventStore(t)
	ctx := context.Background()
	if err := s.Publish(ctx, scope, "2026-10-30", eventEdition(), nil); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(time.Second)
	var d Delivery
	for attempt := 1; attempt <= 12; attempt++ {
		var err error
		d, err = s.ClaimDelivery(ctx, at)
		if err != nil || d.Attempts != attempt {
			t.Fatal(attempt, d, err)
		}
		if _, err = s.ClaimDelivery(ctx, at); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("lease not honored", err)
		}
		at = at.Add(time.Minute)
	}
	if _, err := s.ClaimDelivery(ctx, at); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("attempt limit", err)
	}
	var failed sql.NullString
	if err := s.db.QueryRow("SELECT failed_at FROM deliveries").Scan(&failed); err != nil || !failed.Valid {
		t.Fatal("not marked failed", err)
	}
}

func TestFeedbackWakeupsCoalesceByPaperAndMinute(t *testing.T) {
	s, _ := eventStore(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, v := range []struct {
		paper string
		at    time.Time
	}{
		{"daily", at}, {"daily", at.Add(59 * time.Second)}, {"other", at}, {"daily", at.Add(time.Minute)},
	} {
		if err = enqueueEventAtTx(ctx, tx, config.Paper{ID: v.paper}, webhook.EventFeedbackNew, map[string]any{}, v.at); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM events").Scan(&count); err != nil || count != 3 {
		t.Fatal(count, err)
	}
}

func enqueueFeedbackAt(t *testing.T, s *Store, p config.Paper, at time.Time) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = enqueueEventAtTx(ctx, tx, p, webhook.EventFeedbackNew, map[string]any{}, at); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestFeedbackWakeupsAfterObservation(t *testing.T) {
	for _, observation := range []string{"delivered", "in flight", "failed", "poll", "poll without webhooks"} {
		t.Run(observation, func(t *testing.T) {
			s, scope := eventStore(t)
			ctx := context.Background()
			p := s.eventPaper("daily")
			if observation == "poll without webhooks" {
				p.Webhooks = nil
			}
			at := time.Date(2026, 10, 30, 12, 0, 1, 0, time.UTC)
			enqueueFeedbackAt(t, s, p, at)
			enqueueFeedbackAt(t, s, p, at.Add(time.Second))
			var first string
			if err := s.db.QueryRow("SELECT id FROM events").Scan(&first); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(observation, "poll") {
				events, cursor, err := s.Events(ctx, scope, "", 1)
				if err != nil || len(events) != 1 || cursor != first {
					t.Fatal(events, cursor, err)
				}
			} else {
				d, err := s.ClaimDelivery(ctx, at.Add(2*time.Second))
				if err != nil || d.Event.ID != first {
					t.Fatal(d, err)
				}
				if observation != "in flight" {
					if err = s.FinishDelivery(ctx, d, at.Add(3*time.Second), 200, observation == "delivered", false, 0); err != nil {
						t.Fatal(err)
					}
				}
			}
			// The other webhook can still be pending. A new wake-up must also
			// reach the receiver which already processed the first event.
			enqueueFeedbackAt(t, s, p, at.Add(19*time.Second))
			enqueueFeedbackAt(t, s, p, at.Add(29*time.Second))
			events, cursor, err := s.Events(ctx, scope, first, 10)
			if err != nil || len(events) != 1 || cursor == first || events[0].Type != webhook.EventFeedbackNew {
				t.Fatal("feedback lost or pending wake-ups not coalesced", events, cursor, err)
			}
			var count int
			if err = s.db.QueryRow("SELECT count(*) FROM deliveries WHERE event_id=?", cursor).Scan(&count); err != nil || count != len(p.Webhooks) {
				t.Fatal("missing new deliveries", count, err)
			}
		})
	}
}

func TestFeedbackPollingPagination(t *testing.T) {
	s, scope := eventStore(t)
	ctx := context.Background()
	if err := s.Publish(ctx, scope, "2026-10-30", eventEdition(), nil); err != nil {
		t.Fatal(err)
	}
	p := config.Paper{ID: "daily"}
	at := time.Date(2026, 10, 30, 12, 0, 1, 0, time.UTC)
	enqueueFeedbackAt(t, s, p, at)
	events, first, err := s.Events(ctx, scope, "", 1)
	if err != nil || len(events) != 1 || events[0].Type != webhook.EventEditionPublished {
		t.Fatal(events, err)
	}
	// A limited page must leave unread feedback eligible for coalescing.
	enqueueFeedbackAt(t, s, p, at.Add(time.Second))
	events, next, err := s.Events(ctx, scope, first, 10)
	if err != nil || len(events) != 1 || events[0].Type != webhook.EventFeedbackNew {
		t.Fatal(events, err)
	}
	enqueueFeedbackAt(t, s, p, at.Add(19*time.Second))
	events, last, err := s.Events(ctx, scope, next, 10)
	if err != nil || len(events) != 1 || last == next {
		t.Fatal("feedback lost after advancing polling cursor", events, last, err)
	}
}

func TestInterruptedClaimResumesAfterRestart(t *testing.T) {
	s, scope := eventStore(t)
	ctx := context.Background()
	if err := s.Publish(ctx, scope, "2026-10-30", eventEdition(), nil); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(time.Second).Truncate(time.Second)
	first, err := s.ClaimDelivery(ctx, at)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, err := Open(s.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if _, err = resumed.ClaimDelivery(ctx, at.Add(59*time.Second)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("lease lost", err)
	}
	second, err := resumed.ClaimDelivery(ctx, at.Add(time.Minute))
	if err != nil || second.Event.ID != first.Event.ID || second.Attempts != 2 {
		t.Fatal(second, err)
	}
}
