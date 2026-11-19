package edition

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
)

var editionType = reflect.TypeFor[Edition]()

// JSONSchema generates the permanently supported v1 schema from the wire types.
// Paper membership, ordering, references, and cross-item limits need Validate.
func JSONSchema() ([]byte, error) {
	return JSONSchemaVersion(1)
}

// JSONSchemaVersion generates either permanently supported edition contract.
func JSONSchemaVersion(version int) ([]byte, error) {
	if version != 1 && version != 2 {
		return nil, fmt.Errorf("schema: must be 1 or 2")
	}
	root := schemaFor(editionType, version)
	root["properties"].(map[string]any)["schema"].(map[string]any)["const"] = version
	root["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	root["title"] = fmt.Sprintf("Night Desk edition v%d", version)
	return json.MarshalIndent(root, "", "  ")
}

func schemaFor(t reflect.Type, version int) map[string]any {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	s := map[string]any{}
	switch t.Kind() {
	case reflect.Struct:
		s["type"] = "object"
		s["additionalProperties"] = false
		properties := map[string]any{}
		var required []string
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if version == 1 && f.Tag.Get("since") == "2" {
				continue
			}
			name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
			field := schemaFor(f.Type, version)
			for _, tag := range []string{"maxLength", "maxItems", "minimum", "maximum", "const"} {
				if v := f.Tag.Get(tag); v != "" {
					n, _ := strconv.Atoi(v)
					field[tag] = n
				}
			}
			for _, tag := range []string{"pattern", "format"} {
				if v := f.Tag.Get(tag); v != "" {
					field[tag] = v
				}
			}
			if v := f.Tag.Get("enum"); v != "" {
				if version == 2 && f.Tag.Get("enumV2") != "" {
					v = f.Tag.Get("enumV2")
				}
				field["enum"] = strings.Split(v, ",")
			}
			if name == "url" || name == "image_url" {
				field["pattern"] = `^[hH][tT][tT][pP][sS]://[^/@\s?#]+([/?#][^\s]*)?$`
			}
			properties[name] = field
			if opts != "omitempty" {
				required = append(required, name)
			}
		}
		s["properties"] = properties
		if len(required) > 0 {
			s["required"] = required
		}
	case reflect.Slice:
		s["type"] = "array"
		s["items"] = schemaFor(t.Elem(), version)
	case reflect.String:
		s["type"] = "string"
		s["minLength"] = 1
	case reflect.Int:
		s["type"] = "integer"
	default:
		panic("unsupported schema type: " + t.String())
	}
	return s
}

// encoding/json accepts null for scalars and matches names case-insensitively.
// Check the exact wire shape as well so it agrees with JSON Schema.
func checkShape(v any, t reflect.Type, path string, version int) error {
	if path == "" {
		path = "$"
	}
	if v == nil {
		return fmt.Errorf("%s: must not be null", path)
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: must be an object", path)
		}
		fields := make(map[string]bool)
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
			fields[name] = true
			child := name
			if path != "$" {
				child = path + "." + name
			}
			value, present := obj[name]
			if version == 1 && f.Tag.Get("since") == "2" {
				if present {
					return fmt.Errorf("%s: requires schema 2", child)
				}
				continue
			}
			if !present {
				if opts != "omitempty" {
					return fmt.Errorf("%s: is required", child)
				}
				continue
			}
			if err := checkShape(value, f.Type, child, version); err != nil {
				return err
			}
		}
		for key := range obj {
			if !fields[key] {
				return fmt.Errorf("%s: unknown field %q", path, key)
			}
		}
	case reflect.Slice:
		values, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s: must be an array", path)
		}
		for i, value := range values {
			if err := checkShape(value, t.Elem(), fmt.Sprintf("%s[%d]", path, i), version); err != nil {
				return err
			}
		}
	case reflect.String:
		if _, ok := v.(string); !ok {
			return fmt.Errorf("%s: must be a string", path)
		}
	case reflect.Int:
		n, ok := v.(float64)
		if !ok || math.Trunc(n) != n {
			return fmt.Errorf("%s: must be an integer", path)
		}
	}
	return nil
}
