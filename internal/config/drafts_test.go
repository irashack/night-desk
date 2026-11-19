package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDraftOperatorConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	write(t, path, valid)
	c, err := Load(path)
	if err != nil || c.Papers[0].AgentRole != "author" || c.Papers[0].DeadlinePolicy != "hold" {
		t.Fatal(c, err)
	}
	for _, role := range []string{"author", "reviewer", "none"} {
		for _, policy := range []string{"hold", "publish-unreviewed"} {
			write(t, path, strings.Replace(valid, "  masthead:", "  agent_role: "+role+"\n  deadline_policy: "+policy+"\n  masthead:", 1))
			c, err = Load(path)
			if err != nil || c.Papers[0].Role() != role || c.Papers[0].Policy() != policy {
				t.Fatal(c, err)
			}
			ed := c.Papers[0].Editorial()
			ed.Masthead = "Changed"
			p := c.Papers[0].WithEditorial(ed)
			if p.Role() != role || p.Policy() != policy {
				t.Fatal("operator settings changed", p)
			}
		}
	}
	for _, tc := range []struct{ line, path string }{
		{"agent_role: writer", "agent_role"}, {"agent_role: ''", "agent_role"}, {"agent_role: null", "agent_role"}, {"agent_role: true", "agent_role"},
		{"deadline_policy: publish", "deadline_policy"}, {"deadline_policy: ''", "deadline_policy"}, {"deadline_policy: null", "deadline_policy"},
	} {
		write(t, path, strings.Replace(valid, "  masthead:", "  "+tc.line+"\n  masthead:", 1))
		if _, err = Load(path); err == nil || !strings.Contains(err.Error(), "papers[0]."+tc.path) {
			t.Fatal(tc, err)
		}
	}
}
func TestReaderNotesValidation(t *testing.T) {
	if got := ReaderNotesProblems(strings.Repeat("界", 4000)); len(got) != 0 {
		t.Fatal(got)
	}
	for _, text := range []string{"", " leading", "trailing ", "line\nbreak", "tab\ttext", "<b>markup</b>", "[link](https://example.com)", strings.Repeat("界", 4001)} {
		got := ReaderNotesProblems(text)
		if len(got) == 0 || !strings.HasPrefix(got[0], "text: ") {
			t.Fatal(text, got)
		}
	}
}

func TestDraftLeadConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	write(t, path, valid)
	c, err := Load(path)
	if err != nil || c.Papers[0].DraftLead != "90m" {
		t.Fatal(c, err)
	}
	for _, value := range []string{"1m", "90m", "24h"} {
		write(t, path, strings.Replace(valid, "  masthead:", "  draft_lead: "+value+"\n  masthead:", 1))
		c, err = Load(path)
		if err != nil || c.Papers[0].DraftLead != value {
			t.Fatal(c, err)
		}
	}
	for _, value := range []string{"0m", "-1m", "25h", "tomorrow", "''", "null", "true", "90"} {
		write(t, path, strings.Replace(valid, "  masthead:", "  draft_lead: "+value+"\n  masthead:", 1))
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "papers[0].draft_lead:") {
			t.Fatal(value, err)
		}
	}
}
