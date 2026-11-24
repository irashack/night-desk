package reader

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/editorial"
	"github.com/irashack/night-desk/internal/push"
	"github.com/irashack/night-desk/internal/store"
	"github.com/irashack/night-desk/internal/weather"
	"github.com/irashack/night-desk/web"
	"golang.org/x/oauth2"
)

const CSP = "default-src 'self'; img-src 'self'; style-src 'self'; script-src 'self'; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

type transaction struct {
	state, nonce, verifier, next string
	expires                      time.Time
}
type Server struct {
	Store     *store.Store
	Config    func() *config.Config
	Weather   *weather.Client
	Editorial *editorial.Desk // when set, readers may edit their paper's sections
	Push      *push.Key       // when set, readers may opt in to web push
	oauth     oauth2.Config
	verifier  *oidc.IDTokenVerifier
	client    *http.Client
	templates *template.Template
	mu        sync.Mutex
	pending   map[[32]byte]transaction
	now       func() time.Time
}

func New(ctx context.Context, db *store.Store, current func() *config.Config, clientSecret string) (*Server, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	if supplied, ok := ctx.Value(oauth2.HTTPClient).(*http.Client); ok {
		copy := *supplied
		copy.Timeout = 10 * time.Second
		client = &copy
	}
	discovery, cancel := context.WithTimeout(oidc.ClientContext(ctx, client), 10*time.Second)
	defer cancel()
	cfg := current()
	provider, err := oidc.NewProvider(discovery, cfg.Server.OIDC.Issuer)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery failed: %w", err)
	}
	t, err := template.New("reader").Funcs(template.FuncMap{"add": func(a, b int) int { return a + b }, "clock": clock, "period": period, "asset": asset, "modes": func() []string { return []string{"auto", "light", "sepia", "dark"} }, "percent": func(v float64) float64 { return v * 100 }}).ParseFS(web.Files, "templates/*.html")
	if err != nil {
		return nil, err
	}
	scopes := cfg.Server.OIDC.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID}
	}
	return &Server{Store: db, Config: current, Weather: weather.New(""), oauth: oauth2.Config{ClientID: cfg.Server.OIDC.ClientID, ClientSecret: clientSecret, Endpoint: provider.Endpoint(), RedirectURL: strings.TrimRight(cfg.Server.PublicBaseURL, "/") + "/auth/callback", Scopes: scopes}, verifier: provider.Verifier(&oidc.Config{ClientID: cfg.Server.OIDC.ClientID}), client: client, templates: t, pending: map[[32]byte]transaction{}, now: time.Now}, nil
}
func random() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("system randomness unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func (s *Server) secure() bool {
	u, _ := url.Parse(s.Config().Server.PublicBaseURL)
	return !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))
}
func (s *Server) cookie(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: s.secure(), SameSite: http.SameSiteLaxMode, MaxAge: age})
}
func (s *Server) lifetime() time.Duration {
	d, err := time.ParseDuration(s.Config().Server.SessionLifetime)
	if err != nil || d <= 0 {
		return 720 * time.Hour
	}
	return d
}
func Headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", CSP)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "")
		next.ServeHTTP(w, r)
	})
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", s.login)
	mux.HandleFunc("GET /auth/callback", s.callback)
	mux.HandleFunc("GET /static/", s.static)
	mux.HandleFunc("GET /sw.js", s.static)
	mux.Handle("POST /logout", s.protect(http.HandlerFunc(s.logout), true))
	mux.Handle("GET /{$}", s.protect(http.HandlerFunc(s.edition), false))
	mux.Handle("GET /e/{date}", s.protect(http.HandlerFunc(s.edition), false))
	mux.Handle("GET /read/{date}/{item}", s.protect(http.HandlerFunc(s.story), false))
	mux.Handle("GET /search", s.protect(http.HandlerFunc(s.search), false))
	mux.Handle("GET /archive", s.protect(http.HandlerFunc(s.archive), false))
	mux.Handle("GET /sections", s.protect(http.HandlerFunc(s.sections), false))
	mux.Handle("POST /sections", s.protect(http.HandlerFunc(s.editSections), false))
	mux.Handle("GET /img/{image}", s.protect(http.HandlerFunc(s.image), false))
	mux.Handle("POST /feedback", s.protect(http.HandlerFunc(s.feedback), false))
	mux.Handle("POST /push/subscribe", s.protect(http.HandlerFunc(s.pushSubscribe), false))
	mux.Handle("POST /push/unsubscribe", s.protect(http.HandlerFunc(s.pushUnsubscribe), false))
	mux.Handle("GET /manifest.webmanifest", s.protect(http.HandlerFunc(s.manifest), false))
	// Every paper a session reads besides its default one lives under /p/{paper}.
	// Sections stay at /sections: only a subject reader's own paper is editable.
	mux.Handle("GET /p/{paper}/{$}", s.protect(http.HandlerFunc(s.edition), false))
	mux.Handle("GET /p/{paper}/e/{date}", s.protect(http.HandlerFunc(s.edition), false))
	mux.Handle("GET /p/{paper}/read/{date}/{item}", s.protect(http.HandlerFunc(s.story), false))
	mux.Handle("GET /p/{paper}/search", s.protect(http.HandlerFunc(s.search), false))
	mux.Handle("GET /p/{paper}/archive", s.protect(http.HandlerFunc(s.archive), false))
	mux.Handle("POST /p/{paper}/feedback", s.protect(http.HandlerFunc(s.feedback), false))
	mux.Handle("POST /p/{paper}/push/subscribe", s.protect(http.HandlerFunc(s.pushSubscribe), false))
	mux.Handle("POST /p/{paper}/push/unsubscribe", s.protect(http.HandlerFunc(s.pushUnsubscribe), false))
	mux.Handle("POST /p/{paper}/push/status", s.protect(http.HandlerFunc(s.pushStatus), false))
	mux.Handle("POST /push/status", s.protect(http.HandlerFunc(s.pushStatus), false))
	return Headers(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	}))
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	key, state, nonce, verifier := random(), random(), random(), oauth2.GenerateVerifier()
	now := s.now()
	s.mu.Lock()
	for k, v := range s.pending {
		if !v.expires.After(now) {
			delete(s.pending, k)
		}
	}
	if len(s.pending) >= 1024 {
		s.mu.Unlock()
		http.Error(w, "Try signing in again shortly", 503)
		return
	}
	if old, err := r.Cookie("nd_login"); err == nil {
		delete(s.pending, sha256.Sum256([]byte(old.Value)))
	}
	s.pending[sha256.Sum256([]byte(key))] = transaction{state, nonce, verifier, returnPath(r.URL.Query().Get("next")), now.Add(10 * time.Minute)}
	s.mu.Unlock()
	s.cookie(w, "nd_login", key, 600)
	oauth := s.oauth
	oauth.Scopes = s.Config().SignInScopes()
	http.Redirect(w, r, oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce)), http.StatusFound)
}
func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("nd_login")
	if err != nil {
		http.Error(w, "Invalid sign-in", 400)
		return
	}
	hash := sha256.Sum256([]byte(c.Value))
	s.mu.Lock()
	tx, ok := s.pending[hash]
	delete(s.pending, hash)
	s.mu.Unlock()
	s.cookie(w, "nd_login", "", -1)
	if !ok || !tx.expires.After(s.now()) || !equal(tx.state, r.URL.Query().Get("state")) || r.URL.Query().Get("code") == "" {
		http.Error(w, "Invalid sign-in", 400)
		return
	}
	ctx, cancel := context.WithTimeout(oidc.ClientContext(r.Context(), s.client), 10*time.Second)
	defer cancel()
	token, err := s.oauth.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(tx.verifier))
	if err != nil {
		http.Error(w, "Sign-in failed", 400)
		return
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "Sign-in failed", 400)
		return
	}
	id, err := s.verifier.Verify(ctx, raw)
	if err != nil || id.Subject == "" || !equal(id.Nonce, tx.nonce) {
		http.Error(w, "Sign-in failed", 400)
		return
	}
	var groups []string
	checked := s.Config().WantsGroups()
	if checked {
		// A claim that cannot be read grants no group, but never blocks a
		// reader whose access does not depend on it.
		groups, _ = groupsClaim(id)
	}
	key := random()
	now := s.now()
	session := auth.Session{Hash: sha256.Sum256([]byte(key)), Issuer: id.Issuer, Subject: id.Subject, CSRF: random(), Created: now, LastSeen: now, Expires: now.Add(s.lifetime()), Groups: groups, GroupsChecked: checked}
	if err = s.Store.InsertSession(r.Context(), session); err != nil {
		http.Error(w, "Sign-in unavailable", 500)
		return
	}
	if old, e := r.Cookie("nd_session"); e == nil {
		_ = s.Store.DeleteSession(r.Context(), sha256.Sum256([]byte(old.Value)))
	}
	s.cookie(w, "nd_session", key, int(s.lifetime().Seconds()))
	next := "/"
	if tx.next != "" {
		next = tx.next
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// returnPath accepts where sign-in should land afterwards: only a paper page
// on this site (/p/{paper}/...), never another origin.
func returnPath(next string) string {
	if len(next) > 512 || !strings.HasPrefix(next, "/p/") || strings.ContainsAny(next, "\\\x00\r\n") {
		return ""
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || !paperPath.MatchString(u.Path) || strings.Contains(u.Path, "..") {
		return ""
	}
	return u.RequestURI()
}

var paperPath = regexp.MustCompile(`^/p/[a-z0-9][a-z0-9-]{0,63}/[A-Za-z0-9/._-]*$`)

// groupsClaim reads the ID token's groups claim: an array of group names (a
// lone string is taken as one group, a missing claim as none). Malformed or
// oversized claims are an error rather than truncated, so membership is never
// guessed.
func groupsClaim(id *oidc.IDToken) ([]string, error) {
	var claims struct {
		Groups json.RawMessage `json:"groups"`
	}
	if err := id.Claims(&claims); err != nil {
		return nil, err
	}
	if len(claims.Groups) == 0 || string(claims.Groups) == "null" {
		return nil, nil
	}
	var groups []string
	if err := json.Unmarshal(claims.Groups, &groups); err != nil {
		var one string
		if json.Unmarshal(claims.Groups, &one) != nil {
			return nil, fmt.Errorf("groups claim is not a list of names")
		}
		groups = []string{one}
	}
	if len(groups) > 512 {
		return nil, fmt.Errorf("groups claim too large")
	}
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		if len(g) > 256 {
			return nil, fmt.Errorf("group name too long")
		}
		if g != "" && !slices.Contains(out, g) {
			out = append(out, g)
		}
	}
	return out, nil
}
func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

type principal struct {
	scope auth.ReaderScope
	paper config.Paper
	base  string // link prefix of the selected paper, see view.Base
}

func paperBase(id string) string { return "/p/" + id }

type principalKey struct{}

func current(r *http.Request) principal { return r.Context().Value(principalKey{}).(principal) }
func (s *Server) protect(next http.Handler, unmappedOK bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("nd_session")
		if err != nil {
			unauthorized(w, r)
			return
		}
		cfg := s.Config()
		scope, err := auth.AuthenticateReader(r.Context(), s.Store, cfg, c.Value, s.now(), s.lifetime())
		if err == auth.ErrUnauthorized {
			s.cookie(w, "nd_session", "", -1)
			unauthorized(w, r)
			return
		}
		if err != nil && err != auth.ErrUnmapped {
			http.Error(w, "Reader unavailable", 500)
			return
		}
		s.cookie(w, "nd_session", c.Value, int(s.lifetime().Seconds()))
		if err == auth.ErrUnmapped && !unmappedOK {
			if r.URL.Path == "/" {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write([]byte("<!doctype html><html lang=\"en\"><title>Night Desk</title><p>No paper for you yet</p></html>"))
			} else {
				http.NotFound(w, r)
			}
			return
		}
		base := ""
		if id := r.PathValue("paper"); id != "" {
			selected, ok := scope.For(id)
			if !ok && scope.NeedsSignIn(id) {
				// Membership as of sign-in has run out, or was never asked
				// for: sign in again to refresh it, then come back here. The
				// session keeps reading its own paper.
				signInAgain(w, r)
				return
			}
			if !ok {
				http.NotFound(w, r)
				return
			}
			scope = selected
			if scope.Default() && r.Method == http.MethodGet {
				// One URL per page: the default paper lives at /.
				target := strings.TrimPrefix(r.URL.Path, paperBase(id))
				if r.URL.RawQuery != "" {
					target += "?" + r.URL.RawQuery
				}
				http.Redirect(w, r, target, http.StatusSeeOther)
				return
			}
			if !scope.Default() {
				base = paperBase(id)
			}
		}
		paper, _ := cfg.Paper(scope.PaperScope().PaperID())
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{scope, paper, base})))
	})
}

