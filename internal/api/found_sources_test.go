package api_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/config"
)

func TestFoundSourcesEndpoint(t *testing.T) {
	a := setup(t, nil)
	a.config.Papers[0].Sections = append(a.config.Papers[0].Sections,
		config.Section{Key: "gardening", Title: "Gardening", FindSources: true},
		config.Section{Key: "declared", Title: "Declared", FindSources: true, Sources: []config.Source{{Feed: "https://declared.example/feed"}}})
	feed := func(i int) string {
		return fmt.Sprintf(`{"feed":"https://g%d.example/rss","outlet":"Garden %d"}`, i, i)
	}
	for _, tc := range []struct {
		section, body string
		code          int
		want          string
	}{
		{"economy", `{"sources":[]}`, 404, "no section that finds"},
		{"declared", `{"sources":[]}`, 404, "no section that finds"},
		{"gardening", `{"sources":[` + feed(1) + `,` + feed(2) + `]}`, 200, `"accepted":2`},
		{"gardening", `{"sources":[` + feed(2) + `]}`, 200, `"accepted":0`},
		{"gardening", `{"sources":[]}`, 200, `"accepted":0`},
		{"gardening", `{"sources":[` + feed(3) + `,` + feed(4) + `,` + feed(5) + `,` + feed(6) + `]}`, 200, `"accepted":3`},
		{"gardening", `{"sources":[{"feed":"ftp://x.example/rss","outlet":"X"}]}`, 422, "sources[0].feed"},
		{"gardening", `{"sources":[{"feed":"https://x.example/rss","outlet":" X"}]}`, 422, "sources[0].outlet"},
		{"gardening", `{"sources":[{"feed":"https://x.example/rss","outlet":"X","text":"y"}]}`, 422, "sources[0].text"},
		{"gardening", `{"sources":[` + feed(7) + `,` + feed(7) + `]}`, 422, "must be unique"},
		{"gardening", `{}`, 422, "must be an array of feeds"},
	} {
		w := a.request("POST", "/api/v1/sections/"+tc.section+"/sources", tc.body, a.key)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("%s %s: %d %s", tc.section, tc.body, w.Code, w.Body)
		}
	}
	w := a.request("GET", "/api/v1/paper", "", a.key)
	body := w.Body.String()
	if !strings.Contains(body, `"found_sources":[{"key":"gardening","sources":[{"feed":"https://g1.example/rss"`) || strings.Contains(body, `"key":"declared","sources":[{"feed":"https://g`) || !strings.Contains(body, `"attempted_at"`) {
		t.Fatalf("paper view: %s", body)
	}
	if strings.Count(body, "https://g") != 5 {
		t.Error("a section keeps at most five found feeds")
	}
}
