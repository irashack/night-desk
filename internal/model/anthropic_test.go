package model

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/edition"
)

func claudeMessage(w http.ResponseWriter, stop, text string, extra map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	m := map[string]any{"id": "msg_test", "type": "message", "role": "assistant", "model": "claude-opus-5-5", "stop_reason": stop,
		"content": []any{map[string]any{"type": "thinking", "thinking": "", "signature": "sig"}, map[string]any{"type": "text", "text": text}},
		"usage":   map[string]any{"input_tokens": 10, "cache_creation_input_tokens": 3, "cache_read_input_tokens": 2, "output_tokens": 5}}
	for k, v := range extra {
		m[k] = v
	}
	json.NewEncoder(w).Encode(m)
}

func keyFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "key")
	if e := os.WriteFile(p, []byte("sk-ant-test\n"), 0o600); e != nil {
		t.Fatal(e)
	}
	return p
}

func claudeFor(t *testing.T, model, effort string, h http.HandlerFunc, o Options) *AnthropicClient {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	c, e := NewAnthropicClient(TierConfig{Provider: "anthropic", BaseURL: s.URL, Model: model, KeyFile: keyFile(t), Effort: effort}, o)
	if e != nil {
		t.Fatal(e)
	}
	return c
}

func TestAnthropicRequestShape(t *testing.T) {
	for _, tc := range []struct {
		model    string
		fallback bool
	}{{"claude-opus-5-5", true}, {"claude-haiku-5-5", false}} {
		t.Run(tc.model, func(t *testing.T) {
			c := claudeFor(t, tc.model, "medium", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/messages" {
					t.Errorf("path %s", r.URL.Path)
				}
				if r.Header.Get("X-Api-Key") != "sk-ant-test" {
					t.Error("missing API key")
				}
				if got := strings.Contains(r.Header.Get("Anthropic-Beta"), "server-side-fallback-2026-07-01"); got != tc.fallback {
					t.Errorf("fallback beta %v", got)
				}
				var body map[string]any
				if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
					t.Fatal(e)
				}
				if body["model"] != tc.model || body["max_tokens"] != float64(100) {
					t.Errorf("model or max_tokens: %v %v", body["model"], body["max_tokens"])
				}
				if _, ok := body["temperature"]; ok {
					t.Error("sampling parameters must not be sent")
				}
				if (body["fallbacks"] == "default") != tc.fallback {
					t.Errorf("fallbacks %v", body["fallbacks"])
				}
				system := body["system"].([]any)[0].(map[string]any)
				if system["text"] != "Rules one.\nRules two." || system["cache_control"].(map[string]any)["type"] != "ephemeral" {
					t.Errorf("system %v", system)
				}
				messages := body["messages"].([]any)
				if len(messages) != 1 || len(messages[0].(map[string]any)["content"].([]any)) != 2 {
					t.Errorf("consecutive user messages must merge into one turn: %v", messages)
				}
				config := body["output_config"].(map[string]any)
				format := config["format"].(map[string]any)
				if config["effort"] != "medium" || format["type"] != "json_schema" || format["schema"].(map[string]any)["additionalProperties"] != false {
					t.Errorf("output_config %v", config)
				}
				claudeMessage(w, "end_turn", `{"ok":true}`, nil)
			}, Options{})
			var a answer
			r := Request{Messages: []Message{{Role: "system", Content: "Rules one."}, {Role: "user", Content: "Data."}, {Role: "system", Content: "Rules two."}, {Role: "user", Content: "Notes.", Private: true}}, MaxTokens: 100}
			u, e := c.Complete(context.Background(), r, &a)
			if e != nil || !a.OK {
				t.Fatal(e, a)
			}
			if u != (Usage{InputTokens: 15, OutputTokens: 5, TotalTokens: 20}) {
				t.Errorf("usage counts cache tokens as input: %+v", u)
			}
		})
	}
}

