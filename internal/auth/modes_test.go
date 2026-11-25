package auth

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/config"
)

func modePapers(readers ...[]config.Reader) []config.Paper {
	var out []config.Paper
	for i, r := range readers {
		out = append(out, config.Paper{ID: string(rune('a' + i)), Readers: r})
	}
	return out
}

func TestReadableLocal(t *testing.T) {
	cfg := &config.Config{Server: config.Server{Auth: config.Auth{Mode: config.AuthLocal}}, Papers: modePapers(
		[]config.Reader{{Issuer: config.LocalIssuer, Subject: "bob"}},
		nil,
		nil,
	)}
	// An open paper is ada's own; the second open paper she reads too.
	if got := Readable(cfg, config.LocalIssuer, "ada", nil); !reflect.DeepEqual(got, []Access{{"b", true}, {"c", false}}) {
		t.Fatal(got)
	}
	// bob's named paper comes first; open papers follow, not his own.
	if got := Readable(cfg, config.LocalIssuer, "bob", nil); !reflect.DeepEqual(got, []Access{{"a", true}, {"b", false}, {"c", false}}) {
		t.Fatal(got)
	}
	// Other issuers read nothing in local mode.
	for _, issuer := range []string{"https://id.example", config.NoneIssuer, config.ProxyIssuer, ""} {
		if got := Readable(cfg, issuer, "ada", nil); got != nil {
			t.Fatal(issuer, got)
		}
	}
}

func TestReadableNone(t *testing.T) {
	cfg := &config.Config{Server: config.Server{Auth: config.Auth{Mode: config.AuthNone}}, Papers: modePapers(nil, nil)}
	if got := Readable(cfg, config.NoneIssuer, config.NoneSubject, nil); !reflect.DeepEqual(got, []Access{{"a", true}, {"b", false}}) {
		t.Fatal(got)
	}
	if got := Readable(cfg, config.LocalIssuer, config.NoneSubject, nil); got != nil {
		t.Fatal(got)
	}
}

func TestReadableOIDCRefusesPseudoIssuers(t *testing.T) {
	// A misconfigured OIDC reader naming a pseudo-issuer still never matches.
	cfg := &config.Config{Server: config.Server{Auth: config.Auth{Mode: config.AuthOIDC}}, Papers: modePapers([]config.Reader{{Issuer: config.LocalIssuer, Subject: "ada"}}, nil)}
	if got := Readable(cfg, config.LocalIssuer, "ada", nil); got != nil {
		t.Fatal(got)
	}
	// And open papers mean nothing outside local mode.
	if got := Readable(cfg, "https://id.example", "ada", nil); got != nil {
		t.Fatal(got)
	}
}

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil || !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatal(h, err)
	}
	if !VerifyPassword(h, "correct horse battery") || VerifyPassword(h, "correct horse batterY") || VerifyPassword(h, "") {
		t.Fatal("verification wrong")
	}
	if h2, _ := HashPassword("correct horse battery"); h2 == h {
		t.Fatal("hashes are not salted")
	}
	// No account, malformed and exhausting hashes all refuse.
	for _, bad := range []string{"", "$argon2i$v=19$m=19456,t=2,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA", "$argon2id$v=19$m=4194304,t=2,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA", "plain"} {
		if VerifyPassword(bad, "night-desk-no-such-account") {
			t.Fatal(bad)
		}
	}
	if CheckNewPassword("123456789") == nil || CheckNewPassword("ünïcödé-ok") != nil || CheckNewPassword(strings.Repeat("x", MaxPasswordLength+1)) == nil {
		t.Fatal("password rules")
	}
}

func TestLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter()
	l.SetClock(func() time.Time { return now })
	for i := 0; i < freeFailures; i++ {
		if l.Wait("u") != 0 {
			t.Fatal("throttled within the free failures")
		}
		l.Fail("u", "addr")
	}
	if l.Wait("u") != 0 {
		t.Fatal("throttled at the free limit")
	}
	l.Fail("u")
	if w := l.Wait("u"); w != time.Second {
		t.Fatal(w)
	}
	for i := 0; i < 30; i++ {
		l.Fail("u")
	}
	if w := l.Wait("u"); w != maxDelay {
		t.Fatal(w)
	}
	// Another key is unaffected; success clears.
	if l.Wait("v") != 0 {
		t.Fatal("other key throttled")
	}
	l.Succeed("u")
	if l.Wait("u") != 0 {
		t.Fatal("success did not clear")
	}
	// A key quiet for long starts over.
	for i := 0; i < 8; i++ {
		l.Fail("w")
	}
	now = now.Add(time.Hour)
	l.Fail("w")
	if l.Wait("w") != 0 {
		t.Fatal("old failures still count")
	}
}