// signInAgain is the sign-in page with a way back to the paper page asked for.
func signInAgain(w http.ResponseWriter, r *http.Request) {
	page := signIn
	if next := returnPath(r.URL.RequestURI()); next != "" {
		page = strings.Replace(page, `<a href="/login">`, `<a href="/login?next=`+template.HTMLEscapeString(url.QueryEscape(next))+`">`, 1)
		page = strings.Replace(page, `Your paper, set overnight.`, `Sign in again to read this paper.`, 1)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(page))
}
func unauthorized(w http.ResponseWriter, r *http.Request) {
	// Do not redirect a navigation through the provider: a failed cross-origin
	// fetch could otherwise be mistaken for an offline navigation by the worker.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(signIn))
}

// signIn is the page before the identity provider: the logo's artwork under a
// desk-lamp glow and one link. It is static, so it needs no session and
// reveals nothing about any paper.
const signIn = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover"><meta name="theme-color" content="#0c0c0d"><meta name="color-scheme" content="dark"><title>Night Desk</title>` +
	`<link rel="icon" href="/static/favicon.svg" type="image/svg+xml"><link rel="icon" href="/static/icon-32.png" type="image/png" sizes="32x32"><link rel="apple-touch-icon" href="/static/apple-touch-icon.png">` +
	`<style>@font-face{font-family:Old Standard TT;src:url(/static/fonts/oldstandard-italic.woff2) format('woff2');font-style:italic;font-display:swap}` +
	`@font-face{font-family:Libre Franklin;src:url(/static/fonts/librefranklin-var.woff2) format('woff2');font-weight:100 900;font-display:swap}` +
	`*{box-sizing:border-box}html{background:#0c0c0d}body{margin:0;min-height:100dvh;display:grid;place-items:center;padding:32px 20px max(32px,env(safe-area-inset-bottom));color:#efe8dc;` +
	`background:radial-gradient(60vmax 44vmax at 50% 30%,rgba(255,178,96,.13),rgba(255,178,96,.04) 45%,transparent 70%),#0c0c0d;font-family:Libre Franklin,system-ui,sans-serif}` +
	`main{width:min(100%,420px);display:grid;justify-items:center;text-align:center;animation:rise .9s cubic-bezier(.2,.7,.2,1) both}` +
	`h1{margin:0;width:100%}h1 img{display:block;width:100%;height:auto}` +
	`p{margin:0}.tag{margin-top:6px;font:italic 19px/1.4 Old Standard TT,Georgia,serif;color:#cfc6b8}` +
	`a{margin-top:30px;display:inline-flex;align-items:center;gap:10px;padding:15px 30px;border-radius:999px;background:#efe8dc;color:#141416;font-weight:600;font-size:15px;letter-spacing:.02em;text-decoration:none;` +
	`box-shadow:0 0 0 1px rgba(255,214,160,.25),0 10px 40px -10px rgba(255,170,80,.45);transition:transform .2s,box-shadow .2s}` +
	`a:hover{transform:translateY(-1px);box-shadow:0 0 0 1px rgba(255,214,160,.4),0 14px 48px -10px rgba(255,170,80,.6)}a:focus-visible{outline:2px solid #e2582f;outline-offset:4px}` +
	`.note{margin-top:18px;font-size:12.5px;color:#8a847a;letter-spacing:.01em}` +
	`@keyframes rise{from{opacity:0;transform:translateY(10px)}}@media(prefers-reduced-motion:reduce){main{animation:none}a{transition:none}}</style></head>` +
	`<body data-logout="true"><main><h1><img src="/static/signin-art.png" width="640" height="420" alt="Night Desk"></h1>` +
	`<p class="tag">Your paper, set overnight.</p><a href="/login">Sign in to Night Desk <span aria-hidden="true">→</span></a>` +
	`<p class="note">You will continue to your sign-in page and come straight back.</p></main><script defer src="/static/app.js"></script></body></html>`

