package config

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Role and Policy also supply defaults for programmatically constructed papers.
func (p Paper) Role() string {
	if p.AgentRole == "" {
		return "author"
	}
	return p.AgentRole
}
func (p Paper) Policy() string {
	if p.DeadlinePolicy == "" {
		return "hold"
	}
	return p.DeadlinePolicy
}

// ReaderNotesProblems uses the same plain-text rules as editorial guidance.
func ReaderNotesProblems(text string) []string {
	// Reader notes have their own, larger character limit.
	var out []string
	checkText(func(_, rule string) { out = append(out, "text: "+rule) }, "text", text)
	if htmlTag.MatchString(text) || strings.Contains(text, "](") {
		out = append(out, "text: must be plain text (no HTML tags or Markdown links)")
	}
	if utf8.RuneCountInString(text) > 4000 {
		out = append(out, "text: must be at most 4000 characters")
	}
	return out
}

const DefaultDraftLead = "90m"
const draftLeadRule = "must be a positive duration at most 24h"

// DraftLeadTime supplies the default for papers constructed outside Load too.
func (p Paper) DraftLeadTime() (time.Duration, error) {
	value := p.DraftLead
	if value == "" {
		value = DefaultDraftLead
	}
	lead, err := time.ParseDuration(value)
	if err != nil || lead <= 0 || lead > 24*time.Hour {
		return 0, fmt.Errorf("draft_lead: %s", draftLeadRule)
	}
	return lead, nil
}
