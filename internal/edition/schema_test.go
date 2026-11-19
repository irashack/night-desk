package edition

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func loadSchema(t *testing.T, versions ...int) (map[string]any, []byte) {
	t.Helper()
	version := 1
	if len(versions) > 0 {
		version = versions[0]
	}
	data, err := os.ReadFile(fmt.Sprintf("../../schema/edition-v%d.json", version))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	return schema, data
}

func TestSchemaMatchesTypes(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			schema, data := loadSchema(t, version)
			if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
				t.Fatal("wrong JSON Schema dialect")
			}
			walkType(t, schema, reflect.TypeFor[Edition](), version)
			if schema["properties"].(map[string]any)["schema"].(map[string]any)["const"] != float64(version) {
				t.Fatal("wrong schema version")
			}
			generated, err := JSONSchemaVersion(version)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, append(generated, '\n')) {
				t.Fatal("schema is stale; run go generate ./internal/edition")
			}
			if version == 1 {
				legacy, err := JSONSchema()
				if err != nil || !bytes.Equal(legacy, generated) {
					t.Fatal("legacy generator changed", err)
				}
			}
		})
	}
	if _, err := JSONSchemaVersion(3); err == nil {
		t.Fatal("unsupported schema accepted")
	}
}

// Independently walk the published properties, required fields, and enums. This
// checks the wire contract, not just whether the generator is reproducible.
func walkType(t *testing.T, schema map[string]any, typ reflect.Type, version int) {
	t.Helper()
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Fatalf("%v: expected closed object", typ)
		}
		props := schema["properties"].(map[string]any)
		count := typ.NumField()
		if version == 1 {
			for i := 0; i < typ.NumField(); i++ {
				if typ.Field(i).Tag.Get("since") == "2" {
					count--
				}
			}
		}
		if len(props) != count {
			t.Fatalf("%v: property count mismatch", typ)
		}
		required := []string{}
		if values, ok := schema["required"].([]any); ok {
			for _, v := range values {
				required = append(required, v.(string))
			}
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if version == 1 && f.Tag.Get("since") == "2" {
				continue
			}
			name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
			child, ok := props[name].(map[string]any)
			if !ok {
				t.Fatalf("%v: missing property %s", typ, name)
			}
			if slices.Contains(required, name) != (opts != "omitempty") {
				t.Fatalf("%v.%s: required mismatch", typ, name)
			}
			values := []string{}
			if enums, ok := child["enum"].([]any); ok {
				for _, v := range enums {
					values = append(values, v.(string))
				}
			}
			expected := []string{}
			if tag := f.Tag.Get("enum"); tag != "" {
				if version == 2 && f.Tag.Get("enumV2") != "" {
					tag = f.Tag.Get("enumV2")
				}
				expected = strings.Split(tag, ",")
			}
			if !slices.Equal(values, expected) {
				t.Fatalf("%v.%s: enum mismatch", typ, name)
			}
			walkType(t, child, f.Type, version)
		}
		for _, name := range required {
			if _, ok := props[name]; !ok {
				t.Fatalf("unknown required property %s", name)
			}
		}
	case reflect.Slice:
		if schema["type"] != "array" {
			t.Fatalf("%v: expected array", typ)
		}
		walkType(t, schema["items"].(map[string]any), typ.Elem(), version)
	case reflect.Int:
		if schema["type"] != "integer" {
			t.Fatalf("%v: expected integer", typ)
		}
	case reflect.String:
		if schema["type"] != "string" {
			t.Fatalf("%v: expected string", typ)
		}
	default:
		t.Fatalf("unsupported type %v", typ)
	}
}

