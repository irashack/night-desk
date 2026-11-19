package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/irashack/night-desk/internal/auth"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/candidate"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/keys"
)

func apiCandidate() candidate.Candidate {
	return candidate.Candidate{
		Section: "economy", CanonicalURL: "https://example.com/article",
		Aliases: []string{"https://example.com/redirect"}, Outlet: "Example",
		Title: "Original report", FetchedAt: time.Now().UTC(),
		Byline: []string{"Jane Reporter"}, Text: "Original article text.", State: "ok",
	}
}

func candidateBatch(t *testing.T, list ...candidate.Candidate) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"candidates": list})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func candidatePool(t *testing.T, a *app, query, key string) []candidate.Candidate {
	t.Helper()
	w := a.request("GET", "/api/v1/candidates"+query, "", key)
	var result struct {
		Candidates []candidate.Candidate `json:"candidates"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Candidates == nil {
		t.Fatalf("candidate pool: %d %s", w.Code, w.Body)
	}
	return result.Candidates
}

func TestCandidateAPIImmutableAliasesAndTenantScope(t *testing.T) {
	a := setup(t, nil)
	a.config.Papers = append(a.config.Papers, config.Paper{ID: "other", Timezone: "UTC", Sections: []config.Section{{Key: "economy", Title: "Economy"}}})
	_, otherKey, err := keys.Issue(context.Background(), a.db, "other", "test", auth.Read, auth.Gather)
	if err != nil {
		t.Fatal(err)
	}
	original := apiCandidate()
	w := a.request("POST", "/api/v1/candidates", candidateBatch(t, original), a.key)
	var response struct{ Accepted int }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Accepted != 1 {
		t.Fatal(w.Code, w.Body)
	}
	pool := candidatePool(t, a, "", a.key)
	if len(pool) != 1 || pool[0].ID == 0 {
		t.Fatal(pool)
	}
	original.ID = pool[0].ID
	if !reflect.DeepEqual(pool[0], original) {
		t.Fatalf("snapshot changed: got %#v, want %#v", pool[0], original)
	}
	if pool := candidatePool(t, a, "", otherKey); len(pool) != 0 {
		t.Fatal("other tenant read candidate", pool)
	}
	changed := original
	changed.Text, changed.Title, changed.Section = "Replacement article text.", "Replacement title", "health"
	changed.Aliases = []string{original.Aliases[0], "https://example.com/new-alias", original.CanonicalURL}
	changed.FetchedAt = time.Now().UTC().Add(time.Hour)
	if w := a.request("POST", "/api/v1/candidates", candidateBatch(t, changed), a.key); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	original.Aliases = append(original.Aliases, "https://example.com/new-alias")
	pool = candidatePool(t, a, "", a.key)
	if len(pool) != 1 || !reflect.DeepEqual(pool[0], original) {
		t.Fatalf("upsert changed snapshot: %#v", pool)
	}
	text, ok, err := a.db.CandidateText(context.Background(), a.scope, "https://example.com/new-alias")
	if err != nil || !ok || text != original.Text {
		t.Fatal("merged alias unavailable", text, ok, err)
	}
	other := apiCandidate()
	other.Title, other.Text = "Other paper report", "Other paper source text."
	if w := a.request("POST", "/api/v1/candidates", candidateBatch(t, other), otherKey); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if pool := candidatePool(t, a, "", otherKey); len(pool) != 1 || pool[0].Text != other.Text {
		t.Fatal("other tenant snapshot", pool)
	}
	if pool := candidatePool(t, a, "", a.key); len(pool) != 1 || pool[0].Text != original.Text {
		t.Fatal("other tenant overwrote snapshot", pool)
	}
	for _, method := range []string{"GET", "POST"} {
		if w := a.request(method, "/api/v1/candidates", candidateBatch(t, original), ""); w.Code != 401 {
			t.Fatal("unauthenticated candidate request", w.Code, w.Body)
		}
	}
}

func TestCandidateAPINullableMetadata(t *testing.T) {
	a := setup(t, nil)
	c := apiCandidate()
	c.Aliases, c.Byline = nil, nil
	body := strings.Replace(candidateBatch(t, c), `"repeat":false`, `"published_at":null,"wire_group":null,"repeat":false`, 1)
	if w := a.request("POST", "/api/v1/candidates", body, a.key); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	pool := candidatePool(t, a, "", a.key)
	if len(pool) != 1 || pool[0].PublishedAt != nil || pool[0].WireGroup != nil || pool[0].Aliases == nil || pool[0].Byline == nil {
		t.Fatal("nullable metadata or normalized arrays", pool)
	}
}

func TestCandidateAPIQueryFiltersAndRetention(t *testing.T) {
	a := setup(t, nil)
	recent := apiCandidate()
	recent.FetchedAt = time.Now().UTC().Add(-time.Hour)
	older := recent
	older.CanonicalURL += "/older"
	older.FetchedAt = recent.FetchedAt.Add(-time.Hour)
	health := recent
	health.CanonicalURL += "/health"
	health.Section = "health"
	expired := recent
	expired.CanonicalURL += "/expired"
	expired.FetchedAt = time.Now().UTC().AddDate(0, 0, -15)
	w := a.request("POST", "/api/v1/candidates", candidateBatch(t, recent, older, health, expired), a.key)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	for _, tc := range []struct {
		query string
		count int
	}{
		{"", 3},
		{"?section=economy", 2},
		{"?section=health", 1},
		{"?since=" + url.QueryEscape(recent.FetchedAt.Format(time.RFC3339Nano)), 2},
		{"?section=economy&since=" + url.QueryEscape(recent.FetchedAt.Format(time.RFC3339Nano)), 1},
		{"?since=" + url.QueryEscape(recent.FetchedAt.Add(time.Nanosecond).Format(time.RFC3339Nano)), 0},
	} {
		if pool := candidatePool(t, a, tc.query, a.key); len(pool) != tc.count {
			t.Fatalf("%s: got %d, want %d", tc.query, len(pool), tc.count)
		}
	}
	for _, query := range []string{"?since=tomorrow", "?since=", "?section=missing", "?section=", "?section=economy&section=health", "?since=2026-10-01T00:00:00Z&since=2026-10-02T00:00:00Z", "?paper_id=other"} {
		w := a.request("GET", "/api/v1/candidates"+query, "", a.key)
		if w.Code != 400 || !json.Valid(w.Body.Bytes()) {
			t.Fatal(query, w.Code, w.Body)
		}
	}
}

func TestCandidateAPIStrictValidation(t *testing.T) {
	a := setup(t, nil)
	valid := candidateBatch(t, apiCandidate())
	change := func(key, raw string) string {
		var batch map[string][]map[string]json.RawMessage
		if err := json.Unmarshal([]byte(valid), &batch); err != nil {
			t.Fatal(err)
		}
		batch["candidates"][0][key] = json.RawMessage(raw)
		body, err := json.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	for _, tc := range []struct{ name, body, path string }{
		{"unknown batch field", `{"extra":true,"candidates":[]}`, "extra"},
		{"unknown candidate field", change("extra", `true`), "candidates[0].extra"},
		{"paper injection", change("paper_id", `"other"`), "candidates[0].paper_id"},
		{"null batch", `null`, "$"},
		{"array batch", `[]`, "$"},
		{"object pool", `{"candidates":{}}`, "candidates"},
		{"scalar candidate", `{"candidates":[true]}`, "candidates[0]"},
		{"duplicate batch field", strings.Replace(valid, `{"candidates":`, `{"candidates":[],"candidates":`, 1), "candidates"},
		{"duplicate candidate field", strings.Replace(valid, `"title":`, `"title":"Injected","title":`, 1), "candidates[0].title"},
		{"null pool", `{"candidates":null}`, "candidates"},
		{"empty pool", `{"candidates":[]}`, "candidates"},
		{"null candidate", `{"candidates":[null]}`, "candidates[0]"},
		{"null title", change("title", `null`), "candidates[0].title"},
		{"null repeat", change("repeat", `null`), "candidates[0].repeat"},
		{"null id", change("id", `null`), "candidates[0].id"},
		{"null alias", change("aliases", `[null]`), "candidates[0].aliases[0]"},
		{"null author", change("byline", `[null]`), "candidates[0].byline[0]"},
		{"wrong repeat type", change("repeat", `"true"`), "candidates[0].repeat"},
		{"wrong alias type", change("aliases", `[1]`), "candidates[0].aliases[0]"},
		{"wrong timestamp type", change("fetched_at", `123`), "candidates[0].fetched_at"},
		{"unknown section", change("section", `"missing"`), "candidates[0].section"},
		{"nonplain title", change("title", `"<b>title</b>"`), "candidates[0].title"},
		{"userinfo", change("canonical_url", `"https://user:password@example.com/article"`), "candidates[0].canonical_url"},
		{"oversize article", change("text", fmt.Sprintf("%q", strings.Repeat("x", candidate.MaxTextBytes+1))), "candidates[0].text"},
		{"trailing object", valid + `{}`, "$"},
		{"trailing garbage", valid + `x`, "$"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := a.request("POST", "/api/v1/candidates", tc.body, a.key)
			var result struct{ Errors []struct{ Path, Rule string } }
			if w.Code != 422 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
				t.Fatalf("got %d %s", w.Code, w.Body)
			}
			for _, p := range result.Errors {
				if p.Path == tc.path && p.Rule != "" {
					return
				}
			}
			t.Fatalf("missing error path %s: %s", tc.path, w.Body)
		})
	}
	if pool := candidatePool(t, a, "", a.key); len(pool) != 0 {
		t.Fatal("invalid request wrote candidates", pool)
	}
}

func TestCandidateAPIBatchAndBodyLimits(t *testing.T) {
	a := setup(t, nil)
	valid, invalid := apiCandidate(), apiCandidate()
	invalid.Section = "missing"
	if w := a.request("POST", "/api/v1/candidates", candidateBatch(t, valid, invalid), a.key); w.Code != 422 {
		t.Fatal(w.Code, w.Body)
	}
	batch := make([]candidate.Candidate, 101)
	for i := range batch {
		batch[i] = apiCandidate()
		batch[i].CanonicalURL += fmt.Sprintf("/%d", i)
	}
	if w := a.request("POST", "/api/v1/candidates", candidateBatch(t, batch...), a.key); w.Code != 422 || !strings.Contains(w.Body.String(), "must contain 1..100 candidates") {
		t.Fatal(w.Code, w.Body)
	}
	if w := a.request("POST", "/api/v1/candidates", strings.Repeat("x", (8<<20)+1), a.key); w.Code != 413 || !json.Valid(w.Body.Bytes()) {
		t.Fatal(w.Code, w.Body)
	}
	if pool := candidatePool(t, a, "", a.key); len(pool) != 0 {
		t.Fatal("oversized request wrote candidates", pool)
	}
}

func TestCandidatesPagination(t *testing.T) {
	a := setup(t, nil)
	for i := 0; i < 25; i++ {
		c := apiCandidate()
		c.CanonicalURL = fmt.Sprintf("https://example.com/%d", i)
		// Equal timestamps exercise the stable ID tie-breaker.
		c.FetchedAt = time.Now().UTC().Truncate(time.Hour)
		if w := a.request("POST", "/api/v1/candidates", candidateBatch(t, c), a.key); w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
	}
	seen := map[int64]bool{}
	cursor := ""
	for page := 0; ; page++ {
		query := "?limit=7"
		if cursor != "" {
			query += "&cursor=" + url.QueryEscape(cursor)
		}
		w := a.request("GET", "/api/v1/candidates"+query, "", a.key)
		var result struct {
			Candidates []candidate.Candidate `json:"candidates"`
			Next       string                `json:"next_cursor"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatal(w.Code, w.Body)
		}
		if len(result.Candidates) > 7 || page > 4 {
			t.Fatal("unbounded page", page, len(result.Candidates))
		}
		for _, c := range result.Candidates {
			if seen[c.ID] {
				t.Fatal("duplicate", c.ID)
			}
			seen[c.ID] = true
		}
		cursor = result.Next
		if cursor == "" {
			break
		}
	}
	if len(seen) != 25 {
		t.Fatal(len(seen))
	}
	for _, query := range []string{"?limit=0", "?limit=21", "?cursor=bad", "?limit=1&limit=2"} {
		if w := a.request("GET", "/api/v1/candidates"+query, "", a.key); w.Code != 400 {
			t.Fatal(query, w.Code, w.Body)
		}
	}
	if got := candidatePool(t, a, "", a.key); len(got) > 20 {
		t.Fatal("unbounded default", len(got))
	}
}

func TestCandidatePagesBoundEscapedResponseBytes(t *testing.T) {
	a := setup(t, nil)
	for i := 0; i < 9; i++ {
		c := apiCandidate()
		c.CanonicalURL = fmt.Sprintf("https://example.com/%d", i)
		// JSON escapes each ampersand to six bytes. The complete pool exceeds the
		// worker's 8 MiB cap even though its raw text is less than two MiB.
		c.Text = strings.Repeat("&", candidate.MaxTextBytes)
		if w := a.request("POST", "/api/v1/candidates", candidateBatch(t, c), a.key); w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
	}
	cursor := ""
	total := 0
	for {
		query := "?limit=20"
		if cursor != "" {
			query += "&cursor=" + url.QueryEscape(cursor)
		}
		w := a.request("GET", "/api/v1/candidates"+query, "", a.key)
		var result struct {
			Candidates []candidate.Candidate `json:"candidates"`
			Next       string                `json:"next_cursor"`
		}
		if w.Code != 200 || w.Body.Len() > 8<<20 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatal(w.Code, w.Body.Len())
		}
		if len(result.Candidates) == 0 {
			t.Fatal("empty page")
		}
		total += len(result.Candidates)
		cursor = result.Next
		if cursor == "" {
			break
		}
		if total >= 9 {
			t.Fatal("cursor did not advance")
		}
	}
	if total != 9 {
		t.Fatal(total)
	}
}
