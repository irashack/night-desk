package reader

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/irashack/night-desk/internal/auth"
)

// A native app signs in through the reader's own sign-in, whatever the mode:
// it opens /app/authorize in the system browser (ASWebAuthenticationSession on
// iOS), the reader signs in there as usual and confirms, and the server
// redirects to the app's URL scheme with a one-time code. The app exchanges
// the code and its PKCE verifier at /api/reader/v1/token for a reader token: a
// session row of its own, named after the app and device, sent as a bearer
// token. Proxy mode needs the proxy to pass /api/reader/ through without its
// own sign-in, since the app has no proxy cookie (docs/configuration.md).

// appRedirects are the app callbacks a code may be sent to. A custom scheme can
// be claimed by another app, which is why the code is bound to the PKCE
// challenge of the app that started the sign-in.
var appRedirects = []string{"nightdesk://auth"}

// maxPendingGrants bounds codes waiting for exchange; codes live two minutes.
const (
	maxPendingGrants = 256
	grantLifetime    = 2 * time.Minute
)

type grant struct {
	session  auth.Session // the browser session that confirmed
	client   string
	redirect string
	verifier [32]byte // sha256 of the expected code_verifier, i.e. the challenge
	expires  time.Time
}

// authorizeRequest is the app's sign-in request, checked.
type authorizeRequest struct {
	Redirect, State, Challenge, Client string
}

var (
	pkceChallenge = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	pkceVerifier  = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
	stateValue    = regexp.MustCompile(`^[A-Za-z0-9._~-]{16,128}$`)
)

func parseAuthorize(q url.Values) (authorizeRequest, string) {
	a := authorizeRequest{Redirect: q.Get("redirect_uri"), State: q.Get("state"), Challenge: q.Get("code_challenge"), Client: clientName(q.Get("client_name"))}
	switch {
	case q.Get("response_type") != "code":
		return a, "response_type must be code"
	case !slices.Contains(appRedirects, a.Redirect):
		return a, "redirect_uri is not a Night Desk app"
	case q.Get("code_challenge_method") != "S256" || !pkceChallenge.MatchString(a.Challenge):
		return a, "code_challenge must be an S256 PKCE challenge"
	case !stateValue.MatchString(a.State):
		return a, "state must be 16 to 128 URL-safe characters"
	}
	return a, ""
}

// clientName keeps a short, printable app and device name ("Night Desk on
// Ira's iPhone"); anything else becomes "Night Desk app".
func clientName(v string) string {
	v = strings.Join(strings.Fields(v), " ")
	if v == "" || !utf8.ValidString(v) || strings.ContainsFunc(v, unicode.IsControl) {
		return "Night Desk app"
	}
	if utf8.RuneCountInString(v) > 80 {
		v = string([]rune(v)[:80])
	}
	return v
}

// appAuthorize asks the signed-in reader to confirm the app's sign-in.
func (s *Server) appAuthorize(w http.ResponseWriter, r *http.Request) {
	a, problem := parseAuthorize(r.URL.Query())
	if problem != "" {
		// Never redirect a malformed request: the redirect target itself may
		// be what is wrong.
		s.renderAuth(w, http.StatusBadRequest, authView{Page: "message", Title: "This app sign-in cannot continue", Error: problem, Next: "/", NextText: "Go to the paper"})
		return
	}
	p := current(r)
	masthead := p.paper.Masthead
	if masthead == "" {
		masthead = "Night Desk"
	}
	s.renderAuth(w, http.StatusOK, authView{Page: "authorize", Title: "Sign in the app", CSRF: p.scope.Session().CSRF, Username: a.Client, Action: masthead})
}

