package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/testhttp"
)

func TestSignVectors(t *testing.T) {
	secret := []byte("It's a Secret to Everybody")
	for _, tt := range []struct {
		body []byte
		want string
	}{
		{[]byte(`{"hello":"world"}`), "sha256=08c0aaa4721d7e090415dc782eb1818362b0e857612ef36e6dcf8c773571b03b"},
		{[]byte{}, "sha256=98c32e0b3aa090e576d675650c120eb14f45b36f0a3286ec1167119d239106e4"},
		{[]byte("line1\nline2"), "sha256=0b3d50ca227bd4695ab8e29dfbe0502441eba92fca09003588edacaf4a6d41bd"},
	} {
		if got := Sign(secret, 1700000000, tt.body); got != tt.want {
			t.Errorf("Sign(%q) = %q, want %q", tt.body, got, tt.want)
		}
	}
}

func TestVerify(t *testing.T) {
	secret := []byte("current")
	old := []byte("old")
	body := []byte(`{"type":"draft.ready"}`)
	timestamp := int64(1700000000)
	now := time.Unix(timestamp, 0)
	currentSig := Sign(secret, timestamp, body)
	oldSig := Sign(old, timestamp, body)
	header := currentSig + ", " + oldSig

	if err := Verify(secret, currentSig, timestamp, body, now, time.Minute); err != nil {
		t.Fatalf("current secret: %v", err)
	}
	if err := Verify(old, header, timestamp, body, now, time.Minute); err != nil {
		t.Fatalf("rotated secret in list: %v", err)
	}
	if err := Verify(secret, header, timestamp, body, now, time.Minute); err != nil {
		t.Fatalf("current secret in list: %v", err)
	}
	if err := Verify([]byte("other"), header, timestamp, body, now, time.Minute); err == nil {
		t.Fatal("wrong secret verified")
	}
	if err := Verify(secret, currentSig, timestamp, append(body, '!'), now, time.Minute); err == nil {
		t.Fatal("changed body verified")
	}
	if err := Verify(secret, "", timestamp, body, now, time.Minute); err == nil {
		t.Fatal("missing signature verified")
	}
	if err := Verify(secret, "sha256=not-hex", timestamp, body, now, time.Minute); err == nil {
		t.Fatal("invalid hex verified")
	}
	if err := Verify(secret, "md5=abc", timestamp, body, now, time.Minute); err == nil {
		t.Fatal("wrong algorithm verified")
	}

	// Replay: the signature is genuine but the timestamp is too old.
	if err := Verify(secret, currentSig, timestamp, body, now.Add(10*time.Minute), time.Minute); err == nil {
		t.Fatal("stale timestamp verified")
	}
	if err := Verify(secret, currentSig, timestamp, body, now.Add(-10*time.Minute), time.Minute); err == nil {
		t.Fatal("future timestamp verified")
	}
	if err := Verify(secret, currentSig, timestamp, body, now.Add(time.Minute), time.Minute); err != nil {
		t.Fatalf("timestamp at tolerance boundary: %v", err)
	}
	if err := Verify(secret, currentSig, timestamp, body, now.Add(10*time.Minute), 0); err != nil {
		t.Fatalf("zero tolerance should skip the time check: %v", err)
	}
}

func newClient(t *testing.T, handler http.HandlerFunc) (*http.Client, string) {
	t.Helper()
	network := testhttp.New()
	srv := network.Server(handler)
	t.Cleanup(srv.Close)
	return &http.Client{Transport: network.Transport()}, srv.URL
}

func testEvent() Event {
	return Event{
		ID:    "evt_123",
		Type:  EventDraftReady,
		Paper: "daily",
		At:    time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
		Data:  json.RawMessage(`{"draft":"2026-10-30"}`),
	}
}

