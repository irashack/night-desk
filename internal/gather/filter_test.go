package gather

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/api"
	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/keys"
	"github.com/irashack/night-desk/internal/store"
)

func TestBuiltinFilters(t *testing.T) {
	names := []string{}
	for _, b := range builtinFilters {
		names = append(names, b.name)
	}
	if !slices.Equal(names, config.IngestFilters) {
		t.Fatalf("built-in rules %v must match config.IngestFilters %v", names, config.IngestFilters)
	}
	f := newFilter(nil)
	for _, tc := range []struct{ title, link, want string }{
		{"Chiefs vs. Bills best bets, picks and odds", "https://x.test/a", "betting"},
		{"NBA player props for Tuesday", "https://x.test/a", "betting"},
		{"College football picks against the spread, Week 9", "https://x.test/a", "betting"},
		{"DraftKings promo code: get $200 in bonus bets", "https://x.test/a", "betting"},
		{"Week 9 waiver wire: five pickups", "https://x.test/a", "fantasy"},
		{"Start/sit decisions for Week 9", "https://x.test/a", "fantasy"},
		{"Fantasy football rankings: running backs", "https://x.test/a", "fantasy"},
		{"Morning briefing: what to know today", "https://x.test/a", "roundup"},
		{"What we're reading this weekend", "https://x.test/a", "roundup"},
		{"In case you missed it: the week's stories", "https://x.test/a", "roundup"},
		{"Editorial cartoon: the budget standoff", "https://x.test/a", "cartoon"},
		{"Cartoon: Election night", "https://x.test/a", "cartoon"},
		{"Tuesday", "https://x.test/opinion/cartoons/2026/11/02/tuesday", "cartoon"},
		// Near-misses: news about these subjects is kept.
		{"State lawmakers weigh legal sports betting expansion", "https://x.test/a", ""},
		{"Odds of a shutdown rise as talks stall", "https://x.test/a", ""},
		{"DraftKings shares fall after earnings miss", "https://x.test/a", ""},
		{"Fantasy author wins national book award", "https://x.test/a", ""},
		{"Cartoonist's archive goes to the state library", "https://x.test/a", ""},
		{"Governor delivers a morning briefing on the storm", "https://x.test/a", ""},
		{"The defense picks apart the offense", "https://x.test/a", ""},
		{"Startup sits on a cash pile", "https://x.test/a", ""},
	} {
		if got := f.drop(tc.title, tc.link, "Wire"); got != tc.want {
			t.Errorf("%q %s: got %q, want %q", tc.title, tc.link, got, tc.want)
		}
	}
}

func TestPaperFilters(t *testing.T) {
	f := newFilter(&config.Filters{BlockedOutlets: []string{"Daily Picks", "tabloid.example"}, BlockedKeywords: []string{"horoscope", "C++", "#ad"}, Disabled: []string{"roundup"}})
	for _, tc := range []struct{ title, link, outlet, want string }{
		{"A story", "https://tabloid.example/a", "Wire", "blocked_outlet"},
		{"A story", "https://news.TABLOID.example./a", "Wire", "blocked_outlet"},
		{"A story", "https://nottabloid.example/a", "Wire", ""},
		{"A story", "https://x.test/a", "daily picks", "blocked_outlet"},
		{"Your Horoscope for Tuesday", "https://x.test/a", "Wire", "blocked_keyword"},
		{"Horoscopes are back", "https://x.test/a", "Wire", ""},
		{"Why C++ still matters", "https://x.test/a", "Wire", "blocked_keyword"},
		{"New shoes #ad", "https://x.test/a", "Wire", "blocked_keyword"},
		{"Morning briefing: what to know", "https://x.test/a", "Wire", ""},
		{"Best bets for Sunday", "https://x.test/a", "Wire", "betting"},
	} {
		if got := f.drop(tc.title, tc.link, tc.outlet); got != tc.want {
			t.Errorf("%q %s %q: got %q, want %q", tc.title, tc.link, tc.outlet, got, tc.want)
		}
	}
}

func TestWorkerDropsFilteredItemsBeforeFetching(t *testing.T) {
	ctx := context.Background()
	var origin string
	var mu sync.Mutex
	fetched := map[string]bool{}
	mux := http.NewServeMux()
	mux.HandleFunc("/feed", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<rss version="2.0"><channel><title>Local Wire</title><link>%[1]s</link><description>News</description>
  <item><title>Council passes budget</title><link>%[1]s/budget</link></item>
  <item><title>Best bets for Sunday</title><link>%[1]s/bets</link></item>
  <item><title>Week 9 waiver wire</title><link>%[1]s/waiver</link></item>
  <item><title>Your horoscope</title><link>%[1]s/stars</link></item>
  <item><title>Tuesday</title><link>%[1]s/cartoons/tuesday</link></item>
  <item><title>Morning briefing: what to know</title><link>%[1]s/briefing</link></item>
  </channel></rss>`, origin)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fetched[r.URL.Path] = true
		mu.Unlock()
		fmt.Fprint(w, `<html><body><article>Reporting.</article></body></html>`)
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
	_, key, err := keys.Issue(ctx, db, "daily", "worker", auth.Read, auth.Gather)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Gather: config.Gather{AllowPrivate: []string{parsed.Host}}, Papers: []config.Paper{{ID: "daily", Timezone: "America/Chicago", Sources: []config.Source{{Feed: origin + "/feed"}}, Sections: []config.Section{{Key: "news", Title: "News"}}, RetentionDays: 14,
		Filters: &config.Filters{BlockedKeywords: []string{"horoscope"}, Disabled: []string{"roundup"}}}}}
	server := httptest.NewServer((&api.Server{Store: db, Config: func() *config.Config { return cfg }}).Handler())
	defer server.Close()
	w, err := New(server.URL, key, extractFunc(func(_ string, _ []byte) (string, string, error) { return "", "Reporting.", nil }))
	if err != nil {
		t.Fatal(err)
	}
	w.Groups = func([]string) [][]int { return nil }
	result, err := w.RunOnce(ctx)
	if err != nil || result.Candidates != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	want := map[string]int{"betting": 1, "fantasy": 1, "blocked_keyword": 1, "cartoon": 1}
	if fmt.Sprint(result.Dropped) != fmt.Sprint(want) {
		t.Errorf("dropped %v, want %v", result.Dropped, want)
	}
	for _, path := range []string{"/bets", "/waiver", "/stars", "/cartoons/tuesday"} {
		if fetched[path] {
			t.Errorf("dropped item %s was fetched", path)
		}
	}
	if !fetched["/budget"] || !fetched["/briefing"] {
		t.Errorf("kept items must be fetched: %v", fetched)
	}
	if _, err = w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("America/Chicago")
	today := time.Now().In(loc).Format("2006-01-02")
	data, err := db.Quality(ctx, "daily", today, today, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(data.DroppedByRule) != fmt.Sprint(map[string]int{"betting": 2, "fantasy": 2, "blocked_keyword": 2, "cartoon": 2}) {
		t.Errorf("quality counts accumulate across runs: %v", data.DroppedByRule)
	}
	for _, rule := range []string{"roundup", "blocked_outlet"} {
		if _, ok := data.DroppedByRule[rule]; ok || strings.Contains(fmt.Sprint(data), "horoscope") {
			t.Errorf("unexpected %s count or content in report", rule)
		}
	}
}
