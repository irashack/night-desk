package reader

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/store"
)

// Local accounts (server.auth.mode local): password sign-in, first-run setup
// of the administrator, invite and reset links, the account page, the readers
// page and optional passkeys. Forms before sign-in carry a double-submit token
// (the nd_form cookie) and must come from this origin.

type authView struct {
	Page, Title, Error, Notice string
	Form, CSRF, Next, Token    string
	Username                   string
	Admin, Reset, Passkeys     bool
	Logout                     bool
	PasskeyRows                []passkeyRow
	Accounts                   []accountRow
	Link, LinkExpires, Action  string
	Pending                    int
}

type passkeyRow struct{ ID, Name, Created, LastUsed string }
type accountRow struct {
	Username, Created string
	Admin             bool
	Passkeys          int
}

// maxPasskeys bounds an account's passkeys.
const maxPasskeys = 20

func (s *Server) renderAuth(w http.ResponseWriter, status int, v authView) {
	if v.Title == "" {
		v.Title = "Sign in"
	}
	v.Passkeys = v.Passkeys && s.passkeys != nil
	var b bytes.Buffer
	if err := s.templates.ExecuteTemplate(&b, "auth", v); err != nil {
		http.Error(w, "Reader unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b.Bytes())
}

// formToken returns the nd_form double-submit token, setting a new one when
// the request has none.
func (s *Server) formToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie("nd_form"); err == nil && len(c.Value) == 43 {
		return c.Value
	}
	v := random()
	s.cookie(w, "nd_form", v, 3600)
	return v
}

// sameOrigin rejects a cross-site form or fetch. Sec-Fetch-Site, which
// current browsers send, must say same-origin; a real Origin header must be
// this server's public origin. Forms are sent with Origin "null" because the
// reader's Referrer-Policy is no-referrer, so a missing or null Origin leaves
// the decision to the CSRF or double-submit token.
func (s *Server) sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" || origin == "null" {
		return true
	}
	want, err := url.Parse(s.Config().Server.PublicBaseURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(origin, want.Scheme+"://"+want.Host)
}

// validForm checks a pre-sign-in form: same origin and the double-submit token.
func (s *Server) validForm(r *http.Request, token string) bool {
	c, err := r.Cookie("nd_form")
	return err == nil && s.sameOrigin(r) && token != "" && equal(c.Value, token)
}

// clientKey identifies the connecting address for the sign-in limiter. Behind
// a reverse proxy it is the proxy's, so accounts are throttled on their own.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return "addr:" + host
}

func waitMessage(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Second {
		d = time.Second
	}
	return "Too many attempts. Try again in " + d.String() + "."
}

// startSession signs a reader in: a new session (never a reused one) and its
// cookie, replacing the request's previous session.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, issuer, subject string, groups []string, checked bool) (string, error) {
	key := random()
	now := s.now()
	session := auth.Session{Hash: sha256.Sum256([]byte(key)), Issuer: issuer, Subject: subject, CSRF: random(), Created: now, LastSeen: now, Expires: now.Add(s.lifetime()), Groups: groups, GroupsChecked: checked}
	if err := s.Store.InsertSession(r.Context(), session); err != nil {
		return "", err
	}
	if old, e := r.Cookie("nd_session"); e == nil {
		_ = s.Store.DeleteSession(r.Context(), sha256.Sum256([]byte(old.Value)))
	}
	s.cookie(w, "nd_session", key, int(s.lifetime().Seconds()))
	return key, nil
}

