package store

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/edition"
)

func TestDeadlineIgnoresStaleApproval(t *testing.T) {
	for _, policy := range []string{"hold", "publish-unreviewed"} {
		t.Run(policy, func(t *testing.T) {
			ctx := context.Background()
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err = s.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			// Model a retained approval for an older revision, rather than NULL.
			body := `{"schema":1,"lead":"one","sections":[{"key":"news","items":[{"id":"one","kind":"news","title":"Headline","summary":"Summary","source":"Desk","url":"https://example.com/story"}]}]}`
			if _, err = s.db.ExecContext(ctx, "INSERT INTO drafts(paper_id,date,state,revision,body,approved_revision,updated_at) VALUES (?,?,'in_review',2,?,1,?)", "daily", "2026-10-30", body, now()); err != nil {
				t.Fatal(err)
			}
			if err = s.ApplyDeadlinePolicy(ctx, "daily", "2026-10-30", 2, policy == "hold", true, nil); err != nil {
				t.Fatal(err)
			}
			wantState, wantAction := "held", "hold"
			if policy == "publish-unreviewed" {
				wantState, wantAction = "published", "publish-unreviewed"
			}
			d, err := s.RunDraft(ctx, "daily", "2026-10-30")
			if err != nil || d.State != wantState || d.Unreviewed != (policy == "publish-unreviewed") {
				t.Fatal(d, err)
			}
			var action string
			if err = s.db.QueryRowContext(ctx, "SELECT action FROM draft_actions WHERE paper_id=? AND date=? AND revision=3", "daily", "2026-10-30").Scan(&action); err != nil || action != wantAction {
				t.Fatalf("action=%q, want %q: %v", action, wantAction, err)
			}
		})
	}
}

func TestDraftPublishRollsBackAllWrites(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	key := "nd_" + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	if err = s.InsertKey(ctx, "atomic-test", "daily", "", sha256.Sum256([]byte(key))); err != nil {
		t.Fatal(err)
	}
	scope, err := auth.Authenticate(ctx, s, key)
	if err != nil {
		t.Fatal(err)
	}
	ed := &edition.Edition{Schema: 1, Lead: "one", Sections: []edition.Section{{Key: "news", Items: []edition.Item{{ID: "one", Kind: "news", Title: "Headline", Summary: "Summary", Source: "Desk", URL: "https://example.com/story"}}}}}
	rev, err := s.SubmitDraft(ctx, scope, "2026-10-30", "author", ed)
	if err != nil {
		t.Fatal(err)
	}
	// Fail the final action-log insertion, after the edition and images were written.
	if _, err = s.db.ExecContext(ctx, `CREATE TRIGGER fail_publish BEFORE INSERT ON draft_actions WHEN NEW.action='publish' BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err = s.PublishDraft(ctx, scope, "2026-10-30", rev, true, "author", map[string]Image{"one": {SHA256: strings.Repeat("a", 64), Width: 1, Height: 1, Bytes: 10}})
	if err == nil {
		t.Fatal("expected failed transaction")
	}
	for _, table := range []string{"editions", "edition_images", "images", "edition_review"} {
		var count int
		if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatal(table, count, err)
		}
	}
	d, err := s.Draft(ctx, scope, "2026-10-30")
	if err != nil || d.State != "drafted" || d.Revision != rev || len(d.Actions) != 1 {
		t.Fatal(d, err)
	}
}
