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
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-webauthn/webauthn/webauthn"
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

// maxPendingLogins bounds the sign-ins waiting for the identity provider.
const maxPendingLogins = 1024

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
	Version   string          // the server's release, shown to apps (reader API)
	Log       *slog.Logger
	// Banner, when set, replaces the banner every page shows (the demo's);
	// NoBanner shows none (the demo's screenshots).
	Banner    string
	NoBanner  bool
	oauth     oauth2.Config
	verifier  *oidc.IDTokenVerifier
	client    *http.Client
	templates *template.Template
	mu        sync.Mutex
	pending   map[[32]byte]transaction
	now       func() time.Time
	// Local accounts: the sign-in limiter, the first-run setup code (empty
	// once an account exists), passkeys when enabled and their challenges.
	// PasskeyProblem says why configured passkeys are off.
	PasskeyProblem string
	limiter        *auth.Limiter
	setupCode      string
	passkeys       *webauthn.WebAuthn
	logins         map[[32]byte]ceremony
	registrations  map[[32]byte]ceremony
	ambient        map[string]string
	lastGroups     map[string]string
	// grants are app sign-ins confirmed in the browser, waiting for the app
	// to exchange their code (appauth.go).
	grants map[[32]byte]grant
}

// New builds the reader for the configured sign-in mode. Only oidc contacts
// the provider (for discovery); the others need nothing outside the server.
func New(ctx context.Context, db *store.Store, current func() *config.Config, clientSecret string) (*Server, error) {
	t, err := template.New("reader").Funcs(template.FuncMap{"add": func(a, b int) int { return a + b }, "clock": clock, "period": period, "asset": asset, "modes": func() []string { return []string{"auto", "light", "sepia", "dark"} }, "percent": func(v float64) float64 { return v * 100 }, "longdate": longDate}).ParseFS(web.Files, "templates/*.html")
	if err != nil {
		return nil, err
	}
	cfg := current()
	s := &Server{Store: db, Config: current, Weather: weather.New(""), templates: t, pending: map[[32]byte]transaction{}, now: time.Now, limiter: auth.NewLimiter(), logins: map[[32]byte]ceremony{}, registrations: map[[32]byte]ceremony{}, ambient: map[string]string{}, lastGroups: map[string]string{}}
	switch cfg.AuthMode() {
	case config.AuthOIDC:
		return s, s.discover(ctx, clientSecret)
	case config.AuthLocal:
		if s.passkeys, s.PasskeyProblem, err = newPasskeys(cfg); err != nil {
			return nil, fmt.Errorf("passkeys: %w", err)
		}
		n, err := db.CountAccounts(ctx)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			s.setupCode = setupCode()
		}
	}
	return s, nil
}

// SetupCode is the first-run code that creates the first local account, or ""
// when one exists.
func (s *Server) SetupCode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setupCode
}

// setupCode is twelve characters from an alphabet without look-alikes,
// grouped in threes: about 60 bits, entered once from the server's log.
func setupCode() string {
	// 32 characters (no 0, o, 1 or l), so a byte maps to one without bias.
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic("system randomness unavailable")
	}
	var out strings.Builder
	for i, v := range b {
		if i > 0 && i%3 == 0 {
			out.WriteByte('-')
		}
		out.WriteByte(alphabet[int(v)%len(alphabet)])
	}
	return out.String()
}

