package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/lean"
)

func TestRatingsImport(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("good.csv", "outlet,domain,lean,factuality\nExample News,news.example,Lean Left,high\nWire,wire.example,center\n")
	bad := write("bad.csv", "Example News,news.example,sideways\nOther,not a domain,center\n")
	out := filepath.Join(dir, "ratings.yaml")
	source := []string{"--name", "Example Ratings", "--url", "https://ratings.example", "--license", "CC BY 4.0", "--attribution", "Ratings by Example"}
	for _, tt := range []struct {
		name   string
		args   []string
		code   int
		output string
	}{
		{"imports", append(append([]string{"ratings", "import"}, source...), "--out", out, good), 0, "Wrote 2 outlet ratings."},
		{"refuses overwrite", append(append([]string{"ratings", "import"}, source...), "--out", out, good), 1, "use --force"},
		{"force", append(append([]string{"ratings", "import"}, source...), "--out", out, "--force", good), 0, "Wrote 2"},
		{"problems", append(append([]string{"ratings", "import"}, source...), "--out", filepath.Join(dir, "x.yaml"), bad), 1, "ratings[0].lean: must be one of"},
		{"missing source", []string{"ratings", "import", "--out", filepath.Join(dir, "y.yaml"), good}, 1, "source"},
		{"no out", append(append([]string{"ratings", "import"}, source...), good), 2, "usage:"},
		{"no subcommand", []string{"ratings"}, 2, "usage:"},
		{"missing csv", append(append([]string{"ratings", "import"}, source...), "--out", filepath.Join(dir, "z.yaml"), filepath.Join(dir, "none.csv")), 1, "ratings CSV unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr)
			if all := stdout.String() + stderr.String(); code != tt.code || !strings.Contains(all, tt.output) {
				t.Fatalf("code %d, output %q", code, all)
			}
		})
	}
	for _, name := range []string{"x.yaml", "y.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s written despite problems", name)
		}
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ratings, problems, err := lean.Load(f)
	if err != nil || len(problems) > 0 || len(ratings.Ratings) != 2 || ratings.Ratings[0].Lean != lean.LeanLeft || ratings.Source.Attribution != "Ratings by Example" {
		t.Fatalf("written file must load: %+v %v %v", ratings, problems, err)
	}
}

func TestPromptsListAndReset(t *testing.T) {
	dir := t.TempDir()
	prompts := filepath.Join(dir, "prompts")
	if err := os.Mkdir(prompts, 0o700); err != nil {
		t.Fatal(err)
	}
	override := filepath.Join(prompts, "same-story.md")
	if err := os.WriteFile(override, []byte("Group stories about one event.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(prompts, "notes.md")
	if err := os.WriteFile(other, []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "worker.yaml")
	if err := os.WriteFile(cfg, []byte("prompts:\n  directory: prompts\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(dir, "bare.yaml")
	if err := os.WriteFile(bare, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		args   []string
		code   int
		output string
	}{
		{"list", []string{"prompts", "list", "--worker-config", cfg}, 0, "same-story\toverride-"},
		{"list default", []string{"prompts", "list", "--worker-config", cfg}, 0, "section-fit\t"},
		{"reset", []string{"prompts", "reset", "--worker-config", cfg, "same-story"}, 0, "Reset same-story to the default"},
		{"reset again", []string{"prompts", "reset", "--worker-config", cfg, "same-story"}, 0, "already uses the default"},
		{"unknown", []string{"prompts", "reset", "--worker-config", cfg, "notes"}, 1, `unknown prompt "notes"`},
		{"traversal", []string{"prompts", "reset", "--worker-config", cfg, "../worker"}, 1, "unknown prompt"},
		{"no directory", []string{"prompts", "reset", "--worker-config", bare, "same-story"}, 0, "No prompts.directory"},
		{"no name", []string{"prompts", "reset", "--worker-config", cfg}, 2, "usage:"},
		{"no config", []string{"prompts", "list"}, 2, "usage:"},
		{"bad subcommand", []string{"prompts", "edit"}, 2, "usage:"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr)
			if all := stdout.String() + stderr.String(); code != tt.code || !strings.Contains(all, tt.output) {
				t.Fatalf("code %d, output %q", code, all)
			}
		})
	}
	if _, err := os.Stat(override); !os.IsNotExist(err) {
		t.Error("override must be removed")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("only <name>.md for a known prompt may be removed")
	}
	var stdout bytes.Buffer
	run([]string{"prompts", "list", "--worker-config", cfg}, &stdout, &bytes.Buffer{})
	if strings.Contains(stdout.String(), "overridden") {
		t.Errorf("after reset every prompt is default: %s", stdout.String())
	}
}
