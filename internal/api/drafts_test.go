package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/api"
	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/images"
	"github.com/irashack/night-desk/internal/keys"
)

func TestDeadlinePolicyHonorsCurrentApproval(t *testing.T) {
	for _, policy := range []string{"hold", "publish-unreviewed"} {
		for _, approval := range []string{"none", "current", "invalidated"} {
			t.Run(policy+"/"+approval, func(t *testing.T) {
				a := setup(t, nil)
				p := &a.config.Papers[0]
				p.AgentRole, p.DeadlinePolicy = "reviewer", policy
				const date = "2026-10-30"
				const path = "/api/v1/drafts/" + date
				rev := draftRevision(t, a.request("PUT", path, fixture(t), a.key))
				if approval != "none" {
					rev = draftRevision(t, a.request("POST", path+"/review", fmt.Sprintf(`{"revision":%d,"action":"approve"}`, rev), a.key))
				}
				if approval == "invalidated" {
					rev = draftRevision(t, a.request("POST", path+"/review", fmt.Sprintf(`{"revision":%d,"action":"note","text":"Check again."}`, rev), a.key))
				}
				if _, err := a.db.EnsureRun(context.Background(), p.ID, date); err != nil {
					t.Fatal(err)
				}
				server := &api.Server{Store: a.db}
				if err := server.ApplyDeadline(context.Background(), *p, date); err != nil {
					t.Fatal(err)
				}
				wantState, wantAction := "published", "publish"
				wantUnreviewed := approval != "current" && policy == "publish-unreviewed"
				if approval != "current" && policy == "hold" {
					wantState, wantAction = "held", "hold"
				} else if wantUnreviewed {
					wantAction = "publish-unreviewed"
				}
				d, err := a.db.Draft(context.Background(), a.scope, date)
				if err != nil || d.State != wantState || d.Revision != rev+1 || d.Unreviewed != wantUnreviewed || d.Actions[len(d.Actions)-1].Action != wantAction {
					t.Fatal(d, err)
				}
				published, err := a.db.RunPublished(context.Background(), p.ID, date)
				if err != nil || published != (wantState == "published") {
					t.Fatal(published, err)
				}
				if published {
					flag, err := a.db.EditionUnreviewed(context.Background(), a.scope, date)
					if err != nil || flag != wantUnreviewed {
						t.Fatal(flag, err)
					}
				}
				run, err := a.db.EnsureRun(context.Background(), p.ID, date)
				if err != nil || run.State != wantState {
					t.Fatal(run, err)
				}
			})
		}
	}
}

func TestPublishDraftAfterHold(t *testing.T) {
	for _, role := range []string{"author", "reviewer"} {
		t.Run(role, func(t *testing.T) {
			a := setup(t, nil)
			p := &a.config.Papers[0]
			p.AgentRole, p.DeadlinePolicy = role, "hold"
			const date = "2026-10-30"
			const path = "/api/v1/drafts/" + date
			draftRevision(t, a.request("PUT", path, fixture(t), a.key))
			if _, err := a.db.EnsureRun(context.Background(), p.ID, date); err != nil {
				t.Fatal(err)
			}
			server := &api.Server{Store: a.db}
			if err := server.ApplyDeadline(context.Background(), *p, date); err != nil {
				t.Fatal(err)
			}
			d, err := a.db.Draft(context.Background(), a.scope, date)
			if err != nil || d.State != "held" {
				t.Fatal(d, err)
			}
			rev := d.Revision
			if role == "reviewer" {
				w := a.request("POST", path+"/publish", fmt.Sprintf(`{"revision":%d}`, rev), a.key)
				if w.Code != 409 {
					t.Fatal("held reviewer draft published without approval", w.Code, w.Body.String())
				}
				rev = draftRevision(t, a.request("POST", path+"/review", fmt.Sprintf(`{"revision":%d,"action":"approve"}`, rev), a.key))
			}
			// A later deadline pass cannot auto-publish a held draft, even if approved.
			if err = server.ApplyDeadline(context.Background(), *p, date); err != nil {
				t.Fatal(err)
			}
			d, err = a.db.Draft(context.Background(), a.scope, date)
			if err != nil || d.State != "held" || d.Revision != rev {
				t.Fatal(d, err)
			}
			_, reviewKey, err := keys.Issue(context.Background(), a.db, p.ID, "review only", auth.Review)
			if err != nil {
				t.Fatal(err)
			}
			w := a.request("POST", path+"/publish", fmt.Sprintf(`{"revision":%d}`, rev), reviewKey)
			if w.Code != 403 {
				t.Fatal("publish capability bypassed", w.Code, w.Body.String())
			}
			published := draftRevision(t, a.request("POST", path+"/publish", fmt.Sprintf(`{"revision":%d}`, rev), a.key))
			d, err = a.db.Draft(context.Background(), a.scope, date)
			if err != nil || d.State != "published" || d.Revision != published || d.Unreviewed {
				t.Fatal(d, err)
			}
			run, err := a.db.EnsureRun(context.Background(), p.ID, date)
			if err != nil || run.State != "published" {
				t.Fatal(run, err)
			}
		})
	}
}

