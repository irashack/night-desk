package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/api"
)

// A draft may be submitted with cut-off text, but publishing it is refused, by
// hand or at the deadline.
func TestCutOffDraftRefusedAtPublish(t *testing.T) {
	a := setup(t, nil)
	p := &a.config.Papers[0]
	p.AgentRole = "author"
	const date = "2026-10-30"
	const path = "/api/v1/drafts/" + date
	cut := strings.Replace(fixture(t), "Watch the upcoming economic calendar before making short-term plans.", "The council voted to approve the", 1)
	if !strings.Contains(cut, `"The council voted to approve the"`) {
		t.Fatal("fixture shape changed")
	}
	w := a.request("PUT", path, cut, a.key)
	if w.Code != 200 {
		t.Fatal("submission accepts cut-off text", w.Code, w.Body.String())
	}
	rev := draftRevision(t, w)
	w = a.request("POST", path+"/publish", fmt.Sprintf(`{"revision":%d}`, rev), a.key)
	if w.Code != 422 || !strings.Contains(w.Body.String(), "is cut off: ends on dangling word 'the'") {
		t.Fatal("publishing cut-off text must be refused", w.Code, w.Body.String())
	}
	if _, err := a.db.EnsureRun(context.Background(), p.ID, date); err != nil {
		t.Fatal(err)
	}
	if err := (&api.Server{Store: a.db}).ApplyDeadline(context.Background(), *p, date); err != nil {
		t.Fatal(err)
	}
	d, err := a.db.Draft(context.Background(), a.scope, date)
	if err != nil || d.State != "held" {
		t.Fatal("the deadline must hold cut-off text, not publish it", d.State, err)
	}
}
