package gather

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/api"
	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/fetch"
	"github.com/irashack/night-desk/internal/keys"
	"github.com/irashack/night-desk/internal/store"
)

var long = strings.Repeat("Reporting from the council chamber. ", 12)

type siteLog struct {
	mu   sync.Mutex
	hits map[string]int
}

func (l *siteLog) hit(path string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hits[path]++
	return l.hits[path]
}

func testWorker(t *testing.T, h http.Handler) (*Worker, *httptest.Server, *[]time.Duration, *time.Time) {
	t.Helper()
	site := httptest.NewServer(h)
	t.Cleanup(site.Close)
	w, err := New("http://127.0.0.1:1", "key", extractFunc(func(u string, page []byte) (string, string, error) {
		// The fake extractor returns a plain page's body as its text; an HTML
		// page stands for one whose article Readability cannot find.
		if strings.HasPrefix(string(page), "<html") {
			return "Title", "Subscribe", nil
		}
		return "Title", string(page), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	var delays []time.Duration
	w.now = func() time.Time { return now }
	w.sleep = func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil }
	return w, site, &delays, &now
}

func client(site *httptest.Server) (*http.Client, fetch.Allow) {
	u, _ := url.Parse(site.URL)
	return fetch.New(fetch.Allow{u.Host}), fetch.Allow{u.Host}
}

func TestExtractionFallbacks(t *testing.T) {
	var origin string
	mux := http.NewServeMux()
	mux.HandleFunc("/good", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, long) })
	mux.HandleFunc("/jsonld", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><head><script type="application/ld+json">{"@context":"https://schema.org","@graph":[{"@type":"WebPage"},{"@type":["NewsArticle"],"articleBody":%q}]}</script></head><body>Subscribe</body></html>`, long)
	})
	mux.HandleFunc("/amp-source", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><link rel="amphtml" href="/amp/story"><script type="application/ld+json">{"@type":"WebPage","articleBody":"not an article"}</script></head><body>Subscribe</body></html>`)
	})
	mux.HandleFunc("/amp/story", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, long) })
	mux.HandleFunc("/thin", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "Short teaser.") })
	w, site, _, _ := testWorker(t, mux)
	origin = site.URL
	a := w.articles(client(site))
	for _, tc := range []struct{ path, want string }{
		{"/good", long}, {"/jsonld", long}, {"/amp-source", long}, {"/thin", "Short teaser."},
	} {
		page, final, err := a.read(context.Background(), origin+tc.path)
		if err != nil {
			t.Fatal(tc.path, err)
		}
		if _, text := a.extract(context.Background(), final, page); text != tc.want {
			t.Errorf("%s: got %.40q", tc.path, text)
		}
	}
	h := a.health[hostOf(origin)]
	if h.Readability != 2 || h.JSONLD != 1 || h.AMP != 1 || h.OK != 5 {
		t.Errorf("health %+v", *h)
	}
}

func TestStatusAwareRetries(t *testing.T) {
	log := &siteLog{hits: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/flaky", func(w http.ResponseWriter, r *http.Request) {
		if log.hit(r.URL.Path) == 1 {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, long)
	})
	mux.HandleFunc("/limited", func(w http.ResponseWriter, r *http.Request) {
		if log.hit(r.URL.Path) == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(429)
			return
		}
		fmt.Fprint(w, long)
	})
	mux.HandleFunc("/gone", func(w http.ResponseWriter, r *http.Request) { log.hit(r.URL.Path); w.WriteHeader(404) })
	mux.HandleFunc("/slow-down", func(w http.ResponseWriter, r *http.Request) {
		log.hit(r.URL.Path)
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(429)
	})
	mux.HandleFunc("/refused", func(w http.ResponseWriter, r *http.Request) {
		log.hit(r.URL.Path)
		http.Redirect(w, r, "http://127.0.0.2:9/private", 302)
	})
	mux.HandleFunc("/down", func(w http.ResponseWriter, r *http.Request) { log.hit(r.URL.Path); w.WriteHeader(502) })
	w, site, delays, _ := testWorker(t, mux)
	a := w.articles(client(site))
	ctx := context.Background()
	for _, path := range []string{"/flaky", "/limited"} {
		if _, _, err := a.read(ctx, site.URL+path); err != nil {
			t.Fatal(path, err)
		}
	}
	if fmt.Sprint(*delays) != "[500ms 2s]" {
		t.Errorf("delays %v: backoff, then Retry-After", *delays)
	}
	var status *fetch.StatusError
	if _, _, err := a.read(ctx, site.URL+"/gone"); !errors.As(err, &status) || status.Code != 404 {
		t.Fatal(err)
	}
	if _, _, err := a.read(ctx, site.URL+"/slow-down"); !errors.As(err, &status) || status.Code != 429 {
		t.Fatal(err)
	}
	if _, _, err := a.read(ctx, site.URL+"/refused"); !errors.Is(err, fetch.ErrRefused) {
		t.Fatal(err)
	}
	// Three failures in a row cooled the host off; forget that for the checks below.
	w.hosts = map[string]*hostState{}
	*delays = nil
	if _, _, err := a.read(ctx, site.URL+"/down"); err == nil {
		t.Fatal("502 must fail after its attempts")
	}
	for path, want := range map[string]int{"/flaky": 2, "/limited": 2, "/gone": 1, "/slow-down": 1, "/refused": 1, "/down": articleAttempts} {
		if log.hits[path] != want {
			t.Errorf("%s requested %d times, want %d", path, log.hits[path], want)
		}
	}
	if fmt.Sprint(*delays) != "[500ms 1s]" {
		t.Errorf("5xx backoff %v", *delays)
	}
	h := a.health[hostOf(site.URL)]
	if h.OK != 2 || h.FailedHTTP4xx != 1 || h.FailedHTTP429 != 1 || h.FailedHTTP5xx != 1 || h.FailedNetwork != 1 || h.CooledOff != 0 {
		t.Errorf("health %+v", *h)
	}
	a.retryLeft = 0
	w.hosts = map[string]*hostState{}
	log.hits["/down"] = 0
	a.read(ctx, site.URL+"/down")
	if log.hits["/down"] != 1 {
		t.Error("an exhausted run retry budget must stop retries")
	}
}

