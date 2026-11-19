package model

import (
	"context"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

type TracingConfig struct {
	OTLPEndpoint  string `json:"otlp_endpoint,omitempty"`
	RecordContent bool   `json:"record_content,omitempty"`
}

// NewTracing never consults OTEL environment variables when disabled and does not
// replace the process-global provider. Endpoint is the full HTTP traces URL,
// normally ending in /v1/traces. Call shutdown on worker exit to flush spans.
func NewTracing(ctx context.Context, c TracingConfig) (trace.TracerProvider, func(context.Context) error, error) {
	if c.OTLPEndpoint == "" {
		return noop.NewTracerProvider(), func(context.Context) error { return nil }, nil
	}
	u, err := url.Parse(c.OTLPEndpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, nil, Problem{"tracing.otlp_endpoint", "must be an absolute http or https URL without userinfo, query or fragment"}
	}
	// Explicit options prevent environment-supplied headers or compression from
	// adding credentials to traces. No prompt content is ever a resource attribute.
	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(c.OTLPEndpoint), otlptracehttp.WithHeaders(map[string]string{}), otlptracehttp.WithTimeout(10*time.Second))
	if err != nil {
		return nil, nil, Problem{"tracing.otlp_endpoint", "could not initialize exporter"}
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "nightdesk-worker"))))
	return provider, provider.Shutdown, nil
}
func redact(s string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[REDACTED]") // Also remove the escaped representation when content is JSON.
			if escaped := strings.Trim(quoted(secret), `"`); escaped != secret {
				s = strings.ReplaceAll(s, escaped, "[REDACTED]")
			}
		}
	}
	return s
}
