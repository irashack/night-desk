package reader

import (
	"net/http/cookiejar"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/config"
)

// withShared adds paper c, read by the "family" group, with one edition.
func (f *fixture) withShared(t *testing.T) {
	t.Helper()
	lat, lon := 41.0, -87.0
	f.cfg.Papers = append(f.cfg.Papers, config.Paper{ID: "c", Masthead: "The Arcadia Daily", Timezone: "America/Chicago", Latitude: &lat, Longitude: &lon, Look: "broadsheet", Sections: []config.Section{{Key: "economy", Title: "Economy"}, {Key: "faith", Title: "Faith"}}, Readers: []config.Reader{{Issuer: f.p.server.URL, Group: "family"}}})
	f.publish(t, paperScope(t, f.db, "c"), "2026-11-20", sampleEdition(), nil)
}
func (f *fixture) newBrowser() {
	f.client.Jar, _ = cookiejar.New(nil)
}

func TestGroupScopeRequested(t *testing.T) {
	f := setup(t)
	r := f.signIn(t, "")
	r.Body.Close()
	if f.p.scope != "openid" {
		t.Fatalf("scope without group readers: %q", f.p.scope)
	}
	if s, _ := f.session(t); len(s.Groups) != 0 {
		t.Fatal("groups stored without group readers", s.Groups)
	}
	f.withShared(t)
	f.p.groups = []string{"family", "family", "chess"}
	f.newBrowser()
	r = f.signIn(t, "")
	r.Body.Close()
	if !slices.Equal(strings.Fields(f.p.scope), []string{"openid", "groups"}) {
		t.Fatalf("scope with a group reader: %q", f.p.scope)
	}
	if s, _ := f.session(t); !slices.Equal(s.Groups, []string{"family", "chess"}) {
		t.Fatal("groups claim not stored", s.Groups)
	}
	// A lone string counts as one group; a malformed claim grants no group but
	// does not block sign-in.
	f.p.groups = "family"
	f.newBrowser()
	r = f.signIn(t, "")
	r.Body.Close()
	if s, _ := f.session(t); !slices.Equal(s.Groups, []string{"family"}) {
		t.Fatal("string groups claim", s.Groups)
	}
	for _, bad := range []any{map[string]any{"family": true}, []any{1, 2}, []string{strings.Repeat("g", 257)}} {
		f.p.groups = bad
		f.newBrowser()
		if r = f.signIn(t, ""); r.StatusCode != 303 {
			t.Fatal("malformed groups claim blocked a subject reader", r.StatusCode)
		}
		r.Body.Close()
		if s, _ := f.session(t); len(s.Groups) != 0 {
			t.Fatal("malformed groups claim stored", s.Groups)
		}
	}
}

