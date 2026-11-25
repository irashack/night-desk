package reader

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/store"
)

// modeFixture serves the reader in a sign-in mode without an OIDC provider.
type modeFixture struct {
	dir  string
	s    *Server
	db   *store.Store
	cfg  *config.Config
	http *httptest.Server
}

func modeSetup(t *testing.T, auth config.Auth, papers ...config.Paper) *modeFixture {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(papers) == 0 {
		papers = []config.Paper{{ID: "a", Masthead: "The A Gazette", Timezone: "America/Chicago", Look: "broadsheet", Sections: []config.Section{{Key: "news", Title: "News"}}}}
	}
	f := &modeFixture{dir: dir, db: db, cfg: &config.Config{Server: config.Server{Auth: auth, SessionLifetime: "720h", DataDir: t.TempDir()}, Papers: papers}}
	f.http = httptest.NewUnstartedServer(nil)
	f.cfg.Server.PublicBaseURL = "http://" + f.http.Listener.Addr().String()
	if auth.Local.Passkeys {
		// Passkeys need a domain name; localhost is one.
		_, port, _ := net.SplitHostPort(f.http.Listener.Addr().String())
		f.cfg.Server.PublicBaseURL = "http://localhost:" + port
	}
	f.s, err = New(context.Background(), db, func() *config.Config { return f.cfg }, "")
	if err != nil {
		t.Fatal(err)
	}
	f.http.Config.Handler = f.s.Handler()
	f.http.Start()
	t.Cleanup(f.http.Close)
	return f
}

// browser is one cookie jar that does not follow redirects.
type browser struct {
	t      *testing.T
	f      *modeFixture
	client *http.Client
	header http.Header
}

func (f *modeFixture) browser(t *testing.T) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, f: f, header: http.Header{}, client: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *browser) do(method, path string, form url.Values) (*http.Response, string) {
	b.t.Helper()
	var req *http.Request
	var err error
	if form != nil {
		req, err = http.NewRequest(method, b.f.http.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", b.f.cfg.Server.PublicBaseURL)
	} else {
		req, err = http.NewRequest(method, b.f.http.URL+path, nil)
	}
	if err != nil {
		b.t.Fatal(err)
	}
	for k, v := range b.header {
		req.Header[k] = v
	}
	r, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	return r, body(b.t, r)
}

func (b *browser) get(path string) (*http.Response, string) { return b.do("GET", path, nil) }
func (b *browser) post(path string, form url.Values) (*http.Response, string) {
	return b.do("POST", path, form)
}

var hiddenInput = regexp.MustCompile(`name="(form|csrf)" value="([^"]+)"`)

func hidden(t *testing.T, page, name string) string {
	t.Helper()
	for _, m := range hiddenInput.FindAllStringSubmatch(page, -1) {
		if m[1] == name {
			return m[2]
		}
	}
	t.Fatalf("no %s token in %q", name, page)
	return ""
}

var linkPattern = regexp.MustCompile(`/invite/[A-Za-z0-9_-]{43}`)

func expect(t *testing.T, r *http.Response, page string, status int, contains ...string) {
	t.Helper()
	if r.StatusCode != status {
		t.Fatalf("status %d, want %d: %s", r.StatusCode, status, page)
	}
	for _, c := range contains {
		if !strings.Contains(page, c) {
			t.Fatalf("page lacks %q: %s", c, page)
		}
	}
}

// signUp creates the first account through /setup.
func (b *browser) setup(username, password string) {
	b.t.Helper()
	_, page := b.get("/setup")
	r, page := b.post("/setup", url.Values{"form": {hidden(b.t, page, "form")}, "code": {b.f.s.SetupCode()}, "username": {username}, "password": {password}, "confirm": {password}})
	expect(b.t, r, page, http.StatusSeeOther)
}

func (b *browser) login(username, password string) (*http.Response, string) {
	b.t.Helper()
	_, page := b.get("/login")
	return b.post("/login", url.Values{"form": {hidden(b.t, page, "form")}, "username": {username}, "password": {password}})
}

func TestLocalFirstRunSetup(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthLocal})
	code := f.s.SetupCode()
	if len(code) != 15 {
		t.Fatalf("setup code %q", code)
	}
	b := f.browser(t)
	// Before any account, the paper sends readers to setup.
	r, _ := b.get("/")
	if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/setup" {
		t.Fatalf("/ before setup: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	_, page := b.get("/setup")
	form := hidden(t, page, "form")
	// A wrong code, a cross-site post and a weak password all fail.
	r, page = b.post("/setup", url.Values{"form": {form}, "code": {"aaa-bbb-ccc-ddd"}, "username": {"ada"}, "password": {"correct horse"}, "confirm": {"correct horse"}})
	expect(t, r, page, http.StatusUnauthorized, "setup code")
	r, page = b.post("/setup", url.Values{"form": {"forged"}, "code": {code}, "username": {"ada"}, "password": {"correct horse"}, "confirm": {"correct horse"}})
	expect(t, r, page, http.StatusForbidden)
	r, page = b.post("/setup", url.Values{"form": {form}, "code": {code}, "username": {"ada"}, "password": {"short"}, "confirm": {"short"}})
	expect(t, r, page, http.StatusBadRequest, "at least 10 characters")
	r, page = b.post("/setup", url.Values{"form": {form}, "code": {code}, "username": {"Ada Lovelace"}, "password": {"correct horse"}, "confirm": {"correct horse"}})
	expect(t, r, page, http.StatusBadRequest, "A username is")
	r, page = b.post("/setup", url.Values{"form": {form}, "code": {code}, "username": {"Ada"}, "password": {"correct horse"}, "confirm": {"correct horse"}})
	expect(t, r, page, http.StatusSeeOther)
	// Signed in at once, as the administrator, reading the paper whose
	// readers list is empty.
	r, page = b.get("/")
	expect(t, r, page, http.StatusOK, "The A Gazette", `href="/account"`, `href="/admin"`, "Sign out")
	if f.s.SetupCode() != "" {
		t.Fatal("setup code still valid after the first account")
	}
	accounts, err := f.db.Accounts(context.Background())
	if err != nil || len(accounts) != 1 || accounts[0].Username != "ada" || !accounts[0].Admin || !strings.HasPrefix(accounts[0].PasswordHash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatal(accounts, err)
	}
	// Setup is closed for good.
	other := f.browser(t)
	r, _ = other.get("/setup")
	if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/login" {
		t.Fatalf("setup after the first account: %d", r.StatusCode)
	}
	r, _ = other.post("/setup", url.Values{"form": {"x"}, "code": {code}, "username": {"eve"}, "password": {"correct horse"}, "confirm": {"correct horse"}})
	if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/login" {
		t.Fatalf("setup post after the first account: %d", r.StatusCode)
	}
}