func TestDeadlineHoldsApprovedDraftInvalidUnderCurrentPaper(t *testing.T) {
	for _, policy := range []string{"hold", "publish-unreviewed"} {
		t.Run(policy, func(t *testing.T) {
			a := setup(t, nil)
			p := &a.config.Papers[0]
			p.AgentRole, p.DeadlinePolicy = "reviewer", policy
			const date = "2026-10-30"
			const path = "/api/v1/drafts/" + date
			rev := draftRevision(t, a.request("PUT", path, fixture(t), a.key))
			draftRevision(t, a.request("POST", path+"/review", fmt.Sprintf(`{"revision":%d,"action":"approve"}`, rev), a.key))
			// Editorial rules can change after approval; validation still applies.
			p.Sections[1].Required = true
			if err := (&api.Server{Store: a.db}).ApplyDeadline(context.Background(), *p, date); err != nil {
				t.Fatal(err)
			}
			d, err := a.db.Draft(context.Background(), a.scope, date)
			if err != nil || d.State != "held" || d.Unreviewed {
				t.Fatal(d, err)
			}
			published, err := a.db.RunPublished(context.Background(), p.ID, date)
			if err != nil || published {
				t.Fatal("invalid approved draft published", published, err)
			}
		})
	}
}

func TestReviewV2Items(t *testing.T) {
	for _, action := range []string{"edit", "nested edit", "swap"} {
		t.Run(action, func(t *testing.T) {
			a := setup(t, nil)
			ed, err := edition.Decode(strings.NewReader(fixture(t)))
			if err != nil {
				t.Fatal(err)
			}
			ed.Schema = 2
			byline, otherByline := "A Writer", "Another Writer"
			item := &ed.Sections[0].Items[0]
			item.Byline = &byline
			item.Sources = []edition.Source{{Outlet: "Other", URL: "https://other.example/story", Byline: &otherByline}}
			raw, _ := json.Marshal(ed)
			const path = "/api/v1/drafts/2026-10-30"
			rev := draftRevision(t, a.request("PUT", path, string(raw), a.key))
			body := fmt.Sprintf(`{"revision":%d,"action":"edit","item":%q,"title":"Reviewed headline"}`, rev, item.ID)
			want := *item
			want.Title = "Reviewed headline"
			switch action {
			case "nested edit":
				body = fmt.Sprintf(`{"revision":%d,"action":"edit","item":%q,"fields":{"title":"Reviewed headline"}}`, rev, item.ID)
			case "swap":
				want.ID, want.Byline = "replacement", &otherByline
				want.Sources = []edition.Source{{Outlet: "Further coverage", URL: "https://further.example/story", Byline: &byline}}
				raw, _ = json.Marshal(want)
				body = fmt.Sprintf(`{"revision":%d,"action":"swap","item":%q,"with":%s}`, rev, item.ID, raw)
			}
			draftRevision(t, a.request("POST", path+"/review", body, a.key))
			d, err := a.db.Draft(context.Background(), a.scope, "2026-10-30")
			if err != nil || d.Body.Schema != 2 || d.Body.Lead != want.ID || !reflect.DeepEqual(d.Body.Sections[0].Items[0], want) {
				t.Fatal(d, err)
			}
		})
	}
}

