// Package model calls OpenAI-compatible chat completions from isolated workers.
package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// TierConfig keys are install configuration, never reader-facing API fields.
// Provider is "openai" (the default: any OpenAI-compatible endpoint) or
// "anthropic" (Claude through Anthropic's SDK). For "openai", BaseURL includes
// the provider API version, e.g. http://localhost:11434/v1; for "anthropic" it
// is optional. KeyFile may be omitted for an unauthenticated local provider.
// Effort applies to "anthropic" only: low, medium, high, xhigh or max.
// Price is optional and only estimates cost; it never limits spending.
type TierConfig struct {
	Provider string `json:"provider,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
	Model    string `json:"model"`
	KeyFile  string `json:"key_file,omitempty"`
	Effort   string `json:"effort,omitempty"`
	Price    *Price `json:"price,omitempty"`
}

// Price is the provider's list price in US dollars per million tokens. Prices
// are configuration, not code, so they follow the provider without a release.
type Price struct {
	InputPerMTok  float64 `json:"input_per_mtok"`
	OutputPerMTok float64 `json:"output_per_mtok"`
}

// MonthlyCost bounds a month of drafting for papers that each spend at most the
// run budget once a day. Ceiling bills every token at the dearest configured
// price; typical bills the writer at 80% input and 20% output. ok is false
// unless both tiers have prices.
func MonthlyCost(c Config, l Limits, papers int) (ceiling, typical float64, ok bool) {
	if c.Fast.Price == nil || c.Writer.Price == nil {
		return 0, 0, false
	}
	tokens := float64(papers) * 31 * float64(l.MaxTokens) / 1e6
	dearest := max(c.Fast.Price.InputPerMTok, c.Fast.Price.OutputPerMTok, c.Writer.Price.InputPerMTok, c.Writer.Price.OutputPerMTok)
	w := c.Writer.Price
	return tokens * dearest, tokens * (0.8*w.InputPerMTok + 0.2*w.OutputPerMTok), true
}

type Config struct {
	Fast   TierConfig `json:"fast"`
	Writer TierConfig `json:"writer"`
}

// WorkerConfig can be embedded by the install configuration loader. Relative
// key_file paths must be resolved against its configuration directory by caller.
type WorkerConfig struct {
	Models  Config        `json:"models"`
	Tracing TracingConfig `json:"tracing,omitempty"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Private bool   `json:"-"`
}
type Request struct {
	Messages                  []Message
	MaxTokens                 int
	PromptName, PromptVersion string
	// Validate supplies semantic rules after strict structural decoding into dst.
	// It must return only paths and rules, with no reader or provider content.
	Validate func() []Problem
}
type Usage struct {
	InputTokens  int `json:"prompt_tokens"`
	OutputTokens int `json:"completion_tokens"`
	TotalTokens  int `json:"total_tokens"`
}
type Completer interface {
	Complete(context.Context, Request, any) (Usage, error)
}

type Options struct {
	HTTPClient    *http.Client
	Timeout       time.Duration
	MaxRetries    int // zero disables retries; each retry consumes the run budget
	Backoff       time.Duration
	Budget        *Budget
	Tracer        trace.Tracer // nil is always no-op, regardless of global OTel state
	RecordContent bool
}
type Client struct {
	price                *Price
	endpoint, model, key string
	http                 *http.Client
	options              Options
}

type Run struct {
	Fast, Writer Completer
	Budget       *Budget
}

// NewRun creates both tiers sharing a single spending guard. Create a fresh Run
// per scheduled paper/date; never reuse a guard across runs.
func NewRun(c Config, limits Limits, opts Options) (*Run, error) {
	budget, err := NewBudget(limits)
	if err != nil {
		return nil, err
	}
	opts.Budget = budget
	fast, err := NewCompleter(c.Fast, opts)
	if err != nil {
		return nil, err
	}
	writer, err := NewCompleter(c.Writer, opts)
	if err != nil {
		return nil, err
	}
	return &Run{fast, writer, budget}, nil
}