// appAuthorizeSubmit records the confirmed grant and returns to the app with
// its code, or with access_denied when the reader declined.
func (s *Server) appAuthorizeSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if r.ParseForm() != nil || !validCSRF(r) {
		http.Error(w, "Invalid CSRF token", http.StatusForbidden)
		return
	}
	a, problem := parseAuthorize(r.URL.Query())
	if problem != "" {
		http.Error(w, problem, http.StatusBadRequest)
		return
	}
	back := url.Values{"state": {a.State}}
	if r.PostForm.Get("decision") != "allow" {
		back.Set("error", "access_denied")
		http.Redirect(w, r, a.Redirect+"?"+back.Encode(), http.StatusSeeOther)
		return
	}
	challenge, _ := base64.RawURLEncoding.DecodeString(a.Challenge)
	g := grant{session: current(r).scope.Session(), client: a.Client, redirect: a.Redirect, expires: s.now().Add(grantLifetime)}
	copy(g.verifier[:], challenge)
	code := random()
	now := s.now()
	s.mu.Lock()
	if s.grants == nil {
		s.grants = map[[32]byte]grant{}
	}
	for k, v := range s.grants {
		if !v.expires.After(now) {
			delete(s.grants, k)
		}
	}
	if len(s.grants) >= maxPendingGrants {
		s.mu.Unlock()
		http.Error(w, "Too many app sign-ins at once; try again in a few minutes", http.StatusServiceUnavailable)
		return
	}
	s.grants[sha256.Sum256([]byte(code))] = g
	s.mu.Unlock()
	back.Set("code", code)
	http.Redirect(w, r, a.Redirect+"?"+back.Encode(), http.StatusSeeOther)
}

// tokenResponse is the OAuth-style answer to a code exchange.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	// ExpiresIn is the token's idle lifetime in seconds: each use renews it.
	ExpiresIn int    `json:"expires_in"`
	Client    string `json:"client"`
}

// appToken exchanges a code and its PKCE verifier for a reader token. It takes
// a form (as OAuth clients send) or JSON.
func (s *Server) appToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var in struct {
		GrantType    string `json:"grant_type"`
		Code         string `json:"code"`
		CodeVerifier string `json:"code_verifier"`
		RedirectURI  string `json:"redirect_uri"`
	}
	if media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); media == "application/json" {
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			apiError(w, http.StatusBadRequest, "invalid_request", "The body is not JSON.")
			return
		}
	} else {
		if r.ParseForm() != nil {
			apiError(w, http.StatusBadRequest, "invalid_request", "The body is not a form.")
			return
		}
		in.GrantType, in.Code, in.CodeVerifier, in.RedirectURI = r.PostForm.Get("grant_type"), r.PostForm.Get("code"), r.PostForm.Get("code_verifier"), r.PostForm.Get("redirect_uri")
	}
	if in.GrantType != "authorization_code" {
		apiError(w, http.StatusBadRequest, "unsupported_grant_type", "grant_type must be authorization_code.")
		return
	}
	if in.Code == "" || !pkceVerifier.MatchString(in.CodeVerifier) {
		apiError(w, http.StatusBadRequest, "invalid_request", "code and a PKCE code_verifier are required.")
		return
	}
	key := sha256.Sum256([]byte(in.Code))
	s.mu.Lock()
	g, ok := s.grants[key]
	// One attempt per code, right or wrong.
	delete(s.grants, key)
	s.mu.Unlock()
	if !ok || !g.expires.After(s.now()) || g.redirect != in.RedirectURI || sha256.Sum256([]byte(in.CodeVerifier)) != g.verifier {
		apiError(w, http.StatusBadRequest, "invalid_grant", "The code is unknown, used, expired or not this app's.")
		return
	}
	token := random()
	now := s.now()
	// Group membership keeps the browser sign-in's age: an app token never
	// extends how long a groups claim counts (see auth.AuthenticateReader).
	session := auth.Session{Hash: sha256.Sum256([]byte(token)), Issuer: g.session.Issuer, Subject: g.session.Subject, CSRF: random(), Created: g.session.Created, LastSeen: now, Expires: now.Add(s.lifetime()), Groups: g.session.Groups, GroupsChecked: g.session.GroupsChecked, Client: g.client}
	if err := s.Store.InsertSession(r.Context(), session); err != nil {
		apiError(w, http.StatusInternalServerError, "unavailable", "Night Desk could not save the sign-in.")
		return
	}
	apiJSON(w, r, http.StatusOK, tokenResponse{AccessToken: token, TokenType: "Bearer", ExpiresIn: int(s.lifetime().Seconds()), Client: g.client})
}