func TestReviewVersionedFields(t *testing.T) {
	for _, schema := range []int{1, 2} {
		for _, nested := range []bool{false, true} {
			for _, field := range []string{"byline", "sources"} {
				t.Run(fmt.Sprintf("v%d/nested=%t/%s", schema, nested, field), func(t *testing.T) {
					a := setup(t, nil)
					body := strings.Replace(fixture(t), `"schema": 1`, fmt.Sprintf(`"schema": %d`, schema), 1)
					const path = "/api/v1/drafts/2026-10-30"
					rev := draftRevision(t, a.request("PUT", path, body, a.key))
					value := `"A Writer"`
					if field == "sources" {
						value = `[{"outlet":"Other","url":"https://other.example/story"}]`
					}
					patch := fmt.Sprintf(`%q:%s`, field, value)
					errorPath := field
					if nested {
						patch = `"fields":{` + patch + `}`
						errorPath = "fields." + field
					}
					w := a.request("POST", path+"/review", fmt.Sprintf(`{"revision":%d,"action":"edit","item":"economy-1",%s}`, rev, patch), a.key)
					if schema == 2 {
						draftRevision(t, w)
					} else if want := fmt.Sprintf(`{"errors":[{"path":%q,"rule":"requires schema 2"}]}`, errorPath); w.Code != 422 || strings.TrimSpace(w.Body.String()) != want {
						t.Fatal(w.Code, w.Body.String(), "want", want)
					}
				})
			}
		}
	}
}