func TestLocalPasswordSignIn(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthLocal})
	f.browser(t).setup("ada", "correct horse")
	b := f.browser(t)
	// Without a session the paper answers with the sign-in form (401).
	r, page := b.get("/")
	expect(t, r, page, http.StatusUnauthorized, `action="/login"`, `autocomplete="current-password"`)
	if strings.Contains(page, "passkey") {
		t.Fatal("passkey button without passkeys enabled")
	}
	r, page = b.login("ada", "wrong password")
	expect(t, r, page, http.StatusUnauthorized, "do not match")
	// An unknown account gets the same answer.
	r, page = b.login("nobody", "correct horse")
	expect(t, r, page, http.StatusUnauthorized, "do not match")
	// A post from another site is refused before checking the password.
	_, page = b.get("/login")
	b.header.Set("Origin", "https://evil.example")
	req, _ := http.NewRequest("POST", f.http.URL+"/login", strings.NewReader(url.Values{"form": {hidden(t, page, "form")}, "username": {"ada"}, "password": {"correct horse"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	r, err := b.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	expect(t, r, body(t, r), http.StatusForbidden)
	b.header.Del("Origin")
	r, page = b.login("ADA", "correct horse")
	expect(t, r, page, http.StatusSeeOther)
	r, page = b.get("/")
	expect(t, r, page, http.StatusOK, "The A Gazette")
	// Sign out ends the session.
	r, page = b.post("/logout", url.Values{"csrf": {hidden(t, page, "csrf")}})
	expect(t, r, page, http.StatusOK, "You are signed out")
	r, _ = b.get("/")
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after sign-out: %d", r.StatusCode)
	}
	// A return path survives sign-in, but never to another origin.
	_, page = b.get("/login?next=" + url.QueryEscape("/p/a/archive"))
	r, _ = b.post("/login", url.Values{"form": {hidden(t, page, "form")}, "username": {"ada"}, "password": {"correct horse"}, "next": {"/p/a/archive"}})
	if r.Header.Get("Location") != "/p/a/archive" {
		t.Fatalf("next: %s", r.Header.Get("Location"))
	}
	_, page = b.get("/login")
	r, _ = b.post("/login", url.Values{"form": {hidden(t, page, "form")}, "username": {"ada"}, "password": {"correct horse"}, "next": {"//evil.example/"}})
	if r.Header.Get("Location") != "/" {
		t.Fatalf("next to another origin: %s", r.Header.Get("Location"))
	}
}

func TestLocalSignInIsThrottled(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthLocal})
	f.browser(t).setup("ada", "correct horse")
	now := time.Now()
	f.s.limiter.SetClock(func() time.Time { return now })
	b := f.browser(t)
	for i := 0; i < 6; i++ {
		r, page := b.login("ada", "guess")
		expect(t, r, page, http.StatusUnauthorized)
	}
	// After the free failures even the right password waits.
	r, page := b.login("ada", "correct horse")
	expect(t, r, page, http.StatusTooManyRequests, "Too many attempts")
	now = now.Add(2 * time.Second)
	r, page = b.login("ada", "correct horse")
	expect(t, r, page, http.StatusSeeOther)
	// Success clears the account's count.
	r, page = b.login("ada", "guess")
	expect(t, r, page, http.StatusUnauthorized)
}

