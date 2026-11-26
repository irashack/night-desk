package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

// TestPaperEditsDoNotLoseUpdates reproduces issue #32: two edits computed from
// the same paper revision must not silently overwrite one another.
func TestPaperEditsDoNotLoseUpdates(t *testing.T) {
	a, desk := setupEditorial(t)
	stale := desk.Config() // the effective paper as a slow request authenticated it

	w := a.request("GET", "/api/v1/paper", "", a.key)
	if w.Code != 200 || w.Header().Get("ETag") != `"0"` {
		t.Fatalf("GET paper: %d ETag %q", w.Code, w.Header().Get("ETag"))
	}
	ifMatch := func(body, tag string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PATCH", "/api/v1/paper", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+a.key)
		r.Header.Set("If-Match", tag)
		w := httptest.NewRecorder()
		a.handler.ServeHTTP(w, r)
		return w
	}
	// The reader and the agent both read revision 0; the first save wins.
	if w := ifMatch(`{"brief":"Prefer local reporting."}`, `"0"`); w.Code != 200 || w.Header().Get("ETag") != `"1"` {
		t.Fatalf("first edit: %d %s ETag %q", w.Code, w.Body, w.Header().Get("ETag"))
	}
	if w := ifMatch(`{"masthead":"The Evening"}`, `"0"`); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "revision") {
		t.Fatalf("stale If-Match: %d %s", w.Code, w.Body)
	}
	p := a.paper(t, "GET", "/api/v1/paper", "", 200)
	if p.Revision != 1 || p.Brief == nil || *p.Brief != "Prefer local reporting." || p.Masthead != "The Daily" {
		t.Fatalf("first edit lost or stale edit applied: %+v", p)
	}
	// MCP tools send no headers: the body's expected_revision does the same.
	a.paper(t, "PATCH", "/api/v1/paper", `{"expected_revision":0,"masthead":"The Evening"}`, http.StatusConflict)
	if p := a.paper(t, "PATCH", "/api/v1/paper", `{"expected_revision":1,"masthead":"The Evening"}`, 200); p.Revision != 2 || p.Masthead != "The Evening" || p.Brief == nil {
		t.Fatalf("current expected_revision: %+v", p)
	}
	if w := ifMatch(`{"masthead":"The Night"}`, `*`); w.Code != 200 {
		t.Fatalf("If-Match *: %d %s", w.Code, w.Body)
	}
	if w := ifMatch(`{"masthead":"The Night"}`, `"x"`); w.Code != 400 {
		t.Fatalf("malformed If-Match: %d %s", w.Code, w.Body)
	}
	if w := a.request("POST", "/api/v1/paper/restore", `{"revision":1,"expected_revision":1}`, a.key); w.Code != http.StatusConflict {
		t.Fatalf("stale restore: %d %s", w.Code, w.Body)
	}

	// A client sending no precondition still never builds on a stale snapshot:
	// a request authenticated before the edits above merges onto the newest revision.
	slow := (&api.Server{Store: a.db, Config: func() *config.Config { return stale }, Editorial: desk}).Handler()
	r := httptest.NewRequest("PATCH", "/api/v1/paper", strings.NewReader(`{"boxes":{"sun":true}}`))
	r.Header.Set("Authorization", "Bearer "+a.key)
	w = httptest.NewRecorder()
	slow.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("unconditional edit: %d %s", w.Code, w.Body)
	}
	p = a.paper(t, "GET", "/api/v1/paper", "", 200)
	if p.Revision != 4 || p.Masthead != "The Night" || p.Brief == nil || !p.Boxes.Sun {
		t.Fatalf("unconditional edit dropped earlier changes: %+v", p)
	}
}

func TestPaperEditsRefuseTrailingData(t *testing.T) {
	a, _ := setupEditorial(t)
	for _, tc := range []struct{ method, path, body string }{
		{"PATCH", "/api/v1/paper", `{"masthead":"The Evening"}}`},
		{"PATCH", "/api/v1/paper", `{"masthead":"The Evening"}]`},
		{"POST", "/api/v1/paper/restore", `{"revision":0}}`},
		{"POST", "/api/v1/paper/restore", `{"revision":0} {}`},
	} {
		if w := a.request(tc.method, tc.path, tc.body, a.key); w.Code != 422 {
			t.Errorf("%s %s %s: %d %s", tc.method, tc.path, tc.body, w.Code, w.Body)
		}
	}
	if p := a.paper(t, "GET", "/api/v1/paper", "", 200); p.Revision != 0 || p.Masthead == "The Evening" {
		t.Fatalf("a refused edit changed the paper: %+v", p)
	}
}