// NewCompleter returns the client for the tier's provider.
func NewCompleter(c TierConfig, o Options) (Completer, error) {
	switch c.Provider {
	case "", "openai":
		return NewClient(c, o)
	case "anthropic":
		return NewAnthropicClient(c, o)
	}
	return nil, Problem{"provider", "must be openai or anthropic"}
}

func NewClient(c TierConfig, o Options) (*Client, error) {
	if c.Provider != "" && c.Provider != "openai" {
		return nil, Problem{"provider", "must be openai for this client"}
	}
	if c.Effort != "" {
		return nil, Problem{"effort", "applies only to the anthropic provider"}
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, Problem{"base_url", "must be an absolute http or https URL without userinfo, query or fragment"}
	}
	o, key, err := prepare(c, o)
	if err != nil {
		return nil, err
	}
	hc := http.Client{}
	if o.HTTPClient != nil {
		hc = *o.HTTPClient
	}
	// No redirect may forward credentials or the private request body elsewhere.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	u.Path = strings.TrimRight(u.Path, "/") + "/chat/completions"
	return &Client{price: c.Price, endpoint: u.String(), model: c.Model, key: key, http: &hc, options: o}, nil
}

// prepare validates what every provider shares: the model name, call options
// and the key file. It fills option defaults and returns the key.
func prepare(c TierConfig, o Options) (Options, string, error) {
	if strings.TrimSpace(c.Model) == "" || strings.TrimSpace(c.Model) != c.Model || strings.ContainsAny(c.Model, "\r\n") {
		return o, "", Problem{"model", "must be trimmed non-empty text"}
	}
	if p := c.Price; p != nil {
		for _, f := range []struct {
			path  string
			value float64
		}{{"price.input_per_mtok", p.InputPerMTok}, {"price.output_per_mtok", p.OutputPerMTok}} {
			if !(f.value >= 0 && f.value <= 10000) {
				return o, "", Problem{f.path, "must be between 0 and 10000"}
			}
		}
	}
	if o.Timeout < 0 {
		return o, "", Problem{"timeout", "must be positive"}
	}
	if o.Timeout == 0 {
		o.Timeout = 60 * time.Second
	}
	if o.MaxRetries < 0 || o.MaxRetries > 5 {
		return o, "", Problem{"max_retries", "must be between 0 and 5"}
	}
	if o.Backoff < 0 {
		return o, "", Problem{"backoff", "must be positive"}
	}
	if o.Backoff == 0 {
		o.Backoff = 250 * time.Millisecond
	}
	if o.Backoff > 10*time.Second {
		return o, "", Problem{"backoff", "must be at most 10 seconds"}
	}
	if o.Budget == nil {
		o.Budget, _ = NewBudget(Limits{MaxCalls: 12, MaxTokens: 100000})
	}
	if o.Tracer == nil {
		o.Tracer = noop.NewTracerProvider().Tracer("github.com/irashack/night-desk/internal/model")
	}
	key := ""
	if c.KeyFile != "" {
		// Bound key-file reads; file errors deliberately omit paths and contents.
		f, e := os.Open(c.KeyFile)
		if e != nil {
			return o, "", Problem{"key_file", "must be readable"}
		}
		b, e := io.ReadAll(io.LimitReader(f, 8193))
		f.Close()
		if e != nil || len(b) > 8192 {
			return o, "", Problem{"key_file", "must contain at most 8192 bytes"}
		}
		key = strings.TrimSpace(string(b))
		if key == "" || strings.ContainsAny(key, "\r\n") {
			return o, "", Problem{"key_file", "must contain a non-empty single-line key"}
		}
	}
	return o, key, nil
}

func quoted(v string) string { b, _ := json.Marshal(v); return string(b) }