// loginPage is the local sign-in form, or first-run setup while there is no
// account yet.
func (s *Server) loginPage(w http.ResponseWriter, r *http.Request, status int, v authView) {
	if n, err := s.Store.CountAccounts(r.Context()); err == nil && n == 0 {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	v.Page, v.Form, v.Passkeys = "login", s.formToken(w, r), true
	if v.Next == "" {
		v.Next = returnPath(r.URL.Query().Get("next"))
	}
	s.renderAuth(w, status, v)
}

func (s *Server) localLogin(w http.ResponseWriter, r *http.Request) {
	s.loginPage(w, r, http.StatusOK, authView{})
}

func (s *Server) localLoginSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if r.ParseForm() != nil || !s.validForm(r, r.PostForm.Get("form")) {
		s.loginPage(w, r, http.StatusForbidden, authView{Error: "The form expired. Please try again."})
		return
	}
	username, _ := store.NormalUsername(r.PostForm.Get("username"))
	next := returnPath(r.PostForm.Get("next"))
	v := authView{Username: username, Next: next}
	keys := []string{"user:" + username, clientKey(r)}
	if wait := s.limiter.Wait(keys...); wait > 0 {
		v.Error = waitMessage(wait)
		s.loginPage(w, r, http.StatusTooManyRequests, v)
		return
	}
	account, err := s.Store.Account(r.Context(), username)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Sign-in unavailable", 500)
		return
	}
	// A missing account costs the same derivation as a wrong password.
	if !auth.VerifyPassword(account.PasswordHash, r.PostForm.Get("password")) {
		s.limiter.Fail(keys...)
		v.Error = "That username and password do not match."
		s.loginPage(w, r, http.StatusUnauthorized, v)
		return
	}
	s.limiter.Succeed("user:" + username)
	if _, err = s.startSession(w, r, config.LocalIssuer, account.Username, nil, false); err != nil {
		http.Error(w, "Sign-in unavailable", 500)
		return
	}
	if next == "" {
		next = "/"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// checkNew validates a username (when asked for) and a new password pair.
func checkNew(username string, askUsername bool, password, confirm string) (string, string) {
	if askUsername {
		name, ok := store.NormalUsername(username)
		if !ok {
			return name, "A username is 1 to 64 letters, digits, dots, dashes or underscores, starting with a letter or digit."
		}
		username = name
	}
	if err := auth.CheckNewPassword(password); err != nil {
		return username, strings.ToUpper(err.Error()[:1]) + err.Error()[1:] + "."
	}
	if password != confirm {
		return username, "The two passwords do not match."
	}
	return username, ""
}

func newWebAuthnID() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("system randomness unavailable")
	}
	return b
}

func (s *Server) setupPage(w http.ResponseWriter, r *http.Request) {
	if n, err := s.Store.CountAccounts(r.Context()); err != nil || n > 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.renderAuth(w, http.StatusOK, authView{Page: "setup", Title: "Set up", Form: s.formToken(w, r)})
}

