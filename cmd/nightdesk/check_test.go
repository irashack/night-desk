package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckWorkerConfigCost(t *testing.T) {
	dir := t.TempDir()
	if code := run([]string{"init", "--dir", dir}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatal("init failed")
	}
	cfg := filepath.Join(dir, "config.yaml")
	b, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b = readyConfig(b)
	b = bytes.Replace(b, []byte(`  - id: "daily"`), []byte("  - id: \"daily\"\n    agent_role: reviewer\n    deadline: \"06:00\""), 1)
	if err := os.WriteFile(cfg, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "anthropic-key"), []byte("sk-ant-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	priced := write("priced.yaml", `models:
  fast: {provider: anthropic, model: claude-haiku-5-5, key_file: anthropic-key, price: {input_per_mtok: 0.10, output_per_mtok: 0.50}}
  writer: {provider: anthropic, model: claude-opus-5-5, key_file: anthropic-key, effort: medium, price: {input_per_mtok: 4, output_per_mtok: 20}}
`)
	unpriced := write("unpriced.yaml", `models:
  fast: {provider: anthropic, model: claude-haiku-5-5, key_file: anthropic-key}
  writer: {provider: anthropic, model: claude-opus-5-5, key_file: anthropic-key}
`)
	badPrice := write("bad.yaml", `models:
  fast: {provider: anthropic, model: claude-haiku-5-5, key_file: anthropic-key, price: {input_per_mtok: -1, output_per_mtok: 0.5}}
  writer: {provider: anthropic, model: claude-opus-5-5, key_file: anthropic-key}
`)
	for _, tt := range []struct {
		name   string
		args   []string
		code   int
		output string
	}{
		{"server only", []string{"check", "--config", cfg}, 0, "Configuration valid."},
		// 1 paper x 31 days x 100000 tokens = 3.1M tokens: $62.00 at $20, $22.32 at 0.8x$4 + 0.2x$20.
		{"priced", []string{"check", "--config", cfg, "--worker-config", priced}, 0, "Drafting 1 paper: at most $62.00 a month (31 runs of 100000 tokens each, every token at the dearest configured price); about $22.32"},
		{"unpriced", []string{"check", "--config", cfg, "--worker-config", unpriced}, 0, "Set models.fast.price and models.writer.price"},
		{"bad price", []string{"check", "--config", cfg, "--worker-config", badPrice}, 1, "models.fast.price.input_per_mtok: must be between 0 and 10000"},
		{"missing worker config", []string{"check", "--config", cfg, "--worker-config", filepath.Join(dir, "none.yaml")}, 1, "worker_config: must be readable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := run(tt.args, &out, &errOut)
			if all := out.String() + errOut.String(); code != tt.code || !strings.Contains(all, tt.output) {
				t.Fatalf("code %d, output %q", code, all)
			}
		})
	}
}

// readyConfig fills in the placeholders `init` leaves in its template.
func readyConfig(template []byte) []byte {
	return []byte(strings.NewReplacer(
		"paper.example.invalid", "paper.example.com",
		"identity.example.invalid", "identity.example.com",
		"REPLACE_WITH_OIDC_CLIENT_ID", "nightdesk",
		"readers: []", `readers: [{issuer: "https://identity.example.com", subject: "reader-1"}]`,
	).Replace(string(template)))
}

func TestCheckReportsPlaceholders(t *testing.T) {
	dir := t.TempDir()
	if code := run([]string{"init", "--dir", dir}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatal("init failed")
	}
	cfg := filepath.Join(dir, "config.yaml")
	template, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"check", "--config", cfg}, &out, &errOut); code != 1 || out.Len() != 0 {
		t.Fatalf("unedited template: exit=%d stdout=%q", code, out.String())
	}
	want := "Configuration valid, but not ready to serve:\n" +
		"  server.public_base_url: replace the placeholder host paper.example.invalid\n" +
		"  server.oidc.issuer: replace the placeholder host identity.example.invalid\n" +
		"  server.oidc.client_id: replace REPLACE_WITH_OIDC_CLIENT_ID with your provider's client ID\n" +
		"  papers[0].readers: add at least one verified reader identity\n"
	if errOut.String() != want {
		t.Fatalf("stderr=%q want %q", errOut.String(), want)
	}

	// Each placeholder alone keeps the configuration from being ready.
	ready := string(readyConfig(template))
	for _, tt := range []struct{ name, from, to, problem string }{
		{"public url", "paper.example.com", "paper.example.invalid", "server.public_base_url"},
		{"issuer", "issuer: https://identity.example.com", "issuer: https://identity.example.invalid", "server.oidc.issuer"},
		{"client id", "client_id: nightdesk", "client_id: REPLACE_WITH_OIDC_CLIENT_ID", "server.oidc.client_id"},
		{"reader issuer", `{issuer: "https://identity.example.com"`, `{issuer: "https://idp.example.invalid"`, "papers[0].readers[0].issuer"},
		{"no readers", `readers: [{issuer: "https://identity.example.com", subject: "reader-1"}]`, "readers: []", "papers[0].readers"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(ready, tt.from) {
				t.Fatalf("ready config lacks %q", tt.from)
			}
			if err := os.WriteFile(cfg, []byte(strings.Replace(ready, tt.from, tt.to, 1)), 0o600); err != nil {
				t.Fatal(err)
			}
			var out, errOut bytes.Buffer
			code := run([]string{"check", "--config", cfg}, &out, &errOut)
			if code != 1 || strings.Count(errOut.String(), "\n  ") != 1 || !strings.Contains(errOut.String(), "\n  "+tt.problem+": ") {
				t.Fatalf("exit=%d stderr=%q", code, errOut.String())
			}
		})
	}
	if err := os.WriteFile(cfg, []byte(ready), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if code := run([]string{"check", "--config", cfg}, &out, &errOut); code != 0 || out.String() != "Configuration valid.\n" || errOut.Len() != 0 {
		t.Fatalf("ready config: exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}
