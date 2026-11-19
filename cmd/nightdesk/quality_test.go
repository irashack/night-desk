package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/keys"
	"github.com/irashack/night-desk/internal/store"
)

func TestOperatorQualityCommandCountsAndPrivacy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nightdesk.yaml")
	configFile := `server:
  listen: "127.0.0.1:8080"
  public_base_url: https://paper.example
  data_dir: data
  oidc:
    issuer: https://id.example
    client_id: nightdesk
    client_secret_file: secret-file
papers:
- id: daily
  masthead: The Daily
  look: broadsheet
  timezone: UTC
  latitude: 41
  longitude: -87
  deadline: "23:59"
  sections:
  - key: news
    title: News
    required: true
`
	if err := os.WriteFile(path, []byte(configFile), 0600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"migrate", "--config", path}, new(bytes.Buffer), new(bytes.Buffer)); code != 0 {
		t.Fatal(code)
	}

	db, err := store.Open(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := keys.Issue(context.Background(), db, "daily", "test")
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	paper, err := auth.Authenticate(context.Background(), db, key)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	today := time.Now().UTC().Format("2006-01-02")
	body := &edition.Edition{Schema: 1, Lead: "one", Sections: []edition.Section{{Key: "news", Items: []edition.Item{{
		ID: "one", Kind: "news", Title: "SECRET TITLE", Summary: "SECRET SUMMARY", Source: "source", URL: "https://example.com/one",
	}}}}}
	if err = db.Publish(context.Background(), paper, today, body, nil); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.AddFeedback(context.Background(), paper, store.Feedback{Date: today, ItemID: "one", Kind: "up", ReaderIssuer: "https://id.example", ReaderSubject: "reader-secret"}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	var out, errOut bytes.Buffer
	if code := run([]string{"quality", "--config", path, "--json"}, &out, &errOut); code != 0 {
		t.Fatal(code, errOut.String())
	}
	if strings.Contains(out.String(), "SECRET TITLE") || strings.Contains(out.String(), "SECRET SUMMARY") || strings.Contains(out.String(), "reader-secret") {
		t.Fatal("quality JSON leaked reader content", out.String())
	}
	var report qualityReport
	if err = json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Papers) != 1 {
		t.Fatal(report)
	}
	p := report.Papers[0]
	if p.PaperID != "daily" || p.Days != 14 || p.Published != 1 || p.DeadlinesMet != 1 || p.DeadlinesChecked != 1 {
		t.Fatal(p)
	}
	if p.Unreviewed != 0 || p.Held != 0 || p.EmptyRequiredSections != 0 || p.ReviewTurnaroundSamples != 0 {
		t.Fatal(p)
	}
	if len(p.FeedbackTrend) != 1 || p.FeedbackTrend[0].Up != 1 || p.FeedbackTrend[0].Down != 0 || p.FeedbackTrend[0].Saves != 0 {
		t.Fatal(p.FeedbackTrend)
	}

	out.Reset()
	errOut.Reset()
	if code := run([]string{"quality", "--config", path, "--paper", "daily", "--days", "1"}, &out, &errOut); code != 0 {
		t.Fatal(code, errOut.String())
	}
	text := out.String()
	if strings.Contains(text, "SECRET TITLE") || strings.Contains(text, "SECRET SUMMARY") || strings.Contains(text, "reader-secret") {
		t.Fatal("quality table leaked reader content", text)
	}
	for _, want := range []string{"PAPER", "PUBLISHED/DAYS", "DEADLINES MET/CHECKED", "FEEDBACK", "daily"} {
		if !strings.Contains(text, want) {
			t.Fatalf("quality table missing %q: %s", want, text)
		}
	}

	if code := run([]string{"quality", "--config", path, "--paper", "missing"}, new(bytes.Buffer), new(bytes.Buffer)); code != 1 {
		t.Fatal("unknown paper accepted", code)
	}
	for _, args := range [][]string{
		{"quality", "--config", path, "--days", "0"},
		{"quality", "--config", path, "--days", "bad"},
		{"quality", "--config", path, "extra"},
	} {
		if code := run(args, new(bytes.Buffer), new(bytes.Buffer)); code != 2 {
			t.Fatal("bad quality arguments accepted", args, code)
		}
	}
}

func TestOperatorQualityDeadlineComparison(t *testing.T) {
	deadline := "06:30"
	p := config.Paper{Deadline: &deadline, Timezone: "America/Chicago"}
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		t.Fatal(err)
	}
	editions := []store.QualityEdition{
		{Date: "2026-10-20", PublishedAt: "2026-10-20T11:29:00Z"}, // 06:29 CDT
		{Date: "2026-10-20", PublishedAt: "2026-10-20T11:31:00Z"}, // 06:31 CDT
		{Date: "2026-03-08", PublishedAt: "2026-03-08T11:29:00Z"}, // 06:29 CDT after the spring change
	}
	met, checked, err := deadlinesMet(p, loc, editions)
	if err != nil || met != 2 || checked != 3 {
		t.Fatal(met, checked, err)
	}
	if met, checked, err = deadlinesMet(config.Paper{Timezone: "UTC"}, time.UTC, editions); err != nil || met != 0 || checked != 0 {
		t.Fatal("paper without a deadline", met, checked, err)
	}
}
