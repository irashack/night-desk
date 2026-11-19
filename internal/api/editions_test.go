package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/keys"
	"github.com/irashack/night-desk/internal/store"
)

func TestEditionReadsAndTenantIsolation(t *testing.T) {
	a := setup(t, nil)
	_, otherKey, e := keys.Issue(context.Background(), a.db, "other", "reader", auth.Read)
	if e != nil {
		t.Fatal(e)
	}
	a.config.Papers = append(a.config.Papers, config.Paper{ID: "other"})
	date := "2026-10-30"
	if w := a.request("PUT", "/api/v1/editions/"+date, fixture(t), a.key); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := a.request("GET", "/api/v1/editions/"+date, "", a.key)
	var got map[string]any
	if e = json.Unmarshal(w.Body.Bytes(), &got); e != nil || w.Code != 200 || w.Header().Get("Content-Type") != "application/json" {
		t.Fatal(w.Code, w.Body.String(), e)
	}
	at, ok := got["published_at"].(string)
	if !ok {
		t.Fatal("missing published_at")
	}
	if _, e = time.Parse(time.RFC3339Nano, at); e != nil {
		t.Fatal(e)
	}
	delete(got, "published_at")
	stored, e := a.db.Edition(context.Background(), a.scope, date)
	if e != nil {
		t.Fatal(e)
	}
	body, e := json.Marshal(stored)
	if e != nil {
		t.Fatal(e)
	}
	var want map[string]any
	if e = json.Unmarshal(body, &want); e != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("read changed the stored edition", e)
	}
	// Changing the effective paper must not change an archived document.
	a.config.Papers[0].Sections = nil
	if after := a.request("GET", "/api/v1/editions/"+date, "", a.key); after.Code != 200 || after.Body.String() != w.Body.String() {
		t.Fatal("archive depends on current paper", after.Code, after.Body.String())
	}
	for _, tt := range []struct{ date, key string }{{date, otherKey}, {"2026-10-29", a.key}} {
		w = a.request("GET", "/api/v1/editions/"+tt.date, "", tt.key)
		if w.Code != 404 || w.Body.String() != "{\"error\":\"no such edition\"}\n" {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w = a.request("GET", "/api/v1/editions", "", otherKey)
	if w.Code != 200 || w.Body.String() != "{\"editions\":[]}\n" {
		t.Fatal("other paper saw editions", w.Code, w.Body.String())
	}
	for _, tt := range []struct{ method, path string }{
		{"POST", "/api/v1/editions"}, {"PUT", "/api/v1/editions"}, {"DELETE", "/api/v1/editions/" + date},
	} {
		w = a.request(tt.method, tt.path, "", a.key)
		if w.Code != 405 || w.Header().Get("Content-Type") != "application/json" || !json.Valid(w.Body.Bytes()) {
			t.Fatal(tt.path, w.Code, w.Body.String())
		}
	}
}

func TestEditionListPagination(t *testing.T) {
	a := setup(t, nil)
	for day := 1; day <= 23; day++ {
		if e := a.db.Publish(context.Background(), a.scope, fmt.Sprintf("2026-10-%02d", day), &edition.Edition{Schema: 1}, nil); e != nil {
			t.Fatal(e)
		}
	}
	for _, tt := range []struct {
		query        string
		count, first int
	}{
		{"", 20, 23},
		{"?limit=1", 1, 23},
		{"?limit=2", 2, 23},
		{"?limit=2&before=2026-10-22", 2, 21},
		{"?before=2026-10-04", 3, 3},
		{"?before=2026-10-01", 0, 0},
		{"?limit=50", 23, 23},
	} {
		w := a.request("GET", "/api/v1/editions"+tt.query, "", a.key)
		var result struct {
			Editions []store.EditionSummary `json:"editions"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil || w.Code != 200 || result.Editions == nil || len(result.Editions) != tt.count {
			t.Fatal(tt.query, w.Code, w.Body.String(), e)
		}
		for i, v := range result.Editions {
			if v.Date != fmt.Sprintf("2026-10-%02d", tt.first-i) {
				t.Fatal("incorrect date order", tt.query, result.Editions)
			}
			if _, e := time.Parse(time.RFC3339Nano, v.PublishedAt); e != nil {
				t.Fatal(e)
			}
		}
	}
	for _, query := range []string{"?limit=0", "?limit=51", "?limit=-1", "?limit=bad", "?limit=", "?before=", "?before=2026-02-30", "?before=2026-1-01", "?before=bad"} {
		w := a.request("GET", "/api/v1/editions"+query, "", a.key)
		if w.Code != 400 || !json.Valid(w.Body.Bytes()) {
			t.Fatal(query, w.Code, w.Body.String())
		}
	}
	for _, date := range []string{"bad", "2026-02-30", "2026-1-01"} {
		if w := a.request("GET", "/api/v1/editions/"+date, "", a.key); w.Code != 400 {
			t.Fatal(date, w.Code, w.Body.String())
		}
	}
}
