package gather

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/gnews"
	"github.com/irashack/night-desk/internal/lean"
)

func TestSectionCoverageSearches(t *testing.T) {
	p := Paper{Sources: []config.Source{{Feed: "https://www.existing.example/rss"}, {GoogleNews: &config.GoogleNews{Site: "search.example", Query: "existing query"}}}}
	section := config.Section{Key: "politics", Title: "National politics", Lean: true}
	outlets := []lean.Outlet{{Domain: "existing.example", Lean: lean.Left}, {Domain: "search.example", Lean: lean.Right}}
	for _, side := range []lean.Lean{lean.LeanLeft, lean.Center, lean.LeanRight} {
		for i := 0; i < 5; i++ {
			outlets = append(outlets, lean.Outlet{Domain: fmt.Sprintf("%s%d.example", side, i), Lean: side})
		}
	}
	sources := sectionSources(p, section, outlets)
	if len(sources) != 11 { // original two, plus three on each side
		t.Fatalf("search bound: %+v", sources)
	}
	for _, source := range sources[2:] {
		if source.GoogleNews == nil || source.GoogleNews.Query != section.Title || source.GoogleNews.Site == "existing.example" || source.GoogleNews.Site == "search.example" {
			t.Fatalf("bad side search: %+v", source)
		}
		u := gnews.SearchFeedURL(source.GoogleNews.Site, source.GoogleNews.Query, "en", "US")
		if u == "" {
			t.Fatal("empty search URL")
		}
	}
	if got := sectionSources(p, section, nil); !reflect.DeepEqual(got, p.Sources) {
		t.Fatal("searches without ratings", got)
	}
	section.Lean = false
	if got := sectionSources(p, section, outlets); !reflect.DeepEqual(got, p.Sources) {
		t.Fatal("searches in disabled section", got)
	}
	section.Sources = []config.Source{{Feed: "https://section.example/feed"}}
	if got := sectionSources(p, section, outlets); !reflect.DeepEqual(got, section.Sources) {
		t.Fatal("lost section source override", got)
	}
	section.Lean = true
	if got := sectionSources(p, section, outlets); len(got) != 10 || got[0].Feed != section.Sources[0].Feed {
		t.Fatal("searches ignored section override", got)
	}
}

func TestWorkerFetchesRatingsOnlyForEnabledSections(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			reads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/paper":
					json.NewEncoder(w).Encode(Paper{Sections: []config.Section{{Key: "news", Title: "News", Lean: enabled}, {Key: "other", Title: "Other", Lean: enabled}}})
				case "/api/v1/lean/outlets":
					reads++
					fmt.Fprint(w, `{"outlets":[]}`)
				default:
					t.Error("unexpected worker request", r.URL.Path)
				}
			}))
			defer server.Close()
			worker, err := New(server.URL, "worker-test-key", nil)
			if err != nil {
				t.Fatal(err)
			}
			if result, err := worker.RunOnce(context.Background()); err != nil || result.Candidates != 0 {
				t.Fatal(result, err)
			}
			want := 0
			if enabled {
				want = 1
			}
			if reads != want {
				t.Fatalf("outlets reads=%d want=%d", reads, want)
			}
		})
	}
}

func TestCoverageSearchesDeduplicatePublisherDomains(t *testing.T) {
	p := Paper{Sources: []config.Source{{Feed: "https://feeds.alternate.example/rss"}}}
	section := config.Section{Key: "news", Title: "News", Lean: true}
	outlets := []lean.Outlet{
		{Domain: "main.example", Lean: lean.Left, Outlet: "monitored"},
		{Domain: "alternate.example", Lean: lean.Left, Outlet: "monitored"},
		{Domain: "other.example", Lean: lean.Right, Outlet: "other"},
		{Domain: "other-alias.example", Lean: lean.Right, Outlet: "other"},
	}
	got := sectionSources(p, section, outlets)
	if len(got) != 2 || got[1].GoogleNews.Site != "other.example" {
		t.Fatalf("publisher got duplicate search: %+v", got)
	}
}

func TestNewsSearchLocale(t *testing.T) {
	for locale, want := range map[string]string{"": "ceid=US%3Aen&gl=US&hl=en-US", "en-GB": "ceid=GB%3Aen&gl=GB&hl=en-GB", "de-DE": "ceid=DE%3Ade&gl=DE&hl=de-DE"} {
		l, r := Paper{Locale: locale}.NewsLocale()
		if u := gnews.SearchFeedURL("", "World", l, r); !strings.Contains(u, want) {
			t.Errorf("%q: %s", locale, u)
		}
	}
}