func TestLocalInvitesResetsAndRemoval(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthLocal})
	admin := f.browser(t)
	admin.setup("ada", "correct horse")
	_, page := admin.get("/admin")
	csrf := hidden(t, page, "csrf")
	r, page := admin.post("/admin/invite", url.Values{"csrf": {csrf}})
	expect(t, r, page, http.StatusOK, "Invite created", "1 unused link")
	link := linkPattern.FindString(page)
	if link == "" {
		t.Fatal("no invite link")
	}
	// The invite creates a reader, who is signed in at once.
	bob := f.browser(t)
	r, page = bob.get(link)
	expect(t, r, page, http.StatusOK, "You're invited")
	form := hidden(t, page, "form")
	r, page = bob.post(link, url.Values{"form": {form}, "username": {"ada"}, "password": {"battery staple"}, "confirm": {"battery staple"}})
	expect(t, r, page, http.StatusConflict, "taken")
	r, page = bob.post(link, url.Values{"form": {form}, "username": {"bob"}, "password": {"battery staple"}, "confirm": {"battery staple"}})
	expect(t, r, page, http.StatusSeeOther)
	r, page = bob.get("/")
	expect(t, r, page, http.StatusOK, "The A Gazette", `href="/account"`)
	if strings.Contains(page, `href="/admin"`) {
		t.Fatal("a reader sees the readers page link")
	}
	r, _ = bob.get("/admin")
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("reader on /admin: %d", r.StatusCode)
	}
	// The link works once.
	r, _ = f.browser(t).get(link)
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("used invite: %d", r.StatusCode)
	}
	// A reset link signs bob out everywhere once used; until then his
	// password still works.
	r, page = admin.post("/admin/reset", url.Values{"csrf": {csrf}, "username": {"bob"}})
	expect(t, r, page, http.StatusOK, "reset link for bob")
	reset := linkPattern.FindString(page)
	if r, _ := bob.get("/"); r.StatusCode != http.StatusOK {
		t.Fatal("a reset link alone signed bob out")
	}
	other := f.browser(t)
	_, page = other.get(reset)
	expect(t, r, page, http.StatusOK, "Choose a new password", "bob")
	r, page = other.post(reset, url.Values{"form": {hidden(t, page, "form")}, "password": {"new battery staple"}, "confirm": {"new battery staple"}})
	expect(t, r, page, http.StatusSeeOther)
	if r, _ := bob.get("/"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old session after reset: %d", r.StatusCode)
	}
	if r, _ := f.browser(t).login("bob", "battery staple"); r.StatusCode != http.StatusUnauthorized {
		t.Fatal("old password works after reset")
	}
	if r, _ := bob.login("bob", "new battery staple"); r.StatusCode != http.StatusSeeOther {
		t.Fatal("new password refused")
	}
	// Removal asks first, then ends bob's sessions.
	r, page = admin.post("/admin/remove", url.Values{"csrf": {csrf}, "username": {"bob"}})
	expect(t, r, page, http.StatusOK, "Remove bob")
	r, page = admin.post("/admin/remove", url.Values{"csrf": {csrf}, "username": {"bob"}, "confirm": {"1"}})
	expect(t, r, page, http.StatusOK, "bob is removed")
	if r, _ := bob.get("/"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("removed account's session: %d", r.StatusCode)
	}
	// The administrator cannot remove or demote themself, and a forged
	// token is refused.
	if r, _ := admin.post("/admin/remove", url.Values{"csrf": {csrf}, "username": {"ada"}, "confirm": {"1"}}); r.StatusCode != http.StatusNotFound {
		t.Fatalf("self removal: %d", r.StatusCode)
	}
	if r, _ := admin.post("/admin/invite", url.Values{"csrf": {"forged"}}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("forged csrf: %d", r.StatusCode)
	}
	// Revoking unused links.
	admin.post("/admin/invite", url.Values{"csrf": {csrf}})
	r, page = admin.post("/admin/revoke", url.Values{"csrf": {csrf}})
	expect(t, r, page, http.StatusOK, "revoked")
	if n, _ := f.db.PendingInvites(context.Background(), time.Now()); n != 0 {
		t.Fatal("invites left after revoke")
	}
}

func TestLocalInviteExpires(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthLocal, Local: config.LocalAuth{InviteLifetime: "1h"}})
	admin := f.browser(t)
	admin.setup("ada", "correct horse")
	_, page := admin.get("/admin")
	_, page = admin.post("/admin/invite", url.Values{"csrf": {hidden(t, page, "csrf")}})
	link := linkPattern.FindString(page)
	f.s.now = func() time.Time { return time.Now().Add(61 * time.Minute) }
	if r, _ := f.browser(t).get(link); r.StatusCode != http.StatusNotFound {
		t.Fatalf("expired invite: %d", r.StatusCode)
	}
}

