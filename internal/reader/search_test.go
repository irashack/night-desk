package reader

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/store"
)

func TestNavigationAndLayoutMarkup(t *testing.T) {
	f := setup(t)
	f.publish(t, f.a, "2026-10-27", sampleEdition(), nil)
	f.signIn(t, "").Body.Close()
	html := body(t, f.get(t, "/"))
	if strings.Contains(html, "‹ Front page") || strings.Contains(html, `turnbar js-only`) {
		t.Fatal("duplicate or JavaScript-only navigation")
	}
	for _, layout := range []string{"pages", "list", "reader"} {
		if !strings.Contains(html, `data-layout="`+layout+`"`) {
			t.Errorf("missing %s layout choice", layout)
		}
	}
	if !strings.Contains(html, `class="menu-toggle js-only" aria-expanded="false" aria-controls="menu"`) || !strings.Contains(html, `<div class="menu" id="menu">`) {
		t.Fatal("missing menu")
	}
	if strings.Contains(html, "Keep controls visible") || strings.Contains(html, "Show controls") {
		t.Fatal("controls no longer retract, so nothing restores or pins them")
	}
	bars := regexp.MustCompile(`<nav class="turnbar"[^>]*>(.*?)</nav>`).FindAllStringSubmatch(html, -1)
	if len(bars) != 3 {
		t.Fatalf("got %d page bars, want 3", len(bars))
	}
	for i, want := range []struct{ previous, next string }{{"", "#section-economy"}, {"#front", "#section-faith"}, {"#section-economy", ""}} {
		bar := bars[i][1]
		for _, direction := range []struct{ rel, target string }{{"prev", want.previous}, {"next", want.next}} {
			if direction.target == "" {
				if strings.Contains(bar, `rel="`+direction.rel+`"`) {
					t.Errorf("page %d has unexpected %s link", i, direction.rel)
				}
			} else if !strings.Contains(bar, `href="`+direction.target+`" rel="`+direction.rel+`"`) {
				t.Errorf("page %d missing native %s link to %s", i, direction.rel, direction.target)
			}
		}
		if !strings.Contains(bar, `<a href="#front" class="where"`) || strings.Contains(bar, "<button") {
			t.Errorf("page %d needs native front link", i)
		}
	}
	if !strings.Contains(html, `id="item-story-a"`) || strings.Count(html, `id="item-story-a"`) != 1 {
		t.Fatal("search target must uniquely identify the section's story")
	}
}

