package mcp_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/irashack/night-desk/internal/api"
	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/editorial"
	"github.com/irashack/night-desk/internal/keys"
	bridge "github.com/irashack/night-desk/internal/mcp"
	"github.com/irashack/night-desk/internal/store"
)

func keyFile(t *testing.T, key string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent-key")
	if err := os.WriteFile(path, []byte(key+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func session(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args any, wantError bool) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError != wantError || len(result.Content) != 1 {
		t.Fatalf("%s: IsError=%v, want %v; content=%v", name, result.IsError, wantError, result.Content)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatal("expected text content")
	}
	return text.Text
}

func TestMCPAgainstNightDesk(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	c := &config.Config{Papers: []config.Paper{{ID: "daily", Masthead: "The Daily", Timezone: "America/Chicago", Sections: []config.Section{{Key: "news", Title: "News"}}}}}
	desk, err := editorial.Open(ctx, db, func() *config.Config { return c }, nil)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer((&api.Server{Store: db, Config: desk.Config, Editorial: desk}).Handler())
	t.Cleanup(httpServer.Close)
	_, key, err := keys.Issue(ctx, db, "daily", "mcp-test")
	if err != nil {
		t.Fatal(err)
	}
	server, err := bridge.New(httpServer.URL, keyFile(t, key), httpServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	cs := session(t, server)
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]api.Route{}
	for _, route := range api.Routes() {
		if route.Tool != "" {
			want[route.Tool] = route
		}
	}
	if len(tools.Tools) != len(want) {
		t.Fatalf("got %d tools, want %d", len(tools.Tools), len(want))
	}
	for _, tool := range tools.Tools {
		route, exists := want[tool.Name]
		if !exists || !strings.Contains(tool.Description, string(route.Capability)) {
			t.Fatalf("unexpected or undocumented tool: %s", tool.Name)
		}
		gotSchema, _ := json.Marshal(tool.InputSchema)
		wantSchema, _ := json.Marshal(route.ToolInputSchema())
		if string(gotSchema) != string(wantSchema) {
			t.Fatalf("%s input schema differs from the API contract", tool.Name)
		}
	}
	paper := call(t, cs, "get_paper", map[string]any{}, false)
	var got struct {
		Masthead string           `json:"masthead"`
		Sections []config.Section `json:"sections"`
	}
	if err := json.Unmarshal([]byte(paper), &got); err != nil || got.Masthead != "The Daily" || len(got.Sections) != 1 {
		t.Fatal("incorrect paper response", paper, err)
	}

	// Compare byte-for-byte with HTTP, including the exact path/rule list and
	// trailing newline. MCP must not replace it with SDK validation messages.
	invalidPatch := `{"masthead":""}`
	req, err := http.NewRequest("PATCH", httpServer.URL+"/api/v1/paper", strings.NewReader(invalidPatch))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	response, err := httpServer.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 422 {
		t.Fatal("expected HTTP validation error", err)
	}
	text := call(t, cs, "edit_paper", map[string]any{"body": json.RawMessage(invalidPatch)}, true)
	if text != string(body) {
		t.Fatalf("MCP changed validation errors: %s", text)
	}
	var validation struct{ Errors []struct{ Path, Rule string } }
	if err := json.Unmarshal([]byte(text), &validation); err != nil || len(validation.Errors) != 1 || validation.Errors[0].Path != "masthead" || validation.Errors[0].Rule != "must be trimmed non-empty text" {
		t.Fatal("missing exact path/rule validation errors", text, err)
	}
	call(t, cs, "edit_paper", map[string]any{"body": map[string]any{"masthead": "Evening Paper"}}, false)
	call(t, cs, "paper_revisions", map[string]any{"limit": 1}, false)
	call(t, cs, "restore_paper", map[string]any{"body": map[string]any{"revision": 0}}, false)
	call(t, cs, "list_editions", map[string]any{"before": "2026-10-31", "limit": 1}, false)
	call(t, cs, "get_candidates", map[string]any{"section": "news", "since": "2026-10-30T00:00:00Z"}, false)
	call(t, cs, "get_feedback", map[string]any{"after": 0, "limit": 1}, false)
	call(t, cs, "publish_edition", map[string]any{"date": "2026-10-30", "body": map[string]any{"schema": 2, "lead": "story", "sections": []any{map[string]any{"key": "news", "items": []any{map[string]any{
		"id": "story", "kind": "news", "title": "Title", "summary": "Summary", "source": "Outlet", "url": "https://example.com/story",
	}}}}}}, false)
	edition := call(t, cs, "get_edition", map[string]any{"date": "2026-10-30"}, false)
	if !strings.Contains(edition, `"published_at"`) || !strings.Contains(edition, `"story"`) {
		t.Fatal("incorrect published edition response", edition)
	}
	item := call(t, cs, "get_item", map[string]any{"date": "2026-10-30", "id": "story"}, false)
	if !strings.Contains(item, `"published_at"`) || !strings.Contains(item, `"section":"news"`) || !strings.Contains(item, `"id":"story"`) {
		t.Fatal("incorrect published item response", item)
	}
	call(t, cs, "get_item", map[string]any{"date": "2026-10-30", "id": "missing"}, true)

	_, limitedKey, err := keys.Issue(ctx, db, "daily", "no-read", auth.Publish)
	if err != nil {
		t.Fatal(err)
	}
	limitedServer, err := bridge.New(httpServer.URL, keyFile(t, limitedKey), nil)
	if err != nil {
		t.Fatal(err)
	}
	limited := session(t, limitedServer)
	if text := call(t, limited, "get_paper", map[string]any{}, true); text != "{\"error\":\"forbidden\"}\n" {
		t.Fatalf("403 changed: %s", text)
	}
	if text := call(t, limited, "get_item", map[string]any{"date": "2026-10-30", "id": "story"}, true); text != "{\"error\":\"forbidden\"}\n" {
		t.Fatalf("item lookup bypassed read capability: %s", text)
	}
}

func TestHTTPMappingAndErrors(t *testing.T) {
	var seenMethod, seenURI, seenBody string
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenMethod, seenURI = r.Method, r.URL.RequestURI()
		body, _ := io.ReadAll(r.Body)
		seenBody = string(body)
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(422)
		io.WriteString(w, "{\"errors\":[{\"path\":\"sections[0].items[0].url\",\"rule\":\"must be https\"}]}\n")
	}))
	defer httpServer.Close()
	server, err := bridge.New(httpServer.URL, keyFile(t, "test-key"), nil)
	if err != nil {
		t.Fatal(err)
	}
	cs := session(t, server)
	call(t, cs, "get_candidates", map[string]any{"section": "a&b", "since": "2026-10-30T01:02:03+02:00"}, true)
	if seenURI != "/api/v1/candidates?section=a%26b&since=2026-10-30T01%3A02%3A03%2B02%3A00" || seenMethod != "GET" {
		t.Fatal(seenMethod, seenURI)
	}
	text := call(t, cs, "publish_edition", map[string]any{"date": "2026-10-30", "body": map[string]any{"unknown": true}}, true)
	if seenMethod != "PUT" || seenURI != "/api/v1/editions/2026-10-30" || seenBody != `{"unknown":true}` {
		t.Fatal(seenMethod, seenURI, seenBody)
	}
	if text != "{\"errors\":[{\"path\":\"sections[0].items[0].url\",\"rule\":\"must be https\"}]}\n" {
		t.Fatal(text)
	}
	for _, args := range []any{[]any{}, nil, map[string]any{"extra": true}} {
		call(t, cs, "get_paper", args, true)
	}
	call(t, cs, "get_edition", map[string]any{}, true)
	call(t, cs, "get_candidates", map[string]any{"section": 1}, true)
}

func TestConfigurationAndRedirects(t *testing.T) {
	path := keyFile(t, "test-key")
	for _, origin := range []string{"", "ftp://example.com", "https://user:password@example.com", "https://example.com/path", "https://example.com?key=value", "https://example.com#fragment", "http://example.com"} {
		if _, err := bridge.New(origin, path, nil); err == nil {
			t.Fatal("accepted invalid origin")
		}
	}
	for _, key := range []string{"", "two words", "key\x00value", strings.Repeat("x", 4097)} {
		if _, err := bridge.New("https://example.com", keyFile(t, key), nil); err == nil {
			t.Fatal("accepted invalid key file")
		}
	}
	if _, err := bridge.New("https://example.com", filepath.Join(t.TempDir(), "missing"), nil); err == nil {
		t.Fatal("accepted missing key file")
	}
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	client := origin.Client()
	server, err := bridge.New(origin.URL, path, client)
	if err != nil {
		t.Fatal(err)
	}
	call(t, session(t, server), "get_paper", map[string]any{}, true)
	if redirected || client.CheckRedirect != nil {
		t.Fatal("redirect followed or caller client mutated")
	}
}
