package api_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/keys"
)

func TestItemLookup(t *testing.T) {
	a := setup(t, nil)
	date := "2026-10-30"
	ed, err := edition.Decode(strings.NewReader(fixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	ed.Schema = 2
	byline := "A Reporter"
	ed.Sections[0].Items[0].Byline = &byline
	ed.Sections[0].Items[0].Sources = []edition.Source{{Outlet: "Other", URL: "https://other.example/story", Byline: &byline}}
	if err = a.db.Publish(context.Background(), a.scope, date, ed, nil); err != nil {
		t.Fatal(err)
	}
	_, readKey, err := keys.Issue(context.Background(), a.db, "daily", "read", auth.Read)
	if err != nil {
		t.Fatal(err)
	}
	w := a.request("GET", "/api/v1/items/"+date+"/economy-1", "", readKey)
	var got map[string]any
	if err = json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	at, _ := got["published_at"].(string)
	if _, err = time.Parse(time.RFC3339Nano, at); err != nil || got["date"] != date || got["section"] != "economy" {
		t.Fatal(got, err)
	}
	delete(got, "date")
	delete(got, "section")
	delete(got, "published_at")
	raw, _ := json.Marshal(ed.Sections[0].Items[0])
	var want map[string]any
	_ = json.Unmarshal(raw, &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatal("lookup changed published item", got, want)
	}
	a.config.Papers[0].Sections = nil
	if after := a.request("GET", "/api/v1/items/"+date+"/economy-1", "", readKey); after.Body.String() != w.Body.String() {
		t.Fatal("lookup depends on current paper", after.Body.String())
	}
	a.config.Papers = append(a.config.Papers, config.Paper{ID: "other"})
	_, otherKey, err := keys.Issue(context.Background(), a.db, "other", "read", auth.Read)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ path, key string }{
		{"/api/v1/items/" + date + "/economy-1", otherKey},
		{"/api/v1/items/2026-10-29/economy-1", readKey},
		{"/api/v1/items/" + date + "/missing", readKey},
	} {
		if w := a.request("GET", tt.path, "", tt.key); w.Code != 404 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := a.request("GET", "/api/v1/items/bad/economy-1", "", readKey); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestReplacementItemIdentityError(t *testing.T) {
	a := setup(t, nil)
	path := "/api/v1/editions/2026-10-30"
	body := fixture(t)
	if w := a.request("PUT", path, body, a.key); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	replacement := strings.Replace(body, `"url": "https://example.com/economy"`, `"url": "https://example.com/unrelated"`, 1)
	w := a.request("PUT", path, replacement, a.key)
	var got struct{ Errors []struct{ Path, Rule string } }
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 422 || len(got.Errors) != 1 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	if got.Errors[0].Path != "sections[0].items[0].id" || got.Errors[0].Rule != "already names a different story in this edition; use a new id" {
		t.Fatal(got)
	}
	// Draft publication retains its stricter prohibition on replacing editions.
	if w = a.request("PUT", "/api/v1/drafts/2026-10-30", replacement, a.key); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = a.request("POST", "/api/v1/drafts/2026-10-30/publish", `{"revision":1}`, a.key)
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	// A server-published draft is available through item lookup as well.
	if w = a.request("PUT", "/api/v1/drafts/2026-10-31", body, a.key); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = a.request("POST", "/api/v1/drafts/2026-10-31/publish", `{"revision":1}`, a.key); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = a.request("GET", "/api/v1/items/2026-10-31/economy-1", "", a.key); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
