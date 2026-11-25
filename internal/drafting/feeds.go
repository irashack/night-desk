package drafting

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/sourcecheck"
	"golang.org/x/net/html"
)

// FeedsOnly builds an edition from source excerpts in paper section order,
// keeping the supplied candidate order within each section.
func FeedsOnly(p Paper, candidates []Candidate) (edition.Edition, []Problem, error) {
	e := edition.Edition{Schema: 2, Sections: []edition.Section{}}
	if err := validateInput(p, candidates); err != nil {
		return e, nil, err
	}
	titles := make(map[string][]string)
	items := make([]edition.Item, len(candidates))
	var problems []Problem
	for i, c := range candidates {
		title := feedPlainText(c.Title)
		if title != "" {
			titles[title] = append(titles[title], c.ID)
		}
		item := edition.Item{
			ID: fmt.Sprintf("feed-%d", i+1), Kind: "news", URL: c.URL,
			Title: feedPrefix(title, 160), Summary: feedExcerpt(feedPlainText(c.Text)),
			Source: feedPrefix(feedPlainText(c.Outlet), 80),
		}
		if item.Title == "" || item.Summary == "" || item.Source == "" || sourcecheck.HeadlineTruncated(item.Title) {
			problems = append(problems, Problem{Path: fmt.Sprintf("candidates[%d]", i), Rule: "has unusable headline, excerpt or outlet", Severity: edition.Warning})
			continue
		}
		if byline := feedPrefix(feedPlainText(c.Byline), 120); byline != "" {
			item.Byline = &byline
		}
		items[i] = item
	}
	groups := make([][]string, 0, len(titles))
	for _, ids := range titles {
		if len(ids) > 1 {
			groups = append(groups, ids)
		}
	}
	parents, _ := storyGroups(candidates, groups)
	seen := make(map[int]bool)

	sectionLimit, remaining := 6, 80
	if p.Limits.ItemsPerSection > 0 {
		sectionLimit = min(sectionLimit, p.Limits.ItemsPerSection)
	}
	if p.Limits.ItemsPerEdition > 0 {
		remaining = min(remaining, p.Limits.ItemsPerEdition)
	}
	required := 0
	for _, s := range p.Sections {
		if s.Required {
			required++
		}
	}
	for _, s := range p.Sections {
		if s.Required {
			required--
		}
		// Leave room for at least one story in each later required section.
		limit := min(sectionLimit, remaining-required)
		section := edition.Section{Key: s.Key, Items: []edition.Item{}}
		for i, c := range candidates {
			if len(section.Items) >= limit {
				break
			}
			if c.Section != s.Key || items[i].ID == "" || seen[parents[i]] {
				continue
			}
			seen[parents[i]] = true
			section.Items = append(section.Items, items[i])
			if e.Lead == "" {
				e.Lead = items[i].ID
			}
		}
		if s.Required || len(section.Items) > 0 {
			e.Sections = append(e.Sections, section)
			remaining -= len(section.Items)
		}
	}
	return validateDraft(e, p, problems)
}

var markupStart = regexp.MustCompile(`<([/!?]?[a-zA-Z])`)

func feedPlainText(value string) string {
	z := html.NewTokenizer(strings.NewReader(value))
	var text strings.Builder
	hidden := false
	for {
		kind := z.Next()
		switch kind {
		case html.ErrorToken:
			// Decoded text can itself contain escaped markup.
			plain := markupStart.ReplaceAllString(text.String(), "‹${1}")
			plain = strings.ReplaceAll(plain, "](", "] (")
			return strings.Join(strings.FieldsFunc(plain, func(r rune) bool {
				return unicode.IsSpace(r) || unicode.IsControl(r)
			}), " ")
		case html.TextToken:
			if !hidden {
				text.Write(z.Text())
			}
		case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "script", "style":
				hidden = kind == html.StartTagToken
			case "p", "div", "br", "li":
				text.WriteByte(' ')
			}
		}
	}
}

// feedPrefix clips at a word boundary if there is one within the limit.
func feedPrefix(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	prefix := string(runes[:limit])
	if runes[limit] != ' ' {
		if boundary := strings.LastIndexByte(prefix, ' '); boundary >= 0 {
			prefix = prefix[:boundary]
		}
	}
	return strings.TrimSpace(prefix)
}

var feedSentenceEnd = regexp.MustCompile(`[.!?。！？]["'”’)]*(?: |$)`)

// feedExcerpt takes up to two opening sentences. If none fits, it clips to
// a word boundary and marks the excerpt with an ellipsis.
func feedExcerpt(text string) string {
	const limit = 600
	if text == "" {
		return ""
	}
	end := 0
	for _, match := range feedSentenceEnd.FindAllStringIndex(text, -1) {
		prefix := strings.TrimSpace(text[:match[1]])
		if len([]rune(prefix)) > limit {
			break
		}
		if sourcecheck.SentenceCount(prefix) <= 2 && !sourcecheck.Truncated(prefix) {
			end = len(prefix)
		} else {
			break
		}
	}
	if end > 0 {
		return text[:end]
	}
	if len([]rune(text)) <= limit && !sourcecheck.Truncated(text) {
		return text
	}
	return feedPrefix(text, limit-1) + "…"
}
