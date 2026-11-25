package config

import (
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
)

// authConfig is valid with its server.auth and readers replaced.
func authConfig(server, readers string) string {
	s := strings.Replace(valid, `  oidc:
    issuer: https://id.example
    client_id: nightdesk
    client_secret_file: secret-file
`, server, 1)
	return strings.Replace(s, `  readers:
  - issuer: https://id.example
    subject: reader-one
`, readers, 1)
}

func loadText(t *testing.T, text string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "paper.yaml")
	write(t, path, text)
	return Load(path)
}

func TestAuthModeDefaults(t *testing.T) {
	// An existing OIDC configuration, with no server.auth, keeps OIDC.
	c, err := loadText(t, valid)
	if err != nil || c.AuthMode() != AuthOIDC || c.ReaderIssuer() != "" {
		t.Fatal(c, err)
	}
	// Without server.oidc a new configuration signs in with local accounts.
	c, err = loadText(t, authConfig("", "  readers: []\n"))
	if err != nil || c.AuthMode() != AuthLocal || len(c.Server.OIDC.Scopes) != 0 {
		t.Fatal(c, err)
	}
	// Explicit oidc is the same as the default.
	c, err = loadText(t, strings.Replace(valid, "  oidc:\n", "  auth: {mode: oidc}\n  oidc:\n", 1))
	if err != nil || c.AuthMode() != AuthOIDC {
		t.Fatal(err)
	}
}

func TestAuthModeReaders(t *testing.T) {
	c, err := loadText(t, authConfig("  auth: {mode: local}\n", "  readers:\n  - subject: alice\n"))
	if err != nil {
		t.Fatal(err)
	}
	if r := c.Papers[0].Readers[0]; r.Issuer != LocalIssuer || r.ResolvedSubject() != "alice" {
		t.Fatal(r)
	}
	// Validate runs again on a loaded configuration (check does) and keeps it.
	if err = c.Validate(); err != nil {
		t.Fatal(err)
	}
	c, err = loadText(t, authConfig("  auth: {mode: proxy, proxy: {user_header: Remote-User, groups_header: Remote-Groups, trusted_proxies: [10.0.0.2, \"fd00::/8\"]}}\n", "  readers:\n  - group: family\n"))
	if err != nil {
		t.Fatal(err)
	}
	if r := c.Papers[0].Readers[0]; r.Issuer != ProxyIssuer || r.Group != "family" {
		t.Fatal(r)
	}
	if got := c.TrustedProxies(); len(got) != 2 || got[0] != netip.MustParsePrefix("10.0.0.2/32") || got[1] != netip.MustParsePrefix("fd00::/8") {
		t.Fatal(got)
	}
}

func TestAuthModeProblems(t *testing.T) {
	proxy := func(s string) string { return "  auth: {mode: proxy, proxy: {" + s + "}}\n" }
	for _, tt := range []struct{ name, server, readers, want string }{
		{"unknown mode", "  auth: {mode: magic}\n", "  readers: []\n", "server.auth.mode: must be one of oidc, proxy, local, none"},
		{"local reader with issuer", "  auth: {mode: local}\n", "  readers:\n  - {issuer: https://id.example, subject: a}\n", "papers[0].readers[0].issuer: must be omitted when server.auth.mode is local"},
		{"local group", "  auth: {mode: local}\n", "  readers:\n  - group: family\n", "local accounts have no groups"},
		{"oidc left in local", "  auth: {mode: local}\n  oidc: {issuer: https://id.example}\n", "  readers: []\n", "server.oidc: is only read when server.auth.mode is oidc"},
		{"proxy settings in local", "  auth: {mode: local, proxy: {user_header: X-User}}\n", "  readers: []\n", "server.auth.proxy: is only read when server.auth.mode is proxy"},
		{"proxy without header", proxy("trusted_proxies: [10.0.0.1]"), "  readers: []\n", "server.auth.proxy.user_header: must be an HTTP header name"},
		{"proxy bad header", proxy("user_header: \"Remote User\", trusted_proxies: [10.0.0.1]"), "  readers: []\n", "server.auth.proxy.user_header: must be an HTTP header name"},
		{"proxy without proxies", proxy("user_header: Remote-User"), "  readers: []\n", "server.auth.proxy.trusted_proxies: must list"},
		{"proxy trusts everyone", proxy("user_header: Remote-User, trusted_proxies: [0.0.0.0/0]"), "  readers: []\n", "trusted_proxies[0]: must not trust every address"},
		{"proxy bad address", proxy("user_header: Remote-User, trusted_proxies: [proxy.lan]"), "  readers: []\n", "trusted_proxies[0]: must be an IP address or CIDR range"},
		{"proxy group without header", proxy("user_header: Remote-User, trusted_proxies: [10.0.0.1]"), "  readers:\n  - group: family\n", "papers[0].readers[0].group: needs server.auth.proxy.groups_header"},
		{"none with readers", "  auth: {mode: none}\n", "  readers:\n  - subject: a\n", "server.auth.mode none has one reader"},
		{"none on a public address", "  auth: {mode: none}\n", "  readers: []\n", "server.listen: 203.0.113.9 is a public address"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			text := authConfig(tt.server, tt.readers)
			if tt.name == "none on a public address" {
				text = strings.Replace(text, `"127.0.0.1:8080"`, `"203.0.113.9:8080"`, 1)
			}
			_, err := loadText(t, text)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err=%v want %q", err, tt.want)
			}
		})
	}
}

