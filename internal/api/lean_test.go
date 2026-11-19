package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/api"
	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/keys"
	"github.com/irashack/night-desk/internal/lean"
)

func TestLeanOutletsReadPermission(t *testing.T) {
	a := setup(t, nil)
	a.config.Lean = config.Lean{RatingsFile: "/operator/private/path", Ratings: &lean.Ratings{Source: lean.Source{Attribution: "Attribution"}, Ratings: []lean.Rating{{Outlet: "Example", Domains: []string{"example.com"}, Lean: lean.LeanLeft}}}}
	_, read, err := keys.Issue(context.Background(), a.db, "daily", "reader", auth.Read)
	if err != nil {
		t.Fatal(err)
	}
	_, gather, err := keys.Issue(context.Background(), a.db, "daily", "gather-only", auth.Gather)
	if err != nil {
		t.Fatal(err)
	}
	w := a.request("GET", "/api/v1/lean/outlets", "", read)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"domain":"example.com","lean":"lean-left"`) || strings.Contains(w.Body.String(), "Attribution") || strings.Contains(w.Body.String(), "private") {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, key := range []string{"", gather} {
		w = a.request("GET", "/api/v1/lean/outlets", "", key)
		if w.Code != 401 && w.Code != 403 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	a.config.Lean.Ratings = nil
	w = a.request("GET", "/api/v1/lean/outlets", "", read)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"outlets":[]}` {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestLeanEditorialSwitch(t *testing.T) {
	a, desk := setupEditorial(t)
	p := a.paper(t, "PATCH", "/api/v1/paper", `{"sections":[{"key":"economy","title":"Economy","lean":true},{"key":"health","title":"Health","lean":false}]}`, 200)
	if !p.Sections[0].Lean || p.Sections[1].Lean || !desk.Config().Papers[0].Sections[0].Lean {
		t.Fatal(p)
	}
	p = a.paper(t, "GET", "/api/v1/paper", "", 200)
	if !p.Sections[0].Lean {
		t.Fatal("paper view lost switch")
	}
	for _, value := range []string{`"true"`, `null`, `1`, `{}`} {
		p := a.paper(t, "PATCH", "/api/v1/paper", fmt.Sprintf(`{"sections":[{"key":"economy","title":"Economy","lean":%s}]}`, value), 422)
		if len(p.Errors) != 1 || p.Errors[0].Path != "sections[0].lean" || p.Errors[0].Rule != "must be a boolean" {
			t.Fatal(p.Errors)
		}
	}
}

func TestAllPublicationPathsStoreCoverage(t *testing.T) {
	for _, mode := range []string{"direct", "draft", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			a := setup(t, nil)
			a.config.Lean.Ratings = &lean.Ratings{Source: lean.Source{Attribution: "Original attribution"}, Ratings: []lean.Rating{{Outlet: "Example", Domains: []string{"example.com"}, Lean: lean.Left}}}
			a.config.Papers[0].Sections[0].Lean = true
			if mode == "draft" {
				a.config.Papers[0].AgentRole = "reviewer"
			}
			date := "2026-10-30"
			if mode == "direct" {
				w := a.request("PUT", "/api/v1/editions/"+date, fixture(t), a.key)
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
			} else {
				revision := draftRevision(t, a.request("PUT", "/api/v1/drafts/"+date, fixture(t), a.key))
				if mode == "draft" {
					revision = draftRevision(t, a.request("POST", "/api/v1/drafts/"+date+"/review", fmt.Sprintf(`{"revision":%d,"action":"approve"}`, revision), a.key))
					draftRevision(t, a.request("POST", "/api/v1/drafts/"+date+"/publish", fmt.Sprintf(`{"revision":%d}`, revision), a.key))
				} else {
					p := a.config.Papers[0]
					p.DeadlinePolicy = "publish-unreviewed"
					s := &api.Server{Store: a.db, Config: func() *config.Config { return a.config }}
					if err := s.ApplyDeadline(context.Background(), p, date); err != nil {
						t.Fatal(err)
					}
				}
			}
			got, err := a.db.EditionCoverage(context.Background(), a.scope, date)
			if err != nil || got == nil || len(got.Items) != 1 || got.Items["economy-1"].Attribution != "Original attribution" {
				t.Fatal(got, err)
			}
		})
	}
}

func TestEditionCoverageSurvivesRatingsFileChange(t *testing.T) {
	a := setup(t, nil)
	dir := t.TempDir()
	ratings := &lean.Ratings{Source: lean.Source{Name: "Ratings", URL: "https://ratings.example", License: "CC0", Attribution: "Original attribution"}, Ratings: []lean.Rating{{Outlet: "Example", Domains: []string{"example.com"}, Lean: lean.Left}}}
	path := filepath.Join(dir, "ratings.json")
	load := func() *lean.Ratings {
		t.Helper()
		raw, err := json.Marshal(ratings)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		r, problems, err := lean.Load(file)
		if err != nil || len(problems) > 0 {
			t.Fatal(problems, err)
		}
		return r
	}
	a.config.Lean.Ratings = load()
	a.config.Papers[0].Sections[0].Lean = true
	if w := a.request("PUT", "/api/v1/editions/2026-10-29", fixture(t), a.key); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	before, err := a.db.EditionCoverage(context.Background(), a.scope, "2026-10-29")
	if err != nil || before == nil || before.Items["economy-1"].Left != 1 {
		t.Fatal(before, err)
	}
	ratings.Source.Attribution, ratings.Ratings[0].Lean = "New attribution", lean.Right
	a.config.Lean.Ratings = load()
	if w := a.request("PUT", "/api/v1/editions/2026-10-30", fixture(t), a.key); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	old, err := a.db.EditionCoverage(context.Background(), a.scope, "2026-10-29")
	if err != nil || old.Ratings.Source.Attribution != before.Ratings.Source.Attribution || old.Ratings.Ratings[0].Lean != lean.Left || old.Items["economy-1"] != before.Items["economy-1"] {
		t.Fatal(old, err)
	}
	new, err := a.db.EditionCoverage(context.Background(), a.scope, "2026-10-30")
	if err != nil || new.Ratings.Ratings[0].Lean != lean.Right || new.Ratings.Source.Attribution != "New attribution" || new.Items["economy-1"].Right != 1 || new.Items["economy-1"].Left != 0 {
		t.Fatal(new, err)
	}
}
