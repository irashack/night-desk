package candidate

import (
	"strings"
	"testing"
	"time"
)

func valid() Candidate {
	return Candidate{Section: "news", CanonicalURL: "https://example.com/article", Outlet: "Example", Title: "A report", FetchedAt: time.Now(), Text: "Source text.\nAnother paragraph.", State: "ok"}
}

func TestValidate(t *testing.T) {
	if p := Validate(valid()); len(p) != 0 {
		t.Fatal(p)
	}
	for _, tc := range []struct {
		name, path, rule string
		change           func(*Candidate)
	}{
		{"section", "section", "must match ^[a-z0-9][a-z0-9-]{0,63}$", func(c *Candidate) { c.Section = "Bad Section" }},
		{"url", "canonical_url", "must be an absolute http or https URL without userinfo, at most 8192 bytes", func(c *Candidate) { c.CanonicalURL = "https://user:secret@example.com/" }},
		{"alias", "aliases[0]", "must be an absolute http or https URL without userinfo, at most 8192 bytes", func(c *Candidate) { c.Aliases = []string{"file:///etc/passwd"} }},
		{"title", "title", "must be plain text (no HTML tags or Markdown links)", func(c *Candidate) { c.Title = "<b>Headline</b>" }},
		{"outlet", "outlet", "must be trimmed non-empty text", func(c *Candidate) { c.Outlet = " Example " }},
		{"fetched", "fetched_at", "must be a non-zero RFC 3339 timestamp", func(c *Candidate) { c.FetchedAt = time.Time{} }},
		{"byline", "byline[0]", "must not contain control characters", func(c *Candidate) { c.Byline = []string{"A\x00B"} }},
		{"text", "text", "must be at most 204800 bytes", func(c *Candidate) { c.Text = strings.Repeat("é", MaxTextBytes/2+1) }},
		{"empty", "text", "must be trimmed non-empty text", func(c *Candidate) { c.Text = "" }},
		{"state", "state", "must be one of ok, unavailable", func(c *Candidate) { c.State = "other" }},
		{"unavailable", "text", "must be empty when state is unavailable", func(c *Candidate) { c.State = "unavailable" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			tc.change(&c)
			problems := Validate(c)
			for _, p := range problems {
				if p.Path == tc.path && p.Rule == tc.rule {
					return
				}
			}
			t.Fatalf("missing %s: %s in %v", tc.path, tc.rule, problems)
		})
	}
	c := valid()
	c.State, c.Text = "unavailable", ""
	if p := Validate(c); len(p) != 0 {
		t.Fatal(p)
	}
	c = valid()
	c.Text = strings.Repeat("x", MaxTextBytes)
	if p := Validate(c); len(p) != 0 {
		t.Fatal(p)
	}
}
