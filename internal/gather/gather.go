// Package gather runs the unprivileged, HTTP-only source worker. Untrusted feed
// and article contents are processed as data and never interpreted as commands.
package gather

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/irashack/night-desk/internal/byline"
	"github.com/irashack/night-desk/internal/candidate"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/fetch"
	"github.com/irashack/night-desk/internal/gnews"
	"github.com/irashack/night-desk/internal/lean"
	"github.com/irashack/night-desk/internal/store"
	"github.com/irashack/night-desk/internal/wire"
	"github.com/mmcdole/gofeed"
	"golang.org/x/net/html"
)

const (
	MaxFeedBytes    = 2 << 20
	MaxArticleBytes = 5 << 20
	MaxItems        = 500
)

type Extractor interface {
	Extract(url string, page []byte) (title, text string, err error)
}

type Paper struct {
	Sources       []config.Source        `json:"sources"`
	Sections      []config.Section       `json:"sections"`
	Gather        config.Gather          `json:"gather"`
	Filters       *config.Filters        `json:"filters"`
	RetentionDays int                    `json:"retention_days"`
	RecentURLs    []string               `json:"recent_urls"`
	FoundSources  []store.SectionSources `json:"found_sources"`
}

type Worker struct {
	server string
	key    string
	// APIClient is used only for the configured trusted Night Desk server.
	APIClient *http.Client
	Extractor Extractor
	// Resolve and Groups allow alternate implementations without changing the
	// network boundary: Resolve always receives the guarded client.
	Resolve func(context.Context, *http.Client, string) (string, error)
	Groups  func([]string) [][]int

	// hosts keeps article hosts' cool-off state across runs of this process.
	hosts map[string]*hostState
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

type Result struct {
	Candidates, Unavailable, FeedFailures int
	// Dropped counts items excluded by ingestion filters, by rule.
	Dropped map[string]int
	// Health counts article fetches by host, sorted by host.
	Health []HostHealth
}

func New(server, key string, extractor Extractor) (*Worker, error) {
	u, err := url.Parse(server)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback()))) {
		return nil, errors.New("server must be an https origin (http allowed on loopback)")
	}
	if key == "" || strings.ContainsFunc(key, unicode.IsSpace) || strings.ContainsFunc(key, unicode.IsControl) {
		return nil, errors.New("key file must contain one non-empty key")
	}
	if extractor == nil {
		extractor = Readability{}
	}
	return &Worker{server: strings.TrimSuffix(server, "/"), key: key, Extractor: extractor, APIClient: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("server redirects are refused") }}, Resolve: gnews.Resolve, Groups: wire.Groups, now: time.Now, sleep: sleepCtx}, nil
}

type responseError int

func (e responseError) Error() string { return fmt.Sprintf("server returned HTTP %d", e) }

