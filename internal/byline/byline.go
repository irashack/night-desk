// Ported from MuckScraper (https://github.com/grregis/MuckScraper),
// news_fetcher/bylines.py at 6c8ffaf. Copyright (c) 2026 grregis. MIT License;
// see THIRD_PARTY_NOTICES.md.

// Package byline turns feed, API and publisher-page author data into display
// bylines. Page text is untrusted data; nothing here fetches or stores it.
package byline

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

const maxBylineChars = 200

var (
	emailRE       = regexp.MustCompile(`[\p{L}\p{N}_.+-]+@[\p{L}\p{N}_-]+\.[\p{L}\p{N}_.-]+`)
	rssAuthorRE   = regexp.MustCompile(`^\S+@\S+\s*\((.+)\)$`)
	emptyParensRE = regexp.MustCompile(`\(\s*\)`)
	byPrefixRE    = regexp.MustCompile(`(?i)^\s*(?:written\s+)?by[:\s]+`)
	andAgenciesRE = regexp.MustCompile(`(?i)\s+and\s+agencies$`)
	placeRE       = regexp.MustCompile(`\s+in\s+[A-Z][\p{L}\p{N}_.'-]*(?:\s+(?:(?:de|da|do|dos|das|del|la|le|el|al)\s+)?[A-Z][\p{L}\p{N}_.'-]*)*`)
	roleSuffixRE  = regexp.MustCompile(`(?i),\s*(?:[\w-]+\s+){0,3}(?:contributor|correspondent|reporter|writer|editor|columnist)s?$`)
	partsRE       = regexp.MustCompile(`\s*[,|]\s*|\s+[-–—]\s*`)
	staffRE       = regexp.MustCompile(`\bstaff(?:\s+(?:writers?|reports?))?$`)
	letterRE      = regexp.MustCompile(`[A-Za-z]`)
)

var trailingOutlets = map[string]bool{
	"ap": true, "associated press": true, "the associated press": true,
	"reuters": true, "afp": true, "agence france-presse": true,
	"bloomberg": true, "cnn": true, "bbc": true, "bbc news": true, "upi": true,
}

var notAPerson = map[string]bool{
	"staff": true, "staff writer": true, "staff report": true,
	"staff reports": true, "admin": true, "editor": true, "editors": true,
	"the editors": true, "news desk": true, "newsroom": true,
	"web staff": true, "wire": true, "wire service": true,
	"wire reports": true, "unknown": true, "none": true, "null": true,
	"author": true,
}

const bylineCutset = " ,|;-–—"

// Clean turns whatever a feed, API or page supplied into a display byline.
// It strips an RSS "email (Name)" wrapper, e-mail addresses, a By prefix,
// trailing wire or outlet names, places and role suffixes. Staff labels,
// outlet names, URLs and strings with digits or no letters return "".
//
// outlet is the article's own outlet display name; outletNames are other
// outlets whose names should not become a byline. Names are compared without
// regard to case.
func Clean(raw, outlet string, outletNames []string) string {
	if raw == "" {
		return ""
	}
	text := raw
	if strings.Contains(raw, "<") {
		text = htmlText(raw)
	}
	text = collapse(text)

	// RSS 2.0 <author> is "email (Name)".
	if match := rssAuthorRE.FindStringSubmatch(text); match != nil {
		text = strings.TrimSpace(match[1])
	}
	text = emailRE.ReplaceAllString(text, "")
	text = emptyParensRE.ReplaceAllString(text, "")
	text = byPrefixRE.ReplaceAllString(text, "")
	text = strings.Trim(text, bylineCutset)

	text = andAgenciesRE.ReplaceAllString(text, "")
	text = removePlaces(text)
	text = roleSuffixRE.ReplaceAllString(text, "")

	ownOutlet := strings.ToLower(strings.TrimSpace(outlet))
	for {
		parts := partsRE.Split(text, -1)
		if len(parts) <= 1 {
			break
		}
		last := strings.TrimSpace(parts[len(parts)-1])
		lowerLast := strings.ToLower(last)
		if last == "" || (!trailingOutlets[lowerLast] && (ownOutlet == "" || lowerLast != ownOutlet)) {
			break
		}
		matches := partsRE.FindAllStringIndex(text, -1)
		if len(matches) == 0 {
			break
		}
		text = strings.TrimRight(text[:matches[len(matches)-1][0]], bylineCutset)
	}

	text = strings.Trim(collapse(text), bylineCutset)
	lowered := strings.ToLower(text)
	if text == "" ||
		notAPerson[lowered] ||
		trailingOutlets[lowered] ||
		(ownOutlet != "" && lowered == ownOutlet) ||
		containsLower(outletNames, lowered) ||
		strings.Contains(text, "://") ||
		strings.HasPrefix(lowered, "www.") ||
		!letterRE.MatchString(text) ||
		strings.ContainsFunc(text, unicode.IsDigit) ||
		staffRE.MatchString(lowered) {
		return ""
	}
	if utf8.RuneCountInString(text) > maxBylineChars {
		return ""
	}
	return text
}

