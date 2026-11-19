package reader

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/auth"
)

func subscriptionJSON(endpoint string) string {
	b, _ := json.Marshal(map[string]any{"endpoint": endpoint, "expirationTime": nil, "keys": map[string]string{"p256dh": "BPk", "auth": "c2VjcmV0"}})
	return string(b)
}
func (f *fixture) readerScope(t *testing.T, token string) auth.ReaderScope {
	t.Helper()
	scope, err := auth.AuthenticateReader(context.Background(), f.db, f.cfg, token, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}
func (f *fixture) devices(t *testing.T, token string) int {
	t.Helper()
	n, err := f.db.PushSubscriptions(context.Background(), f.readerScope(t, token))
	if err != nil {
		t.Fatal(err)
	}
	return n
}
func (f *fixture) pushPost(t *testing.T, path, csrf, subscription string) int {
	t.Helper()
	r := f.post(t, path, url.Values{"csrf": {csrf}, "subscription": {subscription}})
	r.Body.Close()
	return r.StatusCode
}

func TestPushToggleMarkup(t *testing.T) {
	f := setup(t)
	f.publish(t, f.a, "2026-10-27", sampleEdition(), nil)
	r := f.signIn(t, "")
	r.Body.Close()
	html := body(t, f.get(t, "/"))
	if !strings.Contains(html, `data-push="`+f.s.Push.PublicKey()+`"`) || !strings.Contains(html, `<div class="pref" hidden>`) || !strings.Contains(html, `role="switch" data-push=`) {
		t.Fatal("push toggle missing or not hidden until supported")
	}
	f.s.Push = nil
	if html = body(t, f.get(t, "/")); strings.Contains(html, "data-push") {
		t.Fatal("push toggle shown without a key")
	}
	session, _ := f.session(t)
	if code := f.pushPost(t, "/push/subscribe", session.CSRF, subscriptionJSON("https://push.example.com/a")); code != 404 {
		t.Fatalf("subscribe without a key: %d", code)
	}
}

func TestPushSubscribeValidation(t *testing.T) {
	f := setup(t)
	if code := f.pushPost(t, "/push/subscribe", "x", subscriptionJSON("https://push.example.com/a")); code != 401 {
		t.Fatalf("unauthenticated subscribe: %d", code)
	}
	r := f.signIn(t, "")
	r.Body.Close()
	session, token := f.session(t)
	if code := f.pushPost(t, "/push/subscribe", "wrong", subscriptionJSON("https://push.example.com/a")); code != 403 {
		t.Fatalf("bad CSRF: %d", code)
	}
	for _, endpoint := range []string{
		"http://push.example.com/a", "https://127.0.0.1/a", "https://10.0.0.8/a", "https://[::1]/a", "https://localhost/a",
		"https://push.localhost/a", "https://router/a", "https://printer.local/a", "https://user:pw@push.example.com/a",
		"https://push.example.com/a#frag", "https://push.example.com:0/a", "push.example.com/a", "",
		"https://push.example.com/" + strings.Repeat("a", 1024),
	} {
		if code := f.pushPost(t, "/push/subscribe", session.CSRF, subscriptionJSON(endpoint)); code != 400 {
			t.Fatalf("%q accepted: %d", endpoint, code)
		}
	}
	for _, raw := range []string{"", "{", `{"endpoint":1}`, `{"endpoint":"https://push.example.com/a","expirationTime":1}`} {
		if code := f.pushPost(t, "/push/subscribe", session.CSRF, raw); code != 400 {
			t.Fatalf("%q accepted: %d", raw, code)
		}
	}
	big := url.Values{"csrf": {session.CSRF}, "subscription": {subscriptionJSON("https://push.example.com/a")}, "pad": {strings.Repeat("x", 9000)}}
	if r = f.post(t, "/push/subscribe", big); r.StatusCode != 400 {
		t.Fatalf("oversized body: %d", r.StatusCode)
	}
	r.Body.Close()
	if f.devices(t, token) != 0 {
		t.Fatal("refused subscription stored")
	}
	future := time.Now().Add(48 * time.Hour).UnixMilli()
	withExpiry := fmt.Sprintf(`{"endpoint":"https://fcm.googleapis.com/fcm/send/abc","expirationTime":%d}`, future)
	if code := f.pushPost(t, "/push/subscribe", session.CSRF, withExpiry); code != 204 {
		t.Fatalf("valid subscription: %d", code)
	}
	if code := f.pushPost(t, "/push/subscribe", session.CSRF, subscriptionJSON("https://fcm.googleapis.com/fcm/send/abc")); code != 204 || f.devices(t, token) != 1 {
		t.Fatal("renewal not idempotent")
	}
	if code := f.pushPost(t, "/push/unsubscribe", session.CSRF, subscriptionJSON("https://fcm.googleapis.com/fcm/send/abc")); code != 204 || f.devices(t, token) != 0 {
		t.Fatal("unsubscribe failed")
	}
}

func TestPushDeviceCap(t *testing.T) {
	f := setup(t)
	r := f.signIn(t, "")
	r.Body.Close()
	session, token := f.session(t)
	for i := 0; i < 10; i++ {
		if code := f.pushPost(t, "/push/subscribe", session.CSRF, subscriptionJSON(fmt.Sprintf("https://push.example.com/%d", i))); code != 204 {
			t.Fatalf("device %d: %d", i, code)
		}
	}
	if code := f.pushPost(t, "/push/subscribe", session.CSRF, subscriptionJSON("https://push.example.com/extra")); code != 409 {
		t.Fatalf("eleventh device: %d", code)
	}
	if code := f.pushPost(t, "/push/subscribe", session.CSRF, subscriptionJSON("https://push.example.com/3")); code != 204 || f.devices(t, token) != 10 {
		t.Fatal("renewing a device at the cap refused")
	}
}

func TestPushIsolationAndSignOut(t *testing.T) {
	f := setup(t)
	endpoint := subscriptionJSON("https://push.example.com/shared")
	r := f.signIn(t, "")
	r.Body.Close()
	a, aToken := f.session(t)
	if code := f.pushPost(t, "/push/subscribe", a.CSRF, endpoint); code != 204 {
		t.Fatal("reader a subscribe", code)
	}
	// Reader b reads paper b in a second browser.
	f.p.subject = "reader-b"
	jar := f.client.Jar
	f.client.Jar, _ = cookiejar.New(nil)
	r = f.signIn(t, "")
	r.Body.Close()
	b, bToken := f.session(t)
	if code := f.pushPost(t, "/push/unsubscribe", b.CSRF, endpoint); code != 204 || f.devices(t, aToken) != 1 {
		t.Fatal("reader b removed reader a's device")
	}
	if code := f.pushPost(t, "/push/subscribe", a.CSRF, endpoint); code != 403 {
		t.Fatal("reader a's CSRF accepted in reader b's session", code)
	}
	if code := f.pushPost(t, "/push/subscribe", b.CSRF, endpoint); code != 204 || f.devices(t, bToken) != 1 || f.devices(t, aToken) != 1 {
		t.Fatal("papers' subscriptions not separate")
	}
	if f.readerScope(t, bToken).PaperScope().PaperID() != "b" {
		t.Fatal("reader b not on paper b")
	}
	// Signing out removes the devices that session registered, and only those.
	r = f.post(t, "/logout", url.Values{"csrf": {b.CSRF}})
	r.Body.Close()
	f.client.Jar = jar
	if f.devices(t, aToken) != 1 {
		t.Fatal("reader b's sign-out removed reader a's device")
	}
	r = f.post(t, "/logout", url.Values{"csrf": {a.CSRF}})
	if r.StatusCode != 200 {
		t.Fatal("logout failed", r.StatusCode)
	}
	r.Body.Close()
	f.p.subject = "reader-a"
	r = f.signIn(t, "")
	r.Body.Close()
	_, token := f.session(t)
	if f.devices(t, token) != 0 {
		t.Fatal("sign-out kept the reader's device")
	}
}
