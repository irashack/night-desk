package push

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/fetch"
	"github.com/irashack/night-desk/internal/store"
)

// MaxEndpoint bounds a stored endpoint; real push services use a few hundred bytes.
const MaxEndpoint = 1024

// CheckEndpoint accepts an absolute https URL on a public host: no credentials,
// fragment, IP literal outside public unicast, or local-only name. Delivery's
// dial-time guard still checks the resolved address.
func CheckEndpoint(raw string) error {
	if raw == "" || len(raw) > MaxEndpoint || strings.ContainsAny(raw, " \t\r\n") {
		return errors.New("endpoint: must be an https URL of at most 1024 bytes")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return errors.New("endpoint: must be an absolute https URL without credentials or fragment")
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return errors.New("endpoint: port must be 1..65535")
		}
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if ip, err := netip.ParseAddr(host); err == nil {
		if fetch.CheckAddress(ip) != nil {
			return errors.New("endpoint: must be a public host")
		}
		return nil
	}
	if !strings.Contains(host, ".") {
		return errors.New("endpoint: must be a public host")
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".home.arpa", ".invalid", ".test", ".example", ".onion"} {
		if strings.HasSuffix("."+host, suffix) {
			return errors.New("endpoint: must be a public host")
		}
	}
	return nil
}

// Loop drains the push outbox from the server. It is a singleton per server.
type Loop struct {
	Key    *Key
	Store  *store.Store
	Config func() *config.Config
	Now    func() time.Time
	Log    *slog.Logger
	client *http.Client // tests only; production uses fetch.New(nil)
}

// Backoff waits before retrying a throttled or failed send: 1, 2, then 4 minutes.
func Backoff(attempt int) time.Duration {
	return time.Minute << min(max(attempt-1, 0), 2)
}

func (l *Loop) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Run resumes pending notifications, then checks once per second.
func (l *Loop) Run(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		if err := l.Tick(ctx); err != nil && ctx.Err() == nil && l.Log != nil {
			l.Log.Error("push outbox operation failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Tick sends a bounded batch. Push services are public: the guarded client gets
// no private exceptions, follows no redirects and gives up after five seconds.
func (l *Loop) Tick(ctx context.Context) error {
	// The client is built once there is something to send: an idle tick is one
	// read query.
	var client *http.Client
	defer func() {
		if client != nil && client != l.client {
			client.CloseIdleConnections()
		}
	}()
	var sent, gone, retried, dropped int
	defer func() {
		if l.Log != nil && sent+gone+retried+dropped > 0 {
			l.Log.Info("web push", "sent", sent, "gone", gone, "retried", retried, "dropped", dropped)
		}
	}()
	for i := 0; i < 100; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		d, err := l.Store.ClaimPush(ctx, l.now())
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if client == nil {
			client = l.client
			if client == nil {
				client = fetch.New(nil)
				client.Timeout = 5 * time.Second
			}
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		}
		c := l.Config()
		if !reads(c, d) {
			// A reader removed from the paper gets nothing; the device row stays
			// until it is unsubscribed, so a restored reader resumes.
			dropped++
			if err = l.Store.FinishPush(ctx, d, false, time.Time{}); err != nil {
				return err
			}
			continue
		}
		status, retryAfter, sendErr := l.send(ctx, client, d, c)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var retry time.Time
		switch {
		case sendErr == nil && status >= 200 && status < 300:
			sent++
		case status == http.StatusNotFound || status == http.StatusGone:
			gone++
		case errors.Is(sendErr, fetch.ErrRefused):
			dropped++
		case sendErr != nil || status == http.StatusTooManyRequests || status >= 500:
			if d.Attempts < store.PushAttempts {
				retried++
				retry = l.now().Add(max(Backoff(d.Attempts), retryAfter))
			} else {
				dropped++
			}
		default:
			dropped++
		}
		if err = l.Store.FinishPush(ctx, d, status == http.StatusNotFound || status == http.StatusGone, retry); err != nil {
			return err
		}
	}
	return nil
}

// reads reports whether the subscription's reader still reads its paper under
// the current configuration: by subject (the same first-match rule as sign-in)
// or through the groups their latest sign-in carried.
func reads(c *config.Config, d store.PushDelivery) bool {
	for _, a := range auth.Readable(c, d.Issuer, d.Subject, d.Groups) {
		if a.Paper == d.Paper {
			return true
		}
	}
	return false
}

// headline trims a lead headline for the lock screen: at most 180 runes,
// cut at a word boundary.
func headline(title string) string {
	runes := []rune(strings.TrimSpace(title))
	if len(runes) <= 180 {
		return string(runes)
	}
	cut := 179
	for cut > 0 && !unicode.IsSpace(runes[cut]) {
		cut--
	}
	if cut == 0 {
		cut = 179
	}
	return strings.TrimSpace(string(runes[:cut])) + "…"
}

// payload is the encrypted Declarative Web Push message naming the paper and
// its lead, or nil for the empty push: the paper did not opt in, the device
// sent no keys, or the edition has no titled, non-opinion lead.
func (l *Loop) payload(ctx context.Context, d store.PushDelivery, c *config.Config, base string) ([]byte, error) {
	paper, _ := c.Paper(d.Paper)
	if !paper.PushHeadline || d.Keys.P256DH == "" || d.Keys.Auth == "" {
		return nil, nil
	}
	masthead, title, err := l.Store.PushLead(ctx, d)
	if err != nil {
		return nil, err
	}
	body := headline(title)
	if body == "" {
		return nil, nil
	}
	if masthead == "" {
		masthead = paper.Masthead
	}
	notification := map[string]any{"title": masthead, "body": body, "navigate": base + "/p/" + paper.ID + "/", "tag": "nd-edition", "silent": false}
	if paper.Locale != "" {
		notification["lang"] = paper.Locale
	}
	message, err := json.Marshal(map[string]any{"web_push": 8030, "notification": notification})
	if err != nil {
		return nil, err
	}
	return encrypt(d.Keys.P256DH, d.Keys.Auth, message)
}

// send posts one VAPID-signed message, empty or with the encrypted headline.
// The push service keeps it for up to 12 hours, and a newer one with the same
// topic replaces it.
func (l *Loop) send(ctx context.Context, client *http.Client, d store.PushDelivery, c *config.Config) (int, time.Duration, error) {
	endpoint := d.Endpoint
	subject := strings.TrimRight(c.Server.PublicBaseURL, "/")
	// The headline is a bonus: when it cannot be built (an unreadable
	// edition, keys the browser no longer accepts), the empty push still goes.
	payload, err := l.payload(ctx, d, c, subject)
	if err != nil {
		payload = nil
	}
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	authorization, err := l.Key.Authorization(endpoint, subject, l.now())
	if err != nil {
		return 0, 0, fetch.ErrRefused
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return 0, 0, fetch.ErrRefused
	}
	if payload != nil {
		req.Header.Set("Content-Encoding", "aes128gcm")
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("TTL", "43200")
	req.Header.Set("Urgency", "normal")
	req.Header.Set("Topic", "edition")
	resp, err := client.Do(req)
	if errors.Is(err, fetch.ErrRefused) {
		return 0, 0, fetch.ErrRefused
	}
	if err != nil {
		return 0, 0, fetch.ErrRequest
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	var retryAfter time.Duration
	if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
		retryAfter = time.Duration(min(seconds, 3600)) * time.Second
	}
	return resp.StatusCode, retryAfter, nil
}
