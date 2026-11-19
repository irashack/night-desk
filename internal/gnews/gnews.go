// Ported from MuckScraper (https://github.com/grregis/MuckScraper),
// news_fetcher/google_news.py at 6c8ffaf. Copyright (c) 2026 grregis. MIT License;
// see THIRD_PARTY_NOTICES.md.

// Package gnews turns site-limited Google News search feeds back into
// publisher links. Feed entries carry coded news.google.com/rss/articles
// addresses; Resolve follows the article page and Google's batchexecute
// endpoint to the publisher's own URL.
//
// Both endpoints are undocumented and can change without notice. Callers
// should use a guarded HTTP client, keep lookups rare and drop an entry that
// cannot be resolved for this run rather than storing a Google News address.
package gnews

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const (
	googleNewsHost   = "news.google.com"
	batchPath        = "/_/DotsSplashUi/data/batchexecute"
	userAgent        = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 " + "(KHTML, like Gecko) Chrome/124.0 Safari/537.36"
	maxResponseBytes = 1 << 20
)

// googleNewsBaseURL is a variable so tests can point the resolver at an
// httptest server standing in for news.google.com.
var googleNewsBaseURL = "https://" + googleNewsHost

var (
	articlePathRE = regexp.MustCompile(`news\.google\.com/(?:rss/)?articles/([A-Za-z0-9_-]+)`)
	signatureRE   = regexp.MustCompile(`data-n-a-sg="([^"]+)"`)
	timestampRE   = regexp.MustCompile(`data-n-a-ts="([^"]+)"`)
)

// SearchFeedURL builds a Google News RSS search URL limited to site. An empty
// lang or country means "en-US" or "US". An empty site or query is omitted
// from the search terms.
func SearchFeedURL(site, query, lang, country string) string {
	lang = strings.TrimSpace(lang)
	if lang == "" {
		lang = "en-US"
	}
	country = strings.TrimSpace(country)
	if country == "" {
		country = "US"
	}
	site = strings.TrimSpace(site)
	query = strings.TrimSpace(query)

	terms := make([]string, 0, 2)
	if site != "" {
		terms = append(terms, "site:"+site)
	}
	if query != "" {
		terms = append(terms, query)
	}
	language := lang
	if i := strings.IndexAny(language, "-_"); i >= 0 {
		language = language[:i]
	}

	values := url.Values{}
	values.Set("q", strings.Join(terms, " "))
	values.Set("hl", lang)
	values.Set("gl", country)
	values.Set("ceid", country+":"+language)
	return strings.TrimRight(googleNewsBaseURL, "/") + "/rss/search?" + values.Encode()
}

// IsFeed reports whether feedURL names Google News, matching the check in
// MuckScraper's google_news.is_google_news_feed.
func IsFeed(feedURL string) bool {
	return strings.Contains(feedURL, googleNewsHost)
}

// ArticleID returns the coded id in a Google News article link.
func ArticleID(link string) (string, bool) {
	match := articlePathRE.FindStringSubmatch(link)
	if match == nil {
		return "", false
	}
	return match[1], true
}

// StripSourceSuffix removes the " - <Outlet>" Google appends to every title.
// Any other " - " in a headline is left alone. MuckScraper can also remove a
// last segment when a caller-supplied same-outlet test recognises it; this
// package's exported signature has no such callback, so only the exact suffix
// is removed.
func StripSourceSuffix(title, sourceTitle string) string {
	title = strings.TrimSpace(title)
	sourceTitle = strings.TrimSpace(sourceTitle)
	if sourceTitle == "" {
		return title
	}
	suffix := " - " + sourceTitle
	if strings.HasSuffix(title, suffix) {
		return strings.TrimSpace(strings.TrimSuffix(title, suffix))
	}
	return title
}

