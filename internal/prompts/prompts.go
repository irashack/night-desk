// Package prompts provides versioned model instructions and per-install overrides.
package prompts

import (
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	SectionFit     = "section-fit"
	SameStory      = "same-story"
	DraftEdition   = "draft-edition"
	FindSources    = "find-sources"
	maxPromptBytes = 256 * 1024
)

// Names lists every prompt: drafting's, in the order it uses them, then source
// discovery's.
var Names = []string{SectionFit, SameStory, DraftEdition, FindSources}

//go:embed defaults/*.md
var defaults embed.FS

// Prompt identifies the exact instructions used for a model call. Override
// versions are content hashes, so editing an override changes its version.
type Prompt struct {
	Name    string
	Version string
	Text    string
}

// Error reports a prompt validation failure without including prompt content.
type Error struct {
	Path string `json:"path"`
	Rule string `json:"rule"`
}

func (e *Error) Error() string { return e.Path + ": " + e.Rule }

// Store reads optional <name>.md overrides from dir on each Get. An empty dir
// uses only embedded defaults. The operator owns the override directory.
type Store struct{ dir string }

func New(dir string) *Store { return &Store{dir: dir} }

// Get returns an installed override when present, otherwise the embedded
// default. Invalid overrides fail explicitly instead of silently reverting.
func (s *Store) Get(name string) (Prompt, error) {
	p, err := defaultPrompt(name)
	if err != nil || s.dir == "" {
		return p, err
	}
	f, err := os.Open(filepath.Join(s.dir, name+".md"))
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return Prompt{}, fmt.Errorf("prompt %s: %w", name, err)
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, maxPromptBytes+1))
	if err != nil {
		return Prompt{}, fmt.Errorf("prompt %s: %w", name, err)
	}
	path := "prompts." + name
	if len(body) > maxPromptBytes {
		return Prompt{}, &Error{Path: path, Rule: "must be at most 262144 bytes"}
	}
	if !utf8.Valid(body) {
		return Prompt{}, &Error{Path: path, Rule: "must be valid UTF-8"}
	}
	if strings.TrimSpace(string(body)) == "" {
		return Prompt{}, &Error{Path: path, Rule: "must not be empty"}
	}
	hash := sha256.Sum256(body)
	p.Version = fmt.Sprintf("override-%x", hash[:6])
	p.Text = string(body)
	return p, nil
}

// Reset removes an override and returns the embedded default. It is safe when
// no override exists; unknown names never access the filesystem.
func (s *Store) Reset(name string) (Prompt, error) {
	p, err := defaultPrompt(name)
	if err != nil || s.dir == "" {
		return p, err
	}
	if err := os.Remove(filepath.Join(s.dir, name+".md")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Prompt{}, fmt.Errorf("reset prompt %s: %w", name, err)
	}
	return p, nil
}

func defaultPrompt(name string) (Prompt, error) {
	switch name {
	case SectionFit, SameStory, DraftEdition, FindSources:
	default:
		return Prompt{}, &Error{Path: "name", Rule: "must be section-fit, same-story, draft-edition, or find-sources"}
	}
	body, err := defaults.ReadFile("defaults/" + name + ".md")
	if err != nil {
		return Prompt{}, fmt.Errorf("default prompt %s: %w", name, err)
	}
	version := name + "-v1"
	if name == DraftEdition {
		version = name + "-v3"
	}
	return Prompt{Name: name, Version: version, Text: string(body)}, nil
}
