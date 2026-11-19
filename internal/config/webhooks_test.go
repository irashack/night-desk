package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebhookSecretsAndValidation(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "key")
	path := filepath.Join(dir, "config.yaml")
	const current = "current-hook-secret"
	const old = "previous-hook-secret"
	write(t, secret, current+"\n"+old+"\n")
	load := func(hook string) (*Config, error) {
		write(t, path, strings.Replace(valid, "  masthead:", "  webhooks: ["+hook+"]\n  masthead:", 1))
		return Load(path)
	}
	hook := fmt.Sprintf(`{url: https://agent.example/hooks, secret_file: %q}`, secret)
	c, err := load(hook)
	if err != nil {
		t.Fatal(err)
	}
	w := c.Papers[0].Webhooks[0]
	if !w.Accepts("draft.ready") || len(w.Secrets()) != 2 || string(w.Secrets()[0]) != current || string(w.Secrets()[1]) != old {
		t.Fatal("rotation or defaults missing")
	}
	copy := w.Secrets()
	copy[0][0] = 'X'
	if string(w.Secrets()[0]) != current {
		t.Fatal("mutable secrets")
	}
	b, _ := json.Marshal(c)
	var logs bytes.Buffer
	slog.New(slog.NewTextHandler(&logs, nil)).Info("config", "snapshot", c)
	for _, output := range []string{string(b), logs.String(), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c)} {
		if strings.Contains(output, current) || strings.Contains(output, old) {
			t.Fatal("secret disclosed")
		}
	}
	filtered := strings.TrimSuffix(hook, "}") + ", events: [feedback.new]}"
	c, err = load(filtered)
	if err != nil || c.Papers[0].Webhooks[0].Accepts("draft.ready") || !c.Papers[0].Webhooks[0].Accepts("feedback.new") {
		t.Fatal("event filter", err)
	}
	for _, tt := range []struct{ hook, path string }{
		{strings.Replace(hook, "https:", "http:", 1), "webhooks[0].url"},
		{strings.Replace(hook, "agent.example", "user:pass@agent.example", 1), "webhooks[0].url"},
		{strings.Replace(hook, "/hooks", "/hooks#fragment", 1), "webhooks[0].url"},
		{strings.Replace(hook, "agent.example", "agent.example:99999", 1), "webhooks[0].url"},
		{strings.Replace(hook, fmt.Sprintf("%q", secret), `relative-key`, 1), "webhooks[0].secret_file"},
		{strings.TrimSuffix(hook, "}") + ", events: []}", "webhooks[0].events"},
		{strings.TrimSuffix(hook, "}") + ", events: [draft.ready, draft.ready]}", "webhooks[0].events[1]"},
		{strings.TrimSuffix(hook, "}") + ", events: [invented]}", "webhooks[0].events[0]"},
		{strings.TrimSuffix(hook, "}") + ", events: null}", "webhooks[0].events"},
		{strings.TrimSuffix(hook, "}") + ", secrets: exposed}", "webhooks[0].secrets"},
		{hook + ", " + hook, "webhooks[1].url"},
	} {
		if _, err = load(tt.hook); err == nil || !strings.Contains(err.Error(), tt.path) {
			t.Errorf("%s: %v", tt.path, err)
		}
	}
	for _, content := range []string{"", "\n", "bad\x00secret", "line\n\nline", " padded ", string([]byte{255}), strings.Repeat("x", 65537)} {
		write(t, secret, content)
		if _, err = load(hook); err == nil || !strings.Contains(err.Error(), "papers[0].webhooks[0].secret_file:") || strings.Contains(err.Error(), "padded") {
			t.Fatal("invalid secret accepted or exposed", err)
		}
	}
	write(t, secret, "rotated\r\nprevious\r\n")
	c, err = load(hook)
	if err != nil || string(c.Papers[0].Webhooks[0].Secrets()[0]) != "rotated" {
		t.Fatal("CRLF rotation", err)
	}
}

func TestWebhookSecretReloadRetainsValidSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	secret := filepath.Join(dir, "secret")
	write(t, secret, "initial-secret\n")
	write(t, path, strings.Replace(valid, "  masthead:", fmt.Sprintf("  webhooks: [{url: https://agent.example/hook, secret_file: %q}]\n  masthead:", secret), 1))
	m, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	first := m.Current()
	write(t, secret, "")
	if err = m.Reload(true); err == nil || m.Current() != first {
		t.Fatal("invalid reload replaced snapshot", err)
	}
	write(t, secret, "rotated-secret\ninitial-secret\n")
	if err = m.Reload(true); err != nil {
		t.Fatal(err)
	}
	if m.Current() == first || string(m.Current().Papers[0].Webhooks[0].Secrets()[0]) != "rotated-secret" || string(first.Papers[0].Webhooks[0].Secrets()[0]) != "initial-secret" {
		t.Fatal("rotation mutated snapshots")
	}
}