// Only structural constraints are interpreted here, not a second implementation
// of the semantic validator or a general-purpose JSON Schema engine.
func structuralMatch(schema map[string]any, value any) bool {
	if value == nil {
		return false
	}
	if c, ok := schema["const"]; ok && !reflect.DeepEqual(c, value) {
		return false
	}
	if enums, ok := schema["enum"].([]any); ok && !slices.ContainsFunc(enums, func(v any) bool { return reflect.DeepEqual(v, value) }) {
		return false
	}
	switch schema["type"] {
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return false
		}
		properties := schema["properties"].(map[string]any)
		if required, ok := schema["required"].([]any); ok {
			for _, key := range required {
				if _, ok := obj[key.(string)]; !ok {
					return false
				}
			}
		}
		for key, v := range obj {
			child, ok := properties[key].(map[string]any)
			if !ok {
				if schema["additionalProperties"] == false {
					return false
				}
				continue
			}
			if !structuralMatch(child, v) {
				return false
			}
		}
	case "array":
		values, ok := value.([]any)
		if !ok {
			return false
		}
		for _, v := range values {
			if !structuralMatch(schema["items"].(map[string]any), v) {
				return false
			}
		}
	case "string":
		if _, ok := value.(string); !ok {
			return false
		}
	case "integer":
		v, ok := value.(float64)
		if !ok || math.Trunc(v) != v {
			return false
		}
	default:
		return false
	}
	return true
}

func TestFixtureStructuralAgreement(t *testing.T) {
	schema, _ := loadSchema(t)
	for _, dir := range []string{"valid", "invalid"} {
		paths, err := filepath.Glob("../../testdata/" + dir + "/*.json")
		if err != nil || len(paths) == 0 {
			t.Fatalf("fixtures: %v", err)
		}
		for _, path := range paths {
			t.Run(dir+"/"+filepath.Base(path), func(t *testing.T) {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var value any
				if err := json.Unmarshal(data, &value); err != nil {
					t.Fatal(err)
				}
				shapeOK := structuralMatch(schema, value)
				e, err := Decode(bytes.NewReader(data))
				goOK := err == nil
				if goOK {
					p := PaperRules{}
					for _, s := range e.Sections {
						p.Sections = append(p.Sections, SectionRule{Key: s.Key})
					}
					for _, problem := range Validate(e, p) {
						if problem.Severity == Error {
							goOK = false
						}
					}
				}
				want := dir == "valid"
				if shapeOK != want || goOK != want {
					t.Fatalf("schema shape=%v Go=%v want=%v decode=%v", shapeOK, goOK, want, err)
				}
			})
		}
	}
}

// Exercise field presence and types at every level, including optional fields
// whose explicit null must not be treated as omission.
func TestStructuralMutations(t *testing.T) {
	schema, _ := loadSchema(t)
	data, err := os.ReadFile("../../testdata/valid/daily.json")
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	var walk func(map[string]any, map[string]any, string)
	check := func(name string, want bool) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			b, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			shapeOK := structuralMatch(schema, root)
			e, err := Decode(bytes.NewReader(b))
			goOK := err == nil
			if goOK {
				p := PaperRules{}
				for _, s := range e.Sections {
					p.Sections = append(p.Sections, SectionRule{Key: s.Key})
				}
				for _, p := range Validate(e, p) {
					if p.Severity == Error {
						goOK = false
					}
				}
			}
			if shapeOK != want || goOK != want {
				t.Fatalf("schema=%v Go=%v want=%v decode=%v", shapeOK, goOK, want, err)
			}
		})
	}
	walk = func(s, obj map[string]any, path string) {
		required, _ := s["required"].([]any)
		for key, property := range s["properties"].(map[string]any) {
			child := property.(map[string]any)
			original, present := obj[key]
			if !present {
				continue
			}
			name := path + key
			delete(obj, key)
			check(name+" missing", !slices.Contains(required, any(key)))
			obj[key] = original
			obj[key] = nil
			check(name+" null", false)
			obj[key] = true
			check(name+" wrong type", false)
			obj[key] = original
			if _, ok := child["enum"]; ok {
				obj[key] = "invalid-enum"
				check(name+" enum", false)
				obj[key] = original
			}
			switch child["type"] {
			case "object":
				walk(child, original.(map[string]any), name+".")
			case "array":
				itemSchema := child["items"].(map[string]any)
				if itemSchema["type"] == "object" {
					for i, v := range original.([]any) {
						if i == 0 {
							walk(itemSchema, v.(map[string]any), name+"[0].")
						}
					}
				}
			}
		}
		obj["unknown"] = true
		check(path+"unknown", false)
		delete(obj, "unknown")
	}
	walk(schema, root, "")
}
