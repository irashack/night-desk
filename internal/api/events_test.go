package api_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/keys"
	"github.com/irashack/night-desk/internal/webhook"
)

func TestEventsAPIIsolationAndPrivacy(t *testing.T) {
	a := setup(t, nil)
	ctx := context.Background()
	a.config.Papers[0].Webhooks = []config.Webhook{{URL: "https://private-hook.example/endpoint", SecretFile: "/private/signing-key"}}
	if w := a.request("PUT", "/api/v1/editions/2026-10-30", fixture(t), a.key); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	_, key, err := keys.Issue(ctx, a.db, "other", "other", auth.Read, auth.Publish)
	if err != nil {
		t.Fatal(err)
	}
	a.config.Papers = append(a.config.Papers, config.Paper{ID: "other"})
	w := a.request("GET", "/api/v1/events?limit=1", "", a.key)
	var result struct {
		Events []webhook.Event `json:"events"`
		Next   string          `json:"next_cursor"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || len(result.Events) != 1 || result.Events[0].Type != "edition.published" || result.Next != result.Events[0].ID {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	for _, path := range []string{"/api/v1/paper", "/api/v1/events"} {
		w = a.request("GET", path, "", a.key)
		if strings.Contains(w.Body.String(), "private") {
			t.Fatal("webhook configuration leaked", w.Body.String())
		}
	}
	w = a.request("GET", "/api/v1/events", "", key)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"events":[]`) {
		t.Fatal("tenant leak", w.Body.String())
	}
	w = a.request("GET", "/api/v1/events?after="+result.Next, "", key)
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"path":"after"`) {
		t.Fatal("foreign cursor", w.Code, w.Body.String())
	}
	w = a.request("GET", "/api/v1/events?after="+result.Next, "", a.key)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"events":[]`) {
		t.Fatal("cursor replay", w.Body.String())
	}
	for _, query := range []string{"limit=0", "limit=501", "limit=bad", "after=invalid"} {
		w = a.request("GET", "/api/v1/events?"+query, "", a.key)
		if w.Code != 400 {
			t.Fatal(query, w.Code)
		}
	}
	_, restricted, err := keys.Issue(ctx, a.db, "daily", "gather", auth.Gather)
	if err != nil {
		t.Fatal(err)
	}
	if w = a.request("GET", "/api/v1/events", "", restricted); w.Code != 403 {
		t.Fatal("read capability not enforced", w.Code)
	}
	if w = a.request("POST", "/api/v1/events", "", a.key); w.Code != 405 {
		t.Fatal("wrong method", w.Code)
	}
	if strings.Contains(a.log.String(), "private") {
		t.Fatal("secret paths logged")
	}
}

func TestEventsPollingWithoutWebhook(t *testing.T) {
	a := setup(t, nil)
	if w := a.request("PUT", "/api/v1/editions/2026-10-30", fixture(t), a.key); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := a.request("GET", "/api/v1/events", "", a.key)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"type":"edition.published"`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

// Repeating an identical PUT (a client retrying after a lost response) must not
// announce the edition again; a changed edition still does (#30).
func TestRepeatedIdenticalPublishIsIdempotent(t *testing.T) {
	a := setup(t, nil)
	ctx := context.Background()
	published := func() []string {
		t.Helper()
		events, _, err := a.db.Events(ctx, a.scope, "", 500)
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, e := range events {
			if e.Type == webhook.EventEditionPublished {
				ids = append(ids, e.ID)
			}
		}
		return ids
	}
	body := fixture(t)
	for range 2 {
		if w := a.request("PUT", "/api/v1/editions/2026-10-30", body, a.key); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	first, err := a.db.Edition(ctx, a.scope, "2026-10-30")
	if err != nil || first == nil {
		t.Fatal(err)
	}
	if got := published(); len(got) != 1 {
		t.Fatalf("identical repeat emitted %d edition.published events, want 1", len(got))
	}
	var ed map[string]any
	if err = json.Unmarshal([]byte(body), &ed); err != nil {
		t.Fatal(err)
	}
	item := ed["sections"].([]any)[0].(map[string]any)["items"].([]any)[0].(map[string]any)
	item["title"] = "A corrected headline"
	raw, _ := json.Marshal(ed)
	if w := a.request("PUT", "/api/v1/editions/2026-10-30", string(raw), a.key); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if got := published(); len(got) != 2 {
		t.Fatalf("changed edition emitted %d edition.published events in total, want 2", len(got))
	}
}
