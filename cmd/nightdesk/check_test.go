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
