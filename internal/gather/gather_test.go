package gather

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/api"
	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/candidate"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/keys"
	"github.com/irashack/night-desk/internal/store"
)

type extractFunc func(string, []byte) (string, string, error)

func (f extractFunc) Extract(u string, b []byte) (string, string, error) { return f(u, b) }

func TestWorkerEndToEnd(t *testing.T) {
	ctx := context.Background()
	var origin string
	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("worker key leaked to feed")
		}
		fmt.Fprintf(w, `<rss version="2.0"><channel><title>Local Wire</title><link>%s</link><description>News</description>
  <item><title>First</title><link>%s/story?feed=1</link></item>
  <item><title>Copy</title><link>%s/copy</link></item>
  <item><title>Unavailable</title><link>%s/missing</link></item>
  <item><title>Unsafe redirect</title><link>%s/redirect</link></item>
  </channel></rss>`, origin, origin, origin, origin, origin)
	})
	mux.HandleFunc("/story", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("worker key leaked to article")
		}
		fmt.Fprint(w, `<html><head><link rel="canonical" href="/canonical"></head><body><article>Source reporting.</article></body></html>`)
	})
	mux.HandleFunc("/copy", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "copy") })
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.2:12345/blocked", 302)
	})
	site := httptest.NewServer(mux)
	defer site.Close()
	origin = site.URL
	parsed, _ := url.Parse(origin)
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	_, key, err := keys.Issue(ctx, db, "daily", "worker", auth.Read, auth.Gather, auth.Draft)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := auth.Authenticate(ctx, db, key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Gather: config.Gather{AllowPrivate: []string{parsed.Host}}, Papers: []config.Paper{{ID: "daily", Sources: []config.Source{{Feed: origin + "/feed"}}, Sections: []config.Section{{Key: "news", Title: "News"}, {Key: "disabled", Title: "Disabled", Sources: []config.Source{}}}, RetentionDays: 14}}}
	if err = db.Publish(ctx, scope, time.Now().Format("2006-01-02"), &edition.Edition{Schema: 1, Sections: []edition.Section{{Key: "news", Items: []edition.Item{{ID: "a", URL: origin + "/canonical"}}}}}, nil); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&api.Server{Store: db, Config: func() *config.Config { return cfg }}).Handler())
	defer server.Close()
	w, err := New(server.URL, key, extractFunc(func(_ string, _ []byte) (string, string, error) {
		return "Article title", "Original source reporting.", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	w.Groups = func(texts []string) [][]int { return [][]int{{0, 1}} }
	result, err := w.RunOnce(ctx)
	if err != nil || result.Candidates != 4 || result.Unavailable != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	list, err := db.Candidates(ctx, scope, "news", time.Time{})
	if err != nil || len(list) != 4 {
		t.Fatalf("%+v %v", list, err)
	}
	pool := map[string]candidate.Candidate{}
	for _, c := range list {
		pool[c.CanonicalURL] = c
	}
	first := pool[origin+"/canonical"]
	if !first.Repeat || first.State != "ok" || first.WireGroup == nil || len(first.Aliases) != 1 || first.Aliases[0] != origin+"/story?feed=1" {
		t.Fatalf("first=%+v", first)
	}
	if pool[origin+"/copy"].Repeat || pool[origin+"/copy"].WireGroup == nil || *first.WireGroup != *pool[origin+"/copy"].WireGroup {
		t.Fatal("wire/repeat metadata incorrect", pool)
	}
	if list, err := db.Candidates(ctx, scope, "disabled", time.Time{}); err != nil || len(list) != 0 {
		t.Fatal(list, err)
	}
	w.Extractor = extractFunc(func(string, []byte) (string, string, error) { return "Changed", "Replacement source text.", nil })
	if _, err = w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if text, ok, err := db.CandidateText(ctx, scope, origin+"/story?feed=1"); err != nil || !ok || text != "Original source reporting." {
		t.Fatal(text, ok, err)
	}
}

func TestWorkerRefusesPrivateFeedWithoutException(t *testing.T) {
	hits := 0
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; fmt.Fprint(w, "never") }))
	defer site.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Paper{Sources: []config.Source{{Feed: site.URL + "/feed"}}, Sections: []config.Section{{Key: "news"}}})
	}))
	defer server.Close()
	w, err := New(server.URL, "test-key", nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := w.RunOnce(context.Background())
	if err == nil || result.FeedFailures != 1 || hits != 0 {
		t.Fatal(result, err, hits)
	}
}