func (w *Worker) request(ctx context.Context, method, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, w.server+path, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid server request")
	}
	req.Header.Set("Authorization", "Bearer "+w.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := w.APIClient.Do(req)
	if err != nil {
		return errors.New("server request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return responseError(resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20+1))
	if err != nil || len(b) > 8<<20 {
		return errors.New("server response exceeds limit or could not be read")
	}
	if out != nil && json.Unmarshal(b, out) != nil {
		return errors.New("invalid server response")
	}
	return nil
}

func (w *Worker) RunOnce(ctx context.Context) (Result, error) {
	result := Result{Dropped: map[string]int{}}
	var p Paper
	if err := w.request(ctx, "GET", "/api/v1/paper", nil, &p); err != nil {
		return result, err
	}
	var ratings struct {
		Outlets []lean.Outlet `json:"outlets"`
	}
	for _, section := range p.Sections {
		if section.Lean {
			if err := w.request(ctx, "GET", "/api/v1/lean/outlets", nil, &ratings); err != nil {
				return result, err
			}
			break
		}
	}
	client := fetch.New(fetch.Allow(p.Gather.AllowPrivate))
	defer client.CloseIdleConnections()
	recent := map[string]bool{}
	for _, u := range p.RecentURLs {
		recent[normalizeURL(u)] = true
	}
	seen := map[string]bool{}
	filter := newFilter(p.Filters)
	fetcher := w.articles(client)
	list := []candidate.Candidate{}
	attempted := 0
	feedCache := map[string]*gofeed.Feed{}
	var failures []error
	for _, section := range p.Sections {
		sources := sectionSources(p, section, ratings.Outlets)
		for _, source := range sources {
			feedURL := source.Feed
			if source.GoogleNews != nil {
				feedURL = gnews.SearchFeedURL(source.GoogleNews.Site, source.GoogleNews.Query, "en", "US")
			}
			if feedURL == "" {
				continue
			}
			feed, exists := feedCache[feedURL]
			if !exists {
				b, _, err := fetch.Read(ctx, client, feedURL, MaxFeedBytes)
				if err == nil {
					feed, err = gofeed.NewParser().Parse(bytes.NewReader(b))
				}
				if err != nil {
					result.FeedFailures++
					failures = append(failures, errors.New("source feed could not be fetched or parsed"))
				}
				feedCache[feedURL] = feed
			}
			if feed == nil {
				continue
			}
			for _, item := range feed.Items {
				if attempted >= MaxItems {
					break
				}
				link := normalizeURL(item.Link)
				if !validURL(link) || seen[link] {
					continue
				}
				original := link
				seen[link] = true
				// Filters read only the feed's title and URL, so a dropped item costs no
				// page fetch and never counts toward MaxItems. A Google News link is
				// checked again once it resolves to the publisher's host.
				if rule := filter.drop(plain(item.Title), link, plain(feed.Title)); rule != "" {
					result.Dropped[rule]++
					continue
				}
				attempted++
				resolved := true
				parsed, _ := url.Parse(link)
				if strings.EqualFold(parsed.Hostname(), "news.google.com") {
					target, err := w.Resolve(ctx, client, link)
					if err != nil || !validURL(target) {
						resolved = false
					} else {
						link = normalizeURL(target)
					}
					if rule := filter.drop(plain(item.Title), link, plain(feed.Title)); resolved && rule != "" {
						result.Dropped[rule]++
						continue
					}
				}
				c := candidate.Candidate{Section: section.Key, CanonicalURL: link, Title: plain(item.Title), Outlet: plain(feed.Title), PublishedAt: item.PublishedParsed, FetchedAt: time.Now().UTC(), State: "unavailable"}
				if c.PublishedAt == nil {
					c.PublishedAt = item.UpdatedParsed
				}
				if c.Title == "" {
					c.Title = parsed.Hostname()
				}
				if c.Outlet == "" {
					c.Outlet = parsed.Hostname()
				}
				if original != link {
					c.Aliases = append(c.Aliases, original)
				}
				if resolved {
					page, final, err := fetcher.read(ctx, link)
					if err == nil {
						final = normalizeURL(final)
						if final != link {
							c.Aliases = append(c.Aliases, link)
						}
						c.CanonicalURL = canonicalURL(final, page)
						if c.CanonicalURL != final {
							c.Aliases = append(c.Aliases, final)
						}
						title, text := fetcher.extract(ctx, final, page)
						c.Text = capText(plain(text), 200*1024)
						if c.Text != "" {
							c.State = "ok"
							if title = plain(title); title != "" {
								c.Title = title
							}
						}
						for _, author := range byline.FromHTML(page) {
							if author = plain(author); author != "" {
								c.Byline = append(c.Byline, author)
							}
						}
					}
				}
				c.Title = capText(c.Title, 1000)
				c.Outlet = capText(c.Outlet, 200)
				if len(c.Byline) > 32 {
					c.Byline = c.Byline[:32]
				}
				for i := range c.Byline {
					c.Byline[i] = capText(c.Byline[i], 200)
				}
				c.Repeat = recent[c.CanonicalURL] || recent[original] || recent[link]
				for _, alias := range c.Aliases {
					c.Repeat = c.Repeat || recent[alias]
				}
				if len(candidate.Validate(c)) > 0 {
					continue
				}
				if c.State == "unavailable" {
					result.Unavailable++
				}
				// A paper has one immutable snapshot per canonical URL, even if several
				// sections monitor the same feed. The first configured section wins.
				duplicate := false
				for i := range list {
					if list[i].CanonicalURL == c.CanonicalURL {
						list[i].Aliases = append(list[i].Aliases, c.Aliases...)
						if original != c.CanonicalURL {
							list[i].Aliases = append(list[i].Aliases, original)
						}
						duplicate = true
						break
					}
				}
				if !duplicate {
					list = append(list, c)
				}
			}
		}
	}
	texts := make([]string, len(list))
	for i := range list {
		texts[i] = list[i].Text
	}
	for _, group := range w.Groups(texts) {
		urls := []string{}
		for _, i := range group {
			if i >= 0 && i < len(list) && list[i].State == "ok" {
				urls = append(urls, list[i].CanonicalURL)
			}
		}
		if len(urls) < 2 {
			continue
		}
		sort.Strings(urls)
		hash := sha256.Sum256([]byte(strings.Join(urls, "\n")))
		id := hex.EncodeToString(hash[:])
		for _, i := range group {
			if i >= 0 && i < len(list) && list[i].State == "ok" {
				list[i].WireGroup = &id
			}
		}
	}
	// Thirty maximum-sized texts fit comfortably within the server's 8 MiB cap.
	for start := 0; start < len(list); {
		end := min(start+30, len(list))
		batch := list[start:end]
		for i := range batch {
			batch[i].Aliases = unique(batch[i].Aliases, batch[i].CanonicalURL)
			if len(batch[i].Aliases) > 100 {
				batch[i].Aliases = batch[i].Aliases[:100]
			}
		}
		body, err := json.Marshal(map[string]any{"candidates": batch})
		if err != nil {
			return result, errors.New("candidate encoding failed")
		}
		for len(body) > 8<<20 && len(batch) > 1 {
			end--
			batch = list[start:end]
			body, err = json.Marshal(map[string]any{"candidates": batch})
			if err != nil {
				return result, errors.New("candidate encoding failed")
			}
		}
		if len(body) > 8<<20 {
			return result, errors.New("candidate exceeds batch limit")
		}
		if err = w.request(ctx, "POST", "/api/v1/candidates", body, nil); err != nil {
			return result, err
		}
		start = end
		result.Candidates += len(batch)
	}
	if len(result.Dropped) > 0 {
		dropped := []map[string]any{}
		for _, rule := range append([]string{"blocked_outlet", "blocked_keyword"}, config.IngestFilters...) {
			if n := result.Dropped[rule]; n > 0 {
				dropped = append(dropped, map[string]any{"rule": rule, "count": min(n, 100000)})
			}
		}
		body, _ := json.Marshal(map[string]any{"dropped": dropped})
		// The candidates are already stored; a server without this endpoint
		// (an older release) must not fail the run.
		if err := w.request(ctx, "POST", "/api/v1/gather/dropped", body, nil); err != nil {
			failures = append(failures, errors.New("filter counts could not be reported"))
		}
	}
	for _, h := range fetcher.health {
		if h.Host != "" {
			result.Health = append(result.Health, *h)
		}
	}
	sort.Slice(result.Health, func(i, j int) bool { return result.Health[i].Host < result.Health[j].Host })
	if len(result.Health) > 0 {
		// The candidates are already stored; a server without this endpoint
		// (an older release) must not fail the run.
		body, _ := json.Marshal(map[string]any{"hosts": result.Health[:min(len(result.Health), MaxHealthHosts)]})
		if err := w.request(ctx, "POST", "/api/v1/gather/health", body, nil); err != nil {
			failures = append(failures, errors.New("fetch health could not be reported"))
		}
	}
	return result, errors.Join(failures...)
}

func normalizeURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw
	}
	u.Fragment = ""
	return u.String()
}
func validURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Opaque != "" || (u.Scheme != "https" && u.Scheme != "http") || len(raw) > 8192 || strings.HasSuffix(u.Host, ":") || strings.ContainsFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}

