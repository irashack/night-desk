package api_test

import (
	"context"
	"testing"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/keys"
)

func TestEveryAgentRouteRequiresCapability(t *testing.T) {
	a, desk := setupEditorial(t)
	for _, tt := range []struct {
		method, path string
		capability   auth.Capability
	}{
		{"GET", "/api/v1/paper", auth.Read},
		{"GET", "/api/v1/feedback", auth.Read},
		{"GET", "/api/v1/editions", auth.Read},
		{"GET", "/api/v1/editions/2026-10-30", auth.Read},
		{"GET", "/api/v1/paper/revisions", auth.Read},
		{"PUT", "/api/v1/editions/2026-10-30", auth.Publish},
		{"PATCH", "/api/v1/paper", auth.Edit},
		{"POST", "/api/v1/paper/restore", auth.Edit},
	} {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			var capabilities []auth.Capability
			for _, c := range []auth.Capability{auth.Read, auth.Publish, auth.Edit, auth.Review, auth.Gather, auth.Draft} {
				if c != tt.capability {
					capabilities = append(capabilities, c)
				}
			}
			_, key, e := keys.Issue(context.Background(), a.db, "daily", "restricted", capabilities...)
			if e != nil {
				t.Fatal(e)
			}
			w := a.request(tt.method, tt.path, `{}`, key)
			if w.Code != 403 || w.Header().Get("Content-Type") != "application/json" || w.Body.String() != "{\"error\":\"forbidden\"}\n" {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	if rev, _ := desk.Revision("daily"); rev != 0 {
		t.Fatal("denied edit changed the paper")
	}
}

func TestWorkerCannotPublishOrEdit(t *testing.T) {
	a, desk := setupEditorial(t)
	capabilities, e := auth.ParseCapabilities(auth.WorkerCapabilities)
	if e != nil {
		t.Fatal(e)
	}
	_, key, e := keys.Issue(context.Background(), a.db, "daily", "worker", capabilities...)
	if e != nil {
		t.Fatal(e)
	}
	for _, tt := range []struct{ method, path, body string }{
		{"PUT", "/api/v1/editions/2026-10-30", fixture(t)},
		{"PATCH", "/api/v1/paper", `{"masthead":"Changed"}`},
		{"POST", "/api/v1/paper/restore", `{"revision":0}`},
	} {
		if w := a.request(tt.method, tt.path, tt.body, key); w.Code != 403 {
			t.Fatal(tt.path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/v1/paper", "/api/v1/feedback", "/api/v1/editions", "/api/v1/paper/revisions"} {
		if w := a.request("GET", path, "", key); w.Code != 200 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	if rev, _ := desk.Revision("daily"); rev != 0 {
		t.Fatal("worker changed the paper")
	}
	if w := a.request("GET", "/api/v1/editions/2026-10-30", "", a.key); w.Code != 404 {
		t.Fatal("worker published an edition", w.Code)
	}
}
