package reader

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
)

// pkce is an app's sign-in secret and the authorize query that carries its
// challenge.
type pkce struct {
	verifier, state string
}

func newPKCE() pkce { return pkce{verifier: random(), state: random()} }

func (p pkce) query() url.Values {
	sum := sha256.Sum256([]byte(p.verifier))
	return url.Values{"response_type": {"code"}, "redirect_uri": {"nightdesk://auth"}, "state": {p.state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}, "client_name": {"Night Desk on Test iPhone"}}
}

func (p pkce) path() string { return "/app/authorize?" + p.query().Encode() }

// requester is a browser of either fixture: method, path and form in, the
// response and its body out.
type requester func(method, path string, form url.Values) (*http.Response, string)

// grantCode confirms the app sign-in as an already signed-in browser and
// returns the code the server sends back to the app.
func grantCode(t *testing.T, do requester, p pkce) string {
	t.Helper()
	r, page := do("GET", p.path(), nil)
	expect(t, r, page, http.StatusOK, "Sign in the app?", "Night Desk on Test iPhone")
	r, page = do("POST", p.path(), url.Values{"csrf": {hidden(t, page, "csrf")}, "decision": {"allow"}})
	expect(t, r, page, http.StatusSeeOther)
	back, err := url.Parse(r.Header.Get("Location"))
	if err != nil || back.Scheme != "nightdesk" || back.Host != "auth" || back.Query().Get("state") != p.state || back.Query().Get("code") == "" {
		t.Fatalf("redirect to the app: %q", r.Header.Get("Location"))
	}
	return back.Query().Get("code")
}

// apiClient calls the reader API with a bearer token and no cookies.
type apiClient struct {
	t     *testing.T
	base  string
	token string
	http  *http.Client
}

func (c apiClient) do(method, path, body string, header ...string) (*http.Response, string) {
	c.t.Helper()
	req, err := http.NewRequest(method, c.base+path, strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	client := c.http
	if client == nil {
		client = http.DefaultClient
	}
	r, err := client.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r, string(b)
}

func (c apiClient) json(method, path, body string, status int, out any) *http.Response {
	c.t.Helper()
	r, page := c.do(method, path, body)
	if r.StatusCode != status {
		c.t.Fatalf("%s %s: status %d, want %d: %s", method, path, r.StatusCode, status, page)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(page), out); err != nil {
			c.t.Fatalf("%s %s: %v: %s", method, path, err, page)
		}
	}
	return r
}

