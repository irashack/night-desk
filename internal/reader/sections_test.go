package reader

import (
	"context"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/editorial"
)

// editing gives the fixture's reader server an editorial desk over its papers,
// as serve does.
func (f *fixture) editing(t *testing.T) *editorial.Desk {
	t.Helper()
	f.cfg.Papers[0].Sections[0] = config.Section{Key: "economy", Title: "Economy", Required: true, Lean: true, Sources: []config.Source{{Feed: "https://example.test/economy.xml"}}}
	desk, err := editorial.Open(context.Background(), f.db, func() *config.Config { return f.cfg }, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.s.Config, f.s.Editorial = desk.Config, desk
	return desk
}

func (f *fixture) sectionsOf(t *testing.T, paper string) []config.Section {
	t.Helper()
	p, ok := f.s.Config().Paper(paper)
	if !ok {
		t.Fatal("no paper", paper)
	}
	return p.Sections
}

func revisions(t *testing.T, desk *editorial.Desk, scope auth.PaperScope) int {
	t.Helper()
	revs, err := desk.Revisions(context.Background(), scope, 50)
	if err != nil {
		t.Fatal(err)
	}
	return len(revs)
}

func sectionKeys(sections []config.Section) string {
	var out []string
	for _, s := range sections {
		out = append(out, s.Key)
	}
	return strings.Join(out, ",")
}

// edit posts one Sections action and expects a redirect to the confirmation.
func (f *fixture) edit(t *testing.T, csrf string, values url.Values) {
	t.Helper()
	values.Set("csrf", csrf)
	r := f.post(t, "/sections", values)
	text := body(t, r)
	if r.StatusCode != 303 || !strings.HasPrefix(r.Header.Get("Location"), "/sections?done=") {
		t.Fatalf("%s: %d %s", values.Get("action"), r.StatusCode, text)
	}
}

func TestSectionsEditor(t *testing.T) {
	f := setup(t)
	desk := f.editing(t)
	declared := f.sectionsOf(t, "a")[0]
	r := f.signIn(t, "")
	r.Body.Close()
	session, _ := f.session(t)
	html := body(t, f.get(t, "/"))
	if !strings.Contains(html, `<a href="/sections">Sections</a>`) {
		t.Fatal("Menu lacks the Sections link")
	}
	r = f.get(t, "/sections")
	html = body(t, r)
	if r.StatusCode != 200 || !strings.Contains(html, `value="Economy"`) || !strings.Contains(html, `value="Faith"`) || !strings.Contains(html, "find their own sources on the next run") || strings.Contains(html, "style=") {
		t.Fatalf("sections page: %d %s", r.StatusCode, html)
	}

	f.edit(t, session.CSRF, url.Values{"action": {"add"}, "title": {" Local Arts "}, "brief": {"Galleries, music and theater nearby."}})
	sections := f.sectionsOf(t, "a")
	added := sections[2]
	if sectionKeys(sections) != "economy,faith,local-arts" || added.Title != "Local Arts" || added.Brief == nil || *added.Brief != "Galleries, music and theater nearby." || !added.FindSources || added.Sources != nil || added.Lean || added.Required {
		t.Fatalf("added section: %+v", added)
	}
	if n := revisions(t, desk, f.a); n != 1 {
		t.Fatal("add revisions", n)
	}
	r = f.get(t, "/sections?done=added")
	if html = body(t, r); !strings.Contains(html, "Section added") {
		t.Fatal("no confirmation")
	}

	f.edit(t, session.CSRF, url.Values{"action": {"add"}, "title": {"Economy"}})
	if sectionKeys(f.sectionsOf(t, "a")) != "economy,faith,local-arts,economy-2" {
		t.Fatal("duplicate title key", sectionKeys(f.sectionsOf(t, "a")))
	}

	f.edit(t, session.CSRF, url.Values{"action": {"save"}, "key": {"economy"}, "title": {"Money"}, "brief": {"Markets and wages."}})
	f.edit(t, session.CSRF, url.Values{"action": {"down"}, "key": {"economy"}})
	f.edit(t, session.CSRF, url.Values{"action": {"up"}, "key": {"economy-2"}})
	sections = f.sectionsOf(t, "a")
	if sectionKeys(sections) != "faith,economy,economy-2,local-arts" {
		t.Fatal("reorder", sectionKeys(sections))
	}
	money := sections[1]
	if money.Title != "Money" || *money.Brief != "Markets and wages." || !money.Lean || !money.Required || money.FindSources || !reflect.DeepEqual(money.Sources, declared.Sources) {
		t.Fatalf("rename lost declared fields: %+v", money)
	}
	if n := revisions(t, desk, f.a); n != 5 {
		t.Fatal("revisions after rename and moves", n)
	}

	// Moving the first section up changes nothing and saves nothing.
	r = f.post(t, "/sections", url.Values{"csrf": {session.CSRF}, "action": {"up"}, "key": {"faith"}})
	r.Body.Close()
	if n := revisions(t, desk, f.a); n != 5 {
		t.Fatal("no-op move saved", n)
	}

	r = f.post(t, "/sections", url.Values{"csrf": {session.CSRF}, "action": {"remove"}, "key": {"economy-2"}})
	html = body(t, r)
	if r.StatusCode != 200 || !strings.Contains(html, `name="confirm" value="1"`) || len(f.sectionsOf(t, "a")) != 4 || revisions(t, desk, f.a) != 5 {
		t.Fatalf("remove without confirm: %d %s", r.StatusCode, html)
	}
	f.edit(t, session.CSRF, url.Values{"action": {"remove"}, "key": {"economy-2"}, "confirm": {"1"}})
	if sectionKeys(f.sectionsOf(t, "a")) != "faith,economy,local-arts" || revisions(t, desk, f.a) != 6 {
		t.Fatal("remove", sectionKeys(f.sectionsOf(t, "a")))
	}
	if sectionKeys(f.sectionsOf(t, "b")) != "economy" || revisions(t, desk, f.b) != 0 {
		t.Fatal("paper b changed")
	}
}

func TestSectionsEditorRefusesBadInput(t *testing.T) {
	f := setup(t)
	desk := f.editing(t)
	r := f.signIn(t, "")
	r.Body.Close()
	session, _ := f.session(t)
	for _, tc := range []struct {
		name   string
		values url.Values
		want   []string
	}{
		{"empty title", url.Values{"action": {"add"}, "title": {"  "}, "brief": {"Kept brief"}}, []string{"sections[2].title: must be trimmed non-empty text", `value="Kept brief"`}},
		{"long brief", url.Values{"action": {"save"}, "key": {"faith"}, "title": {"Belief"}, "brief": {strings.Repeat("x", 601)}}, []string{"sections[1].brief: must be at most 600 characters", `value="Belief"`}},
		{"html brief", url.Values{"action": {"add"}, "title": {"Arts"}, "brief": {"<b>bold</b> claims"}}, []string{"sections[2].brief: must be plain text (no HTML tags or Markdown links)", `value="Arts"`, `value="&lt;b&gt;bold&lt;/b&gt; claims"`}},
		{"control character", url.Values{"action": {"save"}, "key": {"economy"}, "title": {"Mon\x00ey"}}, []string{"sections[0].title: must be trimmed non-empty text"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.values.Set("csrf", session.CSRF)
			r := f.post(t, "/sections", tc.values)
			html := body(t, r)
			if r.StatusCode != 422 {
				t.Fatalf("%d %s", r.StatusCode, html)
			}
			for _, want := range tc.want {
				if !strings.Contains(html, want) {
					t.Errorf("missing %s", want)
				}
			}
			if strings.Contains(html, "<b>bold") {
				t.Fatal("brief not escaped")
			}
		})
	}
	if n := revisions(t, desk, f.a); n != 0 {
		t.Fatal("refused edits saved", n)
	}
	r = f.post(t, "/sections", url.Values{"csrf": {session.CSRF}, "action": {"remove"}, "key": {"economy"}, "confirm": {"1"}})
	r.Body.Close()
	r = f.post(t, "/sections", url.Values{"csrf": {session.CSRF}, "action": {"remove"}, "key": {"faith"}, "confirm": {"1"}})
	if html := body(t, r); r.StatusCode != 422 || !strings.Contains(html, "sections: must contain at least one section") {
		t.Fatalf("last section removed: %d %s", r.StatusCode, html)
	}
	for i := 0; len(f.sectionsOf(t, "a")) < config.MaxSections; i++ {
		f.edit(t, session.CSRF, url.Values{"action": {"add"}, "title": {"Extra"}})
	}
	r = f.post(t, "/sections", url.Values{"csrf": {session.CSRF}, "action": {"add"}, "title": {"Seventeenth"}})
	if html := body(t, r); r.StatusCode != 422 || !strings.Contains(html, "sections: must contain at most 16 sections") || !strings.Contains(html, `value="Seventeenth"`) {
		t.Fatalf("17th section: %d %s", r.StatusCode, html)
	}
	if len(f.sectionsOf(t, "a")) != config.MaxSections || sectionKeys(f.sectionsOf(t, "a"))[:len("faith,extra,extra-2")] != "faith,extra,extra-2" {
		t.Fatal("sections", sectionKeys(f.sectionsOf(t, "a")))
	}
	for _, values := range []url.Values{{"action": {"save"}, "key": {"gone"}, "title": {"Gone"}}, {"action": {"up"}}} {
		values.Set("csrf", session.CSRF)
		if r = f.post(t, "/sections", values); r.StatusCode != 409 {
			t.Fatal("unknown key", r.StatusCode)
		}
		r.Body.Close()
	}
	if r = f.post(t, "/sections", url.Values{"csrf": {session.CSRF}, "action": {"rename-all"}, "key": {"faith"}}); r.StatusCode != 400 {
		t.Fatal("unknown action", r.StatusCode)
	}
	r.Body.Close()
}

func TestSectionsEditorAccess(t *testing.T) {
	t.Run("csrf and sign-in", func(t *testing.T) {
		f := setup(t)
		desk := f.editing(t)
		values := url.Values{"action": {"add"}, "title": {"Arts"}}
		r := f.get(t, "/sections")
		if r.StatusCode != 401 {
			t.Fatal("unauthenticated page", r.StatusCode)
		}
		r.Body.Close()
		if r = f.post(t, "/sections", values); r.StatusCode != 401 {
			t.Fatal("unauthenticated edit", r.StatusCode)
		}
		r.Body.Close()
		r = f.signIn(t, "")
		r.Body.Close()
		for _, csrf := range []string{"", "wrong"} {
			values.Set("csrf", csrf)
			if r = f.post(t, "/sections", values); r.StatusCode != 403 {
				t.Fatal("CSRF accepted", r.StatusCode)
			}
			r.Body.Close()
		}
		if n := revisions(t, desk, f.a); n != 0 {
			t.Fatal("refused edits saved", n)
		}
	})
	t.Run("tenant isolation", func(t *testing.T) {
		f := setup(t)
		desk := f.editing(t)
		f.p.subject = "reader-b"
		r := f.signIn(t, "")
		r.Body.Close()
		session, _ := f.session(t)
		html := body(t, f.get(t, "/sections"))
		if !strings.Contains(html, "The B Gazette") || strings.Contains(html, `value="Faith"`) {
			t.Fatal("paper b's reader sees paper a's sections")
		}
		// Paper a's keys mean nothing to paper b's reader.
		if r = f.post(t, "/sections", url.Values{"csrf": {session.CSRF}, "action": {"remove"}, "key": {"faith"}, "confirm": {"1"}}); r.StatusCode != 409 {
			t.Fatal("foreign key accepted", r.StatusCode)
		}
		r.Body.Close()
		f.edit(t, session.CSRF, url.Values{"action": {"save"}, "key": {"economy"}, "title": {"B Money"}})
		if f.sectionsOf(t, "a")[0].Title != "Economy" || sectionKeys(f.sectionsOf(t, "a")) != "economy,faith" || revisions(t, desk, f.a) != 0 {
			t.Fatal("paper a changed by paper b's reader")
		}
		if f.sectionsOf(t, "b")[0].Title != "B Money" || revisions(t, desk, f.b) != 1 {
			t.Fatal("paper b not changed")
		}
	})
	t.Run("editing unavailable", func(t *testing.T) {
		f := setup(t)
		r := f.signIn(t, "")
		r.Body.Close()
		session, _ := f.session(t)
		if html := body(t, f.get(t, "/archive")); strings.Contains(html, `href="/sections"`) {
			t.Fatal("Menu links to Sections without a desk")
		}
		if r = f.get(t, "/sections"); r.StatusCode != 404 {
			t.Fatal("page without a desk", r.StatusCode)
		}
		r.Body.Close()
		if r = f.post(t, "/sections", url.Values{"csrf": {session.CSRF}, "action": {"add"}, "title": {"Arts"}}); r.StatusCode != 404 {
			t.Fatal("edit without a desk", r.StatusCode)
		}
		r.Body.Close()
	})
}

func TestSectionKey(t *testing.T) {
	existing := []config.Section{{Key: "arts"}, {Key: "arts-2"}}
	for title, want := range map[string]string{"Arts": "arts-3", "Local  News & Views!": "local-news-views", "Café 24": "caf-24", "日本": "section", strings.Repeat("ab", 40): strings.Repeat("ab", 24)} {
		if got := sectionKey(title, existing); got != want {
			t.Errorf("%q: got %q want %q", title, got, want)
		}
	}
}

// TestSectionsEditorRefusesStaleRevision: a form computed from an older revision
// must not overwrite an edit saved meanwhile (issue #32).
func TestSectionsEditorRefusesStaleRevision(t *testing.T) {
	f := setup(t)
	desk := f.editing(t)
	r := f.signIn(t, "")
	r.Body.Close()
	session, _ := f.session(t)
	if html := body(t, f.get(t, "/sections")); !strings.Contains(html, `name="revision" value="0"`) {
		t.Fatalf("form lacks its revision: %s", html)
	}
	// The agent renames Faith while the reader's page is open.
	p, _ := desk.Config().Paper("a")
	ed := p.Editorial()
	ed.Sections = append([]config.Section(nil), ed.Sections...)
	ed.Sections[1].Title = "Belief"
	if _, problems, err := desk.Save(context.Background(), f.a, ed, 0); err != nil || problems != nil {
		t.Fatal(err, problems)
	}
	r = f.post(t, "/sections", url.Values{"csrf": {session.CSRF}, "revision": {"0"}, "action": {"save"}, "key": {"faith"}, "title": {"Faith and Values"}})
	html := body(t, r)
	if r.StatusCode != 409 || !strings.Contains(html, "changed since you opened this page") || !strings.Contains(html, `value="Belief"`) || !strings.Contains(html, `name="revision" value="1"`) {
		t.Fatalf("stale save: %d %s", r.StatusCode, html)
	}
	if got := f.sectionsOf(t, "a")[1].Title; got != "Belief" || revisions(t, desk, f.a) != 1 {
		t.Fatalf("stale form overwrote the agent's edit: %q", got)
	}
	// With the current revision the same save goes through.
	f.edit(t, session.CSRF, url.Values{"revision": {"1"}, "action": {"save"}, "key": {"faith"}, "title": {"Faith and Values"}})
	if got := f.sectionsOf(t, "a")[1].Title; got != "Faith and Values" {
		t.Fatal("current save", got)
	}
}