// parseSignaturePage extracts the signature and timestamp embedded in the
// coded article page.
func parseSignaturePage(html string) (string, int64, bool) {
	sig := signatureRE.FindStringSubmatch(html)
	ts := timestampRE.FindStringSubmatch(html)
	if sig == nil || ts == nil {
		return "", 0, false
	}
	timestamp, err := strconv.ParseInt(ts[1], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return sig[1], timestamp, true
}

// buildBatchBody builds the form body Google's batchexecute endpoint expects.
func buildBatchBody(gid string, timestamp int64, signature string) string {
	inner := []any{
		"garturlreq",
		[]any{
			[]any{"X", "X", []any{"X", "X"}, nil, nil, 1, 1, "US:en", nil, 1, nil, nil, nil, nil, nil, 0, 1},
			"X", "X", 1, []any{1, 1, 1}, 1, 1, nil, 0, 0, nil, 0,
		},
		gid, timestamp, signature,
	}
	innerJSON, _ := json.Marshal(inner)
	payload := []any{[]any{[]any{"Fbv4je", string(innerJSON), nil, "generic"}}}
	body, _ := json.Marshal(payload)
	return "f.req=" + url.QueryEscape(string(body))
}

// parseBatchResponse pulls the publisher URL out of a batchexecute answer.
func parseBatchResponse(text string) string {
	rest, ok := afterBlankLine(text)
	if !ok {
		return ""
	}
	var rows []json.RawMessage
	if err := json.Unmarshal([]byte(rest), &rows); err != nil || len(rows) == 0 {
		return ""
	}
	// Google appends two metadata elements; MuckScraper drops them. Keep them
	// when a shorter answer is served, since the first row is all we need.
	if len(rows) > 2 {
		rows = rows[:len(rows)-2]
	}
	var row []json.RawMessage
	if err := json.Unmarshal(rows[0], &row); err != nil || len(row) < 3 {
		return ""
	}
	var encoded string
	if err := json.Unmarshal(row[2], &encoded); err != nil {
		return ""
	}
	var result []any
	if err := json.Unmarshal([]byte(encoded), &result); err != nil || len(result) < 2 {
		return ""
	}
	publisher, ok := result[1].(string)
	if !ok || !(strings.HasPrefix(publisher, "http://") || strings.HasPrefix(publisher, "https://")) {
		return ""
	}
	if strings.Contains(publisher, googleNewsHost) {
		return ""
	}
	return publisher
}

func afterBlankLine(text string) (string, bool) {
	if i := strings.Index(text, "\r\n\r\n"); i >= 0 {
		return text[i+4:], true
	}
	if i := strings.Index(text, "\n\n"); i >= 0 {
		return text[i+2:], true
	}
	return "", false
}

// Resolve returns the publisher URL for one Google News article link. It uses
// the caller's HTTP client (normally the SSRF-guarded fetcher), reads at most
// 1 MB per response and only returns an https publisher URL.
func Resolve(ctx context.Context, c *http.Client, link string) (string, error) {
	gid, ok := ArticleID(link)
	if !ok {
		return "", errors.New("gnews: link is not a Google News article")
	}
	if c == nil {
		return "", errors.New("gnews: nil HTTP client")
	}
	base := strings.TrimRight(googleNewsBaseURL, "/")

	pageURL := base + "/rss/articles/" + gid
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", fmt.Errorf("gnews: article page request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	page, err := doRead(c, req)
	if err != nil {
		return "", fmt.Errorf("gnews: article page: %w", err)
	}

	signature, timestamp, ok := parseSignaturePage(string(page))
	if !ok {
		return "", errors.New("gnews: article page has no signature")
	}

	body := buildBatchBody(gid, timestamp, signature)
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, base+batchPath, strings.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("gnews: lookup request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=UTF-8")
	answer, err := doRead(c, req)
	if err != nil {
		return "", fmt.Errorf("gnews: lookup: %w", err)
	}

	publisher := parseBatchResponse(string(answer))
	if publisher == "" {
		return "", errors.New("gnews: lookup returned no publisher URL")
	}
	u, err := url.Parse(publisher)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", errors.New("gnews: lookup returned a non-https publisher URL")
	}
	if strings.Contains(publisher, googleNewsHost) {
		return "", errors.New("gnews: lookup returned another Google News URL")
	}
	return publisher, nil
}

func doRead(c *http.Client, req *http.Request) ([]byte, error) {
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("answered HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	return body, nil
}