func TestLocalChangePassword(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthLocal})
	b := f.browser(t)
	b.setup("ada", "correct horse")
	elsewhere := f.browser(t)
	elsewhere.login("ada", "correct horse")
	_, page := b.get("/account")
	csrf := hidden(t, page, "csrf")
	r, page := b.post("/account/password", url.Values{"csrf": {csrf}, "current": {"wrong"}, "password": {"another horse"}, "confirm": {"another horse"}})
	expect(t, r, page, http.StatusUnauthorized, "current password")
	r, page = b.post("/account/password", url.Values{"csrf": {csrf}, "current": {"correct horse"}, "password": {"another horse"}, "confirm": {"different"}})
	expect(t, r, page, http.StatusBadRequest, "do not match")
	r, page = b.post("/account/password", url.Values{"csrf": {csrf}, "current": {"correct horse"}, "password": {"another horse"}, "confirm": {"another horse"}})
	expect(t, r, page, http.StatusOK, "password is changed")
	// This device stays signed in; the other is signed out.
	if r, _ := b.get("/"); r.StatusCode != http.StatusOK {
		t.Fatal("changing the password signed this device out")
	}
	if r, _ := elsewhere.get("/"); r.StatusCode != http.StatusUnauthorized {
		t.Fatal("other device still signed in")
	}
}

func TestLocalPaperReaders(t *testing.T) {
	// "a" is open to every account; "b" names only bob.
	f := modeSetup(t, config.Auth{Mode: config.AuthLocal},
		config.Paper{ID: "a", Masthead: "The A Gazette", Timezone: "UTC", Look: "broadsheet", Sections: []config.Section{{Key: "news", Title: "News"}}},
		config.Paper{ID: "b", Masthead: "The B Gazette", Timezone: "UTC", Look: "broadsheet", Sections: []config.Section{{Key: "news", Title: "News"}}, Readers: []config.Reader{{Issuer: config.LocalIssuer, Subject: "bob"}}})
	ada := f.browser(t)
	ada.setup("ada", "correct horse")
	_, page := ada.get("/")
	if !strings.Contains(page, "The A Gazette") || strings.Contains(page, "The B Gazette") {
		t.Fatal("ada reads the wrong papers")
	}
	if r, _ := ada.get("/p/b/"); r.StatusCode != http.StatusNotFound {
		t.Fatalf("ada reads b: %d", r.StatusCode)
	}
	_, page = ada.get("/admin")
	csrf := hidden(t, page, "csrf")
	// A general invite cannot take the name paper b keeps for bob.
	_, page = ada.post("/admin/invite", url.Values{"csrf": {csrf}})
	general := linkPattern.FindString(page)
	eve := f.browser(t)
	_, page = eve.get(general)
	r, page := eve.post(general, url.Values{"form": {hidden(t, page, "form")}, "username": {"bob"}, "password": {"battery staple"}, "confirm": {"battery staple"}})
	expect(t, r, page, http.StatusBadRequest, "kept for someone a paper names")
	// An invite made for bob can.
	_, page = ada.post("/admin/invite", url.Values{"csrf": {csrf}, "username": {"Bob"}})
	if !strings.Contains(page, "Invite created for bob") {
		t.Fatal(page)
	}
	link := linkPattern.FindString(page)
	bob := f.browser(t)
	_, page = bob.get(link)
	if strings.Contains(page, `name="username"`) || !strings.Contains(page, "<b>bob</b>") {
		t.Fatal("named invite asks for a username")
	}
	// A username posted anyway is ignored.
	r, page = bob.post(link, url.Values{"form": {hidden(t, page, "form")}, "username": {"mallory"}, "password": {"battery staple"}, "confirm": {"battery staple"}})
	expect(t, r, page, http.StatusSeeOther)
	if _, err := f.db.Account(context.Background(), "mallory"); err == nil {
		t.Fatal("named invite created another name")
	}
	// bob's own paper is b, and he reads a too.
	r, page = bob.get("/")
	expect(t, r, page, http.StatusOK, "The B Gazette", "/p/a/")
	r, page = bob.get("/p/a/")
	expect(t, r, page, http.StatusOK, "The A Gazette")
}

func TestLocalSessionsFromOtherModesEnd(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthLocal})
	f.browser(t).setup("ada", "correct horse")
	// A session left from OIDC (or none) does not read a local paper.
	for _, issuer := range []string{"https://id.example", config.NoneIssuer, config.ProxyIssuer} {
		key := random()
		now := time.Now()
		if err := f.db.InsertSession(context.Background(), auth.Session{Hash: sha256.Sum256([]byte(key)), Issuer: issuer, Subject: "ada", CSRF: random(), Created: now, LastSeen: now, Expires: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		b := f.browser(t)
		u, _ := url.Parse(f.http.URL)
		b.client.Jar.SetCookies(u, []*http.Cookie{{Name: "nd_session", Value: key}})
		if r, _ := b.get("/"); r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s session: %d", issuer, r.StatusCode)
		}
	}
}

