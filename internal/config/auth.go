package config

import (
	"fmt"
	"net"
	"net/netip"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

// Reader sign-in modes. The mode decides who a session belongs to; papers'
// readers then decide what it reads, as before.
const (
	AuthOIDC  = "oidc"
	AuthProxy = "proxy"
	AuthLocal = "local"
	AuthNone  = "none"
)

// Issuers recorded on sessions, feedback and push subscriptions for the modes
// that have no OIDC issuer. They are not URLs, so they never collide with one.
const (
	ProxyIssuer = "proxy"
	LocalIssuer = "local"
	NoneIssuer  = "none"
	// NoneSubject is the single reader of a paper served without sign-in.
	NoneSubject = "reader"
)

// Auth selects how readers sign in. Without it, a configuration with
// server.oidc keeps signing in through OIDC, and one without signs in with
// local accounts.
type Auth struct {
	Mode  string    `json:"mode,omitempty"`
	Proxy ProxyAuth `json:"proxy,omitzero"`
	Local LocalAuth `json:"local,omitzero"`
	None  NoneAuth  `json:"none,omitzero"`
}

// ProxyAuth trusts a reverse proxy's identity header, only from its addresses.
type ProxyAuth struct {
	// UserHeader names the header carrying the signed-in user, such as
	// Remote-User. Required.
	UserHeader string `json:"user_header"`
	// GroupsHeader optionally names a comma-separated list of the user's
	// groups, such as Remote-Groups, for papers with group readers.
	GroupsHeader string `json:"groups_header,omitempty"`
	// TrustedProxies lists the addresses or CIDR ranges the proxy connects
	// from. A request from anywhere else is never signed in by header.
	TrustedProxies []string `json:"trusted_proxies"`
}

// LocalAuth configures password accounts kept in the database.
type LocalAuth struct {
	// Passkeys lets readers add WebAuthn passkeys and sign in with them.
	Passkeys bool `json:"passkeys,omitempty"`
	// InviteLifetime is how long an invite or password-reset link works.
	InviteLifetime string `json:"invite_lifetime,omitempty"`
}

// NoneAuth serves one reader with no sign-in at all.
type NoneAuth struct {
	// AcknowledgePublicAccess allows listening on an address that is not
	// loopback or private. Anyone who can reach the server reads, edits and
	// spends model budget for the paper.
	AcknowledgePublicAccess bool `json:"acknowledge_public_access,omitempty"`
}

// AuthMode is the effective sign-in mode.
func (c *Config) AuthMode() string {
	if c.Server.Auth.Mode != "" {
		return c.Server.Auth.Mode
	}
	if c.Server.OIDC.Issuer != "" {
		return AuthOIDC
	}
	return AuthLocal
}

// ReaderIssuer is the issuer this mode's sessions carry, or "" for OIDC,
// whose readers each name their provider.
func (c *Config) ReaderIssuer() string {
	switch c.AuthMode() {
	case AuthProxy:
		return ProxyIssuer
	case AuthLocal:
		return LocalIssuer
	case AuthNone:
		return NoneIssuer
	}
	return ""
}

// AcceptsIssuer reports whether a session with issuer belongs to this mode:
// one signed in under another mode must sign in again.
func (c *Config) AcceptsIssuer(issuer string) bool {
	if want := c.ReaderIssuer(); want != "" {
		return issuer == want
	}
	return issuer != "" && issuer != ProxyIssuer && issuer != LocalIssuer && issuer != NoneIssuer
}

// InviteLifetime is how long local invite and reset links work.
func (c *Config) InviteLifetime() time.Duration {
	d, err := time.ParseDuration(c.Server.Auth.Local.InviteLifetime)
	if err != nil || d <= 0 {
		return 72 * time.Hour
	}
	return d
}

// TrustedProxies parses proxy.trusted_proxies; Validate has accepted them.
func (c *Config) TrustedProxies() []netip.Prefix {
	var out []netip.Prefix
	for _, v := range c.Server.Auth.Proxy.TrustedProxies {
		if p, err := parsePrefix(v); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func parsePrefix(v string) (netip.Prefix, error) {
	if strings.Contains(v, "/") {
		p, err := netip.ParsePrefix(v)
		return p.Masked(), err
	}
	a, err := netip.ParseAddr(v)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()), nil
}

func (c *Config) checkAuth(add func(string, string)) {
	a := c.Server.Auth
	mode := c.AuthMode()
	switch mode {
	case AuthOIDC, AuthProxy, AuthLocal, AuthNone:
	default:
		add("server.auth.mode", "must be one of oidc, proxy, local, none")
		return
	}
	if mode != AuthProxy && !isZeroProxy(a.Proxy) {
		add("server.auth.proxy", "is only read when server.auth.mode is proxy")
	}
	if mode != AuthLocal && a.Local != (LocalAuth{}) {
		add("server.auth.local", "is only read when server.auth.mode is local")
	}
	if mode != AuthNone && a.None != (NoneAuth{}) {
		add("server.auth.none", "is only read when server.auth.mode is none")
	}
	if mode != AuthOIDC && (c.Server.OIDC.Issuer != "" || c.Server.OIDC.ClientID != "" || c.Server.OIDC.ClientSecretFile != "") {
		add("server.oidc", "is only read when server.auth.mode is oidc; remove it")
	}
	switch mode {
	case AuthProxy:
		p := a.Proxy
		if p.UserHeader == "" || !validHeader(p.UserHeader) {
			add("server.auth.proxy.user_header", "must be an HTTP header name")
		}
		if p.GroupsHeader != "" && (!validHeader(p.GroupsHeader) || strings.EqualFold(p.GroupsHeader, p.UserHeader)) {
			add("server.auth.proxy.groups_header", "must be an HTTP header name other than user_header")
		}
		if len(p.TrustedProxies) == 0 {
			add("server.auth.proxy.trusted_proxies", "must list the proxy's addresses or CIDR ranges")
		}
		for i, v := range p.TrustedProxies {
			pfx, err := parsePrefix(v)
			if err != nil {
				add(fmt.Sprintf("server.auth.proxy.trusted_proxies[%d]", i), "must be an IP address or CIDR range")
				continue
			}
			if pfx.Bits() == 0 {
				add(fmt.Sprintf("server.auth.proxy.trusted_proxies[%d]", i), "must not trust every address")
			}
		}
	case AuthLocal:
		if v := a.Local.InviteLifetime; v != "" {
			if d, err := time.ParseDuration(v); err != nil || d <= 0 {
				add("server.auth.local.invite_lifetime", "must be a positive duration")
			}
		}
	case AuthNone:
		if !a.None.AcknowledgePublicAccess {
			if problem := publicListen(c.Server.Listen); problem != "" {
				add("server.listen", problem+"; with server.auth.mode none, listen on a loopback or private address, or set server.auth.none.acknowledge_public_access")
			}
			// A private listener behind a public reverse proxy is public too.
			if u, err := url.Parse(c.Server.PublicBaseURL); err == nil && !privateHost(u.Hostname()) {
				add("server.public_base_url", u.Hostname()+" may be a public address; with server.auth.mode none, use a loopback or private address or a local name (.local, .lan, .home.arpa, .internal), or set server.auth.none.acknowledge_public_access")
			}
		}
	}
}

func isZeroProxy(p ProxyAuth) bool {
	return p.UserHeader == "" && p.GroupsHeader == "" && len(p.TrustedProxies) == 0
}

func validHeader(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, r := range name {
		if !(r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return textproto.CanonicalMIMEHeaderKey(name) != ""
}

// PrivateAddr reports whether a is loopback, link-local, private or CGNAT.
func PrivateAddr(a netip.Addr) bool { return privateAddr(a) }

// privateAddr reports whether a is loopback, link-local or private.
func privateAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() ||
		// CGNAT, which Tailscale and carriers use.
		netip.MustParsePrefix("100.64.0.0/10").Contains(a)
}

// privateHost reports whether a public_base_url host names this machine or a
// private network: localhost, a loopback or private address, or a name under a
// suffix reserved for local networks. Any other name may resolve publicly.
func privateHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if a, err := netip.ParseAddr(host); err == nil {
		return privateAddr(a)
	}
	if host == "localhost" {
		return true
	}
	for _, suffix := range []string{".localhost", ".local", ".lan", ".home.arpa", ".internal", ".localdomain"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// interfaceAddrs lists this machine's addresses; tests replace it.
var interfaceAddrs = func() ([]netip.Addr, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, a := range addrs {
		var raw net.IP
		switch v := a.(type) {
		case *net.IPNet:
			raw = v.IP
		case *net.IPAddr:
			raw = v.IP
		default:
			return nil, fmt.Errorf("unknown address type %T", a)
		}
		if ip, ok := netip.AddrFromSlice(raw); ok {
			out = append(out, ip.Unmap())
		}
	}
	return out, nil
}

// publicListen explains why listen may be reachable from a public address,
// or returns "". An unspecified address (0.0.0.0 or ::) is public when any of
// this machine's interfaces has a public address; inside a container it
// usually has only private ones.
func publicListen(listen string) string {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return ""
	}
	if host == "localhost" {
		return ""
	}
	if host == "" {
		host = "0.0.0.0"
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return "must be an IP address, not a host name"
	}
	if !a.IsUnspecified() {
		if privateAddr(a) {
			return ""
		}
		return host + " is a public address"
	}
	addrs, err := interfaceAddrs()
	if err != nil {
		return "cannot list this machine's addresses to check that " + host + " is private"
	}
	// Go listens on both families for 0.0.0.0 too, so every address counts.
	for _, ip := range addrs {
		if !privateAddr(ip) && !ip.IsUnspecified() && !ip.IsMulticast() {
			return host + " includes the public address " + ip.String()
		}
	}
	return ""
}
