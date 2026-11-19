package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/api"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/editorial"
)

func setupEditorial(t *testing.T) (*app, *editorial.Desk) {
	t.Helper()
	a := setup(t, nil)
	a.config.Papers[0].Masthead = "The Daily"
	desk, e := editorial.Open(context.Background(), a.db, func() *config.Config { return a.config }, nil)
	if e != nil {
		t.Fatal(e)
	}
	a.handler = (&api.Server{Store: a.db, Config: desk.Config, Log: slog.New(slog.NewJSONHandler(&a.log, nil)), Editorial: desk}).Handler()
	return a, desk
}

type paperResponse struct {
	Masthead string           `json:"masthead"`
	Brief    *string          `json:"brief"`
	Sections []config.Section `json:"sections"`
	Boxes    config.Boxes     `json:"boxes"`
	Revision int64            `json:"revision"`
	Edited   bool             `json:"edited"`
	Errors   []struct{ Path, Rule string }
}

func (a *app) paper(t *testing.T, method, path, body string, want int) paperResponse {
	t.Helper()
	w := a.request(method, path, body, a.key)
	if w.Code != want {
		t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
	}
	var p paperResponse
	if e := json.Unmarshal(w.Body.Bytes(), &p); e != nil {
		t.Fatal(e)
	}
	return p
}

func TestAgentEditsThePaper(t *testing.T) {
	a, desk := setupEditorial(t)
	if p := a.paper(t, "GET", "/api/v1/paper", "", 200); p.Revision != 0 || p.Edited || len(p.Sections) != 2 {
		t.Fatalf("declared paper: %+v", p)
	}
	p := a.paper(t, "PATCH", "/api/v1/paper", `{"brief":"Prefer local reporting.","boxes":{"sun":true},
		"sections":[{"key":"economy","title":"Economy","required":true},{"key":"health","title":"Health"},
		{"key":"river-levels","title":"River Levels","brief":"Gauge readings near Riverside."}]}`, 200)
	if p.Revision != 1 || !p.Edited || len(p.Sections) != 3 || *p.Brief != "Prefer local reporting." || !p.Boxes.Weather || !p.Boxes.Sun {
		t.Fatalf("edited paper: %+v", p)
	}
	// The effective paper governs edition rules and the reader pages too.
	if got, _ := desk.Config().Paper("daily"); len(got.Rules().Sections) != 3 || a.config.Papers[0].Brief != nil {
		t.Fatalf("effective %+v; declaration must stay unchanged", got)
	}
	if p := a.paper(t, "PATCH", "/api/v1/paper", `{"brief":null,"masthead":"The Evening"}`, 200); p.Brief != nil || p.Masthead != "The Evening" || len(p.Sections) != 3 || p.Revision != 2 {
		t.Fatalf("second edit: %+v", p)
	}

	var revs struct {
		Revisions []struct {
			Revision  int64           `json:"revision"`
			Editorial json.RawMessage `json:"editorial"`
		} `json:"revisions"`
	}
	w := a.request("GET", "/api/v1/paper/revisions", "", a.key)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &revs) != nil || len(revs.Revisions) != 2 || revs.Revisions[0].Revision != 2 {
		t.Fatalf("revisions: %d %s", w.Code, w.Body)
	}

	if p := a.paper(t, "POST", "/api/v1/paper/restore", `{"revision":0}`, 200); p.Edited || p.Revision != 3 || len(p.Sections) != 2 || p.Masthead != "The Daily" {
		t.Fatalf("restore declaration: %+v", p)
	}
	if p := a.paper(t, "POST", "/api/v1/paper/restore", `{"revision":1}`, 200); !p.Edited || p.Revision != 4 || len(p.Sections) != 3 || p.Masthead != "The Daily" {
		t.Fatalf("restore revision 1: %+v", p)
	}
	if w := a.request("POST", "/api/v1/paper/restore", `{"revision":99}`, a.key); w.Code != 404 {
		t.Fatalf("missing revision: %d %s", w.Code, w.Body)
	}

	// A restart reloads the latest revision.
	again, e := editorial.Open(context.Background(), a.db, func() *config.Config { return a.config }, nil)
	if e != nil {
		t.Fatal(e)
	}
	if got, _ := again.Config().Paper("daily"); len(got.Sections) != 3 {
		t.Fatalf("reloaded: %+v", got.Sections)
	}
	if rev, edited := again.Revision("daily"); rev != 4 || !edited {
		t.Fatalf("reloaded revision %d %v", rev, edited)
	}
}

func TestPaperEditsAreValidated(t *testing.T) {
	a, _ := setupEditorial(t)
	many := []string{}
	for i := range config.MaxSections + 1 {
		many = append(many, fmt.Sprintf(`{"key":"s%d","title":"S%d"}`, i, i))
	}
	for _, c := range []struct{ body, path string }{
		{`{"sections":[` + strings.Join(many, ",") + `]}`, "sections"},
		{`{"sections":[]}`, "sections"},
		{`{"sections":[{"key":"a","title":"A"},{"key":"a","title":"B"}]}`, "sections[1].key"},
		{`{"sections":[{"key":"a","title":"A","color":"red"}]}`, "sections"},
		{`{"brief":"<b>bold</b>"}`, "brief"},
		{`{"brief":"see [this](https://example.com)"}`, "brief"},
		{`{"deadline":"7am"}`, "deadline"},
		{`{"masthead":""}`, "masthead"},
		{`{"masthead":null}`, "masthead"},
		{`{"timezone":"UTC"}`, "timezone"},
		{`{"boxes":{"tides":true}}`, "boxes.tides"},
		{`[1]`, "$"},
	} {
		p := a.paper(t, "PATCH", "/api/v1/paper", c.body, 422)
		if len(p.Errors) == 0 || p.Errors[0].Path != c.path {
			t.Errorf("%s: %+v", c.body, p.Errors)
		}
	}
	if p := a.paper(t, "GET", "/api/v1/paper", "", 200); p.Revision != 0 {
		t.Fatalf("a rejected edit saved revision %d", p.Revision)
	}
	if w := a.request("POST", "/api/v1/paper/restore", `{}`, a.key); w.Code != 422 {
		t.Fatalf("restore without revision: %d", w.Code)
	}
	if w := a.request("DELETE", "/api/v1/paper/revisions", "", a.key); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method: %d", w.Code)
	}
	if w := a.request("PATCH", "/api/v1/paper", `{}`, ""); w.Code != 401 {
		t.Fatalf("unauthenticated edit: %d", w.Code)
	}
}