func TestHostCoolOff(t *testing.T) {
	log := &siteLog{hits: map[string]int{}}
	w, site, _, now := testWorker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.hit(r.URL.Path)
		if r.URL.Path == "/ok" {
			fmt.Fprint(w, long)
			return
		}
		w.WriteHeader(404)
	}))
	ctx := context.Background()
	a := w.articles(client(site))
	a.read(ctx, site.URL+"/a")
	a.read(ctx, site.URL+"/ok") // a success resets the run of failures
	for _, p := range []string{"/b", "/c", "/d"} {
		a.read(ctx, site.URL+p)
	}
	if _, _, err := a.read(ctx, site.URL+"/e"); !errors.Is(err, errCoolingOff) || log.hits["/e"] != 0 {
		t.Fatalf("expected cool-off after %d consecutive failures: %v", coolOffFailures, err)
	}
	// Cool-off outlives the run, within this worker process.
	b := w.articles(client(site))
	if _, _, err := b.read(ctx, site.URL+"/ok"); !errors.Is(err, errCoolingOff) {
		t.Fatal("cool-off must carry to the next run", err)
	}
	*now = now.Add(coolOffPeriod)
	if _, _, err := b.read(ctx, site.URL+"/ok"); err != nil {
		t.Fatal("cool-off must expire", err)
	}
	if a.health[hostOf(site.URL)].CooledOff != 1 || b.health[hostOf(site.URL)].CooledOff != 1 {
		t.Error("cool-offs must be counted")
	}
}

func TestFetchHealthReachesQualityReport(t *testing.T) {
	ctx := context.Background()
	var origin string
	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<rss version="2.0"><channel><title>Wire</title><link>%[1]s</link><description>News</description>
  <item><title>One</title><link>%[1]s/one</link></item>
  <item><title>Two</title><link>%[1]s/two</link></item>
  </channel></rss>`, origin)
	})
	mux.HandleFunc("/one", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, long) })
	mux.HandleFunc("/two", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
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
	_, key, err := keys.Issue(ctx, db, "daily", "worker", auth.Read, auth.Gather)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Gather: config.Gather{AllowPrivate: []string{parsed.Host}}, Papers: []config.Paper{{ID: "daily", Timezone: "UTC", Sources: []config.Source{{Feed: origin + "/feed"}}, Sections: []config.Section{{Key: "news", Title: "News"}}, RetentionDays: 14}}}
	server := httptest.NewServer((&api.Server{Store: db, Config: func() *config.Config { return cfg }}).Handler())
	defer server.Close()
	w, err := New(server.URL, key, extractFunc(func(_ string, page []byte) (string, string, error) { return "", string(page), nil }))
	if err != nil {
		t.Fatal(err)
	}
	w.Groups = func([]string) [][]int { return nil }
	result, err := w.RunOnce(ctx)
	if err != nil || len(result.Health) != 1 || result.Health[0].OK != 1 || result.Health[0].FailedHTTP4xx != 1 || result.Health[0].Readability != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	today := time.Now().UTC().Format("2006-01-02")
	data, err := db.Quality(ctx, "daily", today, today, nil)
	if err != nil || len(data.FetchHealth) != 1 {
		t.Fatal(data.FetchHealth, err)
	}
	if h := data.FetchHealth[0]; h.Host != "127.0.0.1" || h.OK != 1 || h.Failed() != 1 {
		t.Fatalf("%+v", h)
	}
}
