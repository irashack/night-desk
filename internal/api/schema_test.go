package api

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestReflectedSchemas(t *testing.T) {
	type wire struct {
		Name     string               `json:"name" maxLength:"20"`
		Optional *string              `json:"optional,omitempty" nullable:"true"`
		Enabled  bool                 `json:"enabled"`
		Cursor   int64                `json:"cursor" minimum:"0"`
		At       time.Time            `json:"at"`
		Tags     map[string][]float64 `json:"tags"`
		Hidden   string               `json:"-"`
		private  string
	}
	s := schemaForType(reflect.TypeFor[wire](), true)
	props := s["properties"].(map[string]any)
	if len(props) != 6 || s["additionalProperties"] != false {
		t.Fatal(s)
	}
	if !reflect.DeepEqual(s["required"], []string{"name", "enabled", "cursor", "at", "tags"}) {
		t.Fatal(s["required"])
	}
	for name, typ := range map[string]string{"name": "string", "enabled": "boolean", "cursor": "integer", "at": "string", "tags": "object"} {
		if props[name].(map[string]any)["type"] != typ {
			t.Fatal(name, props[name])
		}
	}
	if props["name"].(map[string]any)["maxLength"] != 20 || props["at"].(map[string]any)["format"] != "date-time" || props["optional"].(map[string]any)["anyOf"] == nil {
		t.Fatal(props)
	}
}

func TestToolSchemasEmbedEditionAndPreservePatchNulls(t *testing.T) {
	for _, route := range Routes() {
		input := route.ToolInputSchema()
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil || input["type"] != "object" {
			t.Fatalf("invalid tool schema: %s", route.Tool)
		}
		props := input["properties"].(map[string]any)
		if route.Tool == "publish_edition" {
			if !reflect.DeepEqual(input["required"], []string{"date", "body"}) {
				t.Fatal(input)
			}
			for i, raw := range props["body"].(map[string]any)["oneOf"].([]any) {
				edition := raw.(map[string]any)
				if edition["$ref"] != nil || edition["$schema"] != nil || edition["properties"].(map[string]any)["schema"].(map[string]any)["const"] != float64(i+1) {
					t.Fatal("MCP edition schema must be self-contained", edition)
				}
			}
		}
		if route.Tool == "edit_paper" {
			fields := props["body"].(map[string]any)["properties"].(map[string]any)
			if fields["brief"].(map[string]any)["anyOf"] == nil || fields["masthead"].(map[string]any)["anyOf"] != nil {
				t.Fatal("patch clearability must match the handler")
			}
		}
	}
}