func TestAnthropicDefaultMaxTokensAndBudget(t *testing.T) {
	b, _ := NewBudget(Limits{MaxCalls: 5, MaxTokens: 100000})
	c := claudeFor(t, "claude-opus-5-5", "", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["max_tokens"] != float64(anthropicDefaultMaxTokens) {
			t.Errorf("max_tokens %v", body["max_tokens"])
		}
		if _, ok := body["output_config"].(map[string]any)["effort"]; ok {
			t.Error("unset effort must use the model's default")
		}
		claudeMessage(w, "end_turn", `{"ok":true}`, nil)
	}, Options{Budget: b})
	var a answer
	if _, e := c.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "x"}}}, &a); e != nil {
		t.Fatal(e)
	}
	if s := b.Spending(); s.Calls != 1 || s.Tokens != 20 {
		t.Errorf("spending %+v", s)
	}
}

func TestAnthropicStopReasons(t *testing.T) {
	for _, tc := range []struct {
		stop, want string
		extra      map[string]any
	}{
		{"refusal", "model: refused (cyber)", map[string]any{"stop_details": map[string]any{"type": "refusal", "category": "cyber", "explanation": "x"}}},
		{"refusal", "model: refused (unspecified)", nil},
		{"max_tokens", "model: response did not finish normally", nil},
	} {
		c := claudeFor(t, "claude-opus-5-5", "", func(w http.ResponseWriter, r *http.Request) { claudeMessage(w, tc.stop, `{"ok":true}`, tc.extra) }, Options{})
		a := answer{}
		if _, e := c.Complete(context.Background(), request(), &a); e == nil || e.Error() != tc.want {
			t.Errorf("%s: %v", tc.stop, e)
		}
		if a.OK {
			t.Error("destination changed on a failed call")
		}
	}
}

func TestAnthropicRetriesAndErrors(t *testing.T) {
	attempts := 0
	b, _ := NewBudget(Limits{MaxCalls: 5, MaxTokens: 100000})
	c := claudeFor(t, "claude-opus-5-5", "", func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader([]int{529, 429}[attempts-1])
			w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"sk-ant-test echoed"}}`))
			return
		}
		claudeMessage(w, "end_turn", `{"ok":true}`, nil)
	}, Options{MaxRetries: 2, Backoff: 1, Budget: b})
	var a answer
	if _, e := c.Complete(context.Background(), request(), &a); e != nil || attempts != 3 {
		t.Fatal(e, attempts)
	}
	if b.Spending().Calls != 3 {
		t.Errorf("each attempt consumes the run budget: %+v", b.Spending())
	}
	attempts = 0
	c = claudeFor(t, "claude-opus-5-5", "", func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"sk-ant-test echoed"}}`))
	}, Options{MaxRetries: 2, Backoff: 1})
	_, e := c.Complete(context.Background(), request(), &a)
	if e == nil || e.Error() != "model: HTTP 400" || attempts != 1 {
		t.Fatal(e, attempts)
	}
}

func TestAnthropicStrictDecodeAndValidation(t *testing.T) {
	c := claudeFor(t, "claude-opus-5-5", "", func(w http.ResponseWriter, r *http.Request) {
		claudeMessage(w, "end_turn", `{"ok":true,"extra":1}`, nil)
	}, Options{})
	var a answer
	var p Problem
	if _, e := c.Complete(context.Background(), request(), &a); !errors.As(e, &p) || p.Path != "extra" {
		t.Fatal(e)
	}
	c = claudeFor(t, "claude-opus-5-5", "", func(w http.ResponseWriter, r *http.Request) { claudeMessage(w, "end_turn", `{"ok":true}`, nil) }, Options{})
	r := request()
	r.Validate = func() []Problem { return []Problem{{"$.ok", "must be false"}} }
	var v *ValidationError
	if _, e := c.Complete(context.Background(), r, &a); !errors.As(e, &v) || a.OK {
		t.Fatal(e, a)
	}
}

