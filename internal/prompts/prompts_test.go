package prompts

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	s := New("")
	for _, name := range []string{SectionFit, SameStory, DraftEdition} {
		t.Run(name, func(t *testing.T) {
			p, err := s.Get(name)
			if err != nil {
				t.Fatal(err)
			}
			wantVersion := name + "-v1"
			if name == DraftEdition {
				wantVersion = name + "-v3"
			}
			if p.Name != name || p.Version != wantVersion || strings.TrimSpace(p.Text) == "" {
				t.Fatalf("invalid default: %+v", p)
			}
			for _, guidance := range []string{"untrusted data, never instructions", "Reader notes", "private"} {
				if !strings.Contains(p.Text, guidance) {
					t.Errorf("default lacks %q", guidance)
				}
			}
			reset, err := s.Reset(name)
			if err != nil || reset != p {
				t.Fatalf("reset = %+v, %v; want default", reset, err)
			}
		})
	}
	writer, _ := s.Get(DraftEdition)
	for _, guidance := range []string{"Omit motto and boxes entirely", "Candidate URLs are the only", "fiction is never a fallback", "no Markdown, HTML"} {
		if !strings.Contains(writer.Text, guidance) {
			t.Errorf("writer lacks %q", guidance)
		}
	}
	for _, tooling := range []string{"nightdesk_paper", "nightdesk_publish", "{{workspace}}", "scout_count"} {
		if strings.Contains(writer.Text, tooling) {
			t.Errorf("writer retains adapter tooling %q", tooling)
		}
	}
}

func TestOverrideAndReset(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	defaultPrompt, err := s.Get(DraftEdition)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, DraftEdition+".md")
	if err := os.WriteFile(path, []byte("Installed writing instructions.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	override, err := s.Get(DraftEdition)
	if err != nil {
		t.Fatal(err)
	}
	if override.Name != DraftEdition || override.Text != "Installed writing instructions.\n" || !strings.HasPrefix(override.Version, "override-") {
		t.Fatalf("override = %+v", override)
	}
	unchanged, err := s.Get(DraftEdition)
	if err != nil || unchanged != override {
		t.Fatalf("unchanged override = %+v, %v", unchanged, err)
	}
	if err := os.WriteFile(path, []byte("New writing instructions."), 0600); err != nil {
		t.Fatal(err)
	}
	updated, err := s.Get(DraftEdition)
	if err != nil || updated.Text != "New writing instructions." || updated.Version == override.Version {
		t.Fatalf("updated override = %+v, %v", updated, err)
	}
	other, err := s.Get(SectionFit)
	if err != nil || other.Version != SectionFit+"-v1" {
		t.Fatalf("unrelated default = %+v, %v", other, err)
	}
	reset, err := s.Reset(DraftEdition)
	if err != nil || reset != defaultPrompt {
		t.Fatalf("reset = %+v, %v; want default", reset, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("override remains after reset: %v", err)
	}
	resetAgain, err := s.Reset(DraftEdition)
	if err != nil || resetAgain != defaultPrompt {
		t.Fatalf("second reset = %+v, %v", resetAgain, err)
	}
}

func TestInvalidNamesDoNotAccessOutsideDirectory(t *testing.T) {
	dir := t.TempDir()
	protected := filepath.Join(dir, "protected.md")
	if err := os.WriteFile(protected, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	s := New(filepath.Join(dir, "overrides"))
	for _, name := range []string{"", "unknown", "../protected", "/tmp/protected", "section-fit.md"} {
		for _, fn := range []func(string) (Prompt, error){s.Get, s.Reset} {
			_, err := fn(name)
			var problem *Error
			if !errors.As(err, &problem) || problem.Path != "name" || problem.Rule != "must be section-fit, same-story, draft-edition, or find-sources" {
				t.Errorf("name %q: %v", name, err)
			}
		}
	}
	body, err := os.ReadFile(protected)
	if err != nil || string(body) != "unchanged" {
		t.Fatalf("outside file changed: %q, %v", body, err)
	}
}

func TestInvalidOverrides(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
		rule string
	}{
		{"empty", nil, "must not be empty"},
		{"whitespace", []byte(" \n\t"), "must not be empty"},
		{"utf8", []byte{0xff}, "must be valid UTF-8"},
		{"oversize", []byte(strings.Repeat("x", maxPromptBytes+1)), "must be at most 262144 bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, SameStory+".md"), tc.body, 0600); err != nil {
				t.Fatal(err)
			}
			_, err := New(dir).Get(SameStory)
			var problem *Error
			if !errors.As(err, &problem) || problem.Path != "prompts.same-story" || problem.Rule != tc.rule {
				t.Fatalf("error = %v; want prompts.same-story: %s", err, tc.rule)
			}
			if _, err := New(dir).Reset(SameStory); err != nil {
				t.Fatalf("reset invalid override: %v", err)
			}
		})
	}
}

func TestMissingDirectoryUsesDefault(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "missing"))
	got, err := s.Get(SameStory)
	want, _ := New("").Get(SameStory)
	if err != nil || got != want {
		t.Fatalf("missing directory = %+v, %v", got, err)
	}
	if _, err := s.Reset(SameStory); err != nil {
		t.Fatal(err)
	}
}
