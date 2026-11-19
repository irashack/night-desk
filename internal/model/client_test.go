package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type answer struct {
	OK bool `json:"ok"`
}

func completion(w http.ResponseWriter, content string) {
	json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}, "finish_reason": "stop"}}, "usage": Usage{10, 5, 15}})
}
func clientFor(t *testing.T, h http.HandlerFunc, o Options) *Client {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	c, e := NewClient(TierConfig{BaseURL: s.URL + "/v1", Model: "test-model"}, o)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func request() Request {
	return Request{Messages: []Message{{Role: "user", Content: "Return JSON."}}, MaxTokens: 100}
}
func TestRetriesAndUsage(t *testing.T) {
	for _, status := range []int{429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			attempts := 0
			c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
				attempts++
				if r.URL.Path != "/v1/chat/completions" {
					t.Errorf("path %s", r.URL.Path)
				}
				var body struct {
					Model          string `json:"model"`
					ResponseFormat struct {
						Type string `json:"type"`
					} `json:"response_format"`
					MaxTokens int `json:"max_tokens"`
				}
				if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
					t.Error(e)
				}
				if body.Model != "test-model" || body.ResponseFormat.Type != "json_object" || body.MaxTokens != 100 {
					t.Errorf("body %+v", body)
				}
				if attempts < 3 {
					w.WriteHeader(status)
					w.Write([]byte("sensitive provider failure"))
					return
				}
				completion(w, `{"ok":true}`)
			}, Options{MaxRetries: 2, Backoff: time.Millisecond})
			var got answer
			u, e := c.Complete(context.Background(), request(), &got)
			if e != nil || !got.OK || u != (Usage{10, 5, 15}) || attempts != 3 {
				t.Fatalf("got=%+v usage=%+v err=%v attempts=%d", got, u, e, attempts)
			}
		})
	}
}
func TestRetryLimitsAndNoRetryOnOtherErrors(t *testing.T) {
	for _, status := range []int{400, 401, 403, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				w.Write([]byte("secret-response-text"))
			}, Options{MaxRetries: 2, Backoff: time.Millisecond})
			var dst answer
			_, e := c.Complete(context.Background(), request(), &dst)
			want := 1
			if status == 429 || status >= 500 {
				want = 3
			}
			if e == nil || strings.Contains(e.Error(), "secret-response-text") || calls != want {
				t.Fatalf("calls=%d err=%v", calls, e)
			}
		})
	}
}
func TestSharedRunBudget(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); completion(w, `{"ok":true}`) }))
	defer s.Close()
	tier := TierConfig{BaseURL: s.URL, Model: "test"}
	run, e := NewRun(Config{tier, tier}, Limits{MaxCalls: 2, MaxTokens: 10000}, Options{})
	if e != nil {
		t.Fatal(e)
	}
	var a answer
	for _, c := range []Completer{run.Fast, run.Writer} {
		if _, e = c.Complete(context.Background(), request(), &a); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = run.Fast.Complete(context.Background(), request(), &a); !errors.Is(e, ErrBudget) {
		t.Fatal(e)
	}
	if calls.Load() != 2 || run.Budget.Spending() != (Spending{Calls: 2, Tokens: 30}) {
		t.Fatalf("calls=%d spending=%+v", calls.Load(), run.Budget.Spending())
	}
}
func TestTokenReservationAndRetryBudget(t *testing.T) {
	for _, tc := range []struct {
		name      string
		limit     Limits
		status    int
		wantCalls int
	}{{"token ceiling", Limits{10, 100}, 200, 0}, {"retry call ceiling", Limits{1, 10000}, 503, 1}, {"retry token ceiling", Limits{10, 300}, 503, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			b, e := NewBudget(tc.limit)
			if e != nil {
				t.Fatal(e)
			}
			c := clientFor(t, func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(tc.status) }, Options{Budget: b, MaxRetries: 2, Backoff: time.Millisecond})
			var a answer
			_, e = c.Complete(context.Background(), request(), &a)
			if !errors.Is(e, ErrBudget) || calls != tc.wantCalls {
				t.Fatalf("calls=%d err=%v", calls, e)
			}
		})
	}
}
func TestConcurrentBudget(t *testing.T) {
	b, _ := NewBudget(Limits{MaxCalls: 3, MaxTokens: 100000})
	var calls atomic.Int32
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); completion(w, `{"ok":true}`) }, Options{Budget: b})
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var a answer
			_, e := c.Complete(context.Background(), request(), &a)
			if e != nil && !errors.Is(e, ErrBudget) {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 3 {
		t.Fatal(calls.Load())
	}
}
func TestJSONValidationAndAtomicDestination(t *testing.T) {
	tests := []struct{ content, path, rule string }{{`{"ok":true,"extra":"secret"}`, "extra", "unknown field"}, {`{}`, "ok", "is required"}, {`{"ok":null}`, "ok", "must not be null"}, {`{"OK":true}`, "ok", "is required"}, {`{"ok":"yes"}`, "ok", "must be a boolean"}, {`{"ok":false,"ok":true}`, "ok", "must not be repeated"}, {`null`, "$", "must not be null"}, {`{"ok":true} {}`, "$", "must contain exactly one JSON object"}, {"```json\n{}\n```", "$", "must be valid JSON"}}
	for _, tc := range tests {
		t.Run(tc.content, func(t *testing.T) {
			c := clientFor(t, func(w http.ResponseWriter, r *http.Request) { completion(w, tc.content) }, Options{})
			dst := answer{true}
			_, e := c.Complete(context.Background(), request(), &dst)
			var p Problem
			if !errors.As(e, &p) || p.Path != tc.path || p.Rule != tc.rule || !dst.OK {
				t.Fatalf("dst=%+v err=%v", dst, e)
			}
		})
	}
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) { completion(w, `{"ok":false}`) }, Options{})
	dst := answer{true}
	r := request()
	r.Validate = func() []Problem {
		if !dst.OK {
			return []Problem{{"ok", "must be true"}}
		}
		return nil
	}
	_, e := c.Complete(context.Background(), r, &dst)
	var v *ValidationError
	if !errors.As(e, &v) || !dst.OK || v.Problems[0] != (Problem{"ok", "must be true"}) {
		t.Fatalf("dst=%+v err=%v", dst, e)
	}
}
func TestTimeoutAndCancelBackoff(t *testing.T) {
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}, Options{Timeout: 10 * time.Millisecond})
	var dst answer
	if _, e := c.Complete(context.Background(), request(), &dst); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	c = clientFor(t, func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "10"); w.WriteHeader(429) }, Options{Timeout: 10 * time.Millisecond, MaxRetries: 2})
	start := time.Now()
	if _, e := c.Complete(context.Background(), request(), &dst); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	if time.Since(start) > time.Second {
		t.Fatal("backoff ignored deadline")
	}
}
func TestKeyFileAndRedirectSafety(t *testing.T) {
	key := "test-api-key-private"
	file := filepath.Join(t.TempDir(), "key")
	if e := os.WriteFile(file, []byte(key+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	calls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; t.Error("followed redirect") }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			t.Error("missing auth")
		}
		http.Redirect(w, r, target.URL, 307)
	}))
	defer server.Close()
	c, e := NewClient(TierConfig{BaseURL: server.URL, Model: "test", KeyFile: file}, Options{})
	if e != nil {
		t.Fatal(e)
	}
	var dst answer
	_, e = c.Complete(context.Background(), request(), &dst)
	if e == nil || calls != 0 || strings.Contains(e.Error(), key) {
		t.Fatalf("calls=%d err=%v", calls, e)
	}
	if _, e = NewClient(TierConfig{BaseURL: server.URL, Model: "test", KeyFile: filepath.Join(t.TempDir(), "missing")}, Options{}); e == nil || e.Error() != "key_file: must be readable" {
		t.Fatal(e)
	}
}
func spanAttributes(attrs []attribute.KeyValue) map[string]attribute.Value {
	m := map[string]attribute.Value{}
	for _, a := range attrs {
		m[string(a.Key)] = a.Value
	}
	return m
}
func TestTraceContentAndPrivateRedaction(t *testing.T) {
	for _, record := range []bool{false, true} {
		for _, private := range []bool{false, true} {
			t.Run(fmt.Sprintf("record=%t/private=%t", record, private), func(t *testing.T) {
				exporter := tracetest.NewInMemoryExporter()
				provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
				defer provider.Shutdown(context.Background())
				key := "test-key-never-traced"
				secret := "reader private medical notes"
				f := filepath.Join(t.TempDir(), "key")
				os.WriteFile(f, []byte(key), 0600)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { completion(w, `{"ok":true}`) }))
				defer server.Close()
				c, e := NewClient(TierConfig{BaseURL: server.URL, Model: "test-model", KeyFile: f}, Options{Tracer: provider.Tracer("test"), RecordContent: record})
				if e != nil {
					t.Fatal(e)
				}
				r := request()
				r.PromptName = "test-prompt"
				r.PromptVersion = "v1"
				r.Messages = append(r.Messages, Message{Role: "user", Content: secret, Private: private}, Message{Role: "user", Content: "Do not print " + key})
				var dst answer
				if _, e = c.Complete(context.Background(), r, &dst); e != nil {
					t.Fatal(e)
				}
				spans := exporter.GetSpans()
				if len(spans) != 1 {
					t.Fatal(len(spans))
				}
				attrs := spanAttributes(spans[0].Attributes)
				if attrs["gen_ai.operation.name"].AsString() != "chat" || attrs["gen_ai.request.model"].AsString() != "test-model" || attrs["gen_ai.usage.input_tokens"].AsInt64() != 10 || attrs["gen_ai.usage.output_tokens"].AsInt64() != 5 || strings.Join(attrs["gen_ai.response.finish_reasons"].AsStringSlice(), ",") != "stop" {
					t.Fatalf("attributes: %v", attrs)
				}
				encoded, _ := json.Marshal(spans)
				if strings.Contains(string(encoded), key) || private && strings.Contains(string(encoded), secret) {
					t.Fatal("secret present in trace")
				}
				_, input := attrs["nd_input_messages"]
				_, output := attrs["nd_output_content"]
				if input != record || output != record {
					t.Fatalf("content attrs input=%t output=%t", input, output)
				}
				if record && private && attrs["nd_output_content"].AsString() != "[REDACTED]" {
					t.Fatal("private completion not suppressed")
				}
			})
		}
	}
}
func TestProviderEchoRedacted(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer provider.Shutdown(context.Background())
	note := "private-reader-note"
	c := clientFor(t, func(w http.ResponseWriter, r *http.Request) { completion(w, `{"ok":true,"leaked":"`+note+`"}`) }, Options{Tracer: provider.Tracer("test"), RecordContent: true})
	r := request()
	r.Messages = append(r.Messages, Message{Role: "user", Content: note, Private: true})
	var dst answer
	_, e := c.Complete(context.Background(), r, &dst)
	if e == nil {
		t.Fatal("expected strict JSON error")
	}
	raw, _ := json.Marshal(exporter.GetSpans())
	if strings.Contains(string(raw), note) || strings.Contains(e.Error(), note) {
		t.Fatal("private echo leaked")
	}
}
func TestTracingDisabledAndOTLPExport(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://invalid.example")
	p, shutdown, e := NewTracing(context.Background(), TracingConfig{})
	if e != nil {
		t.Fatal(e)
	}
	_, span := p.Tracer("test").Start(context.Background(), "no-op")
	if span.IsRecording() {
		t.Fatal("recording when disabled")
	}
	span.End()
	if e = shutdown(context.Background()); e != nil {
		t.Fatal(e)
	}
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/v1/traces" || r.Header.Get("Content-Type") != "application/x-protobuf" {
			t.Errorf("path/content type %s %s", r.URL.Path, r.Header.Get("Content-Type"))
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer s.Close()
	p, shutdown, e = NewTracing(context.Background(), TracingConfig{OTLPEndpoint: s.URL + "/v1/traces"})
	if e != nil {
		t.Fatal(e)
	}
	_, span = p.Tracer("test").Start(context.Background(), "export")
	span.End()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e = shutdown(ctx); e != nil {
		t.Fatal(e)
	}
	if requests.Load() != 1 {
		t.Fatal(requests.Load())
	}
	for _, endpoint := range []string{"ftp://example.org", "https://user:pass@example.org", "https://example.org/?key=secret", "bad"} {
		if _, _, e = NewTracing(context.Background(), TracingConfig{OTLPEndpoint: endpoint}); e == nil {
			t.Fatal("accepted invalid endpoint")
		}
	}
}

