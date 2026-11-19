package gnews

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/testhttp"
)

func TestSearchFeedURL(t *testing.T) {
	got := SearchFeedURL("cnn.com", "city council", "", "")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "news.google.com" || u.Path != "/rss/search" {
		t.Fatalf("unexpected search URL %q", got)
	}
	q := u.Query()
	want := map[string]string{
		"q":    "site:cnn.com city council",
		"hl":   "en-US",
		"gl":   "US",
		"ceid": "US:en",
	}
	for key, value := range want {
		if q.Get(key) != value {
			t.Errorf("%s = %q, want %q", key, q.Get(key), value)
		}
	}

	got = SearchFeedURL("news.example", "& election", "en-GB", "GB")
	u, err = url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	q = u.Query()
	if q.Get("q") != "site:news.example & election" || q.Get("hl") != "en-GB" || q.Get("gl") != "GB" || q.Get("ceid") != "GB:en" {
		t.Fatalf("custom search URL: %q", got)
	}

	got = SearchFeedURL("", "election", "fr", "CA")
	u, err = url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if q = u.Query(); q.Get("q") != "election" || q.Get("hl") != "fr" || q.Get("gl") != "CA" || q.Get("ceid") != "CA:fr" {
		t.Fatalf("query-only search URL: %q", got)
	}
}

