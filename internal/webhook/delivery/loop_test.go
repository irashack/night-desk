package delivery

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/fetch"
	"github.com/irashack/night-desk/internal/keys"
	"github.com/irashack/night-desk/internal/store"
	"github.com/irashack/night-desk/internal/webhook"
)

func hookConfig(t *testing.T, target, secrets string) (*config.Config, func(string) *config.Config) {
	t.Helper()
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "secret")
	path := filepath.Join(dir, "paper.yaml")
	content := fmt.Sprintf(`server:
  listen: "127.0.0.1:8080"
  public_base_url: https://paper.example
  data_dir: data
  oidc:
    issuer: https://id.example
    client_id: nightdesk
    client_secret_file: oidc
papers:
- id: daily
  masthead: Daily
  timezone: UTC
  look: broadsheet
  latitude: 0
  longitude: 0
  sections: [{key: news, title: News}]
  readers: [{issuer: https://id.example, subject: one}]
  webhooks: [{url: %q, secret_file: %q}]
`, target, secretPath)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	load := func(secrets string) *config.Config {
		t.Helper()
		if err := os.WriteFile(secretPath, []byte(secrets), 0600); err != nil {
			t.Fatal(err)
		}
		c, err := config.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	return load(secrets), load
}
func outbox(t *testing.T, c *config.Config) (*store.Store, auth.PaperScope, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	_, key, err := keys.Issue(ctx, s, "daily", "test")
	if err != nil {
		t.Fatal(err)
	}
	scope, err := auth.Authenticate(ctx, s, key)
	if err != nil {
		t.Fatal(err)
	}
	s.SetEventConfig(func() *config.Config { return c })
	ed := &edition.Edition{Schema: 1, Lead: "one", Sections: []edition.Section{{Key: "news", Items: []edition.Item{{ID: "one", Kind: "news", Title: "Headline", Summary: "Summary", Source: "Desk", URL: "https://example.com/news"}}}}}
	if err = s.Publish(ctx, scope, "2026-10-30", ed, nil); err != nil {
		t.Fatal(err)
	}
	return s, scope, dir
}
func state(t *testing.T, dir string) (int, int, int64, bool, bool) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "nightdesk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var attempts, status int
	var next int64
	var delivered, failed sql.NullString
	if err = db.QueryRow(`SELECT attempts,last_status,next_attempt_at,delivered_at,failed_at FROM deliveries`).Scan(&attempts, &status, &next, &delivered, &failed); err != nil {
		t.Fatal(err)
	}
	return attempts, status, next, delivered.Valid, failed.Valid
}

func TestRetryRestartRotationAndStream(t *testing.T) {
	for _, firstStatus := range []int{503, 429} {
		t.Run(strconv.Itoa(firstStatus), func(t *testing.T) {
			at := time.Now().UTC().Add(time.Second).Truncate(time.Second)
			calls := 0
			var eventID string
			ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				timestamp, err := strconv.ParseInt(r.Header.Get("Night-Desk-Timestamp"), 10, 64)
				if err != nil || timestamp != at.Unix() {
					t.Error("clock", err, timestamp)
				}
				active := []string{"current-secret", "old-secret"}
				if calls == 2 {
					active = []string{"rotated-secret", "current-secret"}
				}
				for _, secret := range active {
					if err = webhook.Verify([]byte(secret), r.Header.Get("Night-Desk-Signature-256"), timestamp, body, at, time.Minute); err != nil {
						t.Error("signature", err)
					}
				}
				var event webhook.Event
				if err = json.Unmarshal(body, &event); err != nil {
					t.Error(err)
				}
				if calls == 1 {
					eventID = event.ID
				} else if event.ID != eventID {
					t.Error("retry changed ID")
				}
				if r.Method != "POST" || event.Type != "edition.published" || event.Paper != "daily" || event.ID != r.Header.Get("Night-Desk-Delivery") || event.Type != r.Header.Get("Night-Desk-Event") {
					t.Error("envelope", event)
				}
				if calls == 1 {
					w.WriteHeader(firstStatus)
				} else {
					w.WriteHeader(204)
				}
			}))
			defer ts.Close()
			c, reload := hookConfig(t, ts.URL, "current-secret\nold-secret\n")
			s, scope, dir := outbox(t, c)
			var logs bytes.Buffer
			loop := &Loop{Store: s, Config: func() *config.Config { return c }, Now: func() time.Time { return at }, Log: slog.New(slog.NewJSONHandler(&logs, nil)), newClient: func(fetch.Allow) *http.Client { return ts.Client() }}
			if err := loop.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			attempts, status, next, delivered, failed := state(t, dir)
			if calls != 1 || attempts != 1 || status != firstStatus || next != at.Add(time.Minute).Unix() || delivered || failed {
				t.Fatal(calls, attempts, status, next, delivered, failed)
			}
			events, _, err := s.Events(context.Background(), scope, "", 10)
			if err != nil || len(events) != 1 || events[0].ID != eventID {
				t.Fatal("stream differs", events, err)
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := store.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			loop.Store = reopened
			c = reload("rotated-secret\ncurrent-secret\n")
			at = at.Add(time.Minute - time.Second)
			if err = loop.Tick(context.Background()); err != nil || calls != 1 {
				t.Fatal("early retry", err, calls)
			}
			at = at.Add(time.Second)
			if err = loop.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			attempts, status, _, delivered, failed = state(t, dir)
			if calls != 2 || attempts != 2 || status != 204 || !delivered || failed {
				t.Fatal(calls, attempts, status, delivered, failed)
			}
			at = at.Add(30 * time.Hour)
			if err = loop.Tick(context.Background()); err != nil || calls != 2 {
				t.Fatal("delivered replayed", err, calls)
			}
			if strings.Contains(logs.String(), "secret") {
				t.Fatal("secret logged")
			}
		})
	}
}