// FromHTML extracts the bylines a publisher's page declares. JSON-LD article
// authors come first; when no JSON-LD item names a person, the author meta
// tags are used. Names are cleaned and deduplicated in source order.
func FromHTML(page []byte) []string {
	if len(page) == 0 {
		return nil
	}
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return nil
	}

	var scripts, metas []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script":
				scripts = append(scripts, n)
			case "meta":
				metas = append(metas, n)
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)

	for _, script := range scripts {
		if !hasAttr(script, "type", "application/ld+json") {
			continue
		}
		var payload any
		if json.Unmarshal([]byte(nodeText(script)), &payload) != nil {
			continue
		}
		for _, item := range jsonLDItems(payload) {
			author, ok := item["author"]
			if !ok {
				continue
			}
			names := cleanNames(jsonLDAuthorNames(author))
			if len(names) > 0 {
				return names
			}
		}
	}

	metaAuthor := []struct{ key, value string }{
		{"name", "author"},
		{"property", "article:author"},
		{"name", "byl"},
		{"name", "parsely-author"},
	}
	var names []string
	seen := make(map[string]bool)
	for _, query := range metaAuthor {
		for _, meta := range metas {
			value, ok := attrValue(meta, query.key)
			if !ok || !strings.EqualFold(strings.TrimSpace(value), query.value) {
				continue
			}
			content, ok := attrValue(meta, "content")
			if !ok {
				continue
			}
			name := Clean(content, "", nil)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// Join renders names as a display byline: one name, "A and B", or
// "A, B and C". Surrounding whitespace is trimmed and repeated names are
// dropped, keeping the first occurrence.
func Join(names []string) string {
	unique := make([]string, 0, len(names))
	seen := make(map[string]bool)
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		unique = append(unique, name)
	}
	switch len(unique) {
	case 0:
		return ""
	case 1:
		return unique[0]
	case 2:
		return unique[0] + " and " + unique[1]
	default:
		return strings.Join(unique[:len(unique)-1], ", ") + " and " + unique[len(unique)-1]
	}
}

func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// htmlText collects the text nodes of a byline fragment, mirroring
// BeautifulSoup.get_text(" ").
func htmlText(s string) string {
	var b strings.Builder
	z := html.NewTokenizer(strings.NewReader(s))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return b.String()
		case html.TextToken:
			b.Write(z.Text())
			b.WriteByte(' ')
		}
	}
}

// removePlaces drops MuckScraper's trailing location segments, but only where
// they end the byline or are followed by a co-author (" in Nairobi and ...").
// Go's RE2 has no lookahead, so the check is made after each match.
func removePlaces(text string) string {
	matches := placeRE.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for _, match := range matches {
		if match[1] == len(text) || strings.HasPrefix(text[match[1]:], " and ") {
			b.WriteString(text[last:match[0]])
			last = match[1]
		}
	}
	if last == 0 {
		return text
	}
	b.WriteString(text[last:])
	return b.String()
}

func containsLower(values []string, lower string) bool {
	for _, value := range values {
		if strings.ToLower(strings.TrimSpace(value)) == lower {
			return true
		}
	}
	return false
}

func hasAttr(n *html.Node, key, value string) bool {
	for _, attr := range n.Attr {
		if attr.Key == key && strings.EqualFold(strings.TrimSpace(attr.Val), value) {
			return true
		}
	}
	return false
}

func attrValue(n *html.Node, key string) (string, bool) {
	for _, attr := range n.Attr {
		if attr.Key == key {
			return attr.Val, true
		}
	}
	return "", false
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return b.String()
}

func jsonLDItems(payload any) []map[string]any {
	items := []any{payload}
	if list, ok := payload.([]any); ok {
		items = list
	}
	var result []map[string]any
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		result = append(result, object)
		if graph, ok := object["@graph"].([]any); ok {
			for _, entry := range graph {
				if node, ok := entry.(map[string]any); ok {
					result = append(result, node)
				}
			}
		}
	}
	return result
}

func jsonLDAuthorNames(value any) []string {
	switch value := value.(type) {
	case string:
		return []string{value}
	case map[string]any:
		if name, ok := value["name"].(string); ok {
			return []string{name}
		}
	case []any:
		var names []string
		for _, item := range value {
			names = append(names, jsonLDAuthorNames(item)...)
		}
		return names
	}
	return nil
}

func cleanNames(raw []string) []string {
	var names []string
	seen := make(map[string]bool)
	for _, value := range raw {
		name := Clean(value, "", nil)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}