func plain(s string) string {
	if strings.Contains(s, "<") {
		var b strings.Builder
		z := html.NewTokenizer(strings.NewReader(s))
		for {
			tt := z.Next()
			if tt == html.ErrorToken {
				break
			}
			if tt == html.TextToken {
				b.WriteString(z.Token().Data)
			} else {
				b.WriteByte(' ')
			}
		}
		s = b.String()
	}
	s = strings.ReplaceAll(s, "](", "] (")
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)), " ")
}
func capText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return strings.TrimSpace(s)
}
func unique(values []string, except string) []string {
	out := []string{}
	seen := map[string]bool{except: true}
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
func canonicalURL(final string, page []byte) string {
	base, _ := url.Parse(final)
	z := html.NewTokenizer(bytes.NewReader(page))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return final
		case html.StartTagToken, html.SelfClosingTagToken:
			token := z.Token()
			if token.Data != "link" {
				continue
			}
			rel, href := "", ""
			for _, a := range token.Attr {
				if a.Key == "rel" {
					rel = a.Val
				}
				if a.Key == "href" {
					href = a.Val
				}
			}
			for _, r := range strings.Fields(strings.ToLower(rel)) {
				if r != "canonical" {
					continue
				}
				u, err := url.Parse(href)
				if err == nil {
					u = base.ResolveReference(u)
					u.Fragment = ""
					if validURL(u.String()) && strings.EqualFold(u.Hostname(), base.Hostname()) {
						return u.String()
					}
				}
			}
		}
	}
}
