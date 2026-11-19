package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/keys"
	"github.com/irashack/night-desk/internal/store"
)

func TestAuthorizationJWT(t *testing.T) {
	dir := t.TempDir()
	k, err := LoadKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, KeyFile))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("key file mode: %v %v", info, err)
	}
	again, err := LoadKey(dir)
	if err != nil || again.PublicKey() != k.PublicKey() {
		t.Fatal("key not reloaded", err)
	}
	if strings.Contains(k.String(), k.PublicKey()) || k.GoString() != k.String() {
		t.Fatal("key not redacted")
	}
	at := time.Unix(1_800_000_000, 0)
	header, err := k.Authorization("https://fcm.googleapis.com/fcm/send/abc?x=1", "https://paper.example.com", at)
	if err != nil {
		t.Fatal(err)
	}
	jwt, pub := verify(t, header)
	if pub != k.PublicKey() {
		t.Fatal("k= is not the public key")
	}
	if jwt["aud"] != "https://fcm.googleapis.com" || jwt["sub"] != "https://paper.example.com" || jwt["exp"] != float64(at.Add(12*time.Hour).Unix()) {
		t.Fatalf("claims: %v", jwt)
	}
	if _, err = k.Authorization("not a url", "https://paper.example.com", at); err == nil {
		t.Fatal("invalid endpoint signed")
	}
	bad := filepath.Join(dir, "bad.key")
	if err = os.WriteFile(bad, []byte("not a key"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadKey(bad); err == nil {
		t.Fatal("garbage key accepted")
	}
	if _, err = CreateKey(filepath.Join(dir, KeyFile)); err == nil {
		t.Fatal("existing key replaced")
	}
}

// verify checks an RFC 8292 header's shape and ES256 signature and returns its
// claims and public key.
func verify(t *testing.T, header string) (map[string]any, string) {
	t.Helper()
	rest, ok := strings.CutPrefix(header, "vapid t=")
	token, pub, ok2 := strings.Cut(rest, ", k=")
	if !ok || !ok2 {
		t.Fatalf("header shape: %q", header)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatal("JWT needs three parts")
	}
	head, _ := base64.RawURLEncoding.DecodeString(parts[0])
	if string(head) != `{"typ":"JWT","alg":"ES256"}` {
		t.Fatalf("JWT header: %s", head)
	}
	raw, err := base64.RawURLEncoding.DecodeString(pub)
	if err != nil || len(raw) != 65 || raw[0] != 4 {
		t.Fatal("public key is not an uncompressed P-256 point")
	}
	key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatal("signature is not 64-byte R || S")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int), new(big.Int)
	if !ecdsa.Verify(key, digest[:], r.SetBytes(sig[:32]), s.SetBytes(sig[32:])) {
		t.Fatal("signature does not verify")
	}
	body, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	if err = json.Unmarshal(body, &claims); err != nil {
		t.Fatal(err)
	}
	return claims, pub
}

func TestCheckEndpoint(t *testing.T) {
	for _, ok := range []string{"https://fcm.googleapis.com/fcm/send/abc:def", "https://updates.push.services.mozilla.com/wpush/v2/gAAA", "https://web.push.apple.com/QGx", "https://8.8.8.8/x", "https://push.example.com:8443/x"} {
		if err := CheckEndpoint(ok); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://fcm.googleapis.com/x", "https://192.168.1.2/x", "https://100.64.0.1/x", "https://[fd00::1]/x", "https://localhost/x", "https://a.localhost./x", "https://nas.home.arpa/x", "https://svc.internal/x", "https://intranet/x", "https://u@fcm.googleapis.com/x", "https://fcm.googleapis.com/x y", "https:///x"} {
		if err := CheckEndpoint(bad); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
}

type fakeService struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*http.Request
	bodies   []int
	status   func(path string) int
}

func service(t *testing.T) *fakeService {
	f := &fakeService{status: func(string) int { return 201 }}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, r)
		f.bodies = append(f.bodies, len(b))
		code := f.status(r.URL.Path)
		f.mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(f.Close)
	return f
}
func (f *fakeService) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		out = append(out, r.URL.Path)
	}
	f.requests, f.bodies = nil, nil
	return out
}

type desk struct {
	db     *store.Store
	cfg    *config.Config
	loop   *Loop
	papers map[string]auth.PaperScope
	at     time.Time
}

