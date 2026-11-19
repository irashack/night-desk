package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/api"
	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/keys"
)

func TestOpenAPIIsCurrent(t *testing.T) {
	generated, err := api.OpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	published, err := os.ReadFile("../../docs/reference/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(append(generated, '\n'), published) {
		t.Fatal("regenerate with: go run ./internal/api/openapi-gen docs/reference/openapi.json")
	}
}

func TestOpenAPIRoutesAndCapabilities(t *testing.T) {
	a, _ := setupEditorial(t)
	data, err := api.OpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		OpenAPI string `json:"openapi"`
		Paths   map[string]map[string]struct {
			Capability string `json:"x-nightdesk-capability"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if document.OpenAPI != "3.1.0" {
		t.Fatal(document.OpenAPI)
	}
	operations := 0
	for _, methods := range document.Paths {
		operations += len(methods)
	}
	if operations != len(api.Routes()) {
		t.Fatalf("description has %d operations, route table has %d", operations, len(api.Routes()))
	}
	seen := map[string]bool{}
	for _, route := range api.Routes() {
		pattern := route.Method + " " + route.Path
		if seen[pattern] || route.Handler == nil || route.Response == nil || route.Summary == "" {
			t.Fatalf("incomplete or duplicate route: %s", pattern)
		}
		seen[pattern] = true
		operation, exists := document.Paths[route.Path][strings.ToLower(route.Method)]
		if !exists || operation.Capability != string(route.Capability) {
			t.Fatalf("missing or incorrect operation: %s", pattern)
		}
		var capabilities []auth.Capability
		for _, capability := range []auth.Capability{auth.Read, auth.Publish, auth.Edit, auth.Review, auth.Gather, auth.Draft} {
			if capability != route.Capability && !slices.Contains(route.OrCapabilities, capability) {
				capabilities = append(capabilities, capability)
			}
		}
		_, key, err := keys.Issue(context.Background(), a.db, "daily", "route-check", capabilities...)
		if err != nil {
			t.Fatal(err)
		}
		path := strings.ReplaceAll(route.Path, "{date}", "2026-10-30")
		if response := a.request(route.Method, path, `{}`, key); response.Code != 403 {
			t.Fatalf("%s is not served with documented capability: status %d", pattern, response.Code)
		}
	}
	// Catch direct registrations that bypass the table. The health probe and
	// API JSON fallbacks are deliberately outside the agent contract.
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		syntax, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(syntax, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (selector.Sel.Name != "Handle" && selector.Sel.Name != "HandleFunc") {
				return true
			}
			literal, ok := call.Args[0].(*ast.BasicLit)
			if ok && literal.Kind == token.STRING {
				pattern, _ := strconv.Unquote(literal.Value)
				if strings.Contains(pattern, "/api/v1/") {
					t.Errorf("%s registers %s outside Routes", file, pattern)
				}
			}
			return true
		})
	}
}

func TestOptionalEditorialRoutes(t *testing.T) {
	a := setup(t, nil)
	for _, route := range api.Routes() {
		if route.Editorial {
			if response := a.request(route.Method, route.Path, `{}`, a.key); response.Code != 404 && response.Code != 405 {
				t.Fatalf("optional route %s %s unexpectedly available: %d", route.Method, route.Path, response.Code)
			}
		}
	}
}

func TestOpenAPISchemas(t *testing.T) {
	data, err := api.OpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	components := document["components"].(map[string]any)
	bearer := components["securitySchemes"].(map[string]any)["bearer"].(map[string]any)
	if bearer["type"] != "http" || bearer["scheme"] != "bearer" || document["security"] == nil {
		t.Fatal("missing bearer authentication")
	}
	for path, rawItem := range document["paths"].(map[string]any) {
		for method, rawOperation := range rawItem.(map[string]any) {
			responses := rawOperation.(map[string]any)["responses"].(map[string]any)
			for status, field := range map[string]string{"403": "error", "422": "errors"} {
				schema := responses[status].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
				properties := schema["properties"].(map[string]any)
				if properties[field] == nil {
					t.Fatalf("%s %s missing %s error schema", method, path, status)
				}
				if status == "422" {
					problem := properties["errors"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
					if problem["path"] == nil || problem["rule"] == nil {
						t.Fatal("validation errors must carry path and rule")
					}
				}
			}
		}
	}
	if !bytes.Contains(data, []byte(`"$ref": "../../schema/edition-v2.json"`)) || !bytes.Contains(data, []byte(`"$ref": "../../schema/edition-v1.json"`)) {
		t.Fatal("edition request must reference both supported published schemas")
	}
}