// exchange trades a code for a token at the token endpoint, as a form.
func exchange(t *testing.T, base string, client *http.Client, code, verifier string) (tokenResponse, *http.Response) {
	t.Helper()
	if client == nil {
		client = http.DefaultClient
	}
	r, err := client.PostForm(base+apiPrefix+"/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {"nightdesk://auth"}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var out tokenResponse
	_ = json.NewDecoder(r.Body).Decode(&out)
	return out, r
}

func apiEditionSample() *edition.Edition {
	return &edition.Edition{Schema: 1, Lead: "lead-story", Sections: []edition.Section{{Key: "news", Items: []edition.Item{
		{ID: "lead-story", Kind: "news", Title: "Bridge reopens downtown", Summary: "The river crossing is open again after a year of repairs.", Source: "Wire", URL: "https://example.test/bridge"},
		{ID: "second", Kind: "brief", Title: "Library extends hours", Summary: "Open until nine on weekdays.", Source: "Local", URL: "https://example.test/library"},
	}}}}
}

func TestReaderAPIWithLocalAccounts(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthLocal})
	b := f.browser(t)
	b.setup("ira", "correct horse battery")
	scope := paperScope(t, f.db, "a")
	if err := f.db.Publish(t.Context(), scope, "2026-11-27", apiEditionSample(), nil); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Publish(t.Context(), scope, "2026-11-28", apiEditionSample(), nil); err != nil {
		t.Fatal(err)
	}
	api := apiClient{t: t, base: f.http.URL}

	// The address check needs no sign-in.
	var info apiInfo
	api.json("GET", apiPrefix, "", http.StatusOK, &info)
	if info.API != 1 || info.Auth.Mode != "local" || info.Auth.Authorize != "/app/authorize" || info.Auth.Token != apiPrefix+"/token" {
		t.Fatalf("info: %+v", info)
	}
	// Without a token, and with only the browser's cookie, nothing is served.
	if r, _ := api.do("GET", apiPrefix+"/me", ""); r.StatusCode != http.StatusUnauthorized || r.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("no token: %d", r.StatusCode)
	}
	if r, _ := b.get(apiPrefix + "/me"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cookie only: %d", r.StatusCode)
	}

	p := newPKCE()
	code := grantCode(t, b.do, p)
	// The wrong verifier spends the code.
	if _, r := exchange(t, f.http.URL, nil, code, newPKCE().verifier); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong verifier: %d", r.StatusCode)
	}
	if _, r := exchange(t, f.http.URL, nil, code, p.verifier); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("spent code: %d", r.StatusCode)
	}
	code = grantCode(t, b.do, p)
	tok, r := exchange(t, f.http.URL, nil, code, p.verifier)
	if r.StatusCode != http.StatusOK || tok.TokenType != "Bearer" || tok.AccessToken == "" || tok.ExpiresIn != 720*3600 || tok.Client != "Night Desk on Test iPhone" {
		t.Fatalf("token: %d %+v", r.StatusCode, tok)
	}
	api.token = tok.AccessToken

	var me apiMe
	api.json("GET", apiPrefix+"/me", "", http.StatusOK, &me)
	if me.Name != "ira" || me.Client != "Night Desk on Test iPhone" || len(me.Papers) != 1 || me.Papers[0].ID != "a" || !me.Papers[0].Default || me.Papers[0].Latest == nil || me.Papers[0].Latest.Date != "2026-11-28" || me.Papers[0].Latest.Lead != "Bridge reopens downtown" || me.Papers[0].Latest.Number != 2 {
		t.Fatalf("me: %+v", me)
	}

	var list apiEditions
	api.json("GET", apiPrefix+"/papers/a/editions", "", http.StatusOK, &list)
	if len(list.Editions) != 2 || list.Editions[0].Date != "2026-11-28" || list.NextPage != 0 {
		t.Fatalf("editions: %+v", list)
	}

	var ed apiEdition
	r = api.json("GET", apiPrefix+"/papers/a/editions/latest", "", http.StatusOK, &ed)
	if ed.Date != "2026-11-28" || ed.Paper.Masthead != "The A Gazette" || ed.Number != 2 || ed.Lead == nil || ed.Lead.ID != "lead-story" || ed.Lead.Title != "Bridge reopens downtown" || len(ed.Sections) != 1 || len(ed.Sections[0].Items) != 2 || ed.Previous == nil || ed.Previous.Date != "2026-11-27" {
		t.Fatalf("edition: %+v", ed)
	}
	// An app revalidates what it keeps offline.
	etag := r.Header.Get("ETag")
	if etag == "" || !strings.Contains(r.Header.Get("Cache-Control"), "private") {
		t.Fatalf("caching headers: %v", r.Header)
	}
	if r, _ := api.do("GET", apiPrefix+"/papers/a/editions/2026-11-28", "", "If-None-Match", etag); r.StatusCode != http.StatusNotModified {
		t.Fatalf("revalidation: %d", r.StatusCode)
	}

	// Feedback shows on the next read, as on the page.
	api.json("POST", apiPrefix+"/papers/a/feedback", `{"date":"2026-11-28","item":"second","kind":"up"}`, http.StatusNoContent, nil)
	api.json("POST", apiPrefix+"/papers/a/feedback", `{"date":"2026-11-28","item":"second","kind":"save"}`, http.StatusNoContent, nil)
	api.json("POST", apiPrefix+"/papers/a/feedback", `{"date":"2026-11-28","item":"nope","kind":"up"}`, http.StatusNotFound, nil)
	api.json("POST", apiPrefix+"/papers/a/feedback", `{"date":"2026-11-28","item":"second","kind":"love"}`, http.StatusBadRequest, nil)
	api.json("GET", apiPrefix+"/papers/a/editions/2026-11-28", "", http.StatusOK, &ed)
	if got := ed.Sections[0].Items[1].Feedback; got.Vote != "up" || !got.Saved {
		t.Fatalf("feedback: %+v", got)
	}

	var story apiStory
	api.json("GET", apiPrefix+"/papers/a/editions/2026-11-28/items/second", "", http.StatusOK, &story)
	if story.Item.Title != "Library extends hours" || story.Paragraphs != nil || story.Item.Readable {
		t.Fatalf("story without stored text: %+v", story)
	}
	api.json("GET", apiPrefix+"/papers/a/editions/2026-11-28/items/missing", "", http.StatusNotFound, nil)
	api.json("GET", apiPrefix+"/papers/a/editions/2020-01-01", "", http.StatusNotFound, nil)
	api.json("GET", apiPrefix+"/papers/zzz/editions/latest", "", http.StatusNotFound, nil)
	api.json("GET", apiPrefix+"/nowhere", "", http.StatusNotFound, nil)

	var found struct {
		Results []apiResult `json:"results"`
	}
	api.json("GET", apiPrefix+"/papers/a/search?q=library", "", http.StatusOK, &found)
	if len(found.Results) == 0 || found.Results[0].Item != "second" {
		t.Fatalf("search: %+v", found)
	}
	api.json("GET", apiPrefix+"/papers/a/search?in=nowhere", "", http.StatusBadRequest, nil)

	// The app's sign-out ends only its own token.
	api.json("POST", apiPrefix+"/signout", "", http.StatusNoContent, nil)
	api.json("GET", apiPrefix+"/me", "", http.StatusUnauthorized, nil)
	if r, _ := b.get("/"); r.StatusCode != http.StatusOK {
		t.Fatalf("browser signed out with the app: %d", r.StatusCode)
	}
}

