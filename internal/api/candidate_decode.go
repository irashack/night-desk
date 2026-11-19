package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// candidateShape checks the wire shape before unmarshalling so null scalars,
// duplicate fields and nested unknown fields retain their exact indexed paths.
func candidateShape(body []byte, t reflect.Type) *problem {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var value func(reflect.Type, string) *problem
	value = func(t reflect.Type, path string) *problem {
		bad := func(rule string) *problem { return &problem{path, rule} }
		tok, err := dec.Token()
		if err != nil {
			return bad("must be valid JSON")
		}
		if tok == nil {
			if t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
				return nil
			}
			return bad("must not be null")
		}
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t == reflect.TypeFor[time.Time]() {
			s, ok := tok.(string)
			if !ok {
				return bad("must be RFC3339")
			}
			if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
				return bad("must be RFC3339")
			}
			return nil
		}
		switch t.Kind() {
		case reflect.Struct:
			if tok != json.Delim('{') {
				return bad("must be an object")
			}
			fields := map[string]reflect.Type{}
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				fields[strings.Split(f.Tag.Get("json"), ",")[0]] = f.Type
			}
			seen := map[string]bool{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return bad("must be valid JSON")
				}
				name, ok := key.(string)
				if !ok {
					return bad("must be an object")
				}
				p := name
				if path != "$" {
					p = path + "." + name
				}
				ft, ok := fields[name]
				if !ok {
					return &problem{p, "unknown field"}
				}
				if seen[name] {
					return &problem{p, "duplicate field"}
				}
				seen[name] = true
				if p := value(ft, p); p != nil {
					return p
				}
			}
			if end, err := dec.Token(); err != nil || end != json.Delim('}') {
				return bad("must be valid JSON")
			}
		case reflect.Slice:
			if tok != json.Delim('[') {
				return bad("must be an array")
			}
			for i := 0; dec.More(); i++ {
				if p := value(t.Elem(), fmt.Sprintf("%s[%d]", path, i)); p != nil {
					return p
				}
			}
			if end, err := dec.Token(); err != nil || end != json.Delim(']') {
				return bad("must be valid JSON")
			}
		case reflect.String:
			if _, ok := tok.(string); !ok {
				return bad("must be a string")
			}
		case reflect.Bool:
			if _, ok := tok.(bool); !ok {
				return bad("must be a boolean")
			}
		case reflect.Int64:
			n, ok := tok.(json.Number)
			if !ok {
				return bad("must be an integer")
			}
			if _, err := strconv.ParseInt(string(n), 10, 64); err != nil {
				return bad("must be an integer")
			}
		}
		return nil
	}
	if p := value(t, "$"); p != nil {
		return p
	}
	if _, err := dec.Token(); err != io.EOF {
		return &problem{"$", "must contain one JSON object"}
	}
	return nil
}