func TestProxyMode(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthProxy, Proxy: config.ProxyAuth{UserHeader: "Remote-User", GroupsHeader: "Remote-Groups", TrustedProxies: []string{"127.0.0.1/32", "::1/128"}}},
		config.Paper{ID: "a", Masthead: "The A Gazette", Timezone: "UTC", Look: "broadsheet", Sections: []config.Section{{Key: "news", Title: "News"}}, Readers: []config.Reader{{Issuer: config.ProxyIssuer, Subject: "alice"}}},
		config.Paper{ID: "f", Masthead: "Family Times", Timezone: "UTC", Look: "broadsheet", Sections: []config.Section{{Key: "news", Title: "News"}}, Readers: []config.Reader{{Issuer: config.ProxyIssuer, Group: "family"}}})
	b := f.browser(t)
	r, page := b.get("/")
	expect(t, r, page, http.StatusUnauthorized, "Sign in through your proxy")
	b.header.Set("Remote-User", "alice")
	r, page = b.get("/")
	expect(t, r, page, http.StatusOK, "The A Gazette")
	if strings.Contains(page, "Sign out") || strings.Contains(page, `href="/account"`) {
		t.Fatal("proxy mode offers sign-out or an account page")
	}
	// The session is reused while the proxy names the same reader.
	b.get("/")
	if n := sessions(t, f.db); n != 1 {
		t.Fatalf("%d sessions", n)
	}
	// The proxy now names another user: the old session does not carry over.
	b.header.Set("Remote-User", "mallory")
	r, page = b.get("/")
	expect(t, r, page, http.StatusOK, "No paper for you yet")
	// Groups come from their header, and change with it.
	b.header.Set("Remote-Groups", "friends, family")
	r, page = b.get("/")
	expect(t, r, page, http.StatusOK, "Family Times")
	b.header.Set("Remote-Groups", "friends")
	r, page = b.get("/")
	expect(t, r, page, http.StatusOK, "No paper for you yet")
	// A repeated user header is refused.
	b.header["Remote-User"] = []string{"alice", "mallory"}
	r, page = b.get("/")
	expect(t, r, page, http.StatusUnauthorized)
	// /login has nothing to do.
	b.header.Set("Remote-User", "alice")
	if r, _ := b.get("/login"); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("/login: %d", r.StatusCode)
	}
}

func TestProxyModeIgnoresUntrustedAddresses(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthProxy, Proxy: config.ProxyAuth{UserHeader: "Remote-User", TrustedProxies: []string{"10.0.0.0/8"}}},
		config.Paper{ID: "a", Masthead: "The A Gazette", Timezone: "UTC", Look: "broadsheet", Sections: []config.Section{{Key: "news", Title: "News"}}, Readers: []config.Reader{{Issuer: config.ProxyIssuer, Subject: "alice"}}})
	b := f.browser(t)
	b.header.Set("Remote-User", "alice")
	r, page := b.get("/")
	expect(t, r, page, http.StatusUnauthorized, "Sign in through your proxy")
	if n := sessions(t, f.db); n != 0 {
		t.Fatalf("%d sessions from an untrusted address", n)
	}
}

func TestNoneMode(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthNone},
		config.Paper{ID: "a", Masthead: "The A Gazette", Timezone: "UTC", Look: "broadsheet", Sections: []config.Section{{Key: "news", Title: "News"}}},
		config.Paper{ID: "b", Masthead: "The B Gazette", Timezone: "UTC", Look: "broadsheet", Sections: []config.Section{{Key: "news", Title: "News"}}})
	b := f.browser(t)
	r, page := b.get("/")
	expect(t, r, page, http.StatusOK, "The A Gazette", `class="auth-banner"`, "No sign-in", "/p/b/")
	if strings.Contains(page, "Sign out") {
		t.Fatal("none mode offers sign-out")
	}
	b.get("/archive")
	r, page = b.get("/p/b/")
	expect(t, r, page, http.StatusOK, "The B Gazette")
	if n := sessions(t, f.db); n != 1 {
		t.Fatalf("%d sessions for one browser", n)
	}
	// With the acknowledgement the banner is stronger.
	f.cfg.Server.Auth.None.AcknowledgePublicAccess = true
	r, page = b.get("/")
	expect(t, r, page, http.StatusOK, "may be reachable from the internet")
}

