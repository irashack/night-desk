package model

import (
	"fmt"
	"reflect"
	"strings"
)

// OutputSchema describes t the way DecodeJSON reads it, for providers that
// constrain output to a schema: fields without omitempty are required, null is
// never allowed and every object rejects unknown keys. It uses only keywords
// Claude structured outputs accept; DecodeJSON and the request's Validate still
// enforce everything else.
func OutputSchema(t reflect.Type) (map[string]any, error) {
	return outputSchema(t, map[reflect.Type]bool{})
}

func outputSchema(t reflect.Type, open map[reflect.Type]bool) (map[string]any, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		if open[t] {
			return nil, fmt.Errorf("model: %s is recursive", t)
		}
		open[t] = true
		defer delete(open, t)
		properties := map[string]any{}
		required := []string{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			s, err := outputSchema(f.Type, open)
			if err != nil {
				return nil, err
			}
			properties[name] = s
			if !strings.Contains(opts, "omitempty") {
				required = append(required, name)
			}
		}
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}, nil
	case reflect.Slice, reflect.Array:
		items, err := outputSchema(t.Elem(), open)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items}, nil
	case reflect.String:
		return map[string]any{"type": "string"}, nil
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}, nil
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}, nil
	}
	return nil, fmt.Errorf("model: unsupported destination field type %s", t)
}
