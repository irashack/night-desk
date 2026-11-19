package model

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// AnthropicClient calls Claude through Anthropic's SDK. Output is constrained to
// the destination's schema (structured outputs), the system prompt is cached,
// and a refusal is a failed call, never draft text.
type AnthropicClient struct {
	sdk      anthropic.Client
	price    *Price
	model    string
	key      string
	effort   anthropic.BetaOutputConfigEffort
	fallback bool
	options  Options
}

const anthropicDefaultMaxTokens = 16000

func NewAnthropicClient(c TierConfig, o Options) (*AnthropicClient, error) {
	if c.Provider != "anthropic" {
		return nil, Problem{"provider", "must be anthropic for this client"}
	}
	base := "https://api.anthropic.com/"
	if c.BaseURL != "" {
		u, err := url.Parse(c.BaseURL)
		if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, Problem{"base_url", "must be an absolute http or https URL without userinfo, query or fragment"}
		}
		base = c.BaseURL
	}
	switch c.Effort {
	case "", "low", "medium", "high", "xhigh", "max":
	default:
		return nil, Problem{"effort", "must be low, medium, high, xhigh or max"}
	}
	if c.KeyFile == "" {
		return nil, Problem{"key_file", "is required for the anthropic provider"}
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
	// Every setting is explicit so ANTHROPIC_* environment variables never apply.
	sdk := anthropic.NewClient(option.WithAPIKey(key), option.WithBaseURL(base), option.WithHTTPClient(&hc), option.WithMaxRetries(0))
	return &AnthropicClient{sdk: sdk, price: c.Price, model: c.Model, key: key, effort: anthropic.BetaOutputConfigEffort(c.Effort), fallback: serverFallback(c.Model), options: o}, nil
}