func TestNoneListenAddress(t *testing.T) {
	saved := interfaceAddrs
	t.Cleanup(func() { interfaceAddrs = saved })
	addrs := func(v ...string) func() ([]netip.Addr, error) {
		return func() ([]netip.Addr, error) {
			var out []netip.Addr
			for _, s := range v {
				out = append(out, netip.MustParseAddr(s))
			}
			return out, nil
		}
	}
	for _, tt := range []struct {
		listen string
		ifaces []string
		public bool
	}{
		{"127.0.0.1:8080", nil, false},
		{"[::1]:8080", nil, false},
		{"localhost:8080", nil, false},
		{"192.168.1.5:8080", nil, false},
		{"100.101.102.103:8080", nil, false}, // Tailscale
		{"203.0.113.9:8080", nil, true},
		// A container: every interface private.
		{"0.0.0.0:8080", []string{"127.0.0.1", "172.17.0.2", "::1", "fe80::1"}, false},
		{":8080", []string{"127.0.0.1", "10.0.0.4"}, false},
		// A server with a public interface.
		{"0.0.0.0:8080", []string{"127.0.0.1", "172.17.0.1", "198.51.100.7"}, true},
		{"[::]:8080", []string{"::1", "2001:db8::5"}, true},
	} {
		interfaceAddrs = addrs(tt.ifaces...)
		if got := publicListen(tt.listen) != ""; got != tt.public {
			t.Errorf("%s with %v: public=%v", tt.listen, tt.ifaces, got)
		}
	}
	// The acknowledgement allows a public address.
	interfaceAddrs = addrs("198.51.100.7")
	text := strings.Replace(authConfig("  auth: {mode: none, none: {acknowledge_public_access: true}}\n", "  readers: []\n"), `"127.0.0.1:8080"`, `"0.0.0.0:8080"`, 1)
	if _, err := loadText(t, text); err != nil {
		t.Fatal(err)
	}
	if _, err := loadText(t, strings.Replace(text, "none: {acknowledge_public_access: true}", "none: {}", 1)); err == nil || !strings.Contains(err.Error(), "includes the public address 198.51.100.7") {
		t.Fatal(err)
	}
}

func TestNonePublicBaseURL(t *testing.T) {
	for host, private := range map[string]bool{"localhost": true, "paper.local": true, "nd.home.arpa": true, "desk.lan": true, "192.168.1.4": true, "paper.example.com": false, "203.0.113.5": false, "paper.ts.net": false} {
		if privateHost(host) != private {
			t.Errorf("%s: want private=%v", host, private)
		}
	}
	text := strings.Replace(authConfig("  auth: {mode: none}\n", "  readers: []\n"), "https://paper.example", "https://paper.example.com", 1)
	if _, err := loadText(t, text); err == nil || !strings.Contains(err.Error(), "server.public_base_url: paper.example.com may be a public address") {
		t.Fatal(err)
	}
	if _, err := loadText(t, strings.Replace(text, "https://paper.example.com", "https://paper.home.arpa", 1)); err != nil {
		t.Fatal(err)
	}
}

func TestUnitsAndLocale(t *testing.T) {
	text := strings.Replace(valid, "  look: broadsheet\n", "  look: broadsheet\n  units: metric\n  locale: en-GB\n", 1)
	c, err := loadText(t, text)
	if err != nil || !c.Papers[0].Metric() {
		t.Fatal(err)
	}
	if l, r := c.Papers[0].NewsLocale(); l != "en-GB" || r != "GB" {
		t.Fatal(l, r)
	}
	if l, r := (Paper{}).NewsLocale(); l != "en-US" || r != "US" || (Paper{}).Metric() {
		t.Fatal("defaults")
	}
	for _, bad := range []string{"units: kelvin", "locale: english", "locale: en_GB", "locale: EN-gb"} {
		if _, err := loadText(t, strings.Replace(valid, "  look: broadsheet\n", "  look: broadsheet\n  "+bad+"\n", 1)); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