func TestAppSignInStartsSignedOut(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthLocal})
	b := f.browser(t)
	b.setup("ira", "correct horse battery")
	b.post("/logout", url.Values{"csrf": {csrfOf(t, b)}})
	p := newPKCE()
	// Signed out, the request shows the sign-in form, which comes back to it.
	r, page := b.get(p.path())
	expect(t, r, page, http.StatusUnauthorized, `name="next" value="/app/authorize?`)
	r, page = b.post("/login", url.Values{"form": {hidden(t, page, "form")}, "username": {"ira"}, "password": {"correct horse battery"}, "next": {p.path()}})
	expect(t, r, page, http.StatusSeeOther)
	if loc := r.Header.Get("Location"); !strings.HasPrefix(loc, "/app/authorize?") {
		t.Fatalf("after sign-in: %q", loc)
	}
	code := grantCode(t, b.do, p)
	if tok, r := exchange(t, f.http.URL, nil, code, p.verifier); r.StatusCode != http.StatusOK || tok.AccessToken == "" {
		t.Fatalf("token: %d", r.StatusCode)
	}
}

// csrfOf reads the signed-in page's CSRF token.
func csrfOf(t *testing.T, b *browser) string {
	t.Helper()
	_, page := b.get("/")
	return hidden(t, page, "csrf")
}

func TestAppSignInRefusals(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthLocal})
	b := f.browser(t)
	b.setup("ira", "correct horse battery")
	p := newPKCE()
	for name, change := range map[string]func(url.Values){
		"other redirect": func(q url.Values) { q.Set("redirect_uri", "https://evil.example/cb") },
		"plain pkce":     func(q url.Values) { q.Set("code_challenge_method", "plain") },
		"no state":       func(q url.Values) { q.Del("state") },
		"token flow":     func(q url.Values) { q.Set("response_type", "token") },
	} {
		q := p.query()
		change(q)
		r, page := b.get("/app/authorize?" + q.Encode())
		expect(t, r, page, http.StatusBadRequest, "cannot continue")
		if returnPath("/app/authorize?"+q.Encode()) != "" {
			t.Fatalf("%s: a malformed request is a sign-in return path", name)
		}
	}
	if returnPath(p.path()) != p.path() {
		t.Fatalf("return path: %q", returnPath(p.path()))
	}
	// Declining returns to the app with access_denied and no code.
	_, page := b.get(p.path())
	r, _ := b.post(p.path(), url.Values{"csrf": {hidden(t, page, "csrf")}, "decision": {"deny"}})
	back, _ := url.Parse(r.Header.Get("Location"))
	if r.StatusCode != http.StatusSeeOther || back.Query().Get("error") != "access_denied" || back.Query().Get("code") != "" {
		t.Fatalf("deny: %d %q", r.StatusCode, r.Header.Get("Location"))
	}
	// The confirmation needs the session's CSRF token.
	if r, _ := b.post(p.path(), url.Values{"csrf": {"forged"}, "decision": {"allow"}}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("forged confirmation: %d", r.StatusCode)
	}
	// A code is bound to its redirect.
	code := grantCode(t, b.do, p)
	r, err := http.PostForm(f.http.URL+apiPrefix+"/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {p.verifier}, "redirect_uri": {"nightdesk://other"}})
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("other redirect at exchange: %d", r.StatusCode)
	}
}

func TestAppTokensEndWithTheAccount(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthLocal})
	b := f.browser(t)
	b.setup("ira", "correct horse battery")
	p := newPKCE()
	tok, _ := exchange(t, f.http.URL, nil, grantCode(t, b.do, p), p.verifier)
	api := apiClient{t: t, base: f.http.URL, token: tok.AccessToken}
	api.json("GET", apiPrefix+"/me", "", http.StatusOK, nil)
	// Changing the password signs out the other devices, apps included.
	_, page := b.get("/account")
	r, page := b.post("/account/password", url.Values{"csrf": {hidden(t, page, "csrf")}, "current": {"correct horse battery"}, "password": {"another horse battery"}, "confirm": {"another horse battery"}})
	if r.StatusCode >= 400 {
		t.Fatalf("change password: %d %s", r.StatusCode, page)
	}
	api.json("GET", apiPrefix+"/me", "", http.StatusUnauthorized, nil)
}

