package reader

import (
	"crypto/sha256"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/irashack/night-desk/internal/config"
)

// Proxy and none modes sign a reader in on every request: the proxy's header,
// or no one at all, names who it is. The session they keep exists for its CSRF
// token and for what sessions carry (push subscriptions, sliding expiry).

// proxyIdentity reads the signed-in user from a trusted proxy's headers. A
// request from any other address is never signed in by header, whatever it
// carries.
func proxyIdentity(cfg *config.Config, r *http.Request) (user string, groups []string, ok bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "", nil, false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return "", nil, false
	}
	addr = addr.Unmap()
	if !slices.ContainsFunc(cfg.TrustedProxies(), func(p netip.Prefix) bool { return p.Contains(addr) }) {
		return "", nil, false
	}
	p := cfg.Server.Auth.Proxy
	values := r.Header.Values(p.UserHeader)
	if len(values) != 1 {
		// Missing, or repeated: a proxy that appends instead of replacing
		// could pass a client's own header through.
		return "", nil, false
	}
	user = strings.TrimSpace(values[0])
	if user == "" || len(user) > 256 || !utf8.ValidString(user) || strings.ContainsFunc(user, unicode.IsControl) {
		return "", nil, false
	}
	if p.GroupsHeader != "" {
		for _, v := range r.Header.Values(p.GroupsHeader) {
			for _, g := range strings.Split(v, ",") {
				g = strings.TrimSpace(g)
				if g == "" || len(g) > 256 || !utf8.ValidString(g) || strings.ContainsFunc(g, unicode.IsControl) {
					continue
				}
				if !slices.Contains(groups, g) {
					groups = append(groups, g)
				}
				if len(groups) >= 512 {
					break
				}
			}
		}
	}
	return user, groups, true
}

// ambientSession returns the session token to authenticate with in proxy and
// none modes, creating a session when the request has none for its reader.
// It answers the request itself (and returns false) when no one is signed in.
func (s *Server) ambientSession(w http.ResponseWriter, r *http.Request, cfg *config.Config, token string) (string, bool) {
	issuer, subject := config.NoneIssuer, config.NoneSubject
	var groups []string
	checked := false
	if cfg.AuthMode() == config.AuthProxy {
		user, g, ok := proxyIdentity(cfg, r)
		if !ok {
			s.unauthorized(w, r)
			return "", false
		}
		issuer, subject, groups, checked = config.ProxyIssuer, user, g, cfg.Server.Auth.Proxy.GroupsHeader != ""
	}
	if token != "" {
		session, err := s.Store.Session(r.Context(), sha256.Sum256([]byte(token)))
		fresh := err == nil && session.Expires.After(s.now()) &&
			// Group membership is re-read from the proxy at least once a
			// group lifetime, and at once when it changes.
			(!checked || session.Created.Add(cfg.GroupLifetime()).After(s.now()))
		if fresh && session.Issuer == issuer && session.Subject == subject && session.GroupsChecked == checked && slices.Equal(session.Groups, groups) {
			return token, true
		}
	}
	token, err := s.startSession(w, r, issuer, subject, groups, checked)
	if err != nil {
		http.Error(w, "Reader unavailable", 500)
		return "", false
	}
	return token, true
}

// banner warns every page when the server has no sign-in.
func banner(cfg *config.Config) string {
	if cfg.AuthMode() != config.AuthNone {
		return ""
	}
	if cfg.Server.Auth.None.AcknowledgePublicAccess {
		return "No sign-in, and this server may be reachable from the internet: anyone who reaches it reads this paper and can change its sections."
	}
	return "No sign-in: anyone who can reach this address reads this paper and can change its sections."
}