func (s *Server) setupSubmit(w http.ResponseWriter, r *http.Request) {
	if n, err := s.Store.CountAccounts(r.Context()); err != nil || n > 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	v := authView{Page: "setup", Title: "Set up"}
	if r.ParseForm() != nil || !s.validForm(r, r.PostForm.Get("form")) {
		v.Form, v.Error = s.formToken(w, r), "The form expired. Please try again."
		s.renderAuth(w, http.StatusForbidden, v)
		return
	}
	v.Form = r.PostForm.Get("form")
	v.Username = r.PostForm.Get("username")
	keys := []string{"setup", clientKey(r)}
	if wait := s.limiter.Wait(keys...); wait > 0 {
		v.Error = waitMessage(wait)
		s.renderAuth(w, http.StatusTooManyRequests, v)
		return
	}
	s.mu.Lock()
	code := s.setupCode
	s.mu.Unlock()
	if code == "" || !equal(code, strings.TrimSpace(r.PostForm.Get("code"))) {
		s.limiter.Fail(keys...)
		v.Error = "That setup code is not the one in the server's log."
		s.renderAuth(w, http.StatusUnauthorized, v)
		return
	}
	username, problem := checkNew(r.PostForm.Get("username"), true, r.PostForm.Get("password"), r.PostForm.Get("confirm"))
	if problem != "" {
		v.Username, v.Error = username, problem
		s.renderAuth(w, http.StatusBadRequest, v)
		return
	}
	hash, err := auth.HashPassword(r.PostForm.Get("password"))
	if err != nil {
		http.Error(w, "Setup unavailable", 500)
		return
	}
	if err = s.Store.CreateFirstAdmin(r.Context(), username, hash, newWebAuthnID(), s.now()); errors.Is(err, store.ErrAccountsExist) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	} else if err != nil {
		http.Error(w, "Setup unavailable", 500)
		return
	}
	s.mu.Lock()
	s.setupCode = ""
	s.mu.Unlock()
	s.limiter.Succeed("setup")
	if s.Log != nil {
		s.Log.Info("first account created", "username", username)
	}
	if _, err = s.startSession(w, r, config.LocalIssuer, username, nil, false); err != nil {
		http.Error(w, "Sign-in unavailable", 500)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func inviteHash(token string) ([32]byte, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != token {
		return [32]byte{}, false
	}
	return sha256.Sum256([]byte(token)), true
}

func (s *Server) invalidInvite(w http.ResponseWriter) {
	s.renderAuth(w, http.StatusNotFound, authView{Page: "message", Title: "This link has expired or was already used", Error: "Ask whoever sent it for a new one."})
}

func (s *Server) invitePage(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	hash, ok := inviteHash(token)
	if !ok {
		s.invalidInvite(w)
		return
	}
	invite, err := s.Store.LookupInvite(r.Context(), hash, s.now())
	if err != nil {
		s.invalidInvite(w)
		return
	}
	s.renderAuth(w, http.StatusOK, authView{Page: "invite", Title: "Invitation", Form: s.formToken(w, r), Token: token, Reset: invite.Username != "", Username: invite.Username})
}

func (s *Server) inviteSubmit(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	hash, ok := inviteHash(token)
	if !ok {
		s.invalidInvite(w)
		return
	}
	invite, err := s.Store.LookupInvite(r.Context(), hash, s.now())
	if err != nil {
		s.invalidInvite(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	v := authView{Page: "invite", Title: "Invitation", Token: token, Reset: invite.Username != "", Username: invite.Username}
	if r.ParseForm() != nil || !s.validForm(r, r.PostForm.Get("form")) {
		v.Form, v.Error = s.formToken(w, r), "The form expired. Please try again."
		s.renderAuth(w, http.StatusForbidden, v)
		return
	}
	v.Form = r.PostForm.Get("form")
	username, problem := checkNew(r.PostForm.Get("username"), !v.Reset, r.PostForm.Get("password"), r.PostForm.Get("confirm"))
	if !v.Reset {
		v.Username = username
	}
	if problem != "" {
		v.Error = problem
		s.renderAuth(w, http.StatusBadRequest, v)
		return
	}
	passwordHash, err := auth.HashPassword(r.PostForm.Get("password"))
	if err != nil {
		http.Error(w, "Sign-in unavailable", 500)
		return
	}
	username, err = s.Store.RedeemInvite(r.Context(), hash, username, passwordHash, newWebAuthnID(), s.now())
	switch {
	case errors.Is(err, store.ErrUsernameTaken):
		v.Error = "That username is taken. Choose another."
		s.renderAuth(w, http.StatusConflict, v)
		return
	case errors.Is(err, store.ErrInviteInvalid):
		s.invalidInvite(w)
		return
	case err != nil:
		http.Error(w, "Sign-in unavailable", 500)
		return
	}
	if _, err = s.startSession(w, r, config.LocalIssuer, username, nil, false); err != nil {
		http.Error(w, "Sign-in unavailable", 500)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// account loads the signed-in local account.
func (s *Server) account(w http.ResponseWriter, r *http.Request) (store.Account, bool) {
	a, err := s.Store.Account(r.Context(), current(r).scope.Session().Subject)
	if err != nil {
		http.Error(w, "Account unavailable", 500)
		return a, false
	}
	return a, true
}

func (s *Server) accountView(r *http.Request, a store.Account) authView {
	v := authView{Page: "account", Title: "Account", CSRF: current(r).scope.Session().CSRF, Username: a.Username, Admin: a.Admin, Passkeys: true}
	if s.passkeys != nil {
		list, err := s.Store.Passkeys(r.Context(), a.Username)
		if err == nil {
			for _, p := range list {
				row := passkeyRow{ID: base64.RawURLEncoding.EncodeToString(p.ID), Name: p.Name, Created: p.Created.In(time.Local).Format("2 Jan 2006"), LastUsed: "Never"}
				if !p.LastUsed.IsZero() {
					row.LastUsed = p.LastUsed.In(time.Local).Format("2 Jan 2006")
				}
				v.PasskeyRows = append(v.PasskeyRows, row)
			}
		}
	}
	return v
}

func (s *Server) accountPage(w http.ResponseWriter, r *http.Request) {
	a, ok := s.account(w, r)
	if !ok {
		return
	}
	s.renderAuth(w, http.StatusOK, s.accountView(r, a))
}

// postedCSRF parses a signed-in form and checks its session token and origin.
func (s *Server) postedCSRF(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if r.ParseForm() != nil || !validCSRF(r) || !s.sameOrigin(r) {
		http.Error(w, "Invalid CSRF token", 403)
		return false
	}
	return true
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	if !s.postedCSRF(w, r) {
		return
	}
	a, ok := s.account(w, r)
	if !ok {
		return
	}
	v := s.accountView(r, a)
	keys := []string{"user:" + a.Username, clientKey(r)}
	if wait := s.limiter.Wait(keys...); wait > 0 {
		v.Error = waitMessage(wait)
		s.renderAuth(w, http.StatusTooManyRequests, v)
		return
	}
	if !auth.VerifyPassword(a.PasswordHash, r.PostForm.Get("current")) {
		s.limiter.Fail(keys...)
		v.Error = "Your current password was not right."
		s.renderAuth(w, http.StatusUnauthorized, v)
		return
	}
	if _, problem := checkNew("", false, r.PostForm.Get("password"), r.PostForm.Get("confirm")); problem != "" {
		v.Error = problem
		s.renderAuth(w, http.StatusBadRequest, v)
		return
	}
	hash, err := auth.HashPassword(r.PostForm.Get("password"))
	if err != nil {
		http.Error(w, "Account unavailable", 500)
		return
	}
	keep := current(r).scope.Session().Hash
	if err = s.Store.SetPassword(r.Context(), a.Username, hash, s.now(), &keep); err != nil {
		http.Error(w, "Account unavailable", 500)
		return
	}
	s.limiter.Succeed("user:" + a.Username)
	v.Notice = "Your password is changed. Your other devices are signed out."
	s.renderAuth(w, http.StatusOK, v)
}

// admin loads the signed-in account and requires it to administer.
func (s *Server) admin(w http.ResponseWriter, r *http.Request) (store.Account, bool) {
	a, ok := s.account(w, r)
	if ok && !a.Admin {
		http.NotFound(w, r)
		return a, false
	}
	return a, ok
}

func (s *Server) adminView(r *http.Request, a store.Account) (authView, error) {
	v := authView{Page: "admin", Title: "Readers", CSRF: current(r).scope.Session().CSRF, Username: a.Username}
	list, err := s.Store.Accounts(r.Context())
	if err != nil {
		return v, err
	}
	for _, x := range list {
		v.Accounts = append(v.Accounts, accountRow{Username: x.Username, Admin: x.Admin, Passkeys: x.Passkeys, Created: x.Created.In(time.Local).Format("2 Jan 2006")})
	}
	v.Pending, err = s.Store.PendingInvites(r.Context(), s.now())
	return v, err
}

func (s *Server) renderAdmin(w http.ResponseWriter, r *http.Request, a store.Account, edit func(*authView)) {
	v, err := s.adminView(r, a)
	if err != nil {
		http.Error(w, "Readers unavailable", 500)
		return
	}
	if edit != nil {
		edit(&v)
	}
	s.renderAuth(w, http.StatusOK, v)
}

func (s *Server) adminPage(w http.ResponseWriter, r *http.Request) {
	if a, ok := s.admin(w, r); ok {
		s.renderAdmin(w, r, a, nil)
	}
}

// issueLink creates an invite (username "") or a reset link for username.
func (s *Server) issueLink(r *http.Request, by, username string) (string, time.Time, error) {
	token := random()
	now := s.now()
	expires := now.Add(s.Config().InviteLifetime())
	err := s.Store.CreateInvite(r.Context(), store.Invite{TokenHash: sha256.Sum256([]byte(token)), Username: username, CreatedBy: by, Created: now, Expires: expires})
	return strings.TrimRight(s.Config().Server.PublicBaseURL, "/") + "/invite/" + token, expires, err
}

func (s *Server) adminInvite(w http.ResponseWriter, r *http.Request) {
	if !s.postedCSRF(w, r) {
		return
	}
	a, ok := s.admin(w, r)
	if !ok {
		return
	}
	link, expires, err := s.issueLink(r, a.Username, "")
	if err != nil {
		http.Error(w, "Invite unavailable", 500)
		return
	}
	s.renderAdmin(w, r, a, func(v *authView) {
		v.Link, v.LinkExpires = link, expires.In(time.Local).Format("2 Jan 15:04 MST")
		v.Notice = "Invite created."
	})
}

func (s *Server) adminReset(w http.ResponseWriter, r *http.Request) {
	if !s.postedCSRF(w, r) {
		return
	}
	a, ok := s.admin(w, r)
	if !ok {
		return
	}
	target := r.PostForm.Get("username")
	if _, err := s.Store.Account(r.Context(), target); err != nil || target == a.Username {
		http.NotFound(w, r)
		return
	}
	link, expires, err := s.issueLink(r, a.Username, target)
	if err != nil {
		http.Error(w, "Reset unavailable", 500)
		return
	}
	s.renderAdmin(w, r, a, func(v *authView) {
		v.Link, v.LinkExpires = link, expires.In(time.Local).Format("2 Jan 15:04 MST")
		v.Notice = "Password reset link for " + target + ". Their password works until they use it."
	})
}

func (s *Server) adminRemove(w http.ResponseWriter, r *http.Request) {
	if !s.postedCSRF(w, r) {
		return
	}
	a, ok := s.admin(w, r)
	if !ok {
		return
	}
	target := r.PostForm.Get("username")
	if _, err := s.Store.Account(r.Context(), target); err != nil || target == a.Username {
		http.NotFound(w, r)
		return
	}
	if r.PostForm.Get("confirm") != "1" {
		s.renderAuth(w, http.StatusOK, authView{Page: "confirm", Title: "Remove " + target, Notice: "Their sessions, passkeys and notifications end. Their feedback stays with the paper.", CSRF: current(r).scope.Session().CSRF, Username: target, Action: "/admin/remove"})
		return
	}
	if err := s.Store.RemoveAccount(r.Context(), target); err != nil {
		s.renderAdmin(w, r, a, func(v *authView) { v.Error = "Could not remove " + target + "." })
		return
	}
	s.renderAdmin(w, r, a, func(v *authView) { v.Notice = target + " is removed." })
}

func (s *Server) adminRole(w http.ResponseWriter, r *http.Request) {
	if !s.postedCSRF(w, r) {
		return
	}
	a, ok := s.admin(w, r)
	if !ok {
		return
	}
	target := r.PostForm.Get("username")
	if target == a.Username {
		http.NotFound(w, r)
		return
	}
	makeAdmin := r.PostForm.Get("admin") == "1"
	if err := s.Store.SetAdmin(r.Context(), target, makeAdmin); err != nil {
		s.renderAdmin(w, r, a, func(v *authView) { v.Error = "Could not change " + target + "." })
		return
	}
	s.renderAdmin(w, r, a, func(v *authView) {
		if makeAdmin {
			v.Notice = target + " is now an administrator."
		} else {
			v.Notice = target + " is now a reader."
		}
	})
}

func (s *Server) adminRevoke(w http.ResponseWriter, r *http.Request) {
	if !s.postedCSRF(w, r) {
		return
	}
	a, ok := s.admin(w, r)
	if !ok {
		return
	}
	if err := s.Store.RevokeInvites(r.Context()); err != nil {
		http.Error(w, "Readers unavailable", 500)
		return
	}
	s.renderAdmin(w, r, a, func(v *authView) { v.Notice = "Unused links are revoked." })
}

// Passkeys.

type passkeyUser struct {
	account store.Account
	creds   []webauthn.Credential
}

func (u passkeyUser) WebAuthnID() []byte                         { return u.account.WebAuthnID }
func (u passkeyUser) WebAuthnName() string                       { return u.account.Username }
func (u passkeyUser) WebAuthnDisplayName() string                { return u.account.Username }
func (u passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func (s *Server) passkeyUser(r *http.Request, a store.Account) (passkeyUser, error) {
	u := passkeyUser{account: a}
	list, err := s.Store.Passkeys(r.Context(), a.Username)
	if err != nil {
		return u, err
	}
	for _, p := range list {
		var c webauthn.Credential
		if json.Unmarshal(p.Credential, &c) == nil {
			u.creds = append(u.creds, c)
		}
	}
	return u, nil
}

// newPasskeys configures WebAuthn for the public origin. It returns nil when
// passkeys are off, and nil with a reason when the origin cannot have them:
// browsers allow passkeys only for a domain name, not an IP address.
func newPasskeys(cfg *config.Config) (*webauthn.WebAuthn, string, error) {
	if cfg.AuthMode() != config.AuthLocal || !cfg.Server.Auth.Local.Passkeys {
		return nil, "", nil
	}
	u, err := url.Parse(cfg.Server.PublicBaseURL)
	if err != nil {
		return nil, "", err
	}
	if net.ParseIP(u.Hostname()) != nil {
		return nil, "passkeys need a domain name in server.public_base_url, not an IP address; they are off", nil
	}
	w, err := webauthn.New(&webauthn.Config{RPID: u.Hostname(), RPDisplayName: "Night Desk", RPOrigins: []string{u.Scheme + "://" + u.Host}})
	return w, "", err
}

type ceremony struct {
	data    webauthn.SessionData
	expires time.Time
}

const maxCeremonies = 1024

// holdCeremony keeps a WebAuthn challenge for five minutes under key.
func (s *Server) holdCeremony(key [32]byte, data webauthn.SessionData) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.ceremonies {
		if !v.expires.After(now) {
			delete(s.ceremonies, k)
		}
	}
	for len(s.ceremonies) >= maxCeremonies {
		var oldest [32]byte
		first := true
		for k, v := range s.ceremonies {
			if first || v.expires.Before(s.ceremonies[oldest].expires) {
				oldest, first = k, false
			}
		}
		delete(s.ceremonies, oldest)
	}
	s.ceremonies[key] = ceremony{data, now.Add(5 * time.Minute)}
}

// takeCeremony removes and returns key's unexpired challenge.
func (s *Server) takeCeremony(key [32]byte) (webauthn.SessionData, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.ceremonies[key]
	delete(s.ceremonies, key)
	return c.data, ok && c.expires.After(s.now())
}

// sessionKey names the request's session for its registration challenge.
func sessionKey(r *http.Request) string {
	h := current(r).scope.Session().Hash
	return string(h[:])
}

func ceremonyKey(kind, value string) [32]byte {
	return sha256.Sum256([]byte(kind + "\x00" + value))
}

func jsonError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

type passkeyRequest struct {
	Form       string          `json:"form"`
	CSRF       string          `json:"csrf"`
	Name       string          `json:"name"`
	Next       string          `json:"next"`
	Credential json.RawMessage `json:"credential"`
}

func readPasskeyRequest(w http.ResponseWriter, r *http.Request) (passkeyRequest, bool) {
	var v passkeyRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil || json.Unmarshal(body, &v) != nil {
		jsonError(w, 400, "That request was not understood.")
		return v, false
	}
	return v, true
}

func (s *Server) passkeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	v, ok := readPasskeyRequest(w, r)
	if !ok {
		return
	}
	if !s.validForm(r, v.Form) {
		jsonError(w, 403, "The page expired. Reload it and try again.")
		return
	}
	options, data, err := s.passkeys.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		jsonError(w, 500, "Passkeys are unavailable.")
		return
	}
	s.holdCeremony(ceremonyKey("login", v.Form), *data)
	writeJSON(w, options)
}

func (s *Server) passkeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	v, ok := readPasskeyRequest(w, r)
	if !ok {
		return
	}
	if !s.validForm(r, v.Form) {
		jsonError(w, 403, "The page expired. Reload it and try again.")
		return
	}
	data, ok := s.takeCeremony(ceremonyKey("login", v.Form))
	if !ok {
		jsonError(w, 400, "The passkey request expired. Try again.")
		return
	}
	keys := []string{"passkey", clientKey(r)}
	if wait := s.limiter.Wait(clientKey(r)); wait > 0 {
		jsonError(w, 429, waitMessage(wait))
		return
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(v.Credential)
	if err != nil {
		s.limiter.Fail(keys...)
		jsonError(w, 400, "The passkey did not work.")
		return
	}
	var found store.Account
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		a, err := s.Store.AccountByWebAuthnID(r.Context(), userHandle)
		if err != nil {
			return nil, err
		}
		found = a
		return s.passkeyUser(r, a)
	}
	_, cred, err := s.passkeys.ValidatePasskeyLogin(handler, data, parsed)
	if err != nil || found.Username == "" {
		s.limiter.Fail(keys...)
		jsonError(w, 401, "That passkey is not registered here.")
		return
	}
	if cred.Authenticator.CloneWarning {
		if s.Log != nil {
			s.Log.Warn("passkey signature counter went backwards; refusing it", "username", found.Username)
		}
		jsonError(w, 401, "That passkey may have been copied. Remove it from your account and add it again.")
		return
	}
	stored, _ := json.Marshal(cred)
	if err = s.Store.UsePasskey(r.Context(), cred.ID, stored, s.now()); err != nil {
		jsonError(w, 500, "Sign-in unavailable.")
		return
	}
	if _, err = s.startSession(w, r, config.LocalIssuer, found.Username, nil, false); err != nil {
		jsonError(w, 500, "Sign-in unavailable.")
		return
	}
	next := returnPath(v.Next)
	if next == "" {
		next = "/"
	}
	writeJSON(w, map[string]string{"next": next})
}

// signedInJSON checks a signed-in JSON request's CSRF token and origin.
func (s *Server) signedInJSON(w http.ResponseWriter, r *http.Request) (passkeyRequest, bool) {
	v, ok := readPasskeyRequest(w, r)
	if !ok {
		return v, false
	}
	if !equal(current(r).scope.Session().CSRF, v.CSRF) || !s.sameOrigin(r) {
		jsonError(w, 403, "Invalid CSRF token.")
		return v, false
	}
	return v, true
}

func (s *Server) passkeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.signedInJSON(w, r); !ok {
		return
	}
	a, ok := s.account(w, r)
	if !ok {
		return
	}
	u, err := s.passkeyUser(r, a)
	if err != nil {
		jsonError(w, 500, "Passkeys are unavailable.")
		return
	}
	if len(u.creds) >= maxPasskeys {
		jsonError(w, 400, fmt.Sprintf("An account can have at most %d passkeys.", maxPasskeys))
		return
	}
	options, data, err := s.passkeys.BeginRegistration(u,
		webauthn.WithExclusions(webauthn.Credentials(u.creds).CredentialDescriptors()),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationRequired}))
	if err != nil {
		jsonError(w, 500, "Passkeys are unavailable.")
		return
	}
	s.holdCeremony(ceremonyKey("register", sessionKey(r)), *data)
	writeJSON(w, options)
}