func TestPrivateAndCredentialFieldNames(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		for _, private := range []bool{false, true} {
			t.Run(fmt.Sprintf("duplicate=%t/private=%t", duplicate, private), func(t *testing.T) {
				key := "test-key-must-stay-private"
				note := "actual-reader-note"
				field := key
				if private {
					field = note
				}
				content := `{"ok":true,` + quoted(field) + `:1}`
				if duplicate {
					content = `{"ok":true,` + quoted(field) + `:1,` + quoted(field) + `:2}`
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { completion(w, content) }))
				defer server.Close()
				file := filepath.Join(t.TempDir(), "key")
				if e := os.WriteFile(file, []byte(key), 0600); e != nil {
					t.Fatal(e)
				}
				c, e := NewClient(TierConfig{BaseURL: server.URL, Model: "test", KeyFile: file}, Options{})
				if e != nil {
					t.Fatal(e)
				}
				r := request()
				if private {
					r.Messages = append(r.Messages, Message{Role: "user", Content: "BEGIN PRIVATE_NOTES\n" + note + "\nEND PRIVATE_NOTES", Private: true})
				}
				var dst answer
				_, e = c.Complete(context.Background(), r, &dst)
				if e == nil || strings.Contains(e.Error(), key) || strings.Contains(e.Error(), note) {
					t.Fatalf("unsafe error: %v", e)
				}
			})
		}
	}
}
func TestKnownPaths(t *testing.T) {
	type item struct {
		Title string `json:"title"`
	}
	type output struct {
		Items []item `json:"items"`
	}
	typ := reflect.TypeFor[output]()
	for path, want := range map[string]bool{"$": true, "items": true, "items[0]": true, "items[12].title": true, "items[0].private": false, "private": false, "items[x].title": false, "items[0].title.private": false, "items.": false} {
		if got := knownPath(typ, path); got != want {
			t.Errorf("%s=%t want %t", path, got, want)
		}
	}
}