func TestSubjectReaderWithGroupPaper(t *testing.T) {
	f := setup(t)
	f.editing(t)
	f.withShared(t)
	f.publish(t, f.a, "2026-11-20", sampleEdition(), nil)
	f.p.groups = []string{"family"}
	r := f.signIn(t, "")
	r.Body.Close()
	session, _ := f.session(t)
	// The own paper stays at /, with a switcher to the shared one.
	html := body(t, f.get(t, "/"))
	if !strings.Contains(html, "The A Gazette") || !strings.Contains(html, `<a href="/p/c/">The Arcadia Daily</a>`) || !strings.Contains(html, `<a href="/" aria-current="page">The A Gazette</a>`) || !strings.Contains(html, `href="/sections"`) {
		t.Fatal("own paper or switcher missing")
	}
	r = f.get(t, "/p/c/")
	html = body(t, r)
	if r.StatusCode != 200 || !strings.Contains(html, "The Arcadia Daily") || !strings.Contains(html, `<meta name="base" content="/p/c">`) || !strings.Contains(html, `href="/p/c/archive"`) || strings.Contains(html, `href="/sections"`) {
		t.Fatal("shared paper page", r.StatusCode)
	}
	for _, path := range []string{"/p/c/e/2026-11-20", "/p/c/archive", "/p/c/search?q=researched"} {
		if r = f.get(t, path); r.StatusCode != 200 {
			t.Fatalf("%s: %d", path, r.StatusCode)
		}
		r.Body.Close()
	}
	// The default paper has one URL: /p/a/... redirects to /.
	for path, want := range map[string]string{"/p/a/": "/", "/p/a/e/2026-11-20": "/e/2026-11-20", "/p/a/search?q=x": "/search?q=x"} {
		if r = f.get(t, path); r.StatusCode != 303 || r.Header.Get("Location") != want {
			t.Fatalf("%s: %d %s", path, r.StatusCode, r.Header.Get("Location"))
		}
		r.Body.Close()
	}
	html = body(t, f.get(t, "/p/c/archive"))
	if !strings.Contains(html, `href="/p/c/e/2026-11-20"`) {
		t.Fatal("archive links leave the shared paper")
	}
	// Other papers stay closed, by id or by route.
	for _, path := range []string{"/p/b/", "/p/b/archive", "/p/missing/", "/p/c/sections"} {
		if r = f.get(t, path); r.StatusCode != 404 {
			t.Fatalf("%s: %d", path, r.StatusCode)
		}
		r.Body.Close()
	}
	// Feedback lands on the shared paper, not the own one.
	r = f.post(t, "/p/c/feedback", url.Values{"csrf": {session.CSRF}, "date": {"2026-11-20"}, "item": {"story-a"}, "kind": {"up"}})
	if r.StatusCode != 204 {
		t.Fatal("group feedback", r.StatusCode)
	}
	r.Body.Close()
	if html = body(t, f.get(t, "/p/c/e/2026-11-20")); !strings.Contains(html, `data-kind="up" aria-pressed="true"`) {
		t.Fatal("feedback not shown on the shared paper")
	}
	if html = body(t, f.get(t, "/e/2026-11-20")); strings.Contains(html, `data-kind="up" aria-pressed="true"`) {
		t.Fatal("shared paper feedback shown on the own paper")
	}
	// Past the group lifetime the own paper stays; the shared paper asks for
	// a fresh sign-in, which restores it while the reader is still a member.
	f.s.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	if r = f.get(t, "/p/c/"); r.StatusCode != 401 || !strings.Contains(body(t, r), `href="/login?next=%2Fp%2Fc%2F"`) {
		t.Fatal("stale group paper does not ask for sign-in", r.StatusCode)
	}
	if r = f.get(t, "/p/c/archive"); r.StatusCode != 401 {
		t.Fatal("stale group archive", r.StatusCode)
	}
	r.Body.Close()
	if html = body(t, f.get(t, "/")); !strings.Contains(html, "The A Gazette") || !strings.Contains(html, `<a href="/p/c/">The Arcadia Daily</a>`) {
		t.Fatal("own paper or refresh link after group expiry")
	}
	if r = f.get(t, "/p/b/"); r.StatusCode != 404 {
		t.Fatal("unread paper after group expiry", r.StatusCode)
	}
	r.Body.Close()
	// Signing in again as a non-member keeps only the own paper.
	f.s.now = time.Now
	f.p.groups = []string{"chess"}
	r = f.signIn(t, "")
	r.Body.Close()
	if r = f.get(t, "/p/c/"); r.StatusCode != 404 {
		t.Fatal("removed member reads the shared paper", r.StatusCode)
	}
	r.Body.Close()
}