func (s *Server) passkeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	v, ok := s.signedInJSON(w, r)
	if !ok {
		return
	}
	data, ok := s.takeCeremony(ceremonyKey("register", sessionKey(r)))
	if !ok {
		jsonError(w, 400, "The passkey request expired. Try again.")
		return
	}
	a, ok := s.account(w, r)
	if !ok {
		return
	}
	u, err := s.passkeyUser(r, a)
	if err != nil {
		jsonError(w, 500, "Passkeys are unavailable.")
		return
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(v.Credential)
	if err != nil {
		jsonError(w, 400, "The passkey was not accepted.")
		return
	}
	cred, err := s.passkeys.CreateCredential(u, data, parsed)
	if err != nil {
		jsonError(w, 400, "The passkey was not accepted.")
		return
	}
	name := strings.TrimSpace(v.Name)
	if name == "" || len(name) > 64 || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		name = "Passkey"
	}
	stored, _ := json.Marshal(cred)
	if err = s.Store.AddPasskey(r.Context(), store.Passkey{ID: cred.ID, Username: a.Username, Name: name, Credential: stored, Created: s.now()}); err != nil {
		jsonError(w, 409, "That passkey is already registered.")
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) passkeyRemove(w http.ResponseWriter, r *http.Request) {
	if !s.postedCSRF(w, r) {
		return
	}
	a, ok := s.account(w, r)
	if !ok {
		return
	}
	id, err := base64.RawURLEncoding.DecodeString(r.PostForm.Get("id"))
	v := s.accountView(r, a)
	if err != nil || s.Store.RemovePasskey(r.Context(), a.Username, id) != nil {
		v.Error = "That passkey was not found."
		s.renderAuth(w, http.StatusNotFound, v)
		return
	}
	v = s.accountView(r, a)
	v.Notice = "The passkey is removed."
	s.renderAuth(w, http.StatusOK, v)
}