func TestExhaustionPermanentFailureAndRemovedTarget(t *testing.T) {
	for _, mode := range []string{"exhausted", "permanent", "removed", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch mode {
				case "permanent":
					w.WriteHeader(400)
				case "redirect":
					http.Redirect(w, r, "https://private.example/hook", 302)
				default:
					w.WriteHeader(500)
				}
			}))
			defer ts.Close()
			c, _ := hookConfig(t, ts.URL, "test-secret")
			s, _, dir := outbox(t, c)
			if mode == "removed" {
				c.Papers[0].Webhooks = nil
			}
			at := time.Now().UTC().Add(time.Second).Truncate(time.Second)
			loop := &Loop{Store: s, Config: func() *config.Config { return c }, Now: func() time.Time { return at }, newClient: func(fetch.Allow) *http.Client { return ts.Client() }}
			n := 1
			if mode == "exhausted" {
				n = 12
			}
			for i := 1; i <= n; i++ {
				if err := loop.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
				attempts, _, next, _, _ := state(t, dir)
				if attempts != i {
					t.Fatal("attempt", attempts, i)
				}
				if mode == "exhausted" && next != at.Add(Backoff(i)).Unix() {
					t.Fatal("backoff", i, next)
				}
				at = time.Unix(next, 0)
			}
			attempts, _, _, delivered, failed := state(t, dir)
			if attempts != n || delivered || !failed {
				t.Fatal(attempts, delivered, failed)
			}
			if mode == "removed" {
				if calls != 0 {
					t.Fatal("removed endpoint called")
				}
			} else if calls != n {
				t.Fatal(calls, n)
			}
			at = at.Add(7 * 24 * time.Hour)
			if err := loop.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if mode != "removed" && calls != n {
				t.Fatal("failed delivery retried")
			}
		})
	}
	for _, attempt := range []int{12, 13, 100} {
		if Backoff(attempt) != 24*time.Hour {
			t.Fatal("uncapped backoff")
		}
	}
}

func TestGuardRefusesPrivateAndAllowsExplicitException(t *testing.T) {
	hits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(204) }))
	defer ts.Close()
	c, _ := hookConfig(t, strings.Replace(ts.URL, "http:", "https:", 1), "guard-secret")
	// Use HTTP only in the constructed fixture to test the real guarded transport
	// without introducing a certificate override. Loaded operator URLs require HTTPS.
	c.Papers[0].Webhooks[0].URL = ts.URL
	s, _, dir := outbox(t, c)
	at := time.Now().UTC().Add(time.Second)
	loop := &Loop{Store: s, Config: func() *config.Config { return c }, Now: func() time.Time { return at }}
	if err := loop.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	attempts, status, next, delivered, failed := state(t, dir)
	if hits != 0 || attempts != 1 || status != 0 || delivered || failed {
		t.Fatal(hits, attempts, status, delivered, failed)
	}
	c.Gather.AllowPrivate = []string{strings.TrimPrefix(ts.URL, "http://")}
	at = time.Unix(next, 0)
	if err := loop.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	attempts, status, _, delivered, failed = state(t, dir)
	if hits != 1 || attempts != 2 || status != 204 || !delivered || failed {
		t.Fatal(hits, attempts, status, delivered, failed)
	}
}

func TestRunStopsOnContext(t *testing.T) {
	c, _ := hookConfig(t, "https://127.0.0.1/hook", "test-secret")
	s, _, _ := outbox(t, c)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { (&Loop{Store: s, Config: func() *config.Config { return c }}).Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("loop ignored cancellation")
	}
}

func TestIdleTickBuildsNoClient(t *testing.T) {
	c, _ := hookConfig(t, "https://127.0.0.1/hook", "test-secret")
	s, _, _ := outbox(t, c)
	built := 0
	// Before the first attempt is due, a tick has nothing to send.
	at := time.Now().Add(-time.Hour)
	loop := &Loop{Store: s, Config: func() *config.Config { return c }, Now: func() time.Time { return at }, newClient: func(fetch.Allow) *http.Client { built++; return http.DefaultClient }}
	if err := loop.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if built != 0 {
		t.Fatalf("idle tick built %d clients", built)
	}
}
