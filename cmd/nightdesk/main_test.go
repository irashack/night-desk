package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	valid := "../../testdata/valid/minimal.json"
	daily := "../../testdata/valid/daily.json"
	dir := t.TempDir()
	warning := filepath.Join(dir, "warning.json")
	b, err := os.ReadFile(valid)
	if err != nil {
		t.Fatal(err)
	}
	b = bytes.Replace(b, []byte(`"sections": [`), []byte(`"sections": [{"key":"health","items":[]},`), 1)
	if err := os.WriteFile(warning, b, 0600); err != nil {
		t.Fatal(err)
	}
	httpFile := filepath.Join(dir, "http.json")
	if err := os.WriteFile(httpFile, bytes.ReplaceAll(b, []byte("https://"), []byte("http://")), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		args   []string
		code   int
		output string
	}{
		{"valid", []string{"validate", valid}, 0, ""},
		{"daily", []string{"validate", daily}, 0, ""},
		{"configured", []string{"validate", valid, "--sections", "economy:required,health"}, 0, ""},
		{"warning", []string{"validate", warning, "--sections", "economy,health"}, 0, "sections[1].key: should follow"},
		{"error", []string{"validate", httpFile}, 1, "sections[1].items[0].url: must be https"},
		{"missing required", []string{"validate", valid, "--sections", "economy,health:required"}, 1, `required section "health" must be present`},
		{"unknown section", []string{"validate", valid, "--sections", "health"}, 1, "must be a paper section key"},
		{"decode error", []string{"validate", "../../testdata/invalid/unknown-field.json"}, 1, ": unknown field"},
		{"missing file", []string{"validate", filepath.Join(dir, "missing.json")}, 1, "missing.json"},
		{"no command", nil, 2, "usage: nightdesk <subcommand>"},
		{"help", []string{"help"}, 0, "Subcommands:"},
		{"-h", []string{"-h"}, 0, "Subcommands:"},
		{"--help", []string{"--help"}, 0, "Subcommands:"},
		{"version", []string{"version"}, 0, "nightdesk "},
		{"--version", []string{"--version"}, 0, "nightdesk "},
		{"version extra", []string{"version", "x"}, 2, "usage: nightdesk version"},
		{"unknown command", []string{"other"}, 2, "unknown subcommand"},
		{"no file", []string{"validate"}, 2, "usage:"},
		{"extra args", []string{"validate", valid, "extra"}, 2, "usage:"},
		{"bad flag", []string{"validate", valid, "--other", "economy"}, 2, "expected --sections"},
		{"empty sections", []string{"validate", valid, "--sections", ""}, 2, "sections must be unique"},
		{"duplicate sections", []string{"validate", valid, "--sections", "economy,economy"}, 2, "sections must be unique"},
		{"bad modifier", []string{"validate", valid, "--sections", "economy:optional"}, 2, "sections must be unique"},
		{"untrimmed key", []string{"validate", valid, "--sections", " economy"}, 2, "sections must be unique"},
		{"serve", []string{"serve"}, 2, "usage:"},
		{"migrate", []string{"migrate"}, 1, "configuration failed"},
		{"key", []string{"key", "issue", "paper"}, 1, "configuration failed"},
		{"backup", []string{"backup", "dir"}, 1, "configuration failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := run(tt.args, &out, &errOut)
			all := out.String() + errOut.String()
			if code != tt.code {
				t.Fatalf("exit=%d want=%d output=%s", code, tt.code, all)
			}
			if tt.output == "" && all != "" || !strings.Contains(all, tt.output) {
				t.Fatalf("output=%q want %q", all, tt.output)
			}
		})
	}
}

func TestUsageListsEveryCommand(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"help"}, &out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("help exit=%d", code)
	}
	for _, c := range commands {
		if c.summary == "" || !strings.Contains(out.String(), "  "+c.name+" ") {
			t.Fatalf("usage misses %q:\n%s", c.name, out.String())
		}
	}
}

func TestVersionFromLdflags(t *testing.T) {
	old := version
	t.Cleanup(func() { version = old })
	version = "v9.8.7"
	var out bytes.Buffer
	if code := run([]string{"version"}, &out, &bytes.Buffer{}); code != 0 || out.String() != "nightdesk v9.8.7\n" {
		t.Fatalf("exit=%d output=%q", code, out.String())
	}
}
