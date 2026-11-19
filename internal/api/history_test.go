package api_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/api"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/editorial"
	"github.com/irashack/night-desk/internal/store"
)

func TestPublishRecordsEffectiveLayout(t *testing.T) {
	a, _ := setupEditorial(t)
	a.paper(t, "PATCH", "/api/v1/paper", `{"masthead":"The Edited Gazette","sections":[{"key":"health","title":"Wellbeing"},{"key":"economy","title":"Money"}]}`, 200)
	if w := a.request("PUT", "/api/v1/editions/2026-10-27", fixture(t), a.key); w.Code != 200 {
		t.Fatalf("publish: %d %s", w.Code, w.Body)
	}
	_, layout, err := a.db.EditionWithLayout(context.Background(), a.scope, "2026-10-27")
	want := &store.EditionLayout{Masthead: "The Edited Gazette", Sections: []store.LayoutSection{{Key: "health", Title: "Wellbeing"}, {Key: "economy", Title: "Money"}}}
	if err != nil || !reflect.DeepEqual(layout, want) {
		t.Fatalf("publication layout: %+v %v", layout, err)
	}
}

func TestRejectedRevisionIsReportedAndRetained(t *testing.T) {
	for _, tc := range []struct{ name, body, path, rule string }{
		{"validation", `{"masthead":"Old Gazette","sections":[]}`, "sections", "must contain at least one section"},
		{"decoding", `{"retired_field":true}`, "$", `json: unknown field "retired_field"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := setup(t, nil)
			a.config.Papers[0].Masthead = "The Declaration"
			if _, err := a.db.SaveEditorial(context.Background(), a.scope, json.RawMessage(tc.body)); err != nil {
				t.Fatal(err)
			}
			desk, err := editorial.Open(context.Background(), a.db, func() *config.Config { return a.config }, slog.New(slog.NewJSONHandler(&a.log, nil)))
			if err != nil {
				t.Fatal(err)
			}
			a.handler = (&api.Server{Store: a.db, Config: desk.Config, Editorial: desk}).Handler()
			for range 3 {
				w := a.request("GET", "/api/v1/paper", "", a.key)
				var p struct {
					Masthead string
					Revision int64
					Edited   bool
					Problems []struct{ Path, Rule string } `json:"revision_problems"`
				}
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil {
					t.Fatalf("paper: %d %s", w.Code, w.Body)
				}
				if p.Masthead != "The Declaration" || p.Revision != 1 || p.Edited || len(p.Problems) != 1 || p.Problems[0].Path != tc.path || p.Problems[0].Rule != tc.rule {
					t.Fatalf("paper: %+v", p)
				}
			}
			if strings.Count(a.log.String(), "stored paper editorial rejected") != 1 {
				t.Fatalf("expected one log: %s", a.log.String())
			}
			if problems := desk.RevisionProblems("other"); len(problems) != 0 {
				t.Fatalf("problems leaked: %v", problems)
			}
			problems := desk.RevisionProblems(a.scope.PaperID())
			problems[0] = "changed"
			if desk.RevisionProblems(a.scope.PaperID())[0] == "changed" {
				t.Fatal("problems expose mutable state")
			}
			kept, err := a.db.EditorialRevision(context.Background(), a.scope, 1)
			if err != nil || string(kept.Body) != tc.body {
				t.Fatalf("lost revision: %+v %v", kept, err)
			}
			if w := a.request("POST", "/api/v1/paper/restore", `{"revision":1}`, a.key); w.Code != 422 {
				t.Fatalf("restore invalid: %d %s", w.Code, w.Body)
			}
			a.paper(t, "POST", "/api/v1/paper/restore", `{"revision":0}`, 200)
			if w := a.request("GET", "/api/v1/paper", "", a.key); strings.Contains(w.Body.String(), "revision_problems") {
				t.Fatalf("problems not cleared: %s", w.Body)
			}
		})
	}
}