func draftRevision(t *testing.T, w *httptest.ResponseRecorder) int64 {
	t.Helper()
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var got struct {
		Revision int64 `json:"revision"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Revision < 1 {
		t.Fatal(got, err, w.Body.String())
	}
	return got.Revision
}
func TestDraftReviewAndPublishAPI(t *testing.T) {
	a := setup(t, nil)
	a.config.Papers[0].AgentRole = "reviewer"
	path := "/api/v1/drafts/2026-10-30"
	w := a.request("PUT", "/api/v1/editions/2026-10-30", fixture(t), a.key)
	if w.Code != 409 || strings.TrimSpace(w.Body.String()) != `{"error":"paper publishes through drafts"}` {
		t.Fatal(w.Code, w.Body.String())
	}
	rev := draftRevision(t, a.request("PUT", path, fixture(t), a.key))
	if rev != 1 {
		t.Fatal(rev)
	}
	w = a.request("POST", path+"/publish", `{"revision":1}`, a.key)
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	// A review mutation advances the revision and records the reviewer key id.
	rev = draftRevision(t, a.request("POST", path+"/review", `{"revision":1,"action":"edit","item":"economy-1","title":"Reviewed headline"}`, a.key))
	w = a.request("POST", path+"/review", `{"revision":1,"action":"approve"}`, a.key)
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	rev = draftRevision(t, a.request("POST", path+"/review", fmt.Sprintf(`{"revision":%d,"action":"approve"}`, rev), a.key))
	d, err := a.db.Draft(context.Background(), a.scope, "2026-10-30")
	if err != nil || d.State != "in_review" || d.ApprovedRevision == nil || *d.ApprovedRevision != rev || d.Actions[1].Who != a.id {
		t.Fatal(d, err)
	}
	published := draftRevision(t, a.request("POST", path+"/publish", fmt.Sprintf(`{"revision":%d}`, rev), a.key))
	if published != rev+1 {
		t.Fatal(published)
	}
	got, err := a.db.Edition(context.Background(), a.scope, "2026-10-30")
	if err != nil || got.Sections[0].Items[0].Title != "Reviewed headline" || got.Sections[0].Items[0].ImageURL != nil {
		t.Fatal(got, err)
	}
	for _, method := range []string{"PUT", "POST"} {
		url, body := path, fixture(t)
		if method == "POST" {
			url += "/publish"
			body = fmt.Sprintf(`{"revision":%d}`, rev)
		}
		w = a.request(method, url, body, a.key)
		if w.Code != 409 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w = a.request("GET", "/api/v1/drafts?state=published", "", a.key)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"published"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = a.request("GET", path, "", a.key)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"actions":[`) {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestAuthorDraftAndLegacyPublication(t *testing.T) {
	a := setup(t, nil)
	path := "/api/v1/drafts/2026-10-30"
	rev := draftRevision(t, a.request("PUT", path, fixture(t), a.key))
	draftRevision(t, a.request("POST", path+"/publish", fmt.Sprintf(`{"revision":%d}`, rev), a.key))
	w := a.request("PUT", "/api/v1/editions/2026-10-30", strings.Replace(fixture(t), "The week ahead for household budgets", "Legacy replacement", 1), a.key)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	a.config.Papers[0].AgentRole = "none"
	w = a.request("PUT", "/api/v1/editions/2026-10-30", fixture(t), a.key)
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestPublishChecksRevisionAfterImageFetch(t *testing.T) {
	var a *app
	mutate := false
	a = setup(t, fetchFunc(func(context.Context, string) (images.Result, error) {
		if mutate {
			ed, err := edition.Decode(strings.NewReader(fixture(t)))
			if err != nil {
				return images.Result{}, err
			}
			ed.Sections[0].Items[0].Title = "New draft during image fetch"
			_, err = a.db.SubmitDraft(context.Background(), a.scope, "2026-10-30", a.id, ed)
			if err != nil {
				return images.Result{}, err
			}
		}
		return images.Result{SHA256: strings.Repeat("a", 64), Width: 1, Height: 1, Bytes: 10}, nil
	}))
	path := "/api/v1/drafts/2026-10-30"
	draftRevision(t, a.request("PUT", path, fixture(t), a.key))
	mutate = true
	w := a.request("POST", path+"/publish", `{"revision":1}`, a.key)
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	exists, err := a.db.RunPublished(context.Background(), "daily", "2026-10-30")
	if err != nil || exists {
		t.Fatal("stale publication wrote an edition", exists, err)
	}
	d, err := a.db.Draft(context.Background(), a.scope, "2026-10-30")
	if err != nil || d.Revision != 2 || d.Body.Sections[0].Items[0].Title != "New draft during image fetch" {
		t.Fatal(d, err)
	}
}
func TestDraftActionsAndStrictValidation(t *testing.T) {
	a := setup(t, nil)
	path := "/api/v1/drafts/2026-10-30"
	draftRevision(t, a.request("PUT", path, fixture(t), a.key))
	tests := []struct{ body, path string }{
		{`{"revision":1,"action":"unknown"}`, "action"},
		{`{"action":"approve"}`, "revision"},
		{`{"revision":1,"action":"approve","extra":true}`, "extra"},
		{`{"revision":1,"action":"cut","item":"missing"}`, "item"},
		{`{"revision":1,"action":"cut","item":"economy-1"}`, "lead"},
		{`{"revision":1,"action":"swap","item":"economy-1","with":{"unknown":1}}`, "with.unknown"},
		{`{"revision":1,"action":"swap","item":"economy-1","with":{"id":"new","kind":"news","title":"Title","summary":"Summary","source":"Desk","url":"https://example.com/story","unknown":1}}`, "with.unknown"},
		{`{"revision":1,"action":"swap","item":"economy-1"}`, "with"},
		{`{"revision":1,"action":"swap","item":"economy-1","with":null}`, "with"},
		{`{"revision":1,"action":"edit","item":"economy-1","title":true}`, "title"},
		{`{"revision":1,"action":"edit","item":"economy-1","fields":{"title":true}}`, "fields.title"},
		{`{"revision":1,"action":"edit","item":"economy-1","fields":{"id":"changed"}}`, "fields.id"},
		{`{"revision":1,"action":"edit","item":"economy-1","title":"<b>bad</b>"}`, "sections[0].items[0].title"},
		{`{"revision":1,"action":"note","text":"[link](https://example.com)"}`, "text"},
		{`{"revision":1,"action":"send_back","request":""}`, "request"},
	}
	for _, tc := range tests {
		w := a.request("POST", path+"/review", tc.body, a.key)
		if w.Code != 422 || !strings.Contains(w.Body.String(), `"path":"`+tc.path+`"`) {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
	rev := draftRevision(t, a.request("POST", path+"/review", `{"revision":1,"action":"swap","item":"economy-1","with":{"id":"new-id","kind":"news","title":"New story","summary":"A summary.","source":"Desk","url":"https://example.com/new"}}`, a.key))
	rev = draftRevision(t, a.request("POST", path+"/review", fmt.Sprintf(`{"revision":%d,"action":"edit","item":"new-id","fields":{"summary":"Changed summary."}}`, rev), a.key))
	rev = draftRevision(t, a.request("POST", path+"/review", fmt.Sprintf(`{"revision":%d,"action":"note","text":"A review note."}`, rev), a.key))
	rev = draftRevision(t, a.request("POST", path+"/review", fmt.Sprintf(`{"revision":%d,"action":"send_back","request":"Check the source."}`, rev), a.key))
	d, err := a.db.Draft(context.Background(), a.scope, "2026-10-30")
	if err != nil || d.State != "drafted" || d.Body.Lead != "new-id" || d.Revision != rev || d.Actions[len(d.Actions)-1].Note != "Check the source." {
		t.Fatal(d, err)
	}
	// Cut the lead while another item remains, preserving a valid lead.
	ed := d.Body
	ed.Sections[0].Items = append(ed.Sections[0].Items, edition.Item{ID: "second", Kind: "news", Title: "Second", Summary: "Summary", Source: "Desk", URL: "https://example.com/second"})
	rev, err = a.db.SubmitDraft(context.Background(), a.scope, "2026-10-30", a.id, ed)
	if err != nil {
		t.Fatal(err)
	}
	draftRevision(t, a.request("POST", path+"/review", fmt.Sprintf(`{"revision":%d,"action":"cut","item":"new-id"}`, rev), a.key))
	d, _ = a.db.Draft(context.Background(), a.scope, "2026-10-30")
	if d.Body.Lead != "second" {
		t.Fatal(d)
	}
}
func TestDraftTenantIsolation(t *testing.T) {
	a := setup(t, nil)
	path := "/api/v1/drafts/2026-10-30"
	draftRevision(t, a.request("PUT", path, fixture(t), a.key))
	other := a.config.Papers[0]
	other.ID = "other"
	a.config.Papers = append(a.config.Papers, other)
	_, key, err := keys.Issue(context.Background(), a.db, "other", "other")
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "/review", "/publish"} {
		method, body := "GET", ""
		if suffix != "" {
			method = "POST"
			body = `{"revision":1,"action":"approve"}`
			if suffix == "/publish" {
				body = `{"revision":1}`
			}
		}
		w := a.request(method, path+suffix, body, key)
		if w.Code != 404 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
func TestReaderNotesAPI(t *testing.T) {
	a := setup(t, nil)
	path := "/api/v1/paper/notes"
	w := a.request("GET", path, "", a.key)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"revision":0`) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, text := range []string{"", " trimmed ", "<em>html</em>", "[a](https://example.com)", "line\nbreak", strings.Repeat("界", 4001)} {
		raw, _ := json.Marshal(map[string]string{"text": text})
		w = a.request("PUT", path, string(raw), a.key)
		if w.Code != 422 || !strings.Contains(w.Body.String(), `"path":"text"`) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for rev := 1; rev <= 2; rev++ {
		if got := draftRevision(t, a.request("PUT", path, `{"text":"Prefer local reporting."}`, a.key)); got != int64(rev) {
			t.Fatal(got)
		}
	}
	w = a.request("GET", "/api/v1/paper", "", a.key)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"reader_notes":"Prefer local reporting."`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = a.request("PUT", path, `{"text":"Good.","extra":true}`, a.key)
	if w.Code != 422 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = a.request("PUT", path, `{"text":"Good."} {}`, a.key)
	if w.Code != 422 {
		t.Fatal(w.Code, w.Body.String())
	}
}