func validCSRF(r *http.Request) bool {
	return equal(current(r).scope.Session().CSRF, r.PostForm.Get("csrf"))
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if r.ParseForm() != nil || !validCSRF(r) {
		http.Error(w, "Invalid CSRF token", 403)
		return
	}
	if err := s.Store.SignOut(r.Context(), current(r).scope.Session().Hash); err != nil {
		http.Error(w, "Sign-out failed", 500)
		return
	}
	s.cookie(w, "nd_session", "", -1)
	w.Header().Set("Clear-Site-Data", "\"cache\", \"storage\"")
	s.render(w, "message", view{Message: "You are signed out", Logout: true})
}
func (s *Server) render(w http.ResponseWriter, name string, data view) {
	s.renderStatus(w, http.StatusOK, name, data)
}
func (s *Server) renderStatus(w http.ResponseWriter, status int, name string, data view) {
	var b bytes.Buffer
	if err := s.templates.ExecuteTemplate(&b, name, data); err != nil {
		http.Error(w, "Reader unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b.Bytes())
}
func asset(name string) string {
	b, err := web.Files.ReadFile("static/" + name)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return "/static/" + name + "?v=" + hex.EncodeToString(sum[:6])
}
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "sw.js" {
		name = "static/sw.js"
		w.Header().Set("Service-Worker-Allowed", "/")
	}
	b, err := web.Files.ReadFile(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sum := sha256.Sum256(b)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=3600, must-revalidate")
	if r.URL.Path == "/sw.js" {
		w.Header().Set("Cache-Control", "no-cache")
	}
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(304)
		return
	}
	// ServeContent sets the proper media type and implements range requests.
	http.ServeContent(w, r, filepath.Base(name), time.Time{}, strings.NewReader(string(b)))
}

var imageName = regexp.MustCompile(`^[a-f0-9]{64}\.jpg$`)

func (s *Server) image(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("image")
	if !imageName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	sha := strings.TrimSuffix(name, ".jpg")
	// Image links carry no paper, so any paper the session reads may serve one.
	var ok bool
	for _, a := range current(r).scope.Papers() {
		scope, _ := current(r).scope.For(a.Paper)
		found, err := s.Store.HasImage(r.Context(), scope.PaperScope(), sha)
		if err != nil {
			http.Error(w, "Reader unavailable", 500)
			return
		}
		if ok = found; ok {
			break
		}
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeFile(w, r, filepath.Join(s.Config().Server.DataDir, "images", sha[:2], name))
}
func (s *Server) manifest(w http.ResponseWriter, r *http.Request) {
	b, _ := fs.ReadFile(web.Files, "manifest.webmanifest")
	var v map[string]any
	_ = json.Unmarshal(b, &v)
	v["name"] = current(r).paper.Masthead
	v["short_name"] = current(r).paper.Masthead
	w.Header().Set("Content-Type", "application/manifest+json")
	_ = json.NewEncoder(w).Encode(v)
}