func TestGroupOnlyReader(t *testing.T) {
	f := setup(t)
	f.editing(t)
	f.withShared(t)
	f.p.subject, f.p.groups = "reader-z", []string{"family"}
	r := f.signIn(t, "")
	r.Body.Close()
	session, _ := f.session(t)
	html := body(t, f.get(t, "/"))
	if !strings.Contains(html, "The Arcadia Daily") || strings.Contains(html, `class="papers"`) || strings.Contains(html, `href="/sections"`) || !strings.Contains(html, `<meta name="base" content="">`) {
		t.Fatal("group paper is not the default, or offers editing")
	}
	// Sections stay with subject readers.
	if r = f.get(t, "/sections"); r.StatusCode != 404 {
		t.Fatal("group reader opened sections", r.StatusCode)
	}
	r.Body.Close()
	if r = f.post(t, "/sections", url.Values{"csrf": {session.CSRF}, "action": {"add"}, "title": {"Gardening"}}); r.StatusCode != 404 {
		t.Fatal("group reader edited sections", r.StatusCode)
	}
	r.Body.Close()
	if len(f.sectionsOf(t, "c")) != 2 {
		t.Fatal("sections changed")
	}
	if r = f.get(t, "/p/a/"); r.StatusCode != 404 {
		t.Fatal("group reader opened a subject paper", r.StatusCode)
	}
	r.Body.Close()
	// Push works per paper: status, subscribe, unsubscribe.
	sub := subscriptionJSON("https://push.example.com/z")
	if code := f.pushPost(t, "/push/subscribe", session.CSRF, sub); code != 204 {
		t.Fatal("group reader subscribe", code)
	}
	r = f.post(t, "/push/status", url.Values{"csrf": {session.CSRF}, "subscription": {sub}})
	if html = body(t, r); !strings.Contains(html, `"on":true`) {
		t.Fatal("push status", html)
	}
	r = f.post(t, "/push/unsubscribe", url.Values{"csrf": {session.CSRF}, "subscription": {sub}})
	r.Body.Close()
	if r.StatusCode != 204 || r.Header.Get("X-Push-Remaining") != "0" {
		t.Fatal("unsubscribe", r.StatusCode, r.Header.Get("X-Push-Remaining"))
	}
	// Past the group lifetime the reader signs in again.
	f.s.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	if r = f.get(t, "/"); r.StatusCode != 401 {
		t.Fatal("stale group-only session kept", r.StatusCode)
	}
	r.Body.Close()
	// Without the group a reader is unmapped, as before, until membership is
	// stale; then sign-in is offered again, in case they have joined since.
	f.s.now = time.Now
	f.p.groups = []string{"chess"}
	f.newBrowser()
	r = f.signIn(t, "")
	r.Body.Close()
	if html = body(t, f.get(t, "/")); !strings.Contains(html, "No paper for you yet") {
		t.Fatal("reader outside the group got a paper")
	}
	f.s.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	if r = f.get(t, "/"); r.StatusCode != 401 {
		t.Fatal("stale unmapped session not offered sign-in", r.StatusCode)
	}
	r.Body.Close()
}

func TestPushPerPaper(t *testing.T) {
	f := setup(t)
	f.withShared(t)
	f.p.groups = []string{"family"}
	r := f.signIn(t, "")
	r.Body.Close()
	session, token := f.session(t)
	sub := subscriptionJSON("https://push.example.com/one")
	status := func(base string) string {
		return body(t, f.post(t, base+"/push/status", url.Values{"csrf": {session.CSRF}, "subscription": {sub}}))
	}
	if code := f.pushPost(t, "/p/c/push/subscribe", session.CSRF, sub); code != 204 {
		t.Fatal("subscribe to the shared paper", code)
	}
	if !strings.Contains(status("/p/c"), `"on":true`) || !strings.Contains(status(""), `"on":false`) {
		t.Fatal("status not per paper")
	}
	if code := f.pushPost(t, "/push/subscribe", session.CSRF, sub); code != 204 || f.devices(t, token) != 1 {
		t.Fatal("subscribe to the own paper", code)
	}
	r = f.post(t, "/p/c/push/unsubscribe", url.Values{"csrf": {session.CSRF}, "subscription": {sub}})
	r.Body.Close()
	if r.Header.Get("X-Push-Remaining") != "1" || !strings.Contains(status(""), `"on":true`) {
		t.Fatal("unsubscribing one paper dropped the other")
	}
	if code := f.pushPost(t, "/p/b/push/subscribe", session.CSRF, sub); code != 404 {
		t.Fatal("subscribed to an unread paper", code)
	}
}

