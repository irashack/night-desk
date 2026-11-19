package store_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/store"
)

func qualityTestItemV04(id string) edition.Item {
	return edition.Item{ID: id, Kind: "news", Title: "title", Summary: "summary", Source: "source", URL: "https://example.com/" + id}
}

func qualityTestBodyV04(news, opinion int) *edition.Edition {
	body := &edition.Edition{Schema: 1, Lead: "news-1", Sections: []edition.Section{{Key: "news", Items: []edition.Item{}}}}
	for i := 0; i < news; i++ {
		body.Sections[0].Items = append(body.Sections[0].Items, qualityTestItemV04("news-"+string(rune('1'+i))))
	}
	if opinion > 0 {
		section := edition.Section{Key: "opinion", Items: []edition.Item{}}
		for i := 0; i < opinion; i++ {
			item := qualityTestItemV04("opinion-" + string(rune('1'+i)))
			item.Kind = "opinion"
			section.Items = append(section.Items, item)
		}
		body.Sections = append(body.Sections, section)
	}
	return body
}

func TestOperatorQualityWindowAggregates(t *testing.T) {
	s, dir := open(t)
	a := scope(t, s, "daily")

	// 2026-10-20 has an empty required opinion section.
	if err := s.Publish(ctx, a, "2026-10-20", qualityTestBodyV04(1, 0), nil); err != nil {
		t.Fatal(err)
	}
	// 2026-10-21 has an empty required news section and no opinion section.
	if err := s.Publish(ctx, a, "2026-10-21", qualityTestBodyV04(0, 0), nil); err != nil {
		t.Fatal(err)
	}
	// A deadline publication marks 2026-10-22 unreviewed.
	rev, err := s.SubmitDraft(ctx, a, "2026-10-22", "writer", qualityTestBodyV04(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ApplyDeadline(ctx, "daily", "2026-10-22", rev, false, nil); err != nil {
		t.Fatal(err)
	}
	// 2026-10-23 is held with a draft; 2026-10-24 is a held run without one.
	if err = s.StartDraft(ctx, a, "2026-10-23", "writer"); err != nil {
		t.Fatal(err)
	}
	if err = s.ApplyDeadline(ctx, "daily", "2026-10-23", 1, true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.EnsureRun(ctx, "daily", "2026-10-24"); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishRun(ctx, "daily", "2026-10-24", "held"); err != nil {
		t.Fatal(err)
	}
	// A completed review on 2026-10-25; its submit timestamp is moved ten
	// minutes back to give the turnaround a non-zero median.
	if err = s.StartDraft(ctx, a, "2026-10-25", "writer"); err != nil {
		t.Fatal(err)
	}
	rev, err = s.SubmitDraft(ctx, a, "2026-10-25", "writer", qualityTestBodyV04(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReviewDraft(ctx, a, "2026-10-25", rev, "reviewer", "approve", "", nil); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "nightdesk.db"))
	if err != nil {
		t.Fatal(err)
	}
	submittedAt := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339Nano)
	if _, err = db.Exec(`UPDATE draft_actions SET at=? WHERE paper_id=? AND date=? AND action='submit'`, submittedAt, "daily", "2026-10-25"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	for _, f := range []store.Feedback{
		{Date: "2026-10-20", ItemID: "news-1", Kind: "up", ReaderIssuer: "https://id.example", ReaderSubject: "secret-reader"},
		{Date: "2026-10-20", ItemID: "news-1", Kind: "up", ReaderIssuer: "https://id.example", ReaderSubject: "other-reader"},
		{Date: "2026-10-20", ItemID: "news-1", Kind: "down", ReaderIssuer: "https://id.example", ReaderSubject: "secret-reader"},
		{Date: "2026-10-20", ItemID: "news-1", Kind: "save", ReaderIssuer: "https://id.example", ReaderSubject: "secret-reader"},
		{Date: "2026-10-21", ItemID: "news-1", Kind: "save", ReaderIssuer: "https://id.example", ReaderSubject: "secret-reader"},
		{Date: "2026-10-21", ItemID: "news-1", Kind: "unsave", ReaderIssuer: "https://id.example", ReaderSubject: "secret-reader"},
		{Date: "2026-10-01", ItemID: "news-1", Kind: "up", ReaderIssuer: "https://id.example", ReaderSubject: "secret-reader"},
	} {
		if err = s.AddFeedback(ctx, a, f); err != nil {
			t.Fatal(err)
		}
	}

	data, err := s.Quality(ctx, "daily", "2026-10-20", "2026-10-25", []string{"news", "opinion"})
	if err != nil {
		t.Fatal(err)
	}
	if data.Start != "2026-10-20" || data.End != "2026-10-25" {
		t.Fatal(data.Start, data.End)
	}
	if data.Published != 3 || len(data.Editions) != 3 {
		t.Fatal("published", data.Published, data.Editions)
	}
	if data.Unreviewed != 1 {
		t.Fatal("unreviewed", data.Unreviewed)
	}
	if data.Held != 2 {
		t.Fatal("held", data.Held)
	}
	if data.EmptyRequiredSections != 3 {
		t.Fatal("empty required sections", data.EmptyRequiredSections)
	}
	if data.ReviewSamples != 1 || data.ReviewMedianMinutes < 9.9 || data.ReviewMedianMinutes > 10.1 {
		t.Fatal("turnaround", data.ReviewSamples, data.ReviewMedianMinutes)
	}
	if len(data.Feedback) != 1 || data.Feedback[0] != (store.FeedbackWeek{Week: "2026-10-19", Up: 2, Down: 1, Saves: 2}) {
		t.Fatal("feedback", data.Feedback)
	}

	// Another paper in the same window contributes nothing.
	other := scope(t, s, "other")
	if err = s.Publish(ctx, other, "2026-10-20", qualityTestBodyV04(1, 1), nil); err != nil {
		t.Fatal(err)
	}
	if err = s.AddFeedback(ctx, other, store.Feedback{Date: "2026-10-20", ItemID: "news-1", Kind: "up", ReaderIssuer: "https://id.example", ReaderSubject: "secret-reader"}); err != nil {
		t.Fatal(err)
	}
	empty, err := s.Quality(ctx, "other", "2026-10-26", "2026-10-27", []string{"news"})
	if err != nil || empty.Published != 0 || empty.Held != 0 || empty.Unreviewed != 0 || len(empty.Feedback) != 0 {
		t.Fatal("other paper", empty, err)
	}

	for _, tt := range []struct {
		paper, start, end string
	}{
		{"", "2026-10-20", "2026-10-25"},
		{"daily", "bad", "2026-10-25"},
		{"daily", "2026-10-25", "2026-10-20"},
	} {
		if _, err = s.Quality(ctx, tt.paper, tt.start, tt.end, nil); err == nil {
			t.Fatal("invalid window accepted", tt)
		}
	}
}

func TestOperatorQualityEmptyWindow(t *testing.T) {
	s, _ := open(t)
	data, err := s.Quality(ctx, "quiet", "2026-10-20", "2026-10-21", []string{"news"})
	if err != nil {
		t.Fatal(err)
	}
	if data.Feedback == nil || len(data.Feedback) != 0 || data.ReviewSamples != 0 || data.ReviewMedianMinutes != 0 {
		t.Fatal(data)
	}
}
