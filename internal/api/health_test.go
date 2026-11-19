package api_test

import (
	"strings"
	"testing"
	"time"
)

func TestGatherHealthEndpoint(t *testing.T) {
	a := setup(t, nil)
	for _, tc := range []struct {
		body string
		code int
		want string
	}{
		{`{"hosts":[{"host":"news.example","ok":3,"failed_http_5xx":1,"json_ld":1},{"host":"wire.example","cooled_off":2}]}`, 200, `"accepted":2`},
		{`{"hosts":[]}`, 200, `"accepted":0`},
		{`{"hosts":[{"host":"News.Example"}]}`, 422, "hosts[0].host"},
		{`{"hosts":[{"host":"https://news.example/story"}]}`, 422, "must be a lowercase host name"},
		{`{"hosts":[{"host":"a.example"},{"host":"a.example"}]}`, 422, "must be unique"},
		{`{"hosts":[{"host":"a.example","ok":-1}]}`, 422, "hosts[0].ok"},
		{`{"hosts":[{"host":"a.example","url":"https://a.example/x"}]}`, 422, "hosts[0].url"},
		{`{"hosts":[{"host":"a.example"}],"text":"x"}`, 422, "text"},
		{`{}`, 422, "must be an array of host counts"},
	} {
		w := a.request("POST", "/api/v1/gather/health", tc.body, a.key)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("%s: %d %s", tc.body, w.Code, w.Body)
		}
	}
	if w := a.request("POST", "/api/v1/gather/health", `{"hosts":[]}`, ""); w.Code != 401 {
		t.Errorf("unauthenticated: %d", w.Code)
	}
	loc, _ := time.LoadLocation("America/Chicago")
	today := time.Now().In(loc).Format("2006-01-02")
	data, err := a.db.Quality(t.Context(), "daily", today, today, nil)
	if err != nil || len(data.FetchHealth) != 2 {
		t.Fatal(data.FetchHealth, err)
	}
	// Hosts with failures and cool-offs come first.
	if data.FetchHealth[0].Host != "wire.example" || data.FetchHealth[1].OK != 3 || data.FetchHealth[1].JSONLD != 1 {
		t.Fatalf("%+v", data.FetchHealth)
	}
}