func (c *Client) Complete(ctx context.Context, r Request, dst any) (usage Usage, err error) {
	ctx, cancel := context.WithTimeout(ctx, c.options.Timeout)
	defer cancel()
	ctx, span := c.options.Tracer.Start(ctx, "chat "+redact(c.model, []string{c.key}), trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attribute.String("gen_ai.operation.name", "chat"), attribute.String("gen_ai.request.model", redact(c.model, []string{c.key}))))
	defer func() {
		if err != nil {
			span.SetStatus(codes.Error, "model call failed")
		}
		span.End()
	}()
	t, reserved, private, secrets, err := begin(span, c.options, c.key, &r, dst)
	if err != nil {
		return usage, err
	}
	payload := struct {
		Model          string    `json:"model"`
		Messages       []Message `json:"messages"`
		ResponseFormat any       `json:"response_format"`
		MaxTokens      int       `json:"max_tokens"`
	}{c.model, r.Messages, struct {
		Type string `json:"type"`
	}{"json_object"}, r.MaxTokens}
	body, _ := json.Marshal(payload)
	for attempt := 0; attempt <= c.options.MaxRetries; attempt++ {
		if e := ctx.Err(); e != nil {
			return usage, e
		}
		if e := c.options.Budget.reserve(reserved); e != nil {
			return usage, e
		}
		span.SetAttributes(attribute.Int("nd_attempts", attempt+1))
		req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
		if e != nil {
			return usage, errors.New("model: invalid request")
		}
		req.Header.Set("Content-Type", "application/json")
		if c.key != "" {
			req.Header.Set("Authorization", "Bearer "+c.key)
		}
		resp, e := c.http.Do(req)
		if e != nil {
			if ctx.Err() != nil {
				return usage, ctx.Err()
			}
			return usage, errors.New("model: HTTP request failed")
		}
		if resp.StatusCode == 429 || resp.StatusCode >= 500 && resp.StatusCode <= 599 {
			// Never propagate a provider error body: it may echo keys or prompts.
			io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if attempt == c.options.MaxRetries {
				return usage, fmt.Errorf("model: HTTP %d after bounded retries", resp.StatusCode)
			}
			delay := c.options.Backoff * time.Duration(1<<attempt)
			if delay > 10*time.Second {
				delay = 10 * time.Second
			}
			if seconds, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil && seconds > 0 {
				hint := time.Duration(min(seconds, 10)) * time.Second
				if hint > delay {
					delay = hint
				}
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return usage, ctx.Err()
			case <-timer.C:
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return usage, fmt.Errorf("model: HTTP %d", resp.StatusCode)
		}
		raw, e := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
		resp.Body.Close()
		if e != nil {
			return usage, errors.New("model: could not read response")
		}
		if len(raw) > 8<<20 {
			return usage, errors.New("model: response exceeds 8388608 bytes")
		}
		var result struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *Usage `json:"usage"`
		}
		if json.Unmarshal(raw, &result) != nil {
			return usage, errors.New("model: invalid response envelope")
		}
		if result.Usage != nil {
			usage = *result.Usage
			if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.TotalTokens < 0 || usage.InputTokens > 1000000000 || usage.OutputTokens > 1000000000 || usage.TotalTokens > 1000000000 {
				return Usage{}, errors.New("model: invalid token usage")
			}
			total := max(usage.TotalTokens, usage.InputTokens+usage.OutputTokens)
			usage.TotalTokens = total
			// Missing usage or an all-zero placeholder retains the conservative charge.
			span.SetAttributes(attribute.Int("gen_ai.usage.input_tokens", usage.InputTokens), attribute.Int("gen_ai.usage.output_tokens", usage.OutputTokens))
			c.options.Budget.addCost(c.price, usage.InputTokens, usage.OutputTokens)
			if total > 0 {
				if e := c.options.Budget.settle(reserved, total); e != nil {
					return usage, e
				}
			}
		}
		if len(result.Choices) != 1 {
			return usage, errors.New("model: expected exactly one response choice")
		}
		choice := result.Choices[0]
		switch choice.FinishReason {
		case "stop", "length", "content_filter", "tool_calls", "function_call":
			span.SetAttributes(attribute.StringSlice("gen_ai.response.finish_reasons", []string{choice.FinishReason}))
		default:
			return usage, errors.New("model: invalid finish reason")
		}
		if choice.FinishReason != "stop" {
			return usage, errors.New("model: response did not finish normally")
		}
		return usage, finish(span, c.options, t, dst, r, choice.Message.Content, private, secrets)
	}
	return usage, errors.New("model: retries exhausted")
}

