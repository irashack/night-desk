package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
)

// Problem identifies one exact JSON path and rule without including content.
type Problem struct {
	Path string `json:"path"`
	Rule string `json:"rule"`
}

func (p Problem) Error() string { return p.Path + ": " + p.Rule }

// ValidationError is safe to log: it never contains provider response text.
type ValidationError struct{ Problems []Problem }

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		parts[i] = p.Error()
	}
	return strings.Join(parts, "; ")
}

// DecodeJSON checks exact names, required fields, nulls, duplicate keys, types,
// unknown fields, and trailing data before decoding into a caller-owned struct.
func DecodeJSON(b []byte, dst any) error {
	t := reflect.TypeOf(dst)
	if t == nil || t.Kind() != reflect.Pointer || reflect.ValueOf(dst).IsNil() || t.Elem().Kind() != reflect.Struct {
		return Problem{"$", "destination must be a non-nil pointer to a struct"}
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	v, err := jsonValue(d, "$")
	if err != nil {
		return err
	}
	if _, err = d.Token(); err != io.EOF {
		return Problem{"$", "must contain exactly one JSON object"}
	}
	if err = shape(v, t.Elem(), "$"); err != nil {
		return err
	}
	if err = json.Unmarshal(b, dst); err != nil {
		return Problem{"$", "must match destination field types"}
	}
	return nil
}
func jsonValue(d *json.Decoder, path string) (any, error) {
	tok, err := d.Token()
	if err != nil {
		return nil, Problem{path, "must be valid JSON"}
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch delim {
	case '{':
		obj := map[string]any{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return nil, Problem{path, "must be valid JSON"}
			}
			k, ok := key.(string)
			if !ok {
				return nil, Problem{path, "must have string field names"}
			}
			child := childPath(path, k)
			if _, exists := obj[k]; exists {
				return nil, Problem{child, "must not be repeated"}
			}
			val, e := jsonValue(d, child)
			if e != nil {
				return nil, e
			}
			obj[k] = val
		}
		if _, err = d.Token(); err != nil {
			return nil, Problem{path, "must be valid JSON"}
		}
		return obj, nil
	case '[':
		a := []any{}
		for d.More() {
			v, e := jsonValue(d, fmt.Sprintf("%s[%d]", path, len(a)))
			if e != nil {
				return nil, e
			}
			a = append(a, v)
		}
		if _, err = d.Token(); err != nil {
			return nil, Problem{path, "must be valid JSON"}
		}
		return a, nil
	}
	return nil, Problem{path, "must be valid JSON"}
}
func childPath(path, key string) string {
	if path == "$" {
		return key
	}
	return path + "." + key
}
func shape(v any, t reflect.Type, path string) error {
	if v == nil {
		return Problem{path, "must not be null"}
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	bad := func(rule string) error { return Problem{path, rule} }
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return bad("must be an object")
		}
		fields := map[string]bool{}
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
			fields[name] = true
			value, present := obj[name]
			if !present {
				if !strings.Contains(opts, "omitempty") {
					return Problem{childPath(path, name), "is required"}
				}
				continue
			}
			if err := shape(value, f.Type, childPath(path, name)); err != nil {
				return err
			}
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !fields[k] {
				return Problem{childPath(path, k), "unknown field"}
			}
		}
	case reflect.Slice, reflect.Array:
		a, ok := v.([]any)
		if !ok {
			return bad("must be an array")
		}
		if t.Kind() == reflect.Array && len(a) != t.Len() {
			return bad("must match array length")
		}
		for i, x := range a {
			if err := shape(x, t.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case reflect.String:
		if _, ok := v.(string); !ok {
			return bad("must be a string")
		}
	case reflect.Bool:
		if _, ok := v.(bool); !ok {
			return bad("must be a boolean")
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		n, ok := v.(json.Number)
		if !ok {
			return bad("must be a number")
		}
		ptr := reflect.New(t)
		if json.Unmarshal([]byte(n), ptr.Interface()) != nil {
			return bad("must match numeric field type")
		}
	default:
		return bad("unsupported destination field type")
	}
	return nil
}

// knownPath recognizes only paths derived from the destination schema. A model
// may put private material in an unknown key, so private-input errors must never
// report an unrecognized path even when the completion itself is suppressed.
func knownPath(t reflect.Type, path string) bool {
	if path == "$" {
		return true
	}
	for len(path) > 0 {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t.Kind() == reflect.Struct {
			end := strings.IndexAny(path, ".[")
			if end < 0 {
				end = len(path)
			}
			name := path[:end]
			found := false
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				key, _, _ := strings.Cut(f.Tag.Get("json"), ",")
				if key == "" {
					key = f.Name
				}
				if f.IsExported() && key != "-" && key == name {
					t = f.Type
					found = true
					break
				}
			}
			if !found {
				return false
			}
			path = path[end:]
		} else if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			if path[0] != '[' {
				return false
			}
			end := strings.IndexByte(path, ']')
			if end < 2 {
				return false
			}
			for _, r := range path[1:end] {
				if r < '0' || r > '9' {
					return false
				}
			}
			t = t.Elem()
			path = path[end+1:]
		} else {
			return false
		}
		if strings.HasPrefix(path, ".") {
			path = path[1:]
			if path == "" {
				return false
			}
		}
	}
	return true
}
