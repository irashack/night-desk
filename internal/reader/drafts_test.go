package reader

import (
	"context"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/edition"
)

func TestUnreviewedEditionNote(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	date := "2026-10-30"
	ed := &edition.Edition{Schema: 1, Lead: "one", Sections: []edition.Section{{Key: "economy", Items: []edition.Item{{ID: "one", Kind: "news", Title: "Headline", Summary: "Summary", Source: "Desk", URL: "https://example.com/story"}}}}}
	rev, err := f.db.SubmitDraft(ctx, f.a, date, "author", ed)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.ApplyDeadline(ctx, "a", date, rev, false, nil); err != nil {
		t.Fatal(err)
	}
	response := f.signIn(t, "")
	response.Body.Close()
	response = f.get(t, "/e/"+date)
	text := body(t, response)
	if response.StatusCode != 200 || !strings.Contains(text, `<p class="unreviewed">Unreviewed</p>`) {
		t.Fatal(response.StatusCode, text)
	}
	// The idempotent author route can replace the edition; the old marker clears.
	if err = f.db.Publish(ctx, f.a, date, ed, nil); err != nil {
		t.Fatal(err)
	}
	response = f.get(t, "/e/"+date)
	text = body(t, response)
	if response.StatusCode != 200 || strings.Contains(text, ">Unreviewed</p>") {
		t.Fatal(response.StatusCode, text)
	}
}
