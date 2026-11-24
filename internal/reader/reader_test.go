package reader

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/candidate"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/keys"
	"github.com/irashack/night-desk/internal/push"
	"github.com/irashack/night-desk/internal/store"
	"github.com/irashack/night-desk/internal/testhttp"
	"github.com/irashack/night-desk/internal/weather"
	"github.com/irashack/night-desk/web"
	"golang.org/x/oauth2"
)

type fakeProvider struct {
	server                   *httptest.Server
	key                      *rsa.PrivateKey
	nonce, audience, subject string
	codes                    map[string][2]string
	pkce                     bool
	groups                   any    // the groups claim, when set
	scope                    string // the scope the last authorization asked for
}

func provider(t *testing.T, network *testhttp.Network) *fakeProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{key: key, subject: "reader-a", codes: map[string][2]string{}}
	p.server = network.Server(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": p.server.URL, "authorization_endpoint": p.server.URL + "/authorize", "token_endpoint": p.server.URL + "/token", "jwks_uri": p.server.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "test", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
		case "/authorize":
			q := r.URL.Query()
			if q.Get("code_challenge_method") != "S256" || q.Get("response_type") != "code" || q.Get("nonce") == "" || q.Get("client_id") != "reader" {
				http.Error(w, "bad authorization request", 400)
				return
			}
			p.scope = q.Get("scope")
			code := fmt.Sprint(len(p.codes) + 1)
			p.codes[code] = [2]string{q.Get("nonce"), q.Get("code_challenge")}
			dest, _ := url.Parse(q.Get("redirect_uri"))
			dest.RawQuery = url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
			http.Redirect(w, r, dest.String(), 302)
		case "/token":
			_ = r.ParseForm()
			pair, ok := p.codes[r.FormValue("code")]
			challenge := sha256.Sum256([]byte(r.FormValue("code_verifier")))
			if !ok || base64.RawURLEncoding.EncodeToString(challenge[:]) != pair[1] || r.FormValue("grant_type") != "authorization_code" {
				http.Error(w, "bad verifier", 400)
				return
			}
			p.pkce = true
			nonce := pair[0]
			if p.nonce != "" {
				nonce = p.nonce
			}
			aud := "reader"
			if p.audience != "" {
				aud = p.audience
			}
			claims := map[string]any{"iss": p.server.URL, "sub": p.subject, "aud": aud, "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": nonce, "email": "same@example.test"}
			if p.groups != nil {
				claims["groups"] = p.groups
			}
			payload, _ := json.Marshal(claims)
			header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"test"}`))
			unsigned := header + "." + base64.RawURLEncoding.EncodeToString(payload)
			digest := sha256.Sum256([]byte(unsigned))
			sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
			if err != nil {
				http.Error(w, "sign failed", 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "test-access", "token_type": "Bearer", "expires_in": 3600, "id_token": unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.server.Close)
	return p
}

type fixture struct {
	s      *Server
	db     *store.Store
	cfg    *config.Config
	http   *httptest.Server
	client *http.Client
	p      *fakeProvider
	a, b   auth.PaperScope
}

func setup(t *testing.T) *fixture {
	t.Helper()
	network := testhttp.New()
	p := provider(t, network)
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	lat, lon := 41.8781, -87.6298
	cfg := &config.Config{Server: config.Server{OIDC: config.OIDC{Issuer: p.server.URL, ClientID: "reader", Scopes: []string{"openid"}}, SessionLifetime: "720h"}, Papers: []config.Paper{
		{ID: "a", Masthead: "The A Gazette", Place: "Riverside", Timezone: "America/Chicago", Latitude: &lat, Longitude: &lon, Look: "broadsheet", Sections: []config.Section{{Key: "economy", Title: "Economy"}, {Key: "faith", Title: "Faith"}}, Readers: []config.Reader{{Issuer: p.server.URL, Subject: "reader-a"}}},
		{ID: "b", Masthead: "The B Gazette", Timezone: "America/Chicago", Sections: []config.Section{{Key: "economy", Title: "Economy"}}, Readers: []config.Reader{{Issuer: p.server.URL, Subject: "reader-b"}}}}}
	f := &fixture{db: db, cfg: cfg, p: p}
	cfg.Server.DataDir = t.TempDir()
	// Reserve the app origin before discovery so the callback URL is exact.
	f.http = network.Unstarted(nil)
	cfg.Server.PublicBaseURL = "http://" + f.http.Listener.Addr().String()
	f.s, err = New(context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Transport: network.Transport()}), db, func() *config.Config { return f.cfg }, "test-client-secret")
	if err != nil {
		t.Fatal(err)
	}
	if f.s.Push, err = push.CreateKey(filepath.Join(t.TempDir(), push.KeyFile)); err != nil {
		t.Fatal(err)
	}
	f.http.Config.Handler = f.s.Handler()
	f.http.Start()
	t.Cleanup(f.http.Close)
	jar, _ := cookiejar.New(nil)
	f.client = &http.Client{Transport: network.Transport(), Jar: jar, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	f.a = paperScope(t, db, "a")
	f.b = paperScope(t, db, "b")
	return f
}

func paperScope(t *testing.T, db *store.Store, paper string) auth.PaperScope {
	t.Helper()
	_, key, err := keys.Issue(context.Background(), db, paper, "test")
	if err != nil {
		t.Fatal(err)
	}
	scope, err := auth.Authenticate(context.Background(), db, key)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}
func (f *fixture) get(t *testing.T, path string) *http.Response {
	t.Helper()
	r, err := f.client.Get(f.http.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func body(t *testing.T, r *http.Response) string {
	t.Helper()
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func (f *fixture) signIn(t *testing.T, tamper string) *http.Response {
	t.Helper()
	r := f.get(t, "/login")
	if r.StatusCode != 302 {
		t.Fatalf("login: %d", r.StatusCode)
	}
	dest := r.Header.Get("Location")
	r.Body.Close()
	r, err := f.client.Get(dest)
	if err != nil {
		t.Fatal(err)
	}
	callback := r.Header.Get("Location")
	r.Body.Close()
	u, _ := url.Parse(callback)
	q := u.Query()
	if tamper == "state" {
		q.Set("state", "wrong-state")
	}
	u.RawQuery = q.Encode()
	r, err = f.client.Get(u.String())
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func (f *fixture) session(t *testing.T) (auth.Session, string) {
	t.Helper()
	u, _ := url.Parse(f.http.URL)
	for _, c := range f.client.Jar.Cookies(u) {
		if c.Name == "nd_session" {
			v, err := f.db.Session(context.Background(), sha256.Sum256([]byte(c.Value)))
			if err != nil {
				t.Fatal(err)
			}
			return v, c.Value
		}
	}
	t.Fatal("no session")
	return auth.Session{}, ""
}
func (f *fixture) post(t *testing.T, path string, values url.Values) *http.Response {
	t.Helper()
	r, err := f.client.PostForm(f.http.URL+path, values)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func sampleEdition() *edition.Edition {
	return &edition.Edition{Schema: 1, Lead: "story-a", Motto: &edition.Motto{Kind: "quotation", Text: "Be curious.", Source: "The editor"}, Sections: []edition.Section{{Key: "faith", Items: []edition.Item{{ID: "faith-a", Kind: "brief", Title: "Second configured section", Summary: "Faith summary.", Source: "Local", URL: "https://example.test/faith"}}}, {Key: "economy", Items: []edition.Item{{ID: "story-a", Kind: "news", Title: "<script>alert('x')</script>", Summary: "A long and carefully researched story about this morning's news.", Source: "Wire", URL: "https://example.test/story"}}}}}
}
func (f *fixture) publish(t *testing.T, scope auth.PaperScope, date string, ed *edition.Edition, images map[string]store.Image) {
	t.Helper()
	if err := f.db.Publish(context.Background(), scope, date, ed, images); err != nil {
		t.Fatal(err)
	}
}
func TestOIDC(t *testing.T) {
	for _, tc := range []struct {
		name, tamper, nonce, aud, subject string
		status                            int
	}{{name: "success", status: 303}, {name: "bad state", tamper: "state", status: 400}, {name: "bad nonce", nonce: "wrong", status: 400}, {name: "wrong audience", aud: "other", status: 400}, {name: "unmapped reader", subject: "unknown", status: 303}} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			f.p.nonce = tc.nonce
			f.p.audience = tc.aud
			if tc.subject != "" {
				f.p.subject = tc.subject
			}
			r := f.signIn(t, tc.tamper)
			if r.StatusCode != tc.status {
				t.Fatalf("callback: got %d want %d: %s", r.StatusCode, tc.status, body(t, r))
			}
			r.Body.Close()
			if tc.status == 303 {
				if !f.p.pkce {
					t.Fatal("PKCE not verified")
				}
				session, token := f.session(t)
				raw, err := base64.RawURLEncoding.DecodeString(token)
				if err != nil || len(raw) != 32 || session.Subject != f.p.subject || session.Issuer != f.p.server.URL {
					t.Fatal("incorrect session identity")
				}
				text := body(t, f.get(t, "/"))
				if tc.subject == "unknown" {
					if !strings.Contains(text, "No paper for you yet") || strings.Contains(text, "Archive") {
						t.Fatal("unmapped reader has paper chrome")
					}
				} else if !strings.Contains(text, "Your first paper arrives tomorrow morning") {
					t.Fatal("missing first-edition message")
				}
			} else {
				u, _ := url.Parse(f.http.URL)
				for _, c := range f.client.Jar.Cookies(u) {
					if c.Name == "nd_session" {
						t.Fatal("failed login created session")
					}
				}
			}
		})
	}
}
func TestReaderHTTPIsolationAndRendering(t *testing.T) {
	f := setup(t)
	f.publish(t, f.a, "2026-10-27", sampleEdition(), nil)
	other := sampleEdition()
	other.Sections[1].Items[0].ID = "only-b"
	other.Lead = "only-b"
	sha := strings.Repeat("b", 64)
	f.publish(t, f.b, "2026-10-26", other, map[string]store.Image{"only-b": {SHA256: sha, Width: 1, Height: 1, Bytes: 8}})
	r := f.signIn(t, "")
	r.Body.Close()
	session, _ := f.session(t)
	r = f.get(t, "/")
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	if r.Header.Get("Content-Security-Policy") != CSP || r.Header.Get("X-Content-Type-Options") != "nosniff" || r.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("missing security headers")
	}
	html := body(t, r)
	if strings.Contains(html, "<script>alert") || !strings.Contains(html, "&lt;script&gt;") {
		t.Fatal("title not escaped")
	}
	economy, faith := strings.Index(html, `id="section-economy"`), strings.Index(html, `id="section-faith"`)
	if economy < 0 || faith <= economy {
		t.Fatal("sections not in configuration order")
	}
	for _, want := range []string{`<span class="where-title">Front page</span><span class="where-page">⌂ 1 of 3</span>`, "Inside today", "Tuesday, October 27, 2026", "Riverside", "No. 1", `rel="noopener noreferrer"`, `name="csrf"`, "data-layout=", "data-mode-choice="} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(html, `style=`) || strings.Contains(html, "onclick=") {
		t.Fatal("inline styles or scripts")
	}
	for _, path := range []string{"/e/2026-10-26", "/img/" + sha + ".jpg", "/e/not-a-date"} {
		r = f.get(t, path)
		if r.StatusCode != 404 {
			t.Errorf("%s: %d", path, r.StatusCode)
		}
		r.Body.Close()
	}
	values := url.Values{"date": {"2026-10-26"}, "item": {"only-b"}, "kind": {"up"}, "csrf": {session.CSRF}}
	r = f.post(t, "/feedback", values)
	if r.StatusCode != 404 {
		t.Fatal("foreign feedback accepted", r.StatusCode)
	}
	r.Body.Close()
	values.Set("date", "2026-10-27")
	r = f.post(t, "/feedback", values)
	if r.StatusCode != 404 {
		t.Fatal("foreign item accepted", r.StatusCode)
	}
	r.Body.Close()
	values.Set("item", "story-a")
	values.Set("csrf", "wrong")
	r = f.post(t, "/feedback", values)
	if r.StatusCode != 403 {
		t.Fatal("CSRF accepted", r.StatusCode)
	}
	r.Body.Close()
	values.Set("csrf", session.CSRF)
	for _, kind := range []string{"up", "down", "save", "unsave"} {
		values.Set("kind", kind)
		r = f.post(t, "/feedback", values)
		if r.StatusCode != 204 {
			t.Fatal("feedback failed", r.StatusCode)
		}
		r.Body.Close()
	}
	r = f.get(t, "/")
	persisted := body(t, r)
	if !strings.Contains(persisted, `data-kind="down" aria-pressed="true"`) || strings.Contains(persisted, `data-kind="save" aria-pressed="true"`) {
		t.Fatal("feedback state did not survive reload")
	}
	events, _, err := f.db.Feedback(context.Background(), f.a, 0, 100)
	if err != nil || len(events) != 4 || events[0].ReaderSubject != "reader-a" {
		t.Fatal("feedback identity not recorded")
	}
	r = f.get(t, "/archive")
	html = body(t, r)
	if !strings.Contains(html, "2026-10-27") || strings.Contains(html, "2026-10-26") {
		t.Fatal("archive leaked tenant")
	}
	r = f.get(t, "/manifest.webmanifest")
	if !strings.Contains(body(t, r), "The A Gazette") {
		t.Fatal("manifest not personalized")
	}
	// Offline cache ownership changes even if the same session is remapped.
	originalOwner := regexp.MustCompile(`name="offline-owner" content="([^"]+)"`).FindStringSubmatch(html)[1]
	originalPaperReaders := f.cfg.Papers[0].Readers
	f.cfg.Papers[0].Readers = nil
	f.cfg.Papers[1].Readers = append(f.cfg.Papers[1].Readers, config.Reader{Issuer: f.p.server.URL, Subject: "reader-a"})
	remapped := body(t, f.get(t, "/"))
	remappedOwner := regexp.MustCompile(`name="offline-owner" content="([^"]+)"`).FindStringSubmatch(remapped)[1]
	if originalOwner == remappedOwner {
		t.Fatal("offline ownership did not change with paper mapping")
	}
	f.cfg.Papers[0].Readers = originalPaperReaders
	f.cfg.Papers[1].Readers = f.cfg.Papers[1].Readers[:1]
	// Mapping is read on every request, not copied into the session.
	cfg := *f.cfg
	cfg.Papers = append([]config.Paper(nil), cfg.Papers...)
	cfg.Papers[0].Readers = nil
	f.cfg = &cfg
	r = f.get(t, "/")
	html = body(t, r)
	if !strings.Contains(html, "No paper for you yet") || strings.Contains(html, "The A Gazette") {
		t.Fatal("removed reader retained access")
	}
	r = f.get(t, "/e/2026-10-27")
	if r.StatusCode != 404 {
		t.Fatal("removed reader retained edition access")
	}
	r.Body.Close()
}
func TestSessions(t *testing.T) {
	t.Run("expiry and sliding", func(t *testing.T) {
		f := setup(t)
		r := f.signIn(t, "")
		r.Body.Close()
		before, _ := f.session(t)
		f.s.now = func() time.Time { return before.LastSeen.Add(30 * time.Minute) }
		r = f.get(t, "/")
		r.Body.Close()
		after, _ := f.session(t)
		if !after.LastSeen.Equal(before.LastSeen) || !after.Expires.Equal(f.s.now().Add(720*time.Hour)) {
			t.Fatal("sliding or hourly last seen incorrect")
		}
		f.s.now = func() time.Time { return before.LastSeen.Add(61 * time.Minute) }
		r = f.get(t, "/")
		r.Body.Close()
		after, _ = f.session(t)
		if !after.LastSeen.Equal(f.s.now()) {
			t.Fatal("last seen did not update")
		}
		expires := after.Expires
		f.s.now = func() time.Time { return expires }
		r = f.get(t, "/")
		if r.StatusCode != 401 {
			t.Fatal("expired session accepted", r.StatusCode)
		}
		r.Body.Close()
	})
	t.Run("logout", func(t *testing.T) {
		f := setup(t)
		r := f.signIn(t, "")
		r.Body.Close()
		session, _ := f.session(t)
		r = f.post(t, "/logout", url.Values{"csrf": {"wrong"}})
		if r.StatusCode != 403 {
			t.Fatal("logout CSRF accepted")
		}
		r.Body.Close()
		r = f.post(t, "/logout", url.Values{"csrf": {session.CSRF}})
		if r.StatusCode != 200 || r.Header.Get("Clear-Site-Data") == "" {
			t.Fatal("logout failed")
		}
		html := body(t, r)
		if !strings.Contains(html, `data-logout="true"`) {
			t.Fatal("missing offline cleanup")
		}
		if _, err := f.db.Session(context.Background(), session.Hash); err == nil {
			t.Fatal("session survived logout")
		}
		r = f.get(t, "/")
		if r.StatusCode != 401 {
			t.Fatal("logged out reader accepted")
		}
		r.Body.Close()
	})
	t.Run("transaction expiry and replay", func(t *testing.T) {
		f := setup(t)
		now := time.Now()
		f.s.now = func() time.Time { return now }
		r := f.get(t, "/login")
		dest := r.Header.Get("Location")
		r.Body.Close()
		r, err := f.client.Get(dest)
		if err != nil {
			t.Fatal(err)
		}
		callback := r.Header.Get("Location")
		r.Body.Close()
		f.s.now = func() time.Time { return now.Add(10 * time.Minute) }
		for i := 0; i < 2; i++ {
			r, err = f.client.Get(callback)
			if err != nil {
				t.Fatal(err)
			}
			if r.StatusCode != 400 {
				t.Fatal("expired or replayed callback accepted")
			}
			r.Body.Close()
		}
	})
}
func TestImagesAndStatic(t *testing.T) {
	f := setup(t)
	sha := strings.Repeat("a", 64)
	dir := filepath.Join(f.cfg.Server.DataDir, "images", "aa")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(dir, sha+".jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if err = jpeg.Encode(file, image.NewRGBA(image.Rect(0, 0, 1, 1)), nil); err != nil {
		t.Fatal(err)
	}
	file.Close()
	f.publish(t, f.a, "2026-10-27", sampleEdition(), map[string]store.Image{"story-a": {SHA256: sha, Width: 1, Height: 1, Bytes: 500}})
	r := f.signIn(t, "")
	r.Body.Close()
	r = f.get(t, "/img/"+sha+".jpg")
	if r.StatusCode != 200 || r.Header.Get("Cache-Control") != "private, max-age=31536000, immutable" {
		t.Fatal("image failed", r.StatusCode)
	}
	r.Body.Close()
	r = f.get(t, asset("app.css"))
	etag := r.Header.Get("ETag")
	if etag == "" {
		t.Fatal("no static ETag")
	}
	r.Body.Close()
	req, _ := http.NewRequest("GET", f.http.URL+asset("app.css"), nil)
	req.Header.Set("If-None-Match", etag)
	r, err = f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != 304 {
		t.Fatal("ETag ignored")
	}
	r.Body.Close()
	for _, base := range []struct {
		url    string
		secure bool
	}{{"https://paper.test", true}, {"http://127.0.0.1:9999", false}, {"http://localhost:9999", false}, {"http://[::1]:9999", true}} {
		f.cfg.Server.PublicBaseURL = base.url
		w := httptest.NewRecorder()
		f.s.cookie(w, "nd_session", "test", 3600)
		c := w.Result().Cookies()[0]
		if c.Secure != base.secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
			t.Fatal("incorrect cookie attributes")
		}
	}
}
func TestArchivePaginationAndFirstLoadBudget(t *testing.T) {
	f := setup(t)
	ed := sampleEdition()
	ed.Sections = nil
	f.cfg.Papers[0].Sections = nil
	for i := 0; i < 12; i++ {
		key := fmt.Sprintf("section-%02d", i)
		f.cfg.Papers[0].Sections = append(f.cfg.Papers[0].Sections, config.Section{Key: key, Title: fmt.Sprintf("Section %d", i+1)})
		sec := edition.Section{Key: key}
		for j := 0; j < 2; j++ {
			it := sampleEdition().Sections[1].Items[0]
			it.ID = fmt.Sprintf("item-%d-%d", i, j)
			it.Title = "This morning's reporting from the desk"
			it.Summary = strings.Repeat("The latest reporting and useful context for your day. ", 5)
			sec.Items = append(sec.Items, it)
		}
		ed.Sections = append(ed.Sections, sec)
	}
	ed.Lead = "item-0-0"
	// Every story has text, so each carries its reading-view link.
	var snaps []candidate.Candidate
	for _, sec := range ed.Sections {
		for i := range sec.Items {
			sec.Items[i].URL = "https://example.test/" + sec.Items[i].ID
			snaps = append(snaps, candidate.Candidate{Section: sec.Key, CanonicalURL: sec.Items[i].URL, Outlet: "Wire", Title: "Story", FetchedAt: time.Now().UTC(), Text: "Text.", State: "ok"})
		}
	}
	if err := f.db.UpsertCandidates(t.Context(), f.a, snaps); err != nil {
		t.Fatal(err)
	}
	start, _ := time.Parse("2006-01-02", "2026-08-01")
	for i := 0; i < 65; i++ {
		f.publish(t, f.a, start.AddDate(0, 0, i).Format("2006-01-02"), ed, nil)
	}
	r := f.signIn(t, "")
	r.Body.Close()
	r = f.get(t, "/archive")
	html := body(t, r)
	dates := regexp.MustCompile(`href="/e/([0-9-]+)"`).FindAllStringSubmatch(html, -1)
	if len(dates) != 60 || !strings.Contains(html, "?page=2") || dates[0][1] < dates[59][1] {
		t.Fatal("bad archive page one")
	}
	r = f.get(t, "/archive?page=2")
	html = body(t, r)
	if len(regexp.MustCompile(`href="/e/`).FindAllString(html, -1)) != 5 {
		t.Fatal("bad archive page two")
	}
	r = f.get(t, "/")
	html = body(t, r)
	total := len(html)
	for _, name := range []string{"app.css", "app.js", "fonts/oldstandard-regular.woff2", "fonts/oldstandard-bold.woff2", "fonts/unifrakturcook-bold.woff2", "fonts/librefranklin-var.woff2"} {
		b, err := web.Files.ReadFile("static/" + name)
		if err != nil {
			t.Fatal(err)
		}
		total += len(b)
	}
	t.Logf("uncompressed HTML + CSS + JS + regular fonts: %d bytes", total)
	// Raised from 150 KB for the Sections editor, opinion beside its story,
	// since-published lines and the push toggle; the four fonts are most of it.
	// Raised to 162 KB for the paper switcher and per-paper push status.
	// Raised to 166 KB for each story's reading-view link; the reading view's
	// own CSS and script load only on its page.
	if total >= 166000 {
		t.Fatalf("first load exceeds 166 KB: %d", total)
	}
}

