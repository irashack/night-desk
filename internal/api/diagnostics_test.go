package api_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/irashack/night-desk/internal/api"
	"github.com/irashack/night-desk/internal/candidate"
	"github.com/irashack/night-desk/internal/config"
)

func TestAllPublishPathsPersistSourceDiagnostics(t *testing.T) {
	for _, path := range []string{"put", "draft", "deadline"} {
		t.Run(path, func(t *testing.T) {
			a := setup(t, nil)
			a.config.Papers[0].Sources = []config.Source{{Feed: "https://example.com/feed"}}
			c := apiCandidate()
			c.Text = "There are 40 jobs."
			if err := a.db.UpsertCandidates(context.Background(), a.scope, []candidate.Candidate{c}); err != nil {
				t.Fatal(err)
			}
			body := sourceEdition(t, true) // the additional source is unavailable
			if path == "put" {
				warningsFrom(t, a.request("PUT", "/api/v1/editions/2026-10-30", body, a.key))
			} else {
				rev := draftRevision(t, a.request("PUT", "/api/v1/drafts/2026-10-30", body, a.key))
				if path == "draft" {
					warningsFrom(t, a.request("POST", "/api/v1/drafts/2026-10-30/publish", fmt.Sprintf(`{"revision":%d}`, rev), a.key))
				} else {
					p := a.config.Papers[0]
					p.AgentRole = "none"
					if err := (&api.Server{Store: a.db}).ApplyDeadline(context.Background(), p, "2026-10-30"); err != nil {
						t.Fatal(err)
					}
				}
			}
			data, err := a.db.Quality(context.Background(), "daily", "2026-10-30", "2026-10-30", nil)
			if err != nil || data.Published != 1 || data.WarningsByRule["unsupported-number"] != 3 || data.FailedSourceChecksByRule["unsupported-number"] != 3 || data.UnverifiedSourceChecksByRule["not gathered"] != 1 {
				t.Fatal(data, err)
			}
		})
	}
}

func TestRejectedPublishPersistsValidationWarnings(t *testing.T) {
	a := setup(t, nil)
	// Reordering sections produces a warning; empty required economy is an error.
	body := `{"schema":1,"lead":"one","sections":[{"key":"health","items":[{"id":"one","kind":"news","title":"private","summary":"private","source":"private","url":"https://example.com"}]},{"key":"economy","items":[]}]}`
	w := a.request("PUT", "/api/v1/editions/2026-10-30", body, a.key)
	if w.Code != 422 {
		t.Fatal(w.Code, w.Body.String())
	}
	data, err := a.db.Quality(context.Background(), "daily", "2026-10-30", "2026-10-30", nil)
	if err != nil || data.Published != 0 || data.WarningsByRule["should follow the paper's section order"] != 1 {
		t.Fatal(data, err)
	}
	// Repeat attempts preserve warnings rather than overwriting diagnostics.
	w = a.request("PUT", "/api/v1/editions/2026-10-30", body, a.key)
	if w.Code != 422 {
		t.Fatal(w.Code, w.Body.String())
	}
	data, err = a.db.Quality(context.Background(), "daily", "2026-10-30", "2026-10-30", nil)
	if err != nil || data.WarningsByRule["should follow the paper's section order"] != 2 {
		t.Fatal(data, err)
	}
}
