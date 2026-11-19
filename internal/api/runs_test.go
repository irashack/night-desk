package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/keys"
)

func TestDraftRunClaimReportAndRevisionFence(t *testing.T) {
	a := setup(t, nil)
	claim := a.request("POST", "/api/v1/runs/2026-10-30/claim", `{"max_calls":12,"max_tokens":100000}`, a.key)
	if claim.Code != 200 {
		t.Fatal(claim.Code, claim.Body)
	}
	var lease struct {
		ID string `json:"lease_id"`
	}
	if json.Unmarshal(claim.Body.Bytes(), &lease) != nil || lease.ID == "" {
		t.Fatal(claim.Body)
	}
	if w := a.request("POST", "/api/v1/runs/2026-10-30/claim", `{"max_calls":12,"max_tokens":100000}`, a.key); w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	report := fmt.Sprintf(`{"lease_id":%q,"outcome":"submitted","spend":{"calls":3,"tokens":60},"expected_revision":0,"draft":%s}`, lease.ID, fixture(t))
	if w := a.request("POST", "/api/v1/runs/2026-10-30/report", report, a.key); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	// A retry is idempotent and cannot submit a second draft revision.
	if w := a.request("POST", "/api/v1/runs/2026-10-30/report", report, a.key); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	w := a.request("GET", "/api/v1/drafts/2026-10-30", "", a.key)
	if !strings.Contains(w.Body.String(), `"revision":1`) {
		t.Fatal(w.Body)
	}
	// New attempt reads revision 1, then loses the race to an editor.
	claim = a.request("POST", "/api/v1/runs/2026-10-30/claim", `{"max_calls":120,"max_tokens":1000000}`, a.key)
	if claim.Code != 200 || json.Unmarshal(claim.Body.Bytes(), &lease) != nil {
		t.Fatal(claim.Code, claim.Body)
	}
	if w := a.request("POST", "/api/v1/drafts/2026-10-30/review", `{"revision":1,"action":"approve"}`, a.key); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	report = fmt.Sprintf(`{"lease_id":%q,"outcome":"submitted","spend":{"calls":3,"tokens":60},"expected_revision":1,"draft":%s}`, lease.ID, fixture(t))
	if w := a.request("POST", "/api/v1/runs/2026-10-30/report", report, a.key); w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	report = fmt.Sprintf(`{"lease_id":%q,"outcome":"failed","spend":{"calls":3,"tokens":60}}`, lease.ID)
	if w := a.request("POST", "/api/v1/runs/2026-10-30/report", report, a.key); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
}

func TestDraftRunCapabilityAndValidation(t *testing.T) {
	a := setup(t, nil)
	_, key, err := keys.Issue(context.Background(), a.db, "daily", "gather", auth.Gather, auth.Read)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"claim", "report"} {
		if w := a.request("POST", "/api/v1/runs/2026-10-30/"+action, `{}`, key); w.Code != 403 {
			t.Fatal(w.Code, w.Body)
		}
	}
	for _, tc := range []struct{ action, body, path string }{
		{"claim", `{"max_calls":12,"max_calls":1,"max_tokens":100}`, "max_calls"},
		{"claim", `{"max_calls":0,"max_tokens":100}`, "max_calls"},
		{"claim", `{"max_calls":12,"max_tokens":null}`, "max_tokens"},
		{"claim", `{"max_calls":12,"max_tokens":100,"paper_id":"other"}`, "paper_id"},
		{"report", `{"lease_id":"x","outcome":"failed","spend":{"calls":1,"calls":0,"tokens":0}}`, "spend.calls"},
		{"report", `{"lease_id":"x","outcome":"failed","spend":{"calls":null,"tokens":0}}`, "spend.calls"},
		{"report", `{"lease_id":"x","outcome":"failed","spend":{"calls":0,"tokens":0,"extra":true}}`, "spend.extra"},
		{"report", `{"lease_id":"x","outcome":"submitted","expected_revision":0,"spend":{"calls":0,"tokens":0},"draft":{"schema":2,"extra":true}}`, "draft.extra"},
		{"report", `{"lease_id":"x","outcome":"failed","spend":{"calls":-1,"tokens":0}}`, "spend.calls"},
		{"report", `{"lease_id":"x","outcome":"failed","spend":{"calls":0,"tokens":0,"cost_micros":-5}}`, "spend.cost_micros"},
		{"report", `{"lease_id":"x","outcome":"submitted","spend":{"calls":0,"tokens":0}}`, "draft"},
	} {
		w := a.request("POST", "/api/v1/runs/2026-10-30/"+tc.action, tc.body, a.key)
		if w.Code != 422 || !strings.Contains(w.Body.String(), `"path":"`+tc.path+`"`) {
			t.Fatal(w.Code, w.Body)
		}
	}
}