func TestGuardedResolverClient(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("resolver reached private endpoint") }))
	defer site.Close()
	parsed, _ := url.Parse(site.URL)
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"version":"https://jsonfeed.org/version/1.1","title":"Feed","items":[{"id":"1","url":"https://news.google.com/rss/articles/example","title":"Search result"}]}`)
	}))
	defer feed.Close()
	fp, _ := url.Parse(feed.URL)
	var got candidate.Candidate
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			json.NewEncoder(w).Encode(Paper{Sources: []config.Source{{Feed: feed.URL}}, Sections: []config.Section{{Key: "news"}}, Gather: config.Gather{AllowPrivate: []string{fp.Host}}})
			return
		}
		var batch struct{ Candidates []candidate.Candidate }
		json.NewDecoder(r.Body).Decode(&batch)
		got = batch.Candidates[0]
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	w, err := New(server.URL, "test-key", nil)
	if err != nil {
		t.Fatal(err)
	}
	w.Resolve = func(ctx context.Context, c *http.Client, _ string) (string, error) {
		resp, err := c.Get("http://" + parsed.Host + "/resolve")
		if resp != nil {
			resp.Body.Close()
		}
		return "", err
	}
	result, err := w.RunOnce(context.Background())
	if err != nil || result.Unavailable != 1 || got.State != "unavailable" {
		t.Fatal(result, err, got)
	}
}

func TestReadabilityAndCanonical(t *testing.T) {
	page := []byte(`<html><head><title>A useful report</title></head><body><article><h1>A useful report</h1><p>` + strings.Repeat("Reporters measured the river level and confirmed the result with the local authority. ", 20) + `</p></article></body></html>`)
	title, text, err := (Readability{}).Extract("https://example.com/report", page)
	if err != nil || title != "A useful report" || !strings.Contains(text, "river level") {
		t.Fatal(title, text, err)
	}
	for _, tc := range []struct{ page, want string }{{`<link rel="canonical" href="/real#x">`, "https://example.com/real"}, {`<link rel="canonical" href="http://127.0.0.1/poison">`, "https://example.com/report"}, {`<link rel="canonical" href="https://other.example/report">`, "https://example.com/report"}} {
		if got := canonicalURL("https://example.com/report", []byte(tc.page)); got != tc.want {
			t.Fatal(got, tc.want)
		}
	}
	if got := capText(strings.Repeat("界", 100), 200); !strings.HasSuffix(got, "界") || len(got) > 200 {
		t.Fatal("invalid truncation")
	}
}

func TestServerValidationAndRedirectCredentials(t *testing.T) {
	for _, server := range []string{"http://example.com", "https://user:pass@example.com", "https://example.com/path", "https://example.com?key=value"} {
		if _, err := New(server, "key", nil); err == nil {
			t.Fatal("accepted", server)
		}
	}
	for _, key := range []string{"", "one two", "one\n"} {
		if _, err := New("https://example.com", key, nil); err == nil {
			t.Fatal("accepted key")
		}
	}
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("followed API redirect") }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 302) }))
	defer server.Close()
	worker, err := New(server.URL, "test-key", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = worker.RunOnce(context.Background()); err == nil {
		t.Fatal("followed redirect")
	}
}

func TestWorkerAttemptLimitAndInvalidLinks(t *testing.T) {
	hits := 0
	var origin string
	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<rss version="2.0"><channel><title>News</title><link>%s</link><description>News</description><item><title>Bad</title><link>http://example.com:99999/bad</link></item>`, origin)
		for i := 0; i < MaxItems+10; i++ {
			fmt.Fprintf(w, `<item><title>Copy</title><link>%s/item?n=%d</link></item>`, origin, i)
		}
		fmt.Fprint(w, `</channel></rss>`)
	})
	mux.HandleFunc("/item", func(w http.ResponseWriter, r *http.Request) {
		hits++
		fmt.Fprint(w, `<link rel="canonical" href="/same"><p>Source</p>`)
	})
	site := httptest.NewServer(mux)
	defer site.Close()
	origin = site.URL
	u, _ := url.Parse(site.URL)
	var posted []candidate.Candidate
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			json.NewEncoder(w).Encode(Paper{Sources: []config.Source{{Feed: origin + "/feed"}}, Sections: []config.Section{{Key: "news"}}, Gather: config.Gather{AllowPrivate: []string{u.Host}}})
			return
		}
		var batch struct{ Candidates []candidate.Candidate }
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			t.Error(err)
		}
		posted = append(posted, batch.Candidates...)
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	worker, err := New(server.URL, "test-key", extractFunc(func(string, []byte) (string, string, error) { return "Title", "Text", nil }))
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.RunOnce(context.Background())
	if err != nil || result.Candidates != 1 || hits != MaxItems || len(posted) != 1 || len(posted[0].Aliases) != 100 {
		t.Fatal(result, err, hits, len(posted))
	}
	if problems := candidate.Validate(posted[0]); len(problems) > 0 {
		t.Fatal(problems)
	}
}
