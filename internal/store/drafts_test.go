package store_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/store"
)

func draftEdition() *edition.Edition {
	return &edition.Edition{Schema: 1, Lead: "one", Sections: []edition.Section{{Key: "news", Items: []edition.Item{{ID: "one", Kind: "news", Title: "Headline", Summary: "Summary", Source: "Desk", URL: "https://example.com/story"}}}}}
}
func TestDraftStateAndApproval(t *testing.T) {
	s, _ := open(t)
	a := scope(t, s, "a")
	b := scope(t, s, "b")
	date := "2026-10-30"
	if err := s.StartDraft(ctx, a, date, "worker"); err != nil {
		t.Fatal(err)
	}
	d, err := s.Draft(ctx, a, date)
	if err != nil || d.State != "gathering" || d.Revision != 1 || d.Body != nil {
		t.Fatal(d, err)
	}
	rev, err := s.SubmitDraft(ctx, a, date, "author", draftEdition())
	if err != nil || rev != 2 {
		t.Fatal(rev, err)
	}
	if _, err = s.Draft(ctx, b, date); err == nil {
		t.Fatal("draft crossed paper scope")
	}
	if _, err = s.PublishDraft(ctx, a, date, rev, false, "reviewer", nil); !errors.Is(err, store.ErrDraftConflict) {
		t.Fatal("unapproved published", err)
	}
	rev, err = s.ReviewDraft(ctx, a, date, rev, "reviewer", "approve", "", nil)
	if err != nil || rev != 3 {
		t.Fatal(rev, err)
	}
	d, err = s.Draft(ctx, a, date)
	if err != nil || d.State != "in_review" || d.ApprovedRevision == nil || *d.ApprovedRevision != rev {
		t.Fatal(d, err)
	}
	if _, err = s.ReviewDraft(ctx, a, date, rev-1, "reviewer", "note", "late", nil); !errors.Is(err, store.ErrDraftConflict) {
		t.Fatal(err)
	}
	rev, err = s.ReviewDraft(ctx, a, date, rev, "reviewer", "note", "Please check again.", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishDraft(ctx, a, date, rev, false, "reviewer", nil); !errors.Is(err, store.ErrDraftConflict) {
		t.Fatal("note did not invalidate approval", err)
	}
	rev, err = s.ReviewDraft(ctx, a, date, rev, "reviewer", "send_back", "Find a primary source.", nil)
	if err != nil {
		t.Fatal(err)
	}
	d, _ = s.Draft(ctx, a, date)
	if d.State != "drafted" {
		t.Fatal(d)
	}
	rev, err = s.SubmitDraft(ctx, a, date, "author", draftEdition())
	if err != nil {
		t.Fatal(err)
	}
	rev, err = s.ReviewDraft(ctx, a, date, rev, "reviewer", "approve", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	published, err := s.PublishDraft(ctx, a, date, rev, false, "publisher", nil)
	if err != nil || published != rev+1 {
		t.Fatal(published, err)
	}
	d, err = s.Draft(ctx, a, date)
	if err != nil || d.State != "published" || d.Revision != published || len(d.Actions) != int(published) {
		t.Fatal(d, err)
	}
	for i, action := range d.Actions {
		if action.Revision != int64(i+1) || action.Who == "" || action.At == "" {
			t.Fatal(action)
		}
	}
	if _, err = s.SubmitDraft(ctx, a, date, "late", draftEdition()); !errors.Is(err, store.ErrDraftConflict) {
		t.Fatal(err)
	}
	if _, err = s.ReviewDraft(ctx, a, date, published, "late", "approve", "", nil); !errors.Is(err, store.ErrDraftConflict) {
		t.Fatal(err)
	}
	if _, err = s.Drafts(ctx, auth.PaperScope{}, ""); err == nil {
		t.Fatal("invalid scope accepted")
	}
}
func TestDraftCannotReplaceEdition(t *testing.T) {
	s, _ := open(t)
	a := scope(t, s, "a")
	date := "2026-10-30"
	rev, err := s.SubmitDraft(ctx, a, date, "author", draftEdition())
	if err != nil {
		t.Fatal(err)
	}
	newer := draftEdition()
	newer.Sections[0].Items[0].Title = "Newer edition"
	if err = s.Publish(ctx, a, date, newer, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishDraft(ctx, a, date, rev, true, "late", nil); !errors.Is(err, store.ErrDraftConflict) {
		t.Fatal("overwrote edition", err)
	}
	got, err := s.Edition(ctx, a, date)
	if err != nil || got.Sections[0].Items[0].Title != "Newer edition" {
		t.Fatal(got, err)
	}
}
func TestDraftReplacementClearsApprovalAndHeldAuthorCanPublish(t *testing.T) {
	s, _ := open(t)
	a := scope(t, s, "a")
	date := "2026-10-30"
	rev, err := s.SubmitDraft(ctx, a, date, "author", draftEdition())
	if err != nil {
		t.Fatal(err)
	}
	rev, err = s.ReviewDraft(ctx, a, date, rev, "reviewer", "approve", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	rev, err = s.SubmitDraft(ctx, a, date, "author", draftEdition())
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Draft(ctx, a, date)
	if err != nil || d.ApprovedRevision != nil {
		t.Fatal(d, err)
	}
	if err = s.ApplyDeadline(ctx, "a", date, rev, true, nil); err != nil {
		t.Fatal(err)
	}
	d, err = s.Draft(ctx, a, date)
	if err != nil || d.State != "held" || d.Revision != rev+1 {
		t.Fatal(d, err)
	}
	if _, err = s.PublishDraft(ctx, a, date, d.Revision, true, "late", nil); err != nil {
		t.Fatal(err)
	}
}

func TestDeadlineApprovalPolicies(t *testing.T) {
	for _, method := range []string{"ApplyDeadline", "ApplyDeadlinePolicy"} {
		for _, hold := range []bool{false, true} {
			for _, approved := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/hold=%t/approved=%t", method, hold, approved), func(t *testing.T) {
					s, _ := open(t)
					a := scope(t, s, "a")
					const date = "2026-10-30"
					rev, err := s.SubmitDraft(ctx, a, date, "author", draftEdition())
					if err != nil {
						t.Fatal(err)
					}
					if approved {
						rev, err = s.ReviewDraft(ctx, a, date, rev, "reviewer", "approve", "", nil)
						if err != nil {
							t.Fatal(err)
						}
					}
					if method == "ApplyDeadline" {
						err = s.ApplyDeadline(ctx, "a", date, rev, hold, nil)
					} else {
						err = s.ApplyDeadlinePolicy(ctx, "a", date, rev, hold, true, nil)
					}
					if err != nil {
						t.Fatal(err)
					}
					wantState, wantAction := "published", "publish"
					wantUnreviewed := !approved && !hold
					if hold && !approved {
						wantState, wantAction = "held", "hold"
					} else if wantUnreviewed {
						wantAction = "publish-unreviewed"
					}
					d, err := s.Draft(ctx, a, date)
					if err != nil || d.State != wantState || d.Unreviewed != wantUnreviewed || d.Revision != rev+1 || d.Actions[len(d.Actions)-1].Action != wantAction {
						t.Fatal(d, err)
					}
					if wantState == "published" {
						flag, err := s.EditionUnreviewed(ctx, a, date)
						if err != nil || flag != wantUnreviewed {
							t.Fatal(flag, err)
						}
					}
				})
			}
		}
	}
}

func TestHeldReviewerDraftRequiresCurrentApproval(t *testing.T) {
	s, _ := open(t)
	a := scope(t, s, "a")
	const date = "2026-10-30"
	rev, err := s.SubmitDraft(ctx, a, date, "author", draftEdition())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ApplyDeadline(ctx, "a", date, rev, true, nil); err != nil {
		t.Fatal(err)
	}
	d, err := s.Draft(ctx, a, date)
	if err != nil || d.State != "held" {
		t.Fatal(d, err)
	}
	if _, err = s.PublishDraft(ctx, a, date, d.Revision, false, "publisher", nil); !errors.Is(err, store.ErrDraftConflict) {
		t.Fatal("unapproved held draft published", err)
	}
	rev, err = s.ReviewDraft(ctx, a, date, d.Revision, "reviewer", "approve", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// A further review invalidates approval, including on a held draft.
	rev, err = s.ReviewDraft(ctx, a, date, rev, "reviewer", "note", "Check again.", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishDraft(ctx, a, date, rev, false, "publisher", nil); !errors.Is(err, store.ErrDraftConflict) {
		t.Fatal("stale approval published held draft", err)
	}
	rev, err = s.ReviewDraft(ctx, a, date, rev, "reviewer", "approve", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ApplyDeadline(ctx, "a", date, rev, false, nil); !errors.Is(err, store.ErrDraftConflict) {
		t.Fatal("deadline republished held draft", err)
	}
	if _, err = s.PublishDraft(ctx, a, date, rev-1, false, "publisher", nil); !errors.Is(err, store.ErrDraftConflict) {
		t.Fatal("stale revision published held draft", err)
	}
	if _, err = s.PublishDraft(ctx, a, date, rev, false, "publisher", nil); err != nil {
		t.Fatal(err)
	}
	d, err = s.Draft(ctx, a, date)
	if err != nil || d.State != "published" || d.Unreviewed {
		t.Fatal(d, err)
	}
}
func TestNotesHistoryAndIsolation(t *testing.T) {
	s, _ := open(t)
	a := scope(t, s, "a")
	b := scope(t, s, "b")
	for i := int64(1); i <= 55; i++ {
		n, err := s.SaveReaderNotes(ctx, a, "Local news.")
		if err != nil || n.Revision != i {
			t.Fatal(n, err)
		}
	}
	n, err := s.ReaderNotes(ctx, b)
	if err != nil || n.Text != "" || n.Revision != 0 {
		t.Fatal(n, err)
	}
	n, err = s.ReaderNotes(ctx, a)
	if err != nil || n.Text != "Local news." || n.Revision != 55 {
		t.Fatal(n, err)
	}
}