// begin checks a request and its destination, records the request on the span
// and returns the tokens to reserve and the strings to redact.
func begin(span trace.Span, o Options, key string, r *Request, dst any) (t reflect.Type, reserved int, private bool, secrets []string, err error) {
	t = reflect.TypeOf(dst)
	if t == nil || t.Kind() != reflect.Pointer || reflect.ValueOf(dst).IsNil() || t.Elem().Kind() != reflect.Struct {
		return nil, 0, false, nil, Problem{"$", "destination must be a non-nil pointer to a struct"}
	}
	if len(r.Messages) == 0 {
		return nil, 0, false, nil, Problem{"messages", "must contain at least one message"}
	}
	if r.MaxTokens == 0 {
		r.MaxTokens = 4096
	}
	if r.MaxTokens < 1 || r.MaxTokens > 1000000 {
		return nil, 0, false, nil, Problem{"max_tokens", "must be between 1 and 1000000"}
	}
	reserved = 64 + r.MaxTokens
	secrets = []string{key}
	for i, m := range r.Messages {
		if m.Role != "system" && m.Role != "user" && m.Role != "assistant" {
			return nil, 0, false, nil, Problem{fmt.Sprintf("messages[%d].role", i), "must be system, user or assistant"}
		}
		if m.Content == "" {
			return nil, 0, false, nil, Problem{fmt.Sprintf("messages[%d].content", i), "must be non-empty"}
		}
		if len(m.Content) > 4<<20 {
			return nil, 0, false, nil, Problem{fmt.Sprintf("messages[%d].content", i), "must be at most 4194304 bytes"}
		}
		reserved += len(m.Content) + 16
		if m.Private {
			private = true
			secrets = append(secrets, m.Content)
		}
	}
	if reserved > 8<<20 {
		return nil, 0, false, nil, Problem{"messages", "request must be at most 8388608 reserved tokens"}
	}
	span.SetAttributes(attribute.String("nd_prompt_name", redact(r.PromptName, secrets)), attribute.String("nd_prompt_version", redact(r.PromptVersion, secrets)), attribute.Int("gen_ai.request.max_tokens", r.MaxTokens))
	if o.RecordContent {
		messages := make([]Message, len(r.Messages))
		copy(messages, r.Messages)
		for i, m := range messages {
			if m.Private {
				messages[i].Content = "[REDACTED]"
			} else {
				messages[i].Content = redact(m.Content, secrets)
			}
		}
		b, _ := json.Marshal(messages)
		span.SetAttributes(attribute.String("nd_input_messages", string(b)))
	}
	return t, reserved, private, secrets, nil
}

// finish strictly decodes a completion into dst, which changes only when the
// content decodes and passes the request's validation.
func finish(span trace.Span, o Options, t reflect.Type, dst any, r Request, content string, private bool, secrets []string) error {
	if o.RecordContent {
		recorded := "[REDACTED]"
		if !private {
			recorded = redact(content, secrets)
		}
		// A model can transform or paraphrase private material. With any private
		// input, suppress the whole completion rather than attempting substring PII.
		span.SetAttributes(attribute.String("nd_output_content", recorded))
	}
	// Decode into a temporary value so failed validation never mutates dst.
	tmp := reflect.New(t.Elem())
	if e := DecodeJSON([]byte(content), tmp.Interface()); e != nil {
		var problem Problem
		if errors.As(e, &problem) {
			if private && !knownPath(t.Elem(), problem.Path) {
				problem.Path = "$"
			}
			problem.Path = redact(problem.Path, secrets)
			problem.Rule = redact(problem.Rule, secrets)
			return problem
		}
		return errors.New("model: response JSON failed strict validation")
	}
	original := reflect.ValueOf(dst).Elem()
	saved := reflect.New(t.Elem()).Elem()
	saved.Set(original)
	original.Set(tmp.Elem())
	if r.Validate != nil {
		if problems := r.Validate(); len(problems) > 0 {
			original.Set(saved)
			return &ValidationError{problems}
		}
	}
	return nil
}
