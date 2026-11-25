package gather

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"

	"github.com/irashack/night-desk/internal/fetch"
)

// Article fetching policy, after MuckScraper's scraper (MIT): retry only what
// may succeed later, and stop hammering a host that keeps failing.
const (
	// articleAttempts bounds tries per URL; only 429, 5xx and requests that got
	// no response are retried.
	articleAttempts = 3
	retryBase       = 500 * time.Millisecond
	retryCap        = 5 * time.Second
	// runRetryBudget bounds the total time one run spends waiting to retry.
	runRetryBudget = 30 * time.Second
	// A host cools off after coolOffFailures consecutive failed articles. The
	// state lives in the worker process: the worker has no database, and a
	// restart forgetting a cool-off costs at most coolOffFailures more tries.
	coolOffFailures = 3
	coolOffPeriod   = 30 * time.Minute
	// usableText is the plain-text length, in characters, below which an
	// extraction is treated as a failure and the next method is tried.
	usableText = 280
)

// MaxHealthHosts bounds one run's fetch-health report.
const MaxHealthHosts = 500

var errCoolingOff = errors.New("host is cooling off after repeated failures")

// HostHealth counts one host's article fetches in a run: never URLs or text.
type HostHealth struct {
	Host           string `json:"host"`
	OK             int    `json:"ok"`
	FailedHTTP4xx  int    `json:"failed_http_4xx"`
	FailedHTTP429  int    `json:"failed_http_429"`
	FailedHTTP5xx  int    `json:"failed_http_5xx"`
	FailedNetwork  int    `json:"failed_network"`
	FailedTooLarge int    `json:"failed_too_large"`
	CooledOff      int    `json:"cooled_off"`
	Readability    int    `json:"readability"`
	JSONLD         int    `json:"json_ld"`
	AMP            int    `json:"amp"`
}

type hostState struct {
	failures int
	until    time.Time
}

// articles fetches and extracts one run's articles.
type articles struct {
	w         *Worker
	client    *http.Client
	allow     fetch.Allow
	retryLeft time.Duration
	health    map[string]*HostHealth
}

func (w *Worker) articles(client *http.Client, allow fetch.Allow) *articles {
	if w.hosts == nil {
		w.hosts = map[string]*hostState{}
	}
	return &articles{w: w, client: client, allow: allow, retryLeft: runRetryBudget, health: map[string]*HostHealth{}}
}

func hostOf(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
}

func (a *articles) hostHealth(host string) *HostHealth {
	h := a.health[host]
	if h == nil {
		h = &HostHealth{Host: host}
		a.health[host] = h
	}
	return h
}

// read fetches link with bounded, status-aware retries and the host cool-off.
func (a *articles) read(ctx context.Context, link string) ([]byte, string, error) {
	host := hostOf(link)
	h, state := a.hostHealth(host), a.w.hosts[host]
	if state == nil {
		state = &hostState{}
		a.w.hosts[host] = state
	}
	if a.w.now().Before(state.until) {
		h.CooledOff++
		return nil, "", errCoolingOff
	}
	for attempt := 1; ; attempt++ {
		page, final, err := fetch.Read(ctx, a.client, link, MaxArticleBytes)
		if err == nil {
			state.failures = 0
			h.OK++
			return page, final, nil
		}
		var status *fetch.StatusError
		retry := errors.Is(err, fetch.ErrRequest) || errors.As(err, &status) && (status.Code == 429 || status.Code >= 500)
		delay := min(retryBase<<(attempt-1), retryCap)
		if status != nil && status.RetryAfter > delay {
			delay = status.RetryAfter
		}
		if retry && attempt < articleAttempts && delay <= retryCap && delay <= a.retryLeft && ctx.Err() == nil {
			a.retryLeft -= delay
			if a.w.sleep(ctx, delay) == nil {
				continue
			}
		}
		switch {
		case errors.Is(err, fetch.ErrTooLarge):
			h.FailedTooLarge++
		case status != nil && status.Code == 429:
			h.FailedHTTP429++
		case status != nil && status.Code >= 500:
			h.FailedHTTP5xx++
		case status != nil:
			h.FailedHTTP4xx++
		default:
			h.FailedNetwork++
		}
		if state.failures++; state.failures >= coolOffFailures {
			state.failures, state.until = 0, a.w.now().Add(coolOffPeriod)
		}
		return nil, "", err
	}
}

// extract tries Readability, then the page's JSON-LD articleBody, then its AMP
// version, and keeps the first usable text. When none is usable it keeps any
// Readability text, as before fallbacks existed.
func (a *articles) extract(ctx context.Context, final string, page []byte) (title, text string) {
	h := a.hostHealth(hostOf(final))
	title, text, err := a.w.Extractor.Extract(final, page)
	if err != nil {
		title, text = "", ""
	}
	if usable(text) {
		h.Readability++
		return title, text
	}
	if body := jsonLDBody(page); usable(body) {
		h.JSONLD++
		return title, body
	}
	// An AMP link on a public page never reaches an approved private service.
	if amp := ampURL(final, page); amp != "" && a.allow.Reached(final, amp) {
		if ampPage, ampFinal, err := a.read(ctx, amp); err == nil {
			if ampTitle, ampText, err := a.w.Extractor.Extract(ampFinal, ampPage); err == nil && usable(ampText) {
				h.AMP++
				if title == "" {
					title = ampTitle
				}
				return title, ampText
			}
		}
	}
	if plain(text) != "" {
		h.Readability++
	}
	return title, text
}

func usable(text string) bool { return utf8.RuneCountInString(plain(text)) >= usableText }

var articleTypes = map[string]bool{"Article": true, "NewsArticle": true, "ReportageNewsArticle": true, "AnalysisNewsArticle": true, "BackgroundNewsArticle": true, "OpinionNewsArticle": true, "ReviewNewsArticle": true, "BlogPosting": true}

// jsonLDBody returns the longest articleBody of an article in the page's JSON-LD,
// including items inside @graph.
func jsonLDBody(page []byte) string {
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return ""
	}
	best := ""
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "script" && attr(n, "type") == "application/ld+json" && n.FirstChild != nil {
			var payload any
			if json.Unmarshal([]byte(n.FirstChild.Data), &payload) == nil {
				for _, item := range ldItems(payload) {
					if body, ok := item["articleBody"].(string); ok && isArticle(item["@type"]) && len(body) > len(best) {
						best = body
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return best
}

func ldItems(payload any) []map[string]any {
	var out []map[string]any
	switch v := payload.(type) {
	case []any:
		for _, item := range v {
			out = append(out, ldItems(item)...)
		}
	case map[string]any:
		out = append(out, v)
		if graph, ok := v["@graph"]; ok {
			out = append(out, ldItems(graph)...)
		}
	}
	return out
}

func isArticle(t any) bool {
	switch v := t.(type) {
	case string:
		return articleTypes[v]
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok && articleTypes[s] {
				return true
			}
		}
	}
	return false
}

// ampURL returns the page's <link rel="amphtml"> resolved against final, if it
// is a valid http(s) URL.
func ampURL(final string, page []byte) string {
	base, err := url.Parse(final)
	if err != nil {
		return ""
	}
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return ""
	}
	found := ""
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != "" {
			return
		}
		if n.Type == html.ElementNode && n.Data == "link" && strings.EqualFold(attr(n, "rel"), "amphtml") {
			if ref, err := base.Parse(attr(n, "href")); err == nil && validURL(ref.String()) && ref.String() != final {
				found = ref.String()
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return found
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return strings.TrimSpace(a.Val)
		}
	}
	return ""
}

// sleepCtx waits d or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
