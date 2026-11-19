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
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/keys"
	"github.com/irashack/night-desk/internal/model"
	"github.com/irashack/night-desk/internal/store"
)

func TestFindSourcesVerifiesSuggestions(t *testing.T) {
	ctx := context.Background()
	var origin string
	feedHits := map[string]int{}
	site := http.NewServeMux()
	site.HandleFunc("/garden.xml", func(w http.ResponseWriter, r *http.Request) {
		feedHits[r.URL.Path]++
		fmt.Fprint(w, `<rss version="2.0"><channel><title>Garden Weekly</title><link>https://garden.example</link><description>Gardens</description><item><title>Planting garlic</title><link>https://garden.example/garlic</link></item></channel></rss>`)
	})
	site.HandleFunc("/empty.xml", func(w http.ResponseWriter, r *http.Request) {
		feedHits[r.URL.Path]++
		fmt.Fprint(w, `<rss version="2.0"><channel><title>Empty</title><link>https://e.example</link><description>x</description></channel></rss>`)
	})
	site.HandleFunc("/page.html", func(w http.ResponseWriter, r *http.Request) {
		feedHits[r.URL.Path]++
		fmt.Fprint(w, `<html><body>Not a feed</body></html>`)
	})
	siteServer := httptest.NewServer(site)
	defer siteServer.Close()
	origin = siteServer.URL
	parsed, _ := url.Parse(origin)

	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req struct {
			Messages []model.Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if len(req.Messages) != 2 || !strings.Contains(req.Messages[1].Content, `"section_title":"Gardening"`) || !strings.Contains(req.Messages[1].Content, `"paper_place":"Riverside"`) {
			t.Error("section missing from discovery input")
		}
		content := fmt.Sprintf(`{"feeds":[{"outlet":"Garden Weekly","feed":"%[1]s/garden.xml"},{"outlet":"Empty","feed":"%[1]s/empty.xml"},{"outlet":"Page","feed":"%[1]s/page.html"},{"outlet":"Missing","feed":"%[1]s/missing.xml"},{"outlet":"Google","feed":"https://news.google.com/rss/search?q=garden"},{"outlet":"Garden Weekly","feed":"%[1]s/garden.xml"},{"outlet":"Bad","feed":"ftp://garden.example/feed"}]}`, origin)
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}, "finish_reason": "stop"}}, "usage": model.Usage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150}})
	}))
	defer provider.Close()

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
	brief := "Home gardening in a humid subtropical climate."
	cfg := &config.Config{Gather: config.Gather{AllowPrivate: []string{parsed.Host}}, Papers: []config.Paper{{ID: "daily", Place: "Riverside", Timezone: "UTC", Sections: []config.Section{
		{Key: "news", Title: "News"},
		{Key: "gardening", Title: "Gardening", Brief: &brief, FindSources: true},
		{Key: "declared", Title: "Declared", FindSources: true, Sources: []config.Source{{Feed: "https://declared.example/feed"}}},
	}}}}
	server := httptest.NewServer((&api.Server{Store: db, Config: func() *config.Config { return cfg }}).Handler())
	defer server.Close()
	w, err := New(server.URL, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	tier := model.TierConfig{BaseURL: provider.URL, Model: "fast"}
	dc := DraftConfig{Models: &model.Config{Fast: tier, Writer: tier}}
	// The server stamps attempts with its own clock, as in production.
	now := time.Now()
	kept, err := w.FindSourcesOnce(ctx, dc, model.Options{}, now)
	if err != nil || kept != 1 || calls != 1 {
		t.Fatalf("kept=%d calls=%d err=%v", kept, calls, err)
	}
	if feedHits["/garden.xml"] != 1 {
		t.Errorf("a duplicate suggestion must be fetched once: %v", feedHits)
	}
	found, err := db.FoundSources(ctx, "daily", []string{"gardening", "declared"})
	if err != nil || len(found[0].Sources) != 1 || found[0].Sources[0].Feed != origin+"/garden.xml" || found[0].Sources[0].Outlet != "Garden Weekly" || found[0].AttemptedAt == "" {
		t.Fatalf("%+v %v", found, err)
	}
	if len(found[1].Sources) != 0 || found[1].AttemptedAt != "" {
		t.Error("a section with declared sources does not find its own")
	}
	// Within a day the section is not asked again; after a day it is.
	if _, err = w.FindSourcesOnce(ctx, dc, model.Options{}, now.Add(23*time.Hour)); err != nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	if _, err = w.FindSourcesOnce(ctx, dc, model.Options{}, now.Add(25*time.Hour)); err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	// Without models there is no discovery.
	if kept, err = w.FindSourcesOnce(ctx, DraftConfig{}, model.Options{}, now.Add(72*time.Hour)); kept != 0 || err != nil || calls != 2 {
		t.Fatal(kept, err, calls)
	}
}

func TestFindingSectionSources(t *testing.T) {
	paperFeed := config.Source{Feed: "https://paper.example/feed"}
	p := Paper{Sources: []config.Source{paperFeed}}
	section := config.Section{Key: "gardening", Title: "Gardening", FindSources: true}
	got := sectionSources(p, section, nil)
	if len(got) != 2 || got[0] != paperFeed || got[1].GoogleNews == nil || got[1].GoogleNews.Query != "Gardening" || got[1].GoogleNews.Site != "" {
		t.Fatalf("a new finding section gets the paper's feeds and a topic search: %+v", got)
	}
	p.FoundSources = []store.SectionSources{{Key: "gardening", Sources: []store.FoundSource{{Feed: "https://a.example/rss"}, {Feed: "https://b.example/rss"}}}}
	if got = sectionSources(p, section, nil); len(got) != 4 || got[3].GoogleNews == nil {
		t.Fatalf("found feeds join; the search stays until three are found: %+v", got)
	}
	p.FoundSources[0].Sources = append(p.FoundSources[0].Sources, store.FoundSource{Feed: "https://c.example/rss"})
	got = sectionSources(p, section, nil)
	if len(got) != 4 {
		t.Fatalf("three found feeds retire the search: %+v", got)
	}
	for _, s := range got {
		if s.GoogleNews != nil {
			t.Fatal("search feed still present")
		}
	}
	if got := sectionSources(p, config.Section{Key: "news", Title: "News"}, nil); len(got) != 1 {
		t.Fatalf("other sections are unchanged: %+v", got)
	}
	declared := config.Section{Key: "gardening", Title: "Gardening", FindSources: true, Sources: []config.Source{}}
	if got := sectionSources(p, declared, nil); len(got) != 0 {
		t.Fatal("declared sources, even none, win over finding")
	}
}