func (s *Server) discover(ctx context.Context, clientSecret string) error {
	client := &http.Client{Timeout: 10 * time.Second}
	if supplied, ok := ctx.Value(oauth2.HTTPClient).(*http.Client); ok {
		copy := *supplied
		copy.Timeout = 10 * time.Second
		client = &copy
	}
	discovery, cancel := context.WithTimeout(oidc.ClientContext(ctx, client), 10*time.Second)
	defer cancel()
	cfg := s.Config()
	provider, err := oidc.NewProvider(discovery, cfg.Server.OIDC.Issuer)
	if err != nil {
		return fmt.Errorf("OIDC discovery failed: %w", err)
	}
	scopes := cfg.Server.OIDC.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID}
	}
	s.oauth = oauth2.Config{ClientID: cfg.Server.OIDC.ClientID, ClientSecret: clientSecret, Endpoint: provider.Endpoint(), RedirectURL: strings.TrimRight(cfg.Server.PublicBaseURL, "/") + "/auth/callback", Scopes: scopes}
	s.verifier = provider.Verifier(&oidc.Config{ClientID: cfg.Server.OIDC.ClientID})
	s.client = client
	return nil
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
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		next.ServeHTTP(w, r)
	})
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	switch s.Config().AuthMode() {
	case config.AuthOIDC:
		mux.HandleFunc("GET /login", s.login)
		mux.HandleFunc("GET /auth/callback", s.callback)
	case config.AuthLocal:
		mux.HandleFunc("GET /login", s.localLogin)
		mux.HandleFunc("POST /login", s.localLoginSubmit)
		mux.HandleFunc("GET /setup", s.setupPage)
		mux.HandleFunc("POST /setup", s.setupSubmit)
		mux.HandleFunc("GET /invite/{token}", s.invitePage)
		mux.HandleFunc("POST /invite/{token}", s.inviteSubmit)
		mux.Handle("GET /account", s.protect(http.HandlerFunc(s.accountPage), true))
		mux.Handle("POST /account/password", s.protect(http.HandlerFunc(s.changePassword), true))
		mux.Handle("GET /admin", s.protect(http.HandlerFunc(s.adminPage), true))
		mux.Handle("POST /admin/invite", s.protect(http.HandlerFunc(s.adminInvite), true))
		mux.Handle("POST /admin/reset", s.protect(http.HandlerFunc(s.adminReset), true))
		mux.Handle("POST /admin/remove", s.protect(http.HandlerFunc(s.adminRemove), true))
		mux.Handle("POST /admin/role", s.protect(http.HandlerFunc(s.adminRole), true))
		mux.Handle("POST /admin/revoke", s.protect(http.HandlerFunc(s.adminRevoke), true))
		if s.passkeys != nil {
			mux.HandleFunc("POST /login/passkey/begin", s.passkeyLoginBegin)
			mux.HandleFunc("POST /login/passkey/finish", s.passkeyLoginFinish)
			mux.Handle("POST /account/passkeys/begin", s.protect(http.HandlerFunc(s.passkeyRegisterBegin), true))
			mux.Handle("POST /account/passkeys/finish", s.protect(http.HandlerFunc(s.passkeyRegisterFinish), true))
			mux.Handle("POST /account/passkeys/remove", s.protect(http.HandlerFunc(s.passkeyRemove), true))
		}
	default:
		// Proxy and none sign in on every request; there is no sign-in page.
		mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/", http.StatusSeeOther) })
	}
	mux.Handle("GET /app/authorize", s.protect(http.HandlerFunc(s.appAuthorize), false))
	mux.Handle("POST /app/authorize", s.protect(http.HandlerFunc(s.appAuthorizeSubmit), false))
	s.apiRoutes(mux)
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
	mux.Handle("GET /push/open", s.protect(http.HandlerFunc(s.pushOpen), false))
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
	// At the cap, the oldest unfinished sign-in makes room: refusing instead
	// would let anyone who can reach /login lock every reader out.
	for len(s.pending) >= maxPendingLogins {
		var oldest [sha256.Size]byte
		first := true
		for k, v := range s.pending {
			if first || v.expires.Before(s.pending[oldest].expires) {
				oldest, first = k, false
			}
		}
		delete(s.pending, oldest)
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
		s.signInFailed(w, http.StatusBadRequest, "This sign-in was started too long ago, or in another tab or window.")
		return
	}
	hash := sha256.Sum256([]byte(c.Value))
	s.mu.Lock()
	tx, ok := s.pending[hash]
	delete(s.pending, hash)
	s.mu.Unlock()
	s.cookie(w, "nd_login", "", -1)
	if !ok || !tx.expires.After(s.now()) || !equal(tx.state, r.URL.Query().Get("state")) || r.URL.Query().Get("code") == "" {
		s.signInFailed(w, http.StatusBadRequest, "This sign-in was started too long ago, or in another tab or window.")
		return
	}
	ctx, cancel := context.WithTimeout(oidc.ClientContext(r.Context(), s.client), 10*time.Second)
	defer cancel()
	token, err := s.oauth.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(tx.verifier))
	if err != nil {
		s.signInFailed(w, http.StatusBadRequest, "Your sign-in provider did not confirm who you are.")
		return
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		s.signInFailed(w, http.StatusBadRequest, "Your sign-in provider did not confirm who you are.")
		return
	}
	id, err := s.verifier.Verify(ctx, raw)
	if err != nil || id.Subject == "" || !equal(id.Nonce, tx.nonce) {
		s.signInFailed(w, http.StatusBadRequest, "Your sign-in provider did not confirm who you are.")
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
		s.signInFailed(w, http.StatusInternalServerError, "Night Desk could not save your sign-in.")
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
// on this site (/p/{paper}/...) or an app's sign-in request (/app/authorize),
// never another origin.
func returnPath(next string) string {
	if len(next) > 1024 || strings.ContainsAny(next, "\\\x00\r\n") {
		return ""
	}
	if strings.HasPrefix(next, "/app/authorize?") {
		u, err := url.Parse(next)
		if err != nil || u.Path != "/app/authorize" || u.Scheme != "" || u.Host != "" || u.User != nil {
			return ""
		}
		// Only well-formed requests come back; a bad one has nothing to resume.
		if _, problem := parseAuthorize(u.Query()); problem != "" {
			return ""
		}
		return u.RequestURI()
	}
	if len(next) > 512 || !strings.HasPrefix(next, "/p/") {
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
		cfg := s.Config()
		token := ""
		if c, err := r.Cookie("nd_session"); err == nil {
			token = c.Value
		}
		switch cfg.AuthMode() {
		case config.AuthProxy, config.AuthNone:
			var ok bool
			if token, ok = s.ambientSession(w, r, cfg, token); !ok {
				return
			}
		}
		if token == "" {
			s.unauthorized(w, r)
			return
		}
		scope, err := auth.AuthenticateReader(r.Context(), s.Store, cfg, token, s.now(), s.lifetime())
		if err == auth.ErrUnauthorized {
			s.cookie(w, "nd_session", "", -1)
			s.unauthorized(w, r)
			return
		}
		if err != nil && err != auth.ErrUnmapped {
			http.Error(w, "Reader unavailable", 500)
			return
		}
		s.cookie(w, "nd_session", token, int(s.lifetime().Seconds()))
		if err == auth.ErrUnmapped && !unmappedOK {
			if r.URL.Path == "/" {
				v := authView{Page: "nopaper", Title: "No paper for you yet"}
				// Signing out only helps where a different account could
				// sign in; a proxy or no sign-in at all brings the same one back.
				if mode := cfg.AuthMode(); mode == config.AuthOIDC || mode == config.AuthLocal {
					v.CSRF = scope.Session().CSRF
				}
				s.renderAuth(w, http.StatusOK, v)
			} else {
				s.renderAuth(w, http.StatusNotFound, authView{Page: "message", Title: "Page not found", Next: "/"})
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
	signInFor(w, r, "Sign in again to read this paper.")
}

// signInFor is the sign-in page that returns to the request afterwards, with
// tagline in place of the usual one.
func signInFor(w http.ResponseWriter, r *http.Request, tagline string) {
	page := signIn
	if next := returnPath(r.URL.RequestURI()); next != "" {
		page = strings.Replace(page, `<a href="/login">`, `<a href="/login?next=`+template.HTMLEscapeString(url.QueryEscape(next))+`">`, 1)
		page = strings.Replace(page, `Your paper, set overnight.`, template.HTMLEscapeString(tagline), 1)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(page))
}

// unauthorized answers a request without a session: the sign-in page of the
// mode, with status 401.
func (s *Server) unauthorized(w http.ResponseWriter, r *http.Request) {
	switch s.Config().AuthMode() {
	case config.AuthLocal:
		// Logout clears the offline copy, as the OIDC sign-in page does: the
		// session may have been ended elsewhere.
		v := authView{Logout: true}
		if r.Method == http.MethodGet {
			v.Next = returnPath(r.URL.RequestURI())
		}
		s.loginPage(w, r, http.StatusUnauthorized, v)
		return
	case config.AuthProxy:
		s.renderAuth(w, http.StatusUnauthorized, authView{Page: "message", Title: "Sign in through your proxy", Logout: true, Error: "This Night Desk signs readers in through a reverse proxy, and this request did not come through it signed in."})
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/app/authorize" {
		// An app's sign-in comes back to its confirmation after the provider.
		signInFor(w, r, "Sign in to connect the Night Desk app.")
		return
	}
	unauthorized(w, r)
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
// reveals nothing about any paper. Its styles are a static file because the
// CSP allows no inline style.
var signIn = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover"><meta name="theme-color" content="#0c0c0d"><meta name="color-scheme" content="dark"><title>Night Desk</title>` +
	`<link rel="icon" href="/static/favicon.svg" type="image/svg+xml"><link rel="icon" href="/static/icon-32.png" type="image/png" sizes="32x32"><link rel="apple-touch-icon" href="/static/apple-touch-icon.png">` +
	`<link rel="stylesheet" href="` + asset("signin.css") + `"></head>` +
	`<body data-logout="true"><main><h1><img src="/static/signin-art.png" width="640" height="420" alt="Night Desk"></h1>` +
	`<p class="tag">Your paper, set overnight.</p><a href="/login">Sign in to Night Desk <span aria-hidden="true">→</span></a>` +
	`<p class="note">You will continue to your sign-in page and come straight back.</p></main><script defer src="` + asset("app.js") + `"></script></body></html>`

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

// signInFailed explains an OIDC sign-in that did not finish and offers to
// start it again, instead of a bare error line.
func (s *Server) signInFailed(w http.ResponseWriter, status int, why string) {
	s.renderAuth(w, status, authView{Page: "message", Title: "Sign-in did not finish", Error: why, Next: "/login", NextText: "Sign in again"})
}

// notFound is the reader's page for a date, story or page it does not have,
// inside the paper's chrome with a way back to today's edition.
func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	v := s.base(r)
	v.Message, v.Spot, v.Home = "This page is not in the paper", "paper", true
	s.renderStatus(w, http.StatusNotFound, "message", v)
}

// longDate spells an edition date the way the masthead does:
// "Thursday, November 19, 2026". Anything else is shown as given.
func longDate(date string) string {
	t, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return date
	}
	return t.Format("Monday, January 2, 2006")
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