func sessions(t *testing.T, db *store.Store) int {
	t.Helper()
	n, err := db.CountSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestLocalPasskeyOptions(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthLocal, Local: config.LocalAuth{Passkeys: true}})
	b := f.browser(t)
	b.setup("ada", "correct horse")
	postJSON := func(path, body string) (*http.Response, map[string]any) {
		req, _ := http.NewRequest("POST", f.http.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", f.cfg.Server.PublicBaseURL)
		r, err := b.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		_ = json.NewDecoder(r.Body).Decode(&v)
		r.Body.Close()
		return r, v
	}
	_, page := b.get("/account")
	csrf := hidden(t, page, "csrf")
	if !strings.Contains(page, "data-passkey-register") || !strings.Contains(page, "/static/passkey.js") {
		t.Fatal("account page lacks passkeys")
	}
	if r, _ := postJSON("/account/passkeys/begin", `{"csrf":"forged"}`); r.StatusCode != http.StatusForbidden {
		t.Fatalf("forged registration: %d", r.StatusCode)
	}
	r, v := postJSON("/account/passkeys/begin", `{"csrf":"`+csrf+`","password":"correct horse"}`)
	pk, _ := v["publicKey"].(map[string]any)
	rp, _ := pk["rp"].(map[string]any)
	sel, _ := pk["authenticatorSelection"].(map[string]any)
	if r.StatusCode != 200 || rp["id"] != "localhost" || pk["challenge"] == "" || sel["residentKey"] != "required" || sel["userVerification"] != "required" {
		t.Fatalf("registration options: %d %v", r.StatusCode, v)
	}
	// A registration that never happened cannot be finished.
	if r, _ := postJSON("/account/passkeys/finish", `{"csrf":"`+csrf+`","credential":{}}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("bogus credential: %d", r.StatusCode)
	}
	other := f.browser(t)
	_, page = other.get("/login")
	if !strings.Contains(page, "data-passkey-login") {
		t.Fatal("login page lacks the passkey button")
	}
	form := hidden(t, page, "form")
	b = other
	if r, _ := postJSON("/login/passkey/begin", `{"form":"forged"}`); r.StatusCode != http.StatusForbidden {
		t.Fatalf("forged login: %d", r.StatusCode)
	}
	r, v = postJSON("/login/passkey/begin", `{"form":"`+form+`"}`)
	pk, _ = v["publicKey"].(map[string]any)
	if r.StatusCode != 200 || pk["challenge"] == "" || pk["userVerification"] != "required" || pk["allowCredentials"] != nil {
		t.Fatalf("login options: %d %v", r.StatusCode, v)
	}
	if r, _ := postJSON("/login/passkey/finish", `{"form":"`+form+`","credential":{"id":"x"}}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("bogus assertion: %d", r.StatusCode)
	}
	// The challenge was used up by the failed attempt.
	if r, _ := postJSON("/login/passkey/finish", `{"form":"`+form+`","credential":{"id":"x"}}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("second attempt: %d", r.StatusCode)
	}
}

func TestLocalSignInRacesAccountChanges(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthLocal})
	f.browser(t).setup("ada", "correct horse")
	ctx := context.Background()
	a, _ := f.db.Account(ctx, "ada")
	old := a.PasswordHash
	hash, _ := auth.HashPassword("another horse")
	if err := f.db.SetPassword(ctx, "ada", old, hash, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	// A change from a hash that is no longer current does not overwrite.
	if err := f.db.SetPassword(ctx, "ada", old, old, time.Now(), nil); err != store.ErrAccountChanged {
		t.Fatal(err)
	}
	// A sign-in checked against the old password cannot create a session.
	now := time.Now()
	session := auth.Session{Hash: sha256.Sum256([]byte(random())), Issuer: config.LocalIssuer, Subject: "ada", CSRF: random(), Created: now, LastSeen: now, Expires: now.Add(time.Hour)}
	if err := f.db.InsertLocalSession(ctx, session, old, nil); err != store.ErrAccountChanged {
		t.Fatal(err)
	}
	if err := f.db.InsertLocalSession(ctx, session, hash, nil); err != nil {
		t.Fatal(err)
	}
	// Nor for an account that no longer exists, or a passkey that does not.
	session.Hash = sha256.Sum256([]byte(random()))
	if err := f.db.InsertLocalSession(ctx, session, "", &store.PasskeyUse{ID: []byte("gone"), SignCount: 1}); err != store.ErrAccountChanged {
		t.Fatal(err)
	}
	session.Subject = "nobody"
	if err := f.db.InsertLocalSession(ctx, session, "", nil); err != store.ErrAccountChanged {
		t.Fatal(err)
	}
	// A passkey is added only through a live session of its account.
	if err := f.db.AddPasskey(ctx, store.Passkey{ID: []byte("k"), Username: "ada", Name: "k", Credential: []byte("{}"), Created: now}, sha256.Sum256([]byte("no such session"))); err != store.ErrAccountChanged {
		t.Fatal(err)
	}
	// And a reset link only for an account that exists.
	if err := f.db.CreateInvite(ctx, store.Invite{TokenHash: sha256.Sum256([]byte(random())), Reset: true, Username: "nobody", CreatedBy: "ada", Created: now, Expires: now.Add(time.Hour)}); err == nil {
		t.Fatal("reset link for a missing account")
	}
}

// liveSession stores a session for username and returns its hash.
func liveSession(t *testing.T, db *store.Store, username string) [32]byte {
	t.Helper()
	now := time.Now()
	h := sha256.Sum256([]byte(random()))
	if err := db.InsertSession(context.Background(), auth.Session{Hash: h, Issuer: config.LocalIssuer, Subject: username, CSRF: random(), Created: now, LastSeen: now, Expires: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestPasskeyCounterMustAdvance(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthLocal})
	f.browser(t).setup("ada", "correct horse")
	ctx := context.Background()
	if err := f.db.AddPasskey(ctx, store.Passkey{ID: []byte("key"), Username: "ada", Name: "k", Credential: []byte("{}"), SignCount: 5, Created: time.Now()}, liveSession(t, f.db, "ada")); err != nil {
		t.Fatal(err)
	}
	use := func(count uint32) error {
		now := time.Now()
		session := auth.Session{Hash: sha256.Sum256([]byte(random())), Issuer: config.LocalIssuer, Subject: "ada", CSRF: random(), Created: now, LastSeen: now, Expires: now.Add(time.Hour)}
		return f.db.InsertLocalSession(ctx, session, "", &store.PasskeyUse{ID: []byte("key"), Credential: []byte("{}"), SignCount: count})
	}
	if err := use(5); err != store.ErrAccountChanged {
		t.Fatal("a repeated counter signed in")
	}
	if err := use(6); err != nil {
		t.Fatal(err)
	}
	if err := use(0); err != store.ErrAccountChanged {
		t.Fatal("a counter reset to zero signed in")
	}
}

func TestResetRemovesPasskeys(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthLocal})
	admin := f.browser(t)
	admin.setup("ada", "correct horse")
	ctx := context.Background()
	_, page := admin.get("/admin")
	csrf := hidden(t, page, "csrf")
	_, page = admin.post("/admin/invite", url.Values{"csrf": {csrf}, "username": {"bob"}})
	link := linkPattern.FindString(page)
	bob := f.browser(t)
	_, page = bob.get(link)
	bob.post(link, url.Values{"form": {hidden(t, page, "form")}, "password": {"battery staple"}, "confirm": {"battery staple"}})
	if err := f.db.AddPasskey(ctx, store.Passkey{ID: []byte("intruder"), Username: "bob", Name: "k", Credential: []byte("{}"), Created: time.Now()}, liveSession(t, f.db, "bob")); err != nil {
		t.Fatal(err)
	}
	_, page = admin.post("/admin/reset", url.Values{"csrf": {csrf}, "username": {"bob"}})
	reset := linkPattern.FindString(page)
	other := f.browser(t)
	_, page = other.get(reset)
	expect(t, &http.Response{StatusCode: 200}, page, 200, "passkeys are removed")
	other.post(reset, url.Values{"form": {hidden(t, page, "form")}, "password": {"new battery staple"}, "confirm": {"new battery staple"}})
	if keys, _ := f.db.Passkeys(ctx, "bob"); len(keys) != 0 {
		t.Fatal("passkeys survived a reset")
	}
}

func TestPasskeyRegistrationNeedsPassword(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthLocal, Local: config.LocalAuth{Passkeys: true}})
	b := f.browser(t)
	b.setup("ada", "correct horse")
	_, page := b.get("/account")
	csrf := hidden(t, page, "csrf")
	post := func(body string) int {
		req, _ := http.NewRequest("POST", f.http.URL+"/account/passkeys/begin", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", f.cfg.Server.PublicBaseURL)
		r, err := b.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if code := post(`{"csrf":"` + csrf + `"}`); code != http.StatusUnauthorized {
		t.Fatalf("no password: %d", code)
	}
	if code := post(`{"csrf":"` + csrf + `","password":"wrong"}`); code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", code)
	}
	if code := post(`{"csrf":"` + csrf + `","password":"correct horse"}`); code != http.StatusOK {
		t.Fatalf("right password: %d", code)
	}
}

func TestAmbientSessionsAreReused(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthNone})
	// Clients that keep no cookie share the reader's one session.
	for i := 0; i < 5; i++ {
		r, err := http.Get(f.http.URL + "/")
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Fatal(r.StatusCode)
		}
	}
	if n := sessions(t, f.db); n != 1 {
		t.Fatalf("%d sessions for cookieless clients", n)
	}
}

func TestPrivateClient(t *testing.T) {
	for addr, want := range map[string]bool{"127.0.0.1:5": true, "[::1]:5": true, "10.1.2.3:5": true, "100.64.0.9:5": true, "[fd00::1]:5": true, "203.0.113.4:5": false, "[2001:db8::1]:5": false, "junk": false} {
		if got := privateClient(&http.Request{RemoteAddr: addr}); got != want {
			t.Errorf("%s: %v", addr, got)
		}
	}
	// Behind a proxy (a private address) accounts are throttled alone; a
	// public client is throttled by address too.
	if keys := limitKeys(&http.Request{RemoteAddr: "10.0.0.2:5"}, "user:a"); len(keys) != 1 {
		t.Fatal(keys)
	}
	if keys := limitKeys(&http.Request{RemoteAddr: "203.0.113.4:5"}, "user:a"); len(keys) != 2 || keys[1] != "addr:203.0.113.4" {
		t.Fatal(keys)
	}
}

func TestSessionsFromOtherModesAreDeleted(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthLocal})
	ctx := context.Background()
	now := time.Now()
	for _, issuer := range []string{"https://id.example", config.LocalIssuer, config.NoneIssuer} {
		if err := f.db.InsertSession(ctx, auth.Session{Hash: sha256.Sum256([]byte(issuer)), Issuer: issuer, Subject: "x", CSRF: "c", Created: now, LastSeen: now, Expires: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := f.db.DeleteSessionsNotFrom(ctx, f.cfg.AcceptsIssuer)
	if err != nil || n != 2 || sessions(t, f.db) != 1 {
		t.Fatal(n, err)
	}
}

func TestProxyGroupsHeaderRules(t *testing.T) {
	cfg := &config.Config{Server: config.Server{Auth: config.Auth{Mode: config.AuthProxy, Proxy: config.ProxyAuth{UserHeader: "Remote-User", GroupsHeader: "Remote-Groups", TrustedProxies: []string{"127.0.0.1"}}}}}
	req := func(groups ...string) *http.Request {
		r := &http.Request{RemoteAddr: "127.0.0.1:5", Header: http.Header{"Remote-User": {"alice"}}}
		if groups != nil {
			r.Header["Remote-Groups"] = groups
		}
		return r
	}
	if _, g, ok := proxyIdentity(cfg, req("zeta, alpha,alpha, ")); !ok || strings.Join(g, ",") != "alpha,zeta" {
		t.Fatal(g, ok)
	}
	if _, _, ok := proxyIdentity(cfg, req("family", "admins")); ok {
		t.Fatal("repeated groups header accepted")
	}
	many := make([]string, 513)
	for i := range many {
		many[i] = "g" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + strings.Repeat("y", i/26)
	}
	if _, _, ok := proxyIdentity(cfg, req(strings.Join(many, ","))); ok {
		t.Fatal("over 512 groups accepted")
	}
	if _, g, ok := proxyIdentity(cfg, req()); !ok || len(g) != 0 {
		t.Fatal("no groups header")
	}
}

func TestLocalOnlyAdministratorsEditOpenPapers(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthLocal})
	f.s.Editorial = nil
	admin := f.browser(t)
	admin.setup("ada", "correct horse")
	_, page := admin.get("/")
	scopeAda, err := auth.AuthenticateReader(context.Background(), f.db, f.cfg, cookie(t, admin, "nd_session"), time.Now(), time.Hour)
	if err != nil || !scopeAda.CanEdit() {
		t.Fatal("the administrator cannot edit the open paper", err)
	}
	_, page = admin.get("/admin")
	_, page = admin.post("/admin/invite", url.Values{"csrf": {hidden(t, page, "csrf")}})
	link := linkPattern.FindString(page)
	bob := f.browser(t)
	_, page = bob.get(link)
	bob.post(link, url.Values{"form": {hidden(t, page, "form")}, "username": {"bob"}, "password": {"battery staple"}, "confirm": {"battery staple"}})
	scopeBob, err := auth.AuthenticateReader(context.Background(), f.db, f.cfg, cookie(t, bob, "nd_session"), time.Now(), time.Hour)
	if err != nil || scopeBob.CanEdit() || len(scopeBob.Papers()) != 1 {
		t.Fatal("an invited reader may edit the open paper", err)
	}
	// A removed account's session never reads again, even if it slipped in.
	if err := f.db.RemoveAccount(context.Background(), "bob"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	key := random()
	_ = f.db.InsertSession(context.Background(), auth.Session{Hash: sha256.Sum256([]byte(key)), Issuer: config.LocalIssuer, Subject: "bob", CSRF: random(), Created: now, LastSeen: now, Expires: now.Add(time.Hour)})
	if _, err := auth.AuthenticateReader(context.Background(), f.db, f.cfg, key, now, time.Hour); err != auth.ErrUnauthorized {
		t.Fatal("removed account's session authenticated", err)
	}
}

func cookie(t *testing.T, b *browser, name string) string {
	t.Helper()
	u, _ := url.Parse(b.f.http.URL)
	for _, c := range b.client.Jar.Cookies(u) {
		if c.Name == name {
			return c.Value
		}
	}
	t.Fatalf("no %s cookie", name)
	return ""
}

func TestSignInPagesClearTheOfflineCopy(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthLocal})
	f.browser(t).setup("ada", "correct horse")
	r, page := f.browser(t).get("/archive")
	expect(t, r, page, http.StatusUnauthorized, `data-logout="true"`)
}

func TestProxyGroupChangeRefreshesPushGroups(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthProxy, Proxy: config.ProxyAuth{UserHeader: "Remote-User", GroupsHeader: "Remote-Groups", TrustedProxies: []string{"127.0.0.1/32", "::1/128"}}},
		config.Paper{ID: "f", Masthead: "Family Times", Timezone: "UTC", Look: "broadsheet", Sections: []config.Section{{Key: "news", Title: "News"}}, Readers: []config.Reader{{Issuer: config.ProxyIssuer, Group: "family"}}})
	ctx := context.Background()
	phone, laptop := f.browser(t), f.browser(t)
	for _, b := range []*browser{phone, laptop} {
		b.header.Set("Remote-User", "alice")
		b.header.Set("Remote-Groups", "family")
		if r, _ := b.get("/"); r.StatusCode != http.StatusOK {
			t.Fatal(r.StatusCode)
		}
	}
	scope, err := auth.AuthenticateReader(ctx, f.db, f.cfg, cookie(t, phone, "nd_session"), time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.AddPushSubscription(ctx, scope, "https://push.example/1", nil); err != nil {
		t.Fatal(err)
	}
	groups := func() string {
		var g string
		raw, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "nightdesk.db")+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		if err := raw.QueryRowContext(ctx, `SELECT reader_groups FROM push_subscriptions`).Scan(&g); err != nil {
			t.Fatal(err)
		}
		return g
	}
	// The laptop's new groups move the subscription; the phone's older
	// session, still matching "family", moves it back.
	laptop.header.Set("Remote-Groups", "friends")
	laptop.get("/")
	if g := groups(); g != `["friends"]` {
		t.Fatal(g)
	}
	phone.get("/")
	if g := groups(); g != `["family"]` {
		t.Fatal(g)
	}
}