func TestMissingAndExcessiveProviderUsage(t *testing.T) {
	for _, tc := range []struct {
		name, usage string
		want        Spending
		wantErr     bool
	}{
		{"absent", "", Spending{Calls: 1, Tokens: 192}, false},
		{"zero", `,"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`, Spending{Calls: 1, Tokens: 192}, false},
		{"over ceiling", `,"usage":{"prompt_tokens":299,"completion_tokens":2,"total_tokens":301}`, Spending{Calls: 1, Tokens: 301}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			budget, _ := NewBudget(Limits{MaxCalls: 2, MaxTokens: 300})
			calls := 0
			c := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"stop"}]`+tc.usage+`}`)
			}, Options{Budget: budget})
			dst := answer{}
			_, e := c.Complete(context.Background(), request(), &dst)
			if errors.Is(e, ErrBudget) != tc.wantErr || budget.Spending() != tc.want || dst.OK == tc.wantErr {
				t.Fatalf("dst=%+v spending=%+v err=%v", dst, budget.Spending(), e)
			}
			if _, e = c.Complete(context.Background(), request(), &dst); !errors.Is(e, ErrBudget) || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, e)
			}
		})
	}
}
func TestInvalidProviderEnvelopes(t *testing.T) {
	for _, body := range []string{`invalid`, `{"choices":[]}`, `{"choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"length"}]}`, `{"choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"private-echo"}]}`, `{"usage":{"prompt_tokens":-1}}`} {
		t.Run(body, func(t *testing.T) {
			c := clientFor(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }, Options{})
			dst := answer{true}
			_, e := c.Complete(context.Background(), request(), &dst)
			if e == nil || !dst.OK || strings.Contains(e.Error(), "private-echo") {
				t.Fatalf("dst=%+v err=%v", dst, e)
			}
		})
	}
}