func TestArchiveSearchIsolationSavedAndReplacement(t *testing.T) {
	f := setup(t)
	r := f.get(t, "/search?q=orchard")
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatal("unauthenticated search accepted", r.StatusCode)
	}
	r.Body.Close()
	f.cfg.Papers[0].Readers = append(f.cfg.Papers[0].Readers, config.Reader{Issuer: f.p.server.URL, Subject: "another-reader"})
	ed := sampleEdition()
	ed.Sections[0].Items[0].Title = "Orchard belonging to another reader"
	ed.Sections[1].Items[0].Title = "Orchard belonging to this reader"
	why := "recommendationneedle"
	ed.Sections[1].Items[0].Why = &why
	ed.Sections[1].Items[0].Summary = "summaryneedle"
	ed.Sections[1].Items[0].Source = "sourceneedle"
	f.publish(t, f.a, "2026-10-27", ed, nil)
	foreign := sampleEdition()
	foreign.Sections[1].Items[0].Title = "Orchard belonging to another paper"
	f.publish(t, f.b, "2026-10-27", foreign, nil)
	f.signIn(t, "").Body.Close()
	session, _ := f.session(t)
	save := func(scopePaper bool, issuer, subject, item, kind string) {
		t.Helper()
		scope := f.a
		if !scopePaper {
			scope = f.b
		}
		if err := f.db.AddFeedback(context.Background(), scope, store.Feedback{Date: "2026-10-27", ItemID: item, Kind: kind, ReaderIssuer: issuer, ReaderSubject: subject}); err != nil {
			t.Fatal(err)
		}
	}
	// Match both identity components, the paper, date and item. A later vote
	// must not erase a save, and a later unsave must erase it.
	save(true, session.Issuer, "another-reader", "faith-a", "save")
	save(true, "https://other-issuer.test", session.Subject, "faith-a", "save")
	save(false, session.Issuer, session.Subject, "story-a", "save")
	save(true, session.Issuer, session.Subject, "story-a", "save")
	save(true, session.Issuer, session.Subject, "story-a", "down")
	for _, path := range []string{"/search?q=orchard", "/search?q=orchard&paper=b", "/search?q=orchard&saved=1", "/search?saved=1", "/search?q=summaryneedle", "/search?q=recommendationneedle", "/search?q=sourceneedle"} {
		html := body(t, f.get(t, path))
		if !strings.Contains(html, `/e/2026-10-27#item-story-a`) || strings.Contains(html, "Orchard belonging to another paper") {
			t.Errorf("missing own result or leaked paper at %s", path)
		}
		if strings.Contains(path, "saved=1") && strings.Contains(html, `/e/2026-10-27#item-faith-a`) {
			t.Errorf("leaked another reader's save at %s", path)
		}
	}
	save(true, session.Issuer, session.Subject, "story-a", "unsave")
	if html := body(t, f.get(t, "/search?saved=1")); strings.Contains(html, `/e/2026-10-27#item-`) {
		t.Fatal("unsaved item remained in saved search")
	}
	// A second actual session in the same paper sees only its own save.
	f.p.subject = "another-reader"
	f.signIn(t, "").Body.Close()
	html := body(t, f.get(t, "/search?q=orchard&saved=1"))
	if !strings.Contains(html, `/e/2026-10-27#item-faith-a`) || strings.Contains(html, `/e/2026-10-27#item-story-a`) {
		t.Fatal("second reader did not get its own saved items")
	}
	// Replace the edition, including removing an item. Neither stale text nor
	// removed stories may survive in the index; repeated PUTs stay idempotent.
	ed.Sections = ed.Sections[1:]
	ed.Sections[0].Items[0].Title = "Replacement report"
	for i := 0; i < 2; i++ {
		f.publish(t, f.a, "2026-10-27", ed, nil)
	}
	if html := body(t, f.get(t, "/search?q=orchard")); strings.Contains(html, `/e/2026-10-27#item-`) {
		t.Fatal("replacement retained stale index rows")
	}
	html = body(t, f.get(t, "/search?q=replacement"))
	if strings.Count(html, `/e/2026-10-27#item-story-a`) != 1 {
		t.Fatal("replacement missing or duplicated")
	}
	archive := body(t, f.get(t, "/archive"))
	for _, want := range []string{`action="/search"`, `name="q"`, `name="saved"`, `value="1"`} {
		if !strings.Contains(archive, want) {
			t.Errorf("archive search missing %s", want)
		}
	}
}

func TestSearchTreatsSyntaxAsWords(t *testing.T) {
	f := setup(t)
	ed := sampleEdition()
	ed.Sections[1].Items[0].Title = "Café OR orchard"
	f.publish(t, f.a, "2026-10-27", ed, nil)
	f.signIn(t, "").Body.Close()
	for _, tc := range []struct {
		query string
		found bool
	}{{"café", true}, {"orchard OR absent", false}, {`"orchard"`, true}, {`title:orchard`, false}, {`orchard NOT absent`, false}, {`*** () \"`, false}, {`<script>alert(1)</script>`, false}, {"", false}} {
		r := f.get(t, "/search?q="+url.QueryEscape(tc.query))
		if r.StatusCode != 200 {
			t.Errorf("%q returned %d", tc.query, r.StatusCode)
		}
		html := body(t, r)
		if strings.Contains(html, `/e/2026-10-27#item-story-a`) != tc.found {
			t.Errorf("%q: unexpected matches", tc.query)
		}
		if strings.Contains(html, "<script>alert") {
			t.Fatal("search query was not escaped")
		}
	}
	r := f.get(t, "/search?q="+strings.Repeat("a", 501))
	if r.StatusCode != 400 || !strings.Contains(body(t, r), "q: must be at most 500 characters") {
		t.Fatal("unbounded query accepted or validation path missing")
	}
}

func TestSearchRejectsInvalidFilterAndRemovedReader(t *testing.T) {
	f := setup(t)
	f.signIn(t, "").Body.Close()
	r := f.get(t, "/search?saved=someone")
	if r.StatusCode != http.StatusBadRequest || !strings.Contains(body(t, r), "saved: must be 1 or omitted") {
		t.Fatal("invalid saved filter accepted")
	}
	f.cfg.Papers[0].Readers = nil
	r = f.get(t, "/search?q=news")
	defer r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Fatal("removed reader retained search access", r.StatusCode)
	}
}