func TestReaderAPIInProxyMode(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthProxy, Proxy: config.ProxyAuth{UserHeader: "Remote-User", TrustedProxies: []string{"127.0.0.1/32", "::1/128"}}},
		config.Paper{ID: "a", Masthead: "The A Gazette", Timezone: "UTC", Look: "broadsheet", Sections: []config.Section{{Key: "news", Title: "News"}}, Readers: []config.Reader{{Issuer: config.ProxyIssuer, Subject: "alice"}}})
	b := f.browser(t)
	b.header.Set("Remote-User", "alice")
	p := newPKCE()
	tok, r := exchange(t, f.http.URL, nil, grantCode(t, b.do, p), p.verifier)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("token: %d", r.StatusCode)
	}
	// The app's requests carry no proxy identity: the token is the identity,
	// and a header naming someone else changes nothing.
	api := apiClient{t: t, base: f.http.URL, token: tok.AccessToken}
	var me apiMe
	api.json("GET", apiPrefix+"/me", "", http.StatusOK, &me)
	if me.Name != "alice" || len(me.Papers) != 1 {
		t.Fatalf("me: %+v", me)
	}
	if r, page := api.do("GET", apiPrefix+"/me", "", "Remote-User", "mallory"); r.StatusCode != http.StatusOK || !strings.Contains(page, `"alice"`) {
		t.Fatalf("header changed the reader: %d %s", r.StatusCode, page)
	}
}

func TestReaderAPIInNoneMode(t *testing.T) {
	f := modeSetup(t, config.Auth{Mode: config.AuthNone})
	b := f.browser(t)
	p := newPKCE()
	tok, r := exchange(t, f.http.URL, nil, grantCode(t, b.do, p), p.verifier)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("token: %d", r.StatusCode)
	}
	api := apiClient{t: t, base: f.http.URL, token: tok.AccessToken}
	var me apiMe
	api.json("GET", apiPrefix+"/me", "", http.StatusOK, &me)
	if me.Name != "" || len(me.Papers) != 1 || me.Papers[0].Latest != nil {
		t.Fatalf("me: %+v", me)
	}
	api.json("GET", apiPrefix+"/papers/a/editions/latest", "", http.StatusNotFound, nil)
}

func TestReaderAPIWithOIDC(t *testing.T) {
	f := setup(t)
	f.publish(t, f.a, "2026-11-28", apiEditionSample(), nil)
	do := func(method, path string, form url.Values) (*http.Response, string) {
		t.Helper()
		var r *http.Response
		var err error
		if method == "POST" {
			r, err = f.client.PostForm(f.http.URL+path, form)
		} else {
			r, err = f.client.Get(f.http.URL + path)
		}
		if err != nil {
			t.Fatal(err)
		}
		return r, body(t, r)
	}
	p := newPKCE()
	// Signed out, the sign-in link comes back to the app's request.
	r, page := do("GET", p.path(), nil)
	expect(t, r, page, http.StatusUnauthorized, "/login?next=%2Fapp%2Fauthorize", "connect the Night Desk app")
	r, _ = do("GET", "/login?next="+url.QueryEscape(p.path()), nil)
	r, err := f.client.Get(r.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	r, err = f.client.Get(r.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if loc := r.Header.Get("Location"); r.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/app/authorize?") {
		t.Fatalf("after the provider: %d %q", r.StatusCode, loc)
	}
	browser, _ := f.session(t)
	tok, r := exchange(t, f.http.URL, f.client, grantCode(t, do, p), p.verifier)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("token: %d", r.StatusCode)
	}
	app, err := f.db.Session(t.Context(), sha256.Sum256([]byte(tok.AccessToken)))
	if err != nil {
		t.Fatal(err)
	}
	// The app token is the browser reader's, and its groups keep the
	// browser sign-in's age.
	if app.Issuer != browser.Issuer || app.Subject != browser.Subject || !app.Created.Equal(browser.Created) || app.Client != "Night Desk on Test iPhone" || app.CSRF == browser.CSRF {
		t.Fatalf("app session %+v from %+v", app, browser)
	}
	api := apiClient{t: t, base: f.http.URL, token: tok.AccessToken, http: f.client}
	var me apiMe
	api.json("GET", apiPrefix+"/me", "", http.StatusOK, &me)
	if me.Name != "" || len(me.Papers) != 1 || me.Papers[0].ID != "a" {
		t.Fatalf("me: %+v", me)
	}
	// Another reader's paper is not this token's.
	api.json("GET", apiPrefix+"/papers/b/editions/latest", "", http.StatusNotFound, nil)
}
