package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/irashack/night-desk/internal/webhook"
)

// DeliverWebhooks sends two valid wake-ups followed by a corrupted signature
// and an authentic replay with an hour-old timestamp. The fixture key is also
// the webhook secret. Redirects are refused and response bodies are bounded.
func (s *Server) DeliverWebhooks(ctx context.Context, target string) {
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	u, err := url.Parse(target)
	valid := err == nil && u.Hostname() != "" && u.User == nil && u.Fragment == "" && (u.Scheme == "http" || u.Scheme == "https")
	now := time.Now().UTC()
	for i, name := range []string{"draft.ready", "review.requested", "bad_signature_rejected", "stale_timestamp_rejected"} {
		eventType := webhook.EventDraftReady
		if i == 1 {
			eventType = webhook.EventReviewRequested
		}
		at := now
		if i == 3 {
			at = now.Add(-time.Hour)
		}
		id := "conformance-draft-ready"
		if i == 1 {
			id = "conformance-review-requested"
		}
		if i == 2 {
			id = "conformance-bad-signature"
		}
		// The replay reuses the first delivery ID and event with a valid stale signature.
		body, _ := json.Marshal(webhook.Event{ID: id, Type: eventType, Paper: "conformance", At: at, Data: json.RawMessage(`{"date":"2026-10-30","revision":1}`)})
		timestamp := at.Unix()
		passed := false
		detail := "delivery failed"
		if valid {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
			if err == nil {
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Night-Desk-Event", eventType)
				req.Header.Set("Night-Desk-Delivery", id)
				req.Header.Set("Night-Desk-Timestamp", strconv.FormatInt(timestamp, 10))
				signature := webhook.Sign([]byte(s.key), timestamp, body)
				if i == 2 {
					last := byte('0')
					if signature[len(signature)-1] == last {
						last = '1'
					}
					signature = signature[:len(signature)-1] + string(last)
				}
				req.Header.Set("Night-Desk-Signature-256", signature)
				resp, err := client.Do(req)
				if err == nil {
					_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
					resp.Body.Close()
					passed = resp.StatusCode >= 200 && resp.StatusCode < 300
					if i >= 2 {
						passed = resp.StatusCode >= 400 && resp.StatusCode < 500
					}
					detail = "receiver returned HTTP " + strconv.Itoa(resp.StatusCode)
				}
			}
		}
		s.mu.Lock()
		s.check(name, passed, detail)
		s.mu.Unlock()
	}
	s.mu.Lock()
	s.terminal = true
	s.mu.Unlock()
}