func setup(t *testing.T, client *http.Client) *desk {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Server: config.Server{PublicBaseURL: "https://paper.example.com/"}, Papers: []config.Paper{
		{ID: "daily", Sections: []config.Section{{Key: "news", Title: "News"}}, Readers: []config.Reader{{Issuer: "https://id.example.com", Subject: "one"}, {Issuer: "https://id.example.com", Subject: "two"}}},
		{ID: "other", Sections: []config.Section{{Key: "news", Title: "News"}}, Readers: []config.Reader{{Issuer: "https://id.example.com", Subject: "three"}}},
	}}
	key, err := CreateKey(filepath.Join(t.TempDir(), KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	d := &desk{db: db, cfg: cfg, papers: map[string]auth.PaperScope{}, at: time.Now()}
	d.loop = &Loop{Key: key, Store: db, Config: func() *config.Config { return d.cfg }, Now: func() time.Time { return d.at }, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), client: client}
	for _, paper := range []string{"daily", "other"} {
		_, k, err := keys.Issue(context.Background(), db, paper, "test")
		if err != nil {
			t.Fatal(err)
		}
		if d.papers[paper], err = auth.Authenticate(context.Background(), db, k); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// subscribe signs a reader in and registers one endpoint, returning the session hash.
func (d *desk) subscribe(t *testing.T, subject, endpoint string) [32]byte {
	t.Helper()
	ctx := context.Background()
	raw := sha256.Sum256([]byte(subject + "\x00" + endpoint))
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	hash := sha256.Sum256([]byte(token))
	now := time.Now()
	if err := d.db.InsertSession(ctx, auth.Session{Hash: hash, Issuer: "https://id.example.com", Subject: subject, CSRF: "c", Created: now, LastSeen: now, Expires: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	scope, err := auth.AuthenticateReader(ctx, d.db, d.cfg, token, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.db.AddPushSubscription(ctx, scope, endpoint, nil); err != nil {
		t.Fatal(err)
	}
	return hash
}
func (d *desk) publish(t *testing.T, paper, date string) {
	t.Helper()
	ed := &edition.Edition{Schema: 1, Lead: "a", Sections: []edition.Section{{Key: "news", Items: []edition.Item{{ID: "a", Kind: "news", Title: "Title", Summary: "Summary.", Source: "Wire", URL: "https://example.com/a"}}}}}
	if err := d.db.Publish(context.Background(), d.papers[paper], date, ed, nil); err != nil {
		t.Fatal(err)
	}
	// Publication queues pushes at the wall clock; keep the loop's clock past it.
	if now := time.Now(); now.After(d.at) {
		d.at = now
	}
}
func (d *desk) tick(t *testing.T) {
	t.Helper()
	if err := d.loop.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func same(got []string, want ...string) bool {
	return strings.Join(got, ",") == strings.Join(want, ",")
}

func TestDeliveryOnPublication(t *testing.T) {
	svc := service(t)
	d := setup(t, svc.Client())
	d.subscribe(t, "one", svc.URL+"/one")
	d.subscribe(t, "two", svc.URL+"/two")
	d.subscribe(t, "three", svc.URL+"/three")
	d.publish(t, "daily", "2026-11-03")
	d.tick(t)
	svc.mu.Lock()
	if len(svc.requests) != 2 {
		t.Fatalf("requests: %d", len(svc.requests))
	}
	for i, r := range svc.requests {
		if r.Method != "POST" || svc.bodies[i] != 0 || r.ContentLength != 0 || r.Header.Get("TTL") != "43200" || r.Header.Get("Content-Encoding") != "" {
			t.Fatalf("not an empty push: %s %d %v", r.Method, svc.bodies[i], r.Header)
		}
		claims, pub := verify(t, r.Header.Get("Authorization"))
		if pub != d.loop.Key.PublicKey() || claims["aud"] != svc.URL || claims["sub"] != "https://paper.example.com" {
			t.Fatalf("claims: %v", claims)
		}
	}
	svc.mu.Unlock()
	if got := svc.paths(); !same(got, "/one", "/two") {
		t.Fatalf("paper daily pushed to %v", got)
	}
	// A replaced edition, or a backfilled earlier date, does not notify again.
	d.publish(t, "daily", "2026-11-03")
	d.publish(t, "daily", "2026-11-01")
	d.tick(t)
	if got := svc.paths(); len(got) != 0 {
		t.Fatalf("replacement pushed: %v", got)
	}
	d.publish(t, "other", "2026-11-03")
	d.tick(t)
	if got := svc.paths(); !same(got, "/three") {
		t.Fatalf("paper other pushed to %v", got)
	}
	// A reader no longer in the paper's configuration gets nothing.
	d.cfg = &config.Config{Server: d.cfg.Server, Papers: []config.Paper{{ID: "daily", Readers: []config.Reader{{Issuer: "https://id.example.com", Subject: "two"}}}}}
	d.publish(t, "daily", "2026-11-04")
	d.tick(t)
	if got := svc.paths(); !same(got, "/two") {
		t.Fatalf("removed reader pushed: %v", got)
	}
}

func TestGoneSubscriptionsAreDeleted(t *testing.T) {
	svc := service(t)
	svc.status = func(path string) int {
		if path == "/gone" {
			return 410
		}
		if path == "/missing" {
			return 404
		}
		return 201
	}
	d := setup(t, svc.Client())
	d.subscribe(t, "one", svc.URL+"/gone")
	d.subscribe(t, "one", svc.URL+"/missing")
	d.subscribe(t, "two", svc.URL+"/kept")
	d.publish(t, "daily", "2026-11-03")
	d.tick(t)
	if got := svc.paths(); len(got) != 3 {
		t.Fatalf("first push: %v", got)
	}
	d.publish(t, "daily", "2026-11-04")
	d.tick(t)
	if got := svc.paths(); !same(got, "/kept") {
		t.Fatalf("gone subscriptions kept: %v", got)
	}
}

func TestRetriesAreBounded(t *testing.T) {
	svc := service(t)
	flaky := 0
	svc.status = func(path string) int {
		if path == "/flaky" {
			if flaky++; flaky == 1 {
				return 503
			}
		}
		if path == "/down" {
			return 500
		}
		if path == "/refused" {
			return 400
		}
		return 201
	}
	d := setup(t, svc.Client())
	d.subscribe(t, "one", svc.URL+"/flaky")
	d.publish(t, "daily", "2026-11-03")
	d.tick(t)
	d.tick(t)
	if got := svc.paths(); !same(got, "/flaky") {
		t.Fatalf("retried before backoff: %v", got)
	}
	d.at = d.at.Add(time.Minute)
	d.tick(t)
	if got := svc.paths(); !same(got, "/flaky") {
		t.Fatalf("retry after backoff: %v", got)
	}
	d.at = d.at.Add(time.Hour)
	d.tick(t)
	if got := svc.paths(); len(got) != 0 {
		t.Fatalf("delivered push sent again: %v", got)
	}
	d.subscribe(t, "two", svc.URL+"/down")
	d.subscribe(t, "two", svc.URL+"/refused")
	d.publish(t, "daily", "2026-11-04")
	var down, refused int
	for i := 0; i < 10; i++ {
		d.tick(t)
		for _, p := range svc.paths() {
			switch p {
			case "/down":
				down++
			case "/refused":
				refused++
			}
		}
		d.at = d.at.Add(10 * time.Minute)
	}
	if down != 4 || refused != 1 {
		t.Fatalf("attempts: down %d refused %d", down, refused)
	}
}

func TestProductionClientRefusesPrivateEndpoints(t *testing.T) {
	svc := service(t)
	d := setup(t, nil)
	d.subscribe(t, "one", svc.URL+"/loopback")
	d.publish(t, "daily", "2026-11-03")
	d.tick(t)
	d.at = d.at.Add(time.Hour)
	d.tick(t)
	if got := svc.paths(); len(got) != 0 {
		t.Fatalf("guarded client reached loopback: %v", got)
	}
}

func TestSignOutStopsPushes(t *testing.T) {
	svc := service(t)
	d := setup(t, svc.Client())
	session := d.subscribe(t, "one", svc.URL+"/one")
	d.subscribe(t, "two", svc.URL+"/two")
	if err := d.db.SignOut(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	d.publish(t, "daily", "2026-11-03")
	d.tick(t)
	if got := svc.paths(); !same(got, "/two") {
		t.Fatalf("signed-out device pushed: %v", got)
	}
}
