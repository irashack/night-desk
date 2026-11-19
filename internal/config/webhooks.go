package config

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/irashack/night-desk/internal/webhook"
)

// Webhook is operator-owned configuration. Resolved secrets are never serialized.
type Webhook struct {
	URL        string   `json:"url"`
	SecretFile string   `json:"secret_file"`
	Events     []string `json:"events,omitempty"`
	secrets    [][]byte
}

// String keeps resolved secret bytes out of structured log fallbacks.
func (w Webhook) String() string   { return "webhook configuration (secrets redacted)" }
func (w Webhook) GoString() string { return w.String() }

func (w Webhook) Accepts(event string) bool {
	if w.Events == nil {
		return true
	}
	for _, e := range w.Events {
		if e == event {
			return true
		}
	}
	return false
}

// Secrets returns copies, current first, so callers cannot mutate a snapshot.
func (w Webhook) Secrets() [][]byte {
	out := make([][]byte, len(w.secrets))
	for i, s := range w.secrets {
		out[i] = append([]byte(nil), s...)
	}
	return out
}

func validWebhookEvent(event string) bool {
	switch event {
	case webhook.EventDraftReady, webhook.EventReviewRequested, webhook.EventEditionPublished, webhook.EventFeedbackNew:
		return true
	}
	return false
}

func checkWebhooks(add func(string, string), path string, hooks []Webhook) {
	seen := map[string]bool{}
	for i, w := range hooks {
		p := fmt.Sprintf("%s[%d]", path, i)
		u, err := url.Parse(w.URL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || strings.TrimSpace(w.URL) != w.URL {
			add(p+".url", "must be an absolute https URL without credentials or fragment")
		} else if u.Port() != "" {
			port, err := strconv.Atoi(u.Port())
			if err != nil || port < 1 || port > 65535 {
				add(p+".url", "port must be 1..65535")
			}
		}
		if seen[w.URL] {
			add(p+".url", "must be unique per paper")
		}
		seen[w.URL] = true
		if !filepath.IsAbs(w.SecretFile) || strings.TrimSpace(w.SecretFile) != w.SecretFile || strings.ContainsFunc(w.SecretFile, unicode.IsControl) {
			add(p+".secret_file", "must be an absolute file path")
		}
		events := map[string]bool{}
		if w.Events != nil && len(w.Events) == 0 {
			add(p+".events", "must contain at least one event when provided")
		}
		for j, e := range w.Events {
			if !validWebhookEvent(e) || events[e] {
				add(fmt.Sprintf("%s.events[%d]", p, j), "must be a unique event: draft.ready, review.requested, edition.published or feedback.new")
			}
			events[e] = true
		}
	}
}

func (c *Config) loadWebhookSecrets() error {
	for i := range c.Papers {
		for j := range c.Papers[i].Webhooks {
			w := &c.Papers[i].Webhooks[j]
			bad := func() error {
				return fmt.Errorf("papers[%d].webhooks[%d].secret_file: must contain non-empty UTF-8 secret lines without control characters (at most 64 KiB)", i, j)
			}
			// Bound reads even for a mistakenly configured special file.
			f, err := os.Open(w.SecretFile)
			if err != nil {
				return fmt.Errorf("papers[%d].webhooks[%d].secret_file: could not read secret file", i, j)
			}
			b, err := readWebhookSecrets(f)
			f.Close()
			if err != nil || len(b) == 0 || !utf8.Valid(b) {
				return bad()
			}
			lines := strings.Split(strings.TrimSuffix(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n"), "\n")
			for _, line := range lines {
				if line == "" || line != strings.TrimSpace(line) || strings.ContainsFunc(line, unicode.IsControl) {
					return bad()
				}
				w.secrets = append(w.secrets, []byte(line))
			}
		}
	}
	return nil
}

func readWebhookSecrets(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, (64<<10)+1))
	if len(b) > 64<<10 {
		return nil, fmt.Errorf("secret file too large")
	}
	return b, err
}