func TestDeliverSuccessAndRotation(t *testing.T) {
	current := []byte("current-secret")
	old := []byte("old-secret")
	e := testEvent()
	var calls atomic.Int32
	client, hookURL := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content type = %q", got)
		}
		if got := r.Header.Get(eventHeader); got != e.Type {
			t.Errorf("event header = %q", got)
		}
		if got := r.Header.Get(deliveryHeader); got != e.ID {
			t.Errorf("delivery header = %q", got)
		}
		timestamp, err := parseTimestamp(r.Header.Get(timestampHeader))
		if err != nil {
			t.Error(err)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var delivered Event
		if err := json.Unmarshal(body, &delivered); err != nil {
			t.Error(err)
			return
		}
		if delivered.ID != e.ID || delivered.Type != e.Type || delivered.Paper != e.Paper || !delivered.At.Equal(e.At) || string(delivered.Data) != string(e.Data) {
			t.Errorf("delivered event = %#v", delivered)
		}
		header := r.Header.Get(signatureHeader)
		if strings.Count(header, "sha256=") != 2 {
			t.Errorf("signature header = %q", header)
		}
		for _, secret := range [][]byte{current, old} {
			if err := Verify(secret, header, timestamp, body, time.Now(), time.Minute); err != nil {
				t.Errorf("verify with active secret: %v", err)
			}
		}
		if err := Verify([]byte("revoked"), header, timestamp, body, time.Now(), time.Minute); err == nil {
			t.Error("revoked secret verified")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	s := &Sender{
		Client:      client,
		Secrets:     func(string) [][]byte { return [][]byte{current, old} },
		MaxAttempts: 1,
		Backoff:     func(int) time.Duration { return 0 },
	}
	if err := s.Deliver(context.Background(), hookURL, e); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestDeliverRetries(t *testing.T) {
	secret := []byte("secret")
	e := testEvent()
	for _, tt := range []struct {
		name       string
		statuses   []int
		max        int
		wantCalls  int32
		wantErr    bool
		wantDelays []int
	}{
		{"server error then success", []int{500, 200}, 3, 2, false, []int{1}},
		{"rate limit then success", []int{429, 200}, 3, 2, false, []int{1}},
		{"exhausted server errors", []int{500, 500, 500, 200}, 3, 3, true, []int{1, 2}},
		{"client error is permanent", []int{400, 200}, 3, 1, true, nil},
		{"unauthorized is permanent", []int{401, 200}, 3, 1, true, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			var mu sync.Mutex
			var delays []int
			client, hookURL := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				status := tt.statuses[len(tt.statuses)-1]
				if int(n) <= len(tt.statuses) {
					status = tt.statuses[n-1]
				}
				w.WriteHeader(status)
			})
			s := &Sender{
				Client:      client,
				Secrets:     func(string) [][]byte { return [][]byte{secret} },
				MaxAttempts: tt.max,
				Backoff: func(attempt int) time.Duration {
					mu.Lock()
					delays = append(delays, attempt)
					mu.Unlock()
					return 0
				},
			}
			err := s.Deliver(context.Background(), hookURL, e)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if calls.Load() != tt.wantCalls {
				t.Fatalf("calls = %d, want %d", calls.Load(), tt.wantCalls)
			}
			mu.Lock()
			defer mu.Unlock()
			if fmt.Sprint(delays) != fmt.Sprint(tt.wantDelays) {
				t.Fatalf("backoff attempts = %v, want %v", delays, tt.wantDelays)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDeliverNetworkRetry(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("dial failed")
	})}
	s := &Sender{
		Client:      client,
		Secrets:     func(string) [][]byte { return [][]byte{[]byte("secret")} },
		MaxAttempts: 3,
		Backoff:     func(int) time.Duration { return 0 },
	}
	if err := s.Deliver(context.Background(), "http://example.test/hook", testEvent()); err == nil {
		t.Fatal("network errors did not fail")
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", calls.Load())
	}
}

func TestDeliverContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	client, hookURL := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		cancel()
		w.WriteHeader(http.StatusInternalServerError)
	})
	s := &Sender{
		Client:      client,
		Secrets:     func(string) [][]byte { return [][]byte{[]byte("secret")} },
		MaxAttempts: 5,
		Backoff:     func(int) time.Duration { return time.Hour },
	}
	err := s.Deliver(ctx, hookURL, testEvent())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

func TestDeliverConfigurationErrors(t *testing.T) {
	e := testEvent()
	for _, tt := range []struct {
		name   string
		sender *Sender
		data   json.RawMessage
	}{
		{"nil sender", nil, e.Data},
		{"no secret source", &Sender{}, e.Data},
		{"no secrets", &Sender{Secrets: func(string) [][]byte { return nil }}, e.Data},
		{"empty secrets", &Sender{Secrets: func(string) [][]byte { return [][]byte{nil, {}} }}, e.Data},
		{"invalid data", &Sender{Secrets: func(string) [][]byte { return [][]byte{[]byte("secret")} }}, json.RawMessage(`{`)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			event := e
			event.Data = tt.data
			var calls atomic.Int32
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return nil, errors.New("should not be called")
			})}
			if tt.sender != nil {
				tt.sender.Client = client
			}
			var err error
			if tt.sender == nil {
				err = (*Sender)(nil).Deliver(context.Background(), "http://example.test/hook", event)
			} else {
				err = tt.sender.Deliver(context.Background(), "http://example.test/hook", event)
			}
			if err == nil {
				t.Fatal("Deliver succeeded")
			}
			if calls.Load() != 0 {
				t.Fatalf("made %d requests", calls.Load())
			}
		})
	}
}

func parseTimestamp(value string) (int64, error) {
	var timestamp int64
	_, err := fmt.Sscan(value, &timestamp)
	return timestamp, err
}
