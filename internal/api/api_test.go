package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/api"
	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/images"
	"github.com/irashack/night-desk/internal/keys"
	"github.com/irashack/night-desk/internal/store"
)

type fetchFunc func(context.Context, string) (images.Result, error)

func (f fetchFunc) Fetch(ctx context.Context, u string) (images.Result, error) { return f(ctx, u) }

type app struct {
	handler http.Handler
	db      *store.Store
	key, id string
	scope   auth.PaperScope
	log     bytes.Buffer
	config  *config.Config
}

func setup(t *testing.T, f api.ImageFetcher) *app {
	t.Helper()
	db, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	if e = db.Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	id, key, e := keys.Issue(context.Background(), db, "daily", "test", auth.Read, auth.Publish, auth.Edit, auth.Review, auth.Gather, auth.Draft)
	if e != nil {
		t.Fatal(e)
	}
	scope, e := auth.Authenticate(context.Background(), db, key)
	if e != nil {
		t.Fatal(e)
	}
	a := &app{db: db, key: key, id: id, scope: scope}
	c := &config.Config{Papers: []config.Paper{{ID: "daily", Timezone: "America/Chicago", Sections: []config.Section{{Key: "economy", Title: "Economy", Required: true}, {Key: "health", Title: "Health"}}, Boxes: config.Boxes{Weather: true}}}}
	a.config = c
	a.handler = (&api.Server{Store: db, Config: func() *config.Config { return c }, Images: f, Log: slog.New(slog.NewJSONHandler(&a.log, nil))}).Handler()
	return a
}
func (a *app) request(method, path, body, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, r)
	return w
}
func fixture(t *testing.T) string {
	t.Helper()
	b, e := os.ReadFile("../../testdata/valid/minimal.json")
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func TestPaperEditorialConfiguration(t *testing.T) {
	a := setup(t, nil)
	p := &a.config.Papers[0]
	p.Masthead = "The Daily"
	p.Place = "Chicago"
	brief, deadline, sectionBrief := "Prefer primary sources.", "06:30", "Explain household effects."
	p.Brief, p.Deadline = &brief, &deadline
	p.Motto = &config.Motto{Guidance: "A verified quotation."}
	p.Sections[0].Brief = &sectionBrief
	p.Readers = []config.Reader{{Issuer: "https://id.example", Subject: "private-subject", SubjectFile: "/private/path"}}
	for _, editorial := range []bool{true, false} {
		if !editorial {
			p.Place, p.Brief, p.Deadline, p.Motto, p.Sections[0].Brief = "", nil, nil, nil, nil
		}
		w := a.request("GET", "/api/v1/paper", "", a.key)
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 {
			t.Fatal(w.Code, w.Body.String(), err)
		}
		if got["masthead"] != "The Daily" || got["timezone"] != "America/Chicago" || got["boxes"].(map[string]any)["weather"] != true {
			t.Fatal(got)
		}
		section := got["sections"].([]any)[0].(map[string]any)
		if section["key"] != "economy" || section["required"] != true {
			t.Fatal(section)
		}
		for _, key := range []string{"place", "brief", "deadline", "motto"} {
			if _, ok := got[key]; ok != editorial {
				t.Fatal("optional field", key, got)
			}
		}
		if _, ok := section["brief"]; ok != editorial {
			t.Fatal("optional section brief", section)
		}
		if editorial && (got["place"] != "Chicago" || got["brief"] != brief || got["deadline"] != deadline || got["motto"].(map[string]any)["guidance"] != p.Motto.Guidance || section["brief"] != sectionBrief) {
			t.Fatal("editorial fields changed", got)
		}
		if strings.Contains(w.Body.String(), "private") || got["readers"] != nil || got["latitude"] != nil {
			t.Fatal("private configuration exposed", got)
		}
	}
}
func TestEndToEnd(t *testing.T) {
	a := setup(t, fetchFunc(func(ctx context.Context, u string) (images.Result, error) {
		if d, ok := ctx.Deadline(); !ok || time.Until(d) > 60*time.Second {
			t.Error("missing image budget")
		}
		return images.Result{SHA256: strings.Repeat("a", 64), Width: 1200, Height: 600, Bytes: 1000}, nil
	}))
	body := fixture(t)
	w := a.request("GET", "/api/v1/paper", "", a.key)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "\"required\":true") || !strings.Contains(w.Body.String(), "America/Chicago") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = a.request("PUT", "/api/v1/editions/2026-10-27", body, a.key)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	got, e := a.db.Edition(context.Background(), a.scope, "2026-10-27")
	if e != nil || got.Schema != 1 {
		t.Fatal(got, e)
	}
	if got.Sections[0].Items[0].ImageURL != nil {
		t.Fatal("external image URL retained")
	}
	im, e := a.db.EditionImages(context.Background(), a.scope, "2026-10-27")
	if e != nil || len(im) != 1 {
		t.Fatal(im, e)
	}
	replacement := strings.Replace(body, "The week ahead for household budgets", "Replacement title", 1)
	w = a.request("PUT", "/api/v1/editions/2026-10-27", replacement, a.key)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	got, e = a.db.Edition(context.Background(), a.scope, "2026-10-27")
	if e != nil || got.Sections[0].Items[0].Title != "Replacement title" {
		t.Fatal(got, e)
	}
	for _, tt := range []struct{ body, path string }{
		{strings.Replace(body, "\"schema\": 1", "\"schema\": 3", 1), "schema"},
		{strings.Replace(body, "https://example.com/economy", "http://example.com/economy", 1), "sections[0].items[0].url"},
		{strings.Replace(body, "\"economy\"", "\"health\"", 1), "sections"},
		{strings.Replace(body, "\"title\":", "\"unknown\":", 1), "sections[0].items[0].title"},
		{strings.Replace(body, `"schema": 1`, `"extra": true,"schema": 1`, 1), "$"},
		{"{", "$"},
	} {
		w = a.request("PUT", "/api/v1/editions/2026-10-27", tt.body, a.key)
		if w.Code != 422 {
			t.Fatal(w.Code, w.Body.String())
		}
		var result struct {
			Errors   []struct{ Path, Rule string }
			Warnings []any
		}
		if e = json.Unmarshal(w.Body.Bytes(), &result); e != nil || len(result.Errors) == 0 || result.Warnings == nil {
			t.Fatal(w.Body.String(), e)
		}
		found := false
		for _, p := range result.Errors {
			if p.Path == tt.path && p.Rule != "" {
				found = true
			}
		}
		if !found {
			t.Fatal("missing path", tt.path, w.Body.String())
		}
	}
	got, e = a.db.Edition(context.Background(), a.scope, "2026-10-27")
	if e != nil || got.Sections[0].Items[0].Title != "Replacement title" {
		t.Fatal("invalid replaced stored edition", e)
	}
	for _, key := range []string{"", "wrong-key", "nd_" + strings.Repeat("A", 43)} {
		for _, path := range []string{"/api/v1/paper", "/api/v1/feedback", "/api/v1/editions/2026-10-27"} {
			w = a.request("PUT", path, body, key)
			if w.Code != 401 || w.Body.String() != "{\"error\":\"unauthorized\"}\n" {
				t.Fatal(w.Code, w.Body.String())
			}
		}
	}
	for _, tt := range []struct {
		method, path, body string
		status             int
	}{
		{"PUT", "/api/v1/editions/2026-02-30", body, 400},
		{"PUT", "/api/v1/editions/2026-10-27", strings.Repeat("x", (1<<20)+1), 413},
		{"GET", "/api/v1/feedback?limit=501", "", 400},
		{"GET", "/api/v1/feedback?limit=0", "", 400},
		{"GET", "/api/v1/feedback?after=-1", "", 400},
		{"GET", "/api/v1/feedback?after=bad", "", 400},
		{"POST", "/api/v1/paper", "", 405},
		{"GET", "/api/v1/missing", "", 404},
		{"GET", "/api", "", 404},
		{"GET", "/api/v1//paper", "", 400},
		{"GET", "/api/v1/../paper", "", 400},
	} {
		w = a.request(tt.method, tt.path, tt.body, a.key)
		if w.Code != tt.status || w.Header().Get("Content-Type") != "application/json" || !json.Valid(w.Body.Bytes()) {
			t.Fatal(tt.path, w.Code, w.Body.String())
		}
	}
	w = a.request("GET", "/healthz", "", "")
	if w.Code != 200 || w.Body.String() != "ok" {
		t.Fatal(w.Code, w.Body.String())
	}
	if e = a.db.RevokeKey(context.Background(), a.id); e != nil {
		t.Fatal(e)
	}
	w = a.request("GET", "/api/v1/paper", "", a.key)
	if w.Code != 401 {
		t.Fatal("revoked key allowed")
	}
	if strings.Contains(a.log.String(), a.key) || strings.Contains(a.log.String(), "Replacement title") {
		t.Fatal("sensitive logging")
	}
}
func TestFeedbackPaging(t *testing.T) {
	a := setup(t, nil)
	ctx := context.Background()
	_, otherKey, e := keys.Issue(ctx, a.db, "other", "")
	if e != nil {
		t.Fatal(e)
	}
	other, e := auth.Authenticate(ctx, a.db, otherKey)
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range []auth.PaperScope{other, a.scope, other, a.scope, a.scope} {
		if e = a.db.AddFeedback(ctx, p, store.Feedback{Date: "2026-10-27", ItemID: "economy-1", Kind: "save", ReaderIssuer: "https://issuer", ReaderSubject: "one"}); e != nil {
			t.Fatal(e)
		}
	}
	cursor := int64(0)
	for _, want := range []struct {
		n    int
		next int64
	}{{2, 4}, {1, 5}, {0, 5}} {
		w := a.request("GET", fmt.Sprintf("/api/v1/feedback?after=%d&limit=2", cursor), "", a.key)
		var result struct {
			Events []store.Feedback `json:"events"`
			Next   int64            `json:"next_cursor"`
		}
		if e = json.Unmarshal(w.Body.Bytes(), &result); e != nil || w.Code != 200 || len(result.Events) != want.n || result.Next != want.next {
			t.Fatal(w.Code, w.Body.String(), e)
		}
		cursor = result.Next
	}
}
func TestImageFailuresAndConcurrency(t *testing.T) {
	var active, peak atomic.Int32
	a := setup(t, fetchFunc(func(ctx context.Context, u string) (images.Result, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			return images.Result{}, ctx.Err()
		}
		if strings.HasSuffix(u, "/fail") {
			return images.Result{}, errors.New("private image URL must never leak")
		}
		return images.Result{SHA256: strings.Repeat("b", 64), Width: 1, Height: 1, Bytes: 1}, nil
	}))
	ed := &edition.Edition{Schema: 1, Lead: "item-0"}
	for i, key := range []string{"economy", "health"} {
		section := edition.Section{Key: key}
		for j := 0; j < 6; j++ {
			u := "https://example.com/image"
			if i == 0 && j == 0 {
				u = "https://example.com/fail"
			}
			section.Items = append(section.Items, edition.Item{ID: fmt.Sprintf("item-%d", i*6+j), Kind: "news", Title: "Title", Summary: "Summary", Source: "Source", URL: "https://example.com", ImageURL: &u})
		}
		ed.Sections = append(ed.Sections, section)
	}
	body, e := json.Marshal(ed)
	if e != nil {
		t.Fatal(e)
	}
	w := a.request("PUT", "/api/v1/editions/2026-10-27", string(body), a.key)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "sections[0].items[0].image_url") || strings.Contains(w.Body.String(), "private image URL") {
		t.Fatal(w.Code, w.Body.String())
	}
	if peak.Load() != 4 {
		t.Fatal("concurrency not bounded at four", peak.Load())
	}
	im, e := a.db.EditionImages(context.Background(), a.scope, "2026-10-27")
	if e != nil || len(im) != 11 {
		t.Fatal(im, e)
	}
	if _, ok := im["item-0"]; ok {
		t.Fatal("failed image associated")
	}
	// Replacement clears all old image mappings.
	for i := range ed.Sections {
		for j := range ed.Sections[i].Items {
			ed.Sections[i].Items[j].ImageURL = nil
		}
	}
	body, _ = json.Marshal(ed)
	w = a.request("PUT", "/api/v1/editions/2026-10-27", string(body), a.key)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	im, e = a.db.EditionImages(context.Background(), a.scope, "2026-10-27")
	if e != nil || len(im) != 0 {
		t.Fatal(im, e)
	}
	a.db.Close()
	w = a.request("GET", "/healthz", "", "")
	if w.Code != 503 {
		t.Fatal("closed DB health", w.Code)
	}
}
func TestRoutingNoAuthDetail(t *testing.T) {
	a := setup(t, nil)
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		w := a.request(method, "/api/v1/unknown", "", "")
		b, e := io.ReadAll(w.Result().Body)
		if e != nil || w.Code != 401 || string(b) != "{\"error\":\"unauthorized\"}\n" {
			t.Fatal(w.Code, string(b), e)
		}
	}
}