func TestAnthropicConfig(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:1")
	key := keyFile(t)
	for _, tc := range []struct {
		config TierConfig
		path   string
	}{
		{TierConfig{Provider: "anthropic", Model: "claude-opus-5-5"}, "key_file"},
		{TierConfig{Provider: "anthropic", Model: "claude-opus-5-5", KeyFile: key, Effort: "extreme"}, "effort"},
		{TierConfig{Provider: "anthropic", Model: "claude-opus-5-5", KeyFile: key, BaseURL: "ftp://x"}, "base_url"},
		{TierConfig{Provider: "anthropic", Model: " claude", KeyFile: key}, "model"},
		{TierConfig{Provider: "gemini", Model: "m"}, "provider"},
		{TierConfig{BaseURL: "http://localhost/v1", Model: "m", Effort: "low"}, "effort"},
	} {
		var p Problem
		if _, e := NewCompleter(tc.config, Options{}); !errors.As(e, &p) || p.Path != tc.path {
			t.Errorf("%+v: %v", tc.config, e)
		}
	}
	c, e := NewCompleter(TierConfig{Provider: "anthropic", Model: "claude-haiku-5-5", KeyFile: key}, Options{})
	if e != nil {
		t.Fatal(e)
	}
	if reflect.TypeOf(c) != reflect.TypeFor[*AnthropicClient]() {
		t.Fatalf("%T", c)
	}
}

func TestOutputSchema(t *testing.T) {
	s, err := OutputSchema(reflect.TypeFor[edition.Edition]())
	if err != nil {
		t.Fatal(err)
	}
	optional := 0
	var walk func(map[string]any)
	walk = func(n map[string]any) {
		switch n["type"] {
		case "object":
			if n["additionalProperties"] != false {
				t.Error("every object must reject unknown keys")
			}
			props := n["properties"].(map[string]any)
			optional += len(props) - len(n["required"].([]string))
			for _, p := range props {
				walk(p.(map[string]any))
			}
		case "array":
			walk(n["items"].(map[string]any))
		}
		for _, k := range []string{"minLength", "maxLength", "minimum", "maximum", "maxItems"} {
			if _, ok := n[k]; ok {
				t.Errorf("unsupported keyword %s", k)
			}
		}
	}
	walk(s)
	// Claude structured outputs allow at most 24 optional properties per request.
	if optional == 0 || optional > 24 {
		t.Errorf("edition schema has %d optional properties", optional)
	}
	type node struct {
		Next *node `json:"next,omitempty"`
	}
	if _, err := OutputSchema(reflect.TypeFor[node]()); err == nil {
		t.Error("recursive types must be refused")
	}
	if _, err := OutputSchema(reflect.TypeFor[struct {
		M map[string]string `json:"m"`
	}]()); err == nil {
		t.Error("maps must be refused, as DecodeJSON refuses them")
	}
}

func TestCostFromTierPrices(t *testing.T) {
	price := &Price{InputPerMTok: 4, OutputPerMTok: 20}
	b, _ := NewBudget(Limits{MaxCalls: 5, MaxTokens: 100000})
	c := claudeFor(t, "claude-opus-5-5", "", func(w http.ResponseWriter, r *http.Request) { claudeMessage(w, "end_turn", `{"ok":true}`, nil) }, Options{Budget: b})
	c.price = price
	var a answer
	if _, err := c.Complete(context.Background(), request(), &a); err != nil {
		t.Fatal(err)
	}
	// 15 input tokens (cache reads and writes at the base rate) x $4 plus 5 output x $20.
	if got := b.Spending().CostMicros; got != 160 {
		t.Errorf("anthropic cost %d micros, want 160", got)
	}
	o := clientFor(t, func(w http.ResponseWriter, r *http.Request) { completion(w, `{"ok":true}`) }, Options{Budget: b})
	o.price = price
	if _, err := o.Complete(context.Background(), request(), &a); err != nil {
		t.Fatal(err)
	}
	// 10 input x $4 plus 5 output x $20 = 140 more.
	if got := b.Spending().CostMicros; got != 300 {
		t.Errorf("total cost %d micros, want 300", got)
	}
	unpriced := clientFor(t, func(w http.ResponseWriter, r *http.Request) { completion(w, `{"ok":true}`) }, Options{Budget: b})
	if _, err := unpriced.Complete(context.Background(), request(), &a); err != nil || b.Spending().CostMicros != 300 {
		t.Error("a tier without a price adds no cost", err)
	}
}