func TestBoxes(t *testing.T) {
	f := setup(t)
	f.cfg.Papers[0].Boxes = config.Boxes{Weather: true, Sun: true, Solunar: true, Markets: true}
	ed := sampleEdition()
	ed.Boxes = &edition.Boxes{Markets: &edition.Markets{Source: "Market desk", AsOf: "2026-10-27T12:00:00Z", Quotes: []edition.Quote{{Symbol: "ND", Label: "Example", Value: "123", Change: "+1", Direction: "up"}}}}
	f.publish(t, f.a, "2026-10-27", ed, nil)
	network := testhttp.New()
	srv := network.Server(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 503) }))
	defer srv.Close()
	f.s.Weather = weather.New(srv.URL)
	f.s.Weather.HTTP.Transport = network.Transport()
	r := f.signIn(t, "")
	r.Body.Close()
	r = f.get(t, "/")
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	html := body(t, r)
	for _, want := range []string{"Sunrise", "Sunset", "Solunar", "Major", "Minor", "illuminated", "Market desk", "ND"} {
		if !strings.Contains(html, want) {
			t.Errorf("missing box value %q", want)
		}
	}
	if strings.Contains(html, "mph") || strings.Contains(html, "Open-Meteo") {
		t.Fatal("weather failure invented values")
	}
	market := strings.Index(html, `aria-label="Markets"`)
	economy := strings.Index(html, `id="section-economy"`)
	faith := strings.Index(html, `id="section-faith"`)
	if market < economy || market > faith {
		t.Fatal("markets outside economy section")
	}
	ed.Boxes.Markets.Source = ""
	f.publish(t, f.a, "2026-10-27", ed, nil)
	html = body(t, f.get(t, "/"))
	if strings.Contains(html, `aria-label="Markets"`) {
		t.Fatal("uncited market box displayed")
	}
}
