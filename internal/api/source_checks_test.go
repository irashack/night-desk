package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/api"
	"github.com/irashack/night-desk/internal/candidate"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
)

type sourceWarning struct {
	Path string `json:"path"`
	Rule string `json:"rule"`
}

func warningsFrom(t *testing.T, w *httptest.ResponseRecorder) []sourceWarning {
	t.Helper()
	var response struct {
		Warnings []sourceWarning `json:"warnings"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Warnings == nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	return response.Warnings
}
func sourceEdition(t *testing.T, extra bool) string {
	t.Helper()
	why := "There are 44 reasons."
	item := edition.Item{ID: "one", Kind: "news", Title: "There are 42 jobs.", Summary: "There are 43 jobs.", Why: &why, Source: "Desk", URL: "https://example.com/redirect"}
	if extra {
		item.Sources = []edition.Source{{Outlet: "Other", URL: "https://example.com/extra"}}
	}
	body, err := json.Marshal(edition.Edition{Schema: 2, Lead: "one", Sections: []edition.Section{{Key: "economy", Items: []edition.Item{item}}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
func TestSourceWarningsAtSubmissionAndBothPublishRoutes(t *testing.T) {
	for _, route := range []string{"submit", "legacy", "draft-publish"} {
		t.Run(route, func(t *testing.T) {
			a := setup(t, nil)
			a.config.Papers[0].Sources = []config.Source{{Feed: "https://example.com/feed"}}
			c := apiCandidate()
			c.Text = "There are 40 jobs."
			if err := a.db.UpsertCandidates(context.Background(), a.scope, []candidate.Candidate{c}); err != nil {
				t.Fatal(err)
			}
			body := sourceEdition(t, false)
			var w *httptest.ResponseRecorder
			switch route {
			case "submit":
				w = a.request("PUT", "/api/v1/drafts/2026-10-30", body, a.key)
			case "legacy":
				w = a.request("PUT", "/api/v1/editions/2026-10-30", body, a.key)
			case "draft-publish":
				a.config.Papers[0].AgentRole = "reviewer"
				rev := draftRevision(t, a.request("PUT", "/api/v1/drafts/2026-10-30", body, a.key))
				rev = draftRevision(t, a.request("POST", "/api/v1/drafts/2026-10-30/review", fmt.Sprintf(`{"revision":%d,"action":"approve"}`, rev), a.key))
				w = a.request("POST", "/api/v1/drafts/2026-10-30/publish", fmt.Sprintf(`{"revision":%d}`, rev), a.key)
			}
			want := []sourceWarning{{"sections[0].items[0].title", "unsupported-number: 42"}, {"sections[0].items[0].summary", "unsupported-number: 43"}, {"sections[0].items[0].why", "unsupported-number: 44"}}
			if got := warningsFrom(t, w); !reflect.DeepEqual(got, want) {
				t.Fatalf("got %#v, want %#v", got, want)
			}
		})
	}
}
func TestSourceWarningsCombineFurtherCoverageAndAliases(t *testing.T) {
	a := setup(t, nil)
	primary := apiCandidate()
	primary.Text = "There are 42 jobs."
	extra := apiCandidate()
	extra.CanonicalURL = "https://example.com/extra"
	extra.Aliases = nil
	extra.Text = "There are 43 jobs and 44 reasons."
	if err := a.db.UpsertCandidates(context.Background(), a.scope, []candidate.Candidate{primary, extra}); err != nil {
		t.Fatal(err)
	}
	if got := warningsFrom(t, a.request("PUT", "/api/v1/drafts/2026-10-30", sourceEdition(t, true), a.key)); len(got) != 0 {
		t.Fatal(got)
	}
}
func TestUnavailableSourceWarnsWhenAnotherSourceHasText(t *testing.T) {
	a := setup(t, nil)
	c := apiCandidate()
	c.Text = "There are 42 jobs. There are 43 jobs. There are 44 reasons."
	if err := a.db.UpsertCandidates(context.Background(), a.scope, []candidate.Candidate{c}); err != nil {
		t.Fatal(err)
	}
	got := warningsFrom(t, a.request("PUT", "/api/v1/drafts/2026-10-30", sourceEdition(t, true), a.key))
	want := []sourceWarning{{"sections[0].items[0].sources[0].url", "no stored snapshot of this source; claim unverified"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got, want)
	}
}

func TestUnavailableSourcesWarnWithoutConfiguredFeeds(t *testing.T) {
	for _, route := range []string{"/api/v1/drafts/2026-10-30", "/api/v1/editions/2026-10-30"} {
		t.Run(route, func(t *testing.T) {
			a := setup(t, nil)
			// No paper or section feeds are configured: the item's own source was
			// gathered but its fetch failed, and its further source was never
			// gathered. The source warnings must not depend on feeds.
			c := apiCandidate()
			c.State = "unavailable"
			c.Text = ""
			if err := a.db.UpsertCandidates(context.Background(), a.scope, []candidate.Candidate{c}); err != nil {
				t.Fatal(err)
			}
			got := warningsFrom(t, a.request("PUT", route, sourceEdition(t, true), a.key))
			want := []sourceWarning{
				{"sections[0].items[0].url", "source fetch failed; claim unverified"},
				{"sections[0].items[0].sources[0].url", "no stored snapshot of this source; claim unverified"},
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatal(got, want)
			}
		})
	}
}

func TestDeadlineSourceChecksReportCountOnly(t *testing.T) {
	a := setup(t, nil)
	p := a.config.Papers[0]
	p.AgentRole = "none"
	p.Sources = []config.Source{{Feed: "https://example.com/feed"}}
	c := apiCandidate()
	c.Text = "There are 40 jobs."
	if err := a.db.UpsertCandidates(context.Background(), a.scope, []candidate.Candidate{c}); err != nil {
		t.Fatal(err)
	}
	draftRevision(t, a.request("PUT", "/api/v1/drafts/2026-10-30", sourceEdition(t, false), a.key))
	server := &api.Server{Store: a.db, Log: slog.New(slog.NewJSONHandler(&a.log, nil))}
	if err := server.ApplyDeadline(context.Background(), p, "2026-10-30"); err != nil {
		t.Fatal(err)
	}
	log := a.log.String()
	if !strings.Contains(log, `"count":3`) || strings.Contains(log, "There are") || strings.Contains(log, "unsupported-number") {
		t.Fatal(log)
	}
	d, err := a.db.Draft(context.Background(), a.scope, "2026-10-30")
	if err != nil || d.State != "published" || d.Unreviewed {
		t.Fatal(d, err)
	}
}
