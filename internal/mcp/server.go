// Package mcp exposes the agent HTTP contract as MCP tools. It never opens a
// database: the remote Night Desk server owns authorization and validation.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"time"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/irashack/night-desk/internal/api"
)

// New reads a key only from keyFile and constructs the MCP server. serverURL is
// the Night Desk origin (https, or http on loopback; no credentials, query or fragment).
// An optional HTTP client supplies a transport for tests or operator TLS policy;
// redirects are always refused so a key cannot leave the configured origin.
func New(serverURL, keyFile string, client *http.Client) (*mcp.Server, error) {
	u, err := url.Parse(serverURL)
	if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback()))) || u.Hostname() == "" || u.User != nil || u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("server must be an https origin (http allowed on loopback) without credentials, path, query or fragment")
	}
	f, err := os.Open(keyFile)
	if err != nil {
		return nil, errors.New("agent key file unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	f.Close()
	if err != nil || len(data) > 4096 {
		return nil, errors.New("agent key file invalid")
	}
	key := strings.TrimSpace(string(data))
	if key == "" || strings.ContainsFunc(key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return nil, errors.New("agent key file invalid")
	}
	if client == nil {
		client = &http.Client{Timeout: 75 * time.Second}
	}
	httpClient := *client
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	origin := strings.TrimSuffix(u.String(), "/")
	server := mcp.NewServer(&mcp.Implementation{Name: "nightdesk", Version: "0.4.0"}, nil)
	for _, route := range api.Routes() {
		if route.Tool == "" {
			continue
		}
		server.AddTool(&mcp.Tool{
			Name: route.Tool, Description: route.Summary + ". Requires capability: " + string(route.Capability) + ".",
			InputSchema: route.ToolInputSchema(),
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return call(ctx, &httpClient, origin, key, route, req.Params.Arguments), nil
		})
	}
	return server, nil
}

func call(ctx context.Context, client *http.Client, origin, key string, route api.Route, arguments json.RawMessage) *mcp.CallToolResult {
	var input map[string]json.RawMessage
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	if json.Unmarshal(arguments, &input) != nil || input == nil {
		return toolError("arguments must be a JSON object")
	}
	// Check only the MCP envelope. Body validation stays on the server, including
	// malformed fields, so its exact path/rule diagnostics pass through unchanged.
	properties := route.ToolInputSchema()["properties"].(map[string]any)
	for name := range input {
		if _, ok := properties[name]; !ok {
			return toolError("unknown tool argument")
		}
	}
	path := route.Path
	if route.PathParams != nil {
		for i := 0; i < route.PathParams.NumField(); i++ {
			name, _, _ := strings.Cut(route.PathParams.Field(i).Tag.Get("json"), ",")
			var value string
			if json.Unmarshal(input[name], &value) != nil || value == "" {
				return toolError("path arguments must be non-empty strings")
			}
			path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(value))
		}
	}
	query := url.Values{}
	if route.Query != nil {
		for i := 0; i < route.Query.NumField(); i++ {
			field := route.Query.Field(i)
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if raw, ok := input[name]; ok {
				var value string
				if field.Type.Kind() == reflect.String {
					if json.Unmarshal(raw, &value) != nil {
						return toolError("query argument must be a string")
					}
				} else {
					// Preserve numeric values (including out-of-range values) for
					// the HTTP handler's own validation and error messages.
					value = string(bytes.TrimSpace(raw))
				}
				query.Set(name, value)
			}
		}
	}
	endpoint := origin + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, route.Method, endpoint, bytes.NewReader(input["body"]))
	if err != nil {
		return toolError("could not construct Night Desk request")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	if route.Request != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(req)
	if err != nil {
		return toolError("Night Desk request failed")
	}
	defer response.Body.Close()
	const maxResponse = 32 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil || len(body) > maxResponse {
		return toolError("could not read Night Desk response (maximum 32 MB)")
	}
	return &mcp.CallToolResult{
		IsError: response.StatusCode < 200 || response.StatusCode >= 300,
		Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
	}
}

func toolError(message string) *mcp.CallToolResult {
	body, _ := json.Marshal(map[string]string{"error": message})
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(body)}}}
}