func TestIsFeed(t *testing.T) {
	for _, tt := range []struct {
		url  string
		want bool
	}{
		{"https://news.google.com/rss/search?q=site:cnn.com", true},
		{"https://news.google.com/rss/articles/abc", true},
		{"https://example.com/news.google.com/feed", true},
		{"https://example.com/rss", false},
		{"", false},
	} {
		if got := IsFeed(tt.url); got != tt.want {
			t.Errorf("IsFeed(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

func TestArticleID(t *testing.T) {
	for _, tt := range []struct {
		link string
		id   string
		ok   bool
	}{
		{"https://news.google.com/rss/articles/CBMiABC_def-123?oc=5", "CBMiABC_def-123", true},
		{"https://news.google.com/articles/abc", "abc", true},
		{"http://news.google.com/rss/articles/abc/path", "abc", true},
		{"https://example.com/rss/articles/abc", "", false},
		{"https://news.google.com/rss/search?q=abc", "", false},
		{"", "", false},
	} {
		id, ok := ArticleID(tt.link)
		if id != tt.id || ok != tt.ok {
			t.Errorf("ArticleID(%q) = %q, %v; want %q, %v", tt.link, id, ok, tt.id, tt.ok)
		}
	}
}

func TestStripSourceSuffix(t *testing.T) {
	for _, tt := range []struct{ title, source, want string }{
		{"Headline - CNN", "CNN", "Headline"},
		{"  Headline - CNN  ", "CNN", "Headline"},
		{"Headline - CNN - CNN", "CNN", "Headline - CNN"},
		{"A - B - WSJ", "The Wall Street Journal", "A - B - WSJ"},
		{"Headline - CNBC", "CNN", "Headline - CNBC"},
		{"Headline - Local", "", "Headline - Local"},
		{" - CNN", "CNN", "- CNN"},
		{"Headline", "CNN", "Headline"},
	} {
		if got := StripSourceSuffix(tt.title, tt.source); got != tt.want {
			t.Errorf("StripSourceSuffix(%q, %q) = %q, want %q", tt.title, tt.source, got, tt.want)
		}
	}
}

func TestParseSignaturePage(t *testing.T) {
	signature, timestamp, ok := parseSignaturePage(`<div data-n-a-ts="1700000000" data-n-a-sg="sig-abc_123"></div>`)
	if !ok || signature != "sig-abc_123" || timestamp != 1700000000 {
		t.Fatalf("got %q, %d, %v", signature, timestamp, ok)
	}
	for _, html := range []string{
		"",
		`<div data-n-a-sg="sig"></div>`,
		`<div data-n-a-ts="1700000000"></div>`,
		`<div data-n-a-sg="sig" data-n-a-ts="not-a-number"></div>`,
	} {
		if _, _, ok := parseSignaturePage(html); ok {
			t.Errorf("parseSignaturePage(%q) succeeded", html)
		}
	}
}

func TestBuildBatchBody(t *testing.T) {
	body := buildBatchBody("gid-123", 1700000000, "sig-abc")
	if !strings.HasPrefix(body, "f.req=") {
		t.Fatalf("body does not start with f.req: %q", body)
	}
	values, err := url.ParseQuery(body)
	if err != nil {
		t.Fatal(err)
	}
	var payload []any
	if err := json.Unmarshal([]byte(values.Get("f.req")), &payload); err != nil {
		t.Fatal(err)
	}
	outer, ok := payload[0].([]any)
	if !ok || len(outer) != 1 {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	call, ok := outer[0].([]any)
	if !ok || len(call) != 4 || call[0] != "Fbv4je" || call[2] != nil || call[3] != "generic" {
		t.Fatalf("unexpected call: %#v", outer[0])
	}
	encoded, ok := call[1].(string)
	if !ok {
		t.Fatalf("inner request is %T", call[1])
	}
	var inner []any
	if err := json.Unmarshal([]byte(encoded), &inner); err != nil {
		t.Fatal(err)
	}
	if len(inner) != 5 || inner[0] != "garturlreq" || inner[2] != "gid-123" || inner[3] != float64(1700000000) || inner[4] != "sig-abc" {
		t.Fatalf("unexpected inner request: %#v", inner)
	}
}

func batchAnswer(publisher string) string {
	inner, _ := json.Marshal([]any{"garturlres", publisher, nil})
	row, _ := json.Marshal([]any{"wrb.fr", "Fbv4je", string(inner), nil, nil, nil, "generic"})
	outer, _ := json.Marshal([]any{json.RawMessage(row), nil, nil})
	return ")]}'\n\n" + string(outer)
}

func TestParseBatchResponse(t *testing.T) {
	if got := parseBatchResponse(batchAnswer("https://publisher.example/story")); got != "https://publisher.example/story" {
		t.Fatalf("got %q", got)
	}
	// A short outer array without Google's trailing metadata is still usable.
	inner, _ := json.Marshal([]any{"garturlres", "https://publisher.example/short"})
	row, _ := json.Marshal([]any{"wrb.fr", "Fbv4je", string(inner)})
	short, _ := json.Marshal([]any{json.RawMessage(row)})
	if got := parseBatchResponse(")]}'\n\n" + string(short)); got != "https://publisher.example/short" {
		t.Fatalf("short answer: %q", got)
	}
	if got := parseBatchResponse(strings.Replace(batchAnswer("https://publisher.example/crlf"), "\n\n", "\r\n\r\n", 1)); got != "https://publisher.example/crlf" {
		t.Fatalf("CRLF answer: %q", got)
	}
	// parseBatchResponse accepts http because Resolve, not the parser, enforces
	// https on the final result.
	if got := parseBatchResponse(batchAnswer("http://publisher.example/story")); got != "http://publisher.example/story" {
		t.Errorf("http publisher = %q", got)
	}
	for _, answer := range []string{
		"",
		"no blank line",
		")]}'\n\nnot json",
		batchAnswer("https://news.google.com/rss/articles/abc"),
		batchAnswer("javascript:alert(1)"),
	} {
		if got := parseBatchResponse(answer); got != "" {
			t.Errorf("parseBatchResponse(%q) = %q, want empty", answer, got)
		}
	}
}

func newResolveServer(t *testing.T, handler http.HandlerFunc) (*http.Client, string) {
	t.Helper()
	network := testhttp.New()
	srv := network.Server(handler)
	t.Cleanup(srv.Close)
	old := googleNewsBaseURL
	googleNewsBaseURL = srv.URL
	t.Cleanup(func() { googleNewsBaseURL = old })
	return &http.Client{Transport: network.Transport()}, srv.URL
}

func TestResolve(t *testing.T) {
	const gid = "CBMiABC_def-123"
	client, _ := newResolveServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rss/articles/"+gid:
			if !strings.Contains(r.Header.Get("User-Agent"), "Mozilla/5.0") {
				t.Errorf("article page user agent = %q", r.Header.Get("User-Agent"))
			}
			fmt.Fprint(w, `<html><div data-n-a-sg="sig-abc_123" data-n-a-ts="1700000000"></div></html>`)
		case r.Method == http.MethodPost && r.URL.Path == batchPath:
			if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/x-www-form-urlencoded") {
				t.Errorf("content type = %q", got)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			values, err := url.ParseQuery(string(body))
			if err != nil {
				t.Error(err)
				return
			}
			if values.Get("f.req") == "" {
				t.Error("missing f.req")
				return
			}
			fmt.Fprint(w, batchAnswer("https://publisher.example/story?x=1"))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	got, err := Resolve(context.Background(), client, "https://news.google.com/rss/articles/"+gid+"?oc=5")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://publisher.example/story?x=1" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveErrors(t *testing.T) {
	client, _ := newResolveServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})
	if _, err := Resolve(context.Background(), client, "https://example.com/story"); err == nil {
		t.Error("non-Google link succeeded")
	}
	if _, err := Resolve(context.Background(), nil, "https://news.google.com/rss/articles/abc"); err == nil {
		t.Error("nil client succeeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Resolve(ctx, client, "https://news.google.com/rss/articles/abc"); err == nil {
		t.Error("canceled context succeeded")
	}

	for _, tt := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"article status", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusBadGateway) }},
		{"no signature", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html>nothing</html>") }},
		{"article too large", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(bytes.Repeat([]byte("x"), maxResponseBytes+1))
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := newResolveServer(t, tt.handler)
			if _, err := Resolve(context.Background(), client, "https://news.google.com/rss/articles/abc"); err == nil {
				t.Fatal("Resolve succeeded")
			}
		})
	}

	for _, tt := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"lookup status", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				fmt.Fprint(w, `<div data-n-a-sg="sig" data-n-a-ts="1"></div>`)
				return
			}
			http.Error(w, "no", http.StatusTooManyRequests)
		}},
		{"lookup too large", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				fmt.Fprint(w, `<div data-n-a-sg="sig" data-n-a-ts="1"></div>`)
				return
			}
			_, _ = w.Write(bytes.Repeat([]byte("x"), maxResponseBytes+1))
		}},
		{"bad answer", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				fmt.Fprint(w, `<div data-n-a-sg="sig" data-n-a-ts="1"></div>`)
				return
			}
			fmt.Fprint(w, ")])}'\n\nnot json")
		}},
		{"http publisher", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				fmt.Fprint(w, `<div data-n-a-sg="sig" data-n-a-ts="1"></div>`)
				return
			}
			fmt.Fprint(w, batchAnswer("http://publisher.example/story"))
		}},
		{"google publisher", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				fmt.Fprint(w, `<div data-n-a-sg="sig" data-n-a-ts="1"></div>`)
				return
			}
			fmt.Fprint(w, batchAnswer("https://news.google.com/rss/articles/other"))
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := newResolveServer(t, tt.handler)
			if _, err := Resolve(context.Background(), client, "https://news.google.com/rss/articles/abc"); err == nil {
				t.Fatal("Resolve succeeded")
			}
		})
	}
}