// A session from before group readers asks for the shared paper: sign in
// again and land back on it. Unrelated papers keep their 404.
func TestPreGroupSessionSignsInAgain(t *testing.T) {
	f := setup(t)
	f.publish(t, f.a, "2026-11-20", sampleEdition(), nil)
	r := f.signIn(t, "") // no group readers yet: the sign-in never asks for groups
	r.Body.Close()
	if s, _ := f.session(t); s.GroupsChecked {
		t.Fatal("groups marked checked without group readers")
	}
	f.withShared(t)
	f.p.groups = []string{"family"}
	r = f.get(t, "/p/c/e/2026-11-20?x=1")
	html := body(t, r)
	if r.StatusCode != 401 || !strings.Contains(html, `href="/login?next=%2Fp%2Fc%2Fe%2F2026-11-20%3Fx%3D1"`) || !strings.Contains(html, "Sign in again to read this paper.") {
		t.Fatal("pre-group session not asked to sign in", r.StatusCode)
	}
	for _, path := range []string{"/p/b/", "/p/missing/"} {
		if r = f.get(t, path); r.StatusCode != 404 {
			t.Fatalf("%s: %d", path, r.StatusCode)
		}
		r.Body.Close()
	}
	if html = body(t, f.get(t, "/")); !strings.Contains(html, "The A Gazette") || strings.Contains(html, "/p/c/") {
		t.Fatal("own paper changed, or the unknown paper offered in the switcher")
	}
	// Signing in from that page lands back on the paper page asked for.
	r = f.get(t, "/login?next=%2Fp%2Fc%2Fe%2F2026-11-20")
	dest := r.Header.Get("Location")
	r.Body.Close()
	r, _ = f.client.Get(dest)
	callback := r.Header.Get("Location")
	r.Body.Close()
	r, _ = f.client.Get(callback)
	r.Body.Close()
	if r.StatusCode != 303 || r.Header.Get("Location") != "/p/c/e/2026-11-20" {
		t.Fatal("sign-in did not return to the paper", r.StatusCode, r.Header.Get("Location"))
	}
	if s, _ := f.session(t); !s.GroupsChecked || !slices.Equal(s.Groups, []string{"family"}) {
		t.Fatal("new session groups", s.Groups, s.GroupsChecked)
	}
	if r = f.get(t, "/p/c/"); r.StatusCode != 200 {
		t.Fatal("shared paper after signing in again", r.StatusCode)
	}
	r.Body.Close()
	// Signed in with current groups that lack the paper: a plain 404.
	f.p.groups = []string{"chess"}
	r = f.signIn(t, "")
	r.Body.Close()
	if r = f.get(t, "/p/c/"); r.StatusCode != 404 {
		t.Fatal("non-member asked to sign in again", r.StatusCode)
	}
	r.Body.Close()
}

func TestReturnPath(t *testing.T) {
	for in, want := range map[string]string{
		"/p/c/":                    "/p/c/",
		"/p/c/e/2026-11-20?x=1":    "/p/c/e/2026-11-20?x=1",
		"/p/arcadia-daily/archive": "/p/arcadia-daily/archive",
		"/":                        "",
		"/sections":                "",
		"//evil.example/p/c/":      "",
		"https://evil.example/p/":  "",
		"/p/c":                     "",
		"/p/../sections":           "",
		"/p/c/../../sections":      "",
		"/p/c/\\evil":              "",
		"/p/C/":                    "",
	} {
		if got := returnPath(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

// A story notification opens the paper that announced the newest edition among
// those the reader turned notifications on for, not always the default (#34).
func TestNotificationOpensTheNotifiedPaper(t *testing.T) {
	f := setup(t)
	f.withShared(t)
	f.p.groups = []string{"family"}
	f.publish(t, f.a, "2026-11-19", sampleEdition(), nil)
	r := f.signIn(t, "")
	r.Body.Close()
	session, _ := f.session(t)
	sub := subscriptionJSON("https://push.example.com/one")
	for _, path := range []string{"/push/subscribe", "/p/c/push/subscribe"} {
		if code := f.pushPost(t, path, session.CSRF, sub); code != 204 {
			t.Fatal("subscribe", path, code)
		}
	}
	open := func() string {
		t.Helper()
		r := f.get(t, "/push/open")
		r.Body.Close()
		if r.StatusCode != 303 {
			t.Fatal("open status", r.StatusCode)
		}
		return r.Header.Get("Location")
	}
	f.publish(t, paperScope(t, f.db, "c"), "2026-11-21", sampleEdition(), nil)
	if got := open(); got != "/p/c/" {
		t.Fatalf("group paper's notification opened %q", got)
	}
	time.Sleep(5 * time.Millisecond)
	f.publish(t, f.a, "2026-11-22", sampleEdition(), nil)
	if got := open(); got != "/" {
		t.Fatalf("default paper's notification opened %q", got)
	}
	// The group paper notifies again; then the default paper backfills an
	// earlier date, which notifies no one and must not take the click.
	time.Sleep(5 * time.Millisecond)
	f.publish(t, paperScope(t, f.db, "c"), "2026-11-23", sampleEdition(), nil)
	time.Sleep(5 * time.Millisecond)
	f.publish(t, f.a, "2026-11-20", sampleEdition(), nil)
	if got := open(); got != "/p/c/" {
		t.Fatalf("a backfill took the notification click: %q", got)
	}
}
