// Package webhook sends signed outbound events in the style of GitHub's
// webhooks: an event name, a delivery id, a timestamp and an HMAC-SHA256
// signature over "<timestamp>.<body>". A receiver checks the timestamp against
// a tolerance so a captured request cannot be replayed later.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Event types sent by Night Desk.
const (
	EventDraftReady       = "draft.ready"
	EventReviewRequested  = "review.requested"
	EventEditionPublished = "edition.published"
	EventFeedbackNew      = "feedback.new"
)

// Event is one outbound wake-up. Data is the event-specific JSON payload.
type Event struct {
	ID    string          `json:"id"`
	Type  string          `json:"type"`
	Paper string          `json:"paper"`
	At    time.Time       `json:"at"`
	Data  json.RawMessage `json:"data"`
}

// Sign returns "sha256=" plus the hex HMAC-SHA256 of "<timestamp>.<body>".
func Sign(secret []byte, timestamp int64, body []byte) string {
	return "sha256=" + hex.EncodeToString(mac(secret, timestamp, body))
}

func mac(secret []byte, timestamp int64, body []byte) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(strconv.FormatInt(timestamp, 10)))
	h.Write([]byte{'.'})
	h.Write(body)
	return h.Sum(nil)
}

// Verify checks a comma-separated signature header, accepting any one active
// secret so a receiver can rotate secrets without dropping deliveries. It
// rejects a timestamp further than tolerance from now. A tolerance of zero or
// less disables the timestamp check (the caller can then verify the signature
// alone).
func Verify(secret []byte, header string, timestamp int64, body []byte, now time.Time, tolerance time.Duration) error {
	if tolerance > 0 {
		delta := now.Sub(time.Unix(timestamp, 0))
		if delta < 0 {
			delta = -delta
		}
		if delta > tolerance {
			return errors.New("webhook: timestamp outside tolerance")
		}
	}
	header = strings.TrimSpace(header)
	if header == "" {
		return errors.New("webhook: missing signature")
	}
	expected := mac(secret, timestamp, body)
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if len(candidate) < len("sha256=") || !strings.EqualFold(candidate[:len("sha256=")], "sha256=") {
			continue
		}
		got, err := hex.DecodeString(candidate[len("sha256="):])
		if err != nil {
			continue
		}
		if hmac.Equal(got, expected) {
			return nil
		}
	}
	return errors.New("webhook: signature mismatch")
}

// Sender delivers events. Secrets returns the paper's active secrets, current
// first; several are kept during rotation. A nil Client uses http.DefaultClient.
type Sender struct {
	// Now supplies timestamps for deterministic delivery tests.
	Now         func() time.Time
	Client      *http.Client
	Secrets     func(paper string) [][]byte
	MaxAttempts int
	Backoff     func(attempt int) time.Duration
}

const (
	eventHeader      = "Night-Desk-Event"
	deliveryHeader   = "Night-Desk-Delivery"
	timestampHeader  = "Night-Desk-Timestamp"
	signatureHeader  = "Night-Desk-Signature-256"
	maxResponseDrain = 64 << 10
)

type permanentError struct {
	message string
}

func (e *permanentError) Error() string {
	return e.message
}

// Deliver POSTs e as JSON to url. It retries network errors, 5xx and 429 with
// backoff, treats any other non-2xx answer as permanent, and succeeds on 2xx.
// The request is signed on every attempt with a fresh timestamp and the secrets
// active at that moment.
func (s *Sender) Deliver(ctx context.Context, url string, e Event) error {
	if s == nil {
		return errors.New("webhook: nil sender")
	}
	if s.Secrets == nil {
		return errors.New("webhook: no secret source")
	}
	body, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("webhook: encode event: %w", err)
	}
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	attempts := s.MaxAttempts
	if attempts <= 0 {
		attempts = 3
	}
	backoff := s.Backoff
	if backoff == nil {
		backoff = defaultBackoff
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		_, retry, err := s.send(ctx, client, url, e, body)
		lastErr = err
		if lastErr == nil {
			return nil
		}
		if !retry {
			return lastErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt == attempts {
			break
		}
		delay := backoff(attempt)
		if delay <= 0 {
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

// Send makes one signed attempt. The durable outbox owns retry scheduling.
// Status is zero for transport/setup errors; retry is false for permanent failures.
func (s *Sender) Send(ctx context.Context, url string, e Event) (status int, retry bool, err error) {
	if s == nil || s.Secrets == nil {
		return 0, false, errors.New("webhook: no secret source")
	}
	body, err := json.Marshal(e)
	if err != nil {
		return 0, false, errors.New("webhook: invalid event")
	}
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	return s.send(ctx, client, url, e, body)
}
func (s *Sender) send(ctx context.Context, client *http.Client, url string, e Event, body []byte) (int, bool, error) {
	at := time.Now()
	if s.Now != nil {
		at = s.Now()
	}
	timestamp := at.Unix()
	signatures, err := s.signatures(e.Paper, timestamp, body)
	if err != nil {
		return 0, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, false, errors.New("webhook: invalid delivery URL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(eventHeader, e.Type)
	req.Header.Set(deliveryHeader, e.ID)
	req.Header.Set(timestampHeader, strconv.FormatInt(timestamp, 10))
	req.Header.Set(signatureHeader, signatures)
	resp, err := client.Do(req)
	if err != nil {
		return 0, true, errors.New("webhook: delivery failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseDrain))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, false, nil
	}
	retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 && resp.StatusCode < 600
	return resp.StatusCode, retry, fmt.Errorf("webhook: delivery failed with status %d", resp.StatusCode)
}

func (s *Sender) signatures(paper string, timestamp int64, body []byte) (string, error) {
	secrets := s.Secrets(paper)
	if len(secrets) == 0 {
		return "", &permanentError{message: "webhook: no active secrets"}
	}
	signatures := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if len(secret) == 0 {
			continue
		}
		signatures = append(signatures, Sign(secret, timestamp, body))
	}
	if len(signatures) == 0 {
		return "", &permanentError{message: "webhook: no active secrets"}
	}
	return strings.Join(signatures, ","), nil
}

func defaultBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 5 {
		attempt = 5
	}
	return time.Duration(1<<uint(attempt-1)) * time.Second
}
