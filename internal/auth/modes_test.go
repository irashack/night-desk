package auth

import (
	"context"
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
	// A reader reads the open papers; only an administrator's first open
	// paper is their own, with its sections to edit.
	if got := Readable(cfg, config.LocalIssuer, "ada", nil); !reflect.DeepEqual(got, []Access{{"b", false}, {"c", false}}) {
		t.Fatal(got)
	}
	if got := readable(cfg, config.LocalIssuer, "ada", nil, true); !reflect.DeepEqual(got, []Access{{"b", true}, {"c", false}}) {
		t.Fatal(got)
	}
	// bob's named paper comes first; open papers follow, not his own, even
	// for an administrator.
	if got := readable(cfg, config.LocalIssuer, "bob", nil, true); !reflect.DeepEqual(got, []Access{{"a", true}, {"b", false}, {"c", false}}) {
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
	verify := func(hash, password string) bool {
		ok, err := VerifyPassword(context.Background(), hash, password)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !verify(h, "correct horse battery") || verify(h, "correct horse batterY") || verify(h, "") {
		t.Fatal("verification wrong")
	}
	if h2, _ := HashPassword("correct horse battery"); h2 == h {
		t.Fatal("hashes are not salted")
	}
	// No account, malformed and exhausting hashes all refuse.
	for _, bad := range []string{"", "$argon2i$v=19$m=19456,t=2,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA", "$argon2id$v=19$m=4194304,t=2,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA", "plain"} {
		if verify(bad, "night-desk-no-such-account") {
			t.Fatal(bad)
		}
	}
	// An over-long password is refused alike with or without an account.
	long := strings.Repeat("x", MaxPasswordLength+1)
	if verify(h, long) || verify("", long) {
		t.Fatal("over-long password")
	}
	if CheckNewPassword("123456789") == nil || CheckNewPassword("ünïcödé-ok") != nil || CheckNewPassword(strings.Repeat("x", MaxPasswordLength+1)) == nil {
		t.Fatal("password rules")
	}
}

func TestLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter()
	l.SetClock(func() time.Time { return now })
	// Each attempt counts as a failure until Succeed clears it.
	for i := 0; i < freeFailures; i++ {
		if l.Attempt("u", "addr") != 0 {
			t.Fatal("throttled within the free failures")
		}
	}
	if w := l.Attempt("u"); w != 0 {
		t.Fatal("throttled at the free limit", w)
	}
	// The sixth failure set a one-second wait.
	if w := l.Attempt("u"); w != time.Second {
		t.Fatal(w)
	}
	for i := 0; i < 30; i++ {
		now = now.Add(maxDelay)
		l.Attempt("u")
	}
	if w := l.Attempt("u"); w != maxDelay {
		t.Fatal(w)
	}
	// Another key is unaffected; success clears.
	if l.Attempt("v") != 0 {
		t.Fatal("other key throttled")
	}
	l.Succeed("u")
	if l.Attempt("u") != 0 {
		t.Fatal("success did not clear")
	}
	// A key quiet for long starts over.
	for i := 0; i < 8; i++ {
		now = now.Add(time.Minute)
		l.Attempt("w")
	}
	now = now.Add(time.Hour)
	l.Attempt("w")
	if l.Attempt("w") != 0 {
		t.Fatal("old failures still count")
	}
}

func TestPasswordWorkIsBounded(t *testing.T) {
	// Fill both workers and the queue: the next check is refused at once.
	for i := 0; i < cap(hashing); i++ {
		hashing <- struct{}{}
	}
	for i := 0; i < cap(waiting); i++ {
		waiting <- struct{}{}
	}
	// A missing account ("") reaches argon2 too, through the dummy hash.
	h := ""
	if _, err := VerifyPassword(context.Background(), h, "x"); err != ErrBusy {
		t.Fatal(err)
	}
	<-waiting
	// With room in the queue, a cancelled request stops waiting.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := VerifyPassword(ctx, h, "x"); err != context.Canceled {
		t.Fatal(err)
	}
	for i := 0; i < cap(waiting)-1; i++ {
		<-waiting
	}
	for i := 0; i < cap(hashing); i++ {
		<-hashing
	}
}