// serverFallback reports whether Anthropic re-serves a refused request on
// another model for this one; Haiku has no server-side fallback.
func serverFallback(model string) bool {
	for _, prefix := range []string{"claude-opus-5", "claude-sonnet-5-5", "claude-fable-5"} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

func (c *AnthropicClient) Complete(ctx context.Context, r Request, dst any) (usage Usage, err error) {
	ctx, cancel := context.WithTimeout(ctx, c.options.Timeout)
	defer cancel()
	ctx, span := c.options.Tracer.Start(ctx, "chat "+redact(c.model, []string{c.key}), trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attribute.String("gen_ai.operation.name", "chat"), attribute.String("gen_ai.system", "anthropic"), attribute.String("gen_ai.request.model", redact(c.model, []string{c.key}))))
	defer func() {
		if err != nil {
			span.SetStatus(codes.Error, "model call failed")
		}
		span.End()
	}()
	if r.MaxTokens == 0 {
		// Thinking shares the output allowance, so drafting needs more room.
		r.MaxTokens = anthropicDefaultMaxTokens
	}
	t, reserved, private, secrets, err := begin(span, c.options, c.key, &r, dst)
	if err != nil {
		return usage, err
	}
	schema, err := OutputSchema(t.Elem())
	if err != nil {
		return usage, err
	}
	params := anthropic.BetaMessageNewParams{
		Model:        anthropic.Model(c.model),
		MaxTokens:    int64(r.MaxTokens),
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: c.effort, Format: anthropic.BetaJSONOutputFormatParam{Schema: schema}},
	}
	var system []string
	for i, m := range r.Messages {
		if m.Role == "system" {
			system = append(system, m.Content)
			continue
		}
		role := anthropic.BetaMessageParamRoleUser
		if m.Role == "assistant" {
			if i == len(r.Messages)-1 {
				return usage, Problem{fmt.Sprintf("messages[%d].role", i), "must not end the conversation as assistant"}
			}
			role = anthropic.BetaMessageParamRoleAssistant
		}
		// Consecutive messages of one role become one turn of several blocks.
		if n := len(params.Messages); n > 0 && params.Messages[n-1].Role == role {
			params.Messages[n-1].Content = append(params.Messages[n-1].Content, anthropic.NewBetaTextBlock(m.Content))
			continue
		}
		params.Messages = append(params.Messages, anthropic.BetaMessageParam{Role: role, Content: []anthropic.BetaContentBlockParamUnion{anthropic.NewBetaTextBlock(m.Content)}})
	}
	if len(params.Messages) == 0 {
		return usage, Problem{"messages", "must contain a user message"}
	}
	if len(system) > 0 {
		// The prompt is the stable prefix across papers and attempts; cache it.
		params.System = []anthropic.BetaTextBlockParam{{Text: strings.Join(system, "\n"), CacheControl: anthropic.NewBetaCacheControlEphemeralParam()}}
	}
	if c.fallback {
		params.Fallbacks = anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
	}
	for attempt := 0; attempt <= c.options.MaxRetries; attempt++ {
		if e := ctx.Err(); e != nil {
			return usage, e
		}
		if e := c.options.Budget.reserve(reserved); e != nil {
			return usage, e
		}
		span.SetAttributes(attribute.Int("nd_attempts", attempt+1))
		msg, e := c.sdk.Beta.Messages.New(ctx, params)
		if e != nil {
			if ctx.Err() != nil {
				return usage, ctx.Err()
			}
			var apiErr *anthropic.Error
			if !errors.As(e, &apiErr) {
				return usage, errors.New("model: HTTP request failed")
			}
			// Never propagate a provider error body: it may echo keys or prompts.
			status := apiErr.StatusCode
			if status != 429 && (status < 500 || status > 599) {
				return usage, fmt.Errorf("model: HTTP %d", status)
			}
			if attempt == c.options.MaxRetries {
				return usage, fmt.Errorf("model: HTTP %d after bounded retries", status)
			}
			delay := min(c.options.Backoff*time.Duration(1<<attempt), 10*time.Second)
			if apiErr.Response != nil {
				if seconds, e := strconv.Atoi(apiErr.Response.Header.Get("Retry-After")); e == nil && seconds > 0 {
					delay = max(delay, time.Duration(min(seconds, 10))*time.Second)
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
		u := msg.Usage
		input := int(u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens)
		usage = Usage{InputTokens: input, OutputTokens: int(u.OutputTokens), TotalTokens: input + int(u.OutputTokens)}
		if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.TotalTokens > 1000000000 {
			return Usage{}, errors.New("model: invalid token usage")
		}
		span.SetAttributes(attribute.Int("gen_ai.usage.input_tokens", usage.InputTokens), attribute.Int("gen_ai.usage.output_tokens", usage.OutputTokens), attribute.Int("gen_ai.usage.cache_read_input_tokens", int(u.CacheReadInputTokens)), attribute.String("gen_ai.response.model", redact(string(msg.Model), secrets)), attribute.StringSlice("gen_ai.response.finish_reasons", []string{string(msg.StopReason)}))
		c.options.Budget.addCost(c.price, usage.InputTokens, usage.OutputTokens)
		if usage.TotalTokens > 0 {
			if e := c.options.Budget.settle(reserved, usage.TotalTokens); e != nil {
				return usage, e
			}
		}
		switch msg.StopReason {
		case anthropic.BetaStopReasonEndTurn:
		case anthropic.BetaStopReasonRefusal:
			category := string(msg.StopDetails.Category)
			if category == "" {
				category = "unspecified"
			}
			span.SetAttributes(attribute.String("nd_refusal_category", category))
			return usage, fmt.Errorf("model: refused (%s)", category)
		default:
			return usage, errors.New("model: response did not finish normally")
		}
		var text strings.Builder
		for _, block := range msg.Content {
			if b, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
				text.WriteString(b.Text)
			}
		}
		return usage, finish(span, c.options, t, dst, r, text.String(), private, secrets)
	}
	return usage, errors.New("model: retries exhausted")
}
