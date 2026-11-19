package lean

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// Load reads a ratings file in YAML or JSON and validates it strictly.
//
// Syntax and I/O failures are returned as err. Validation failures are returned
// as Problems with a path and a rule; in that case the Ratings result is nil and
// err is nil. Unknown keys, duplicate domains, bad enums and a missing source or
// attribution are all rejected.
func Load(r io.Reader) (*Ratings, []Problem, error) {
	if r == nil {
		return nil, nil, fmt.Errorf("lean: nil ratings reader")
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, fmt.Errorf("lean: read ratings: %w", err)
	}
	raw, err := yaml.YAMLToJSONStrict(data)
	if err != nil {
		return nil, nil, fmt.Errorf("lean: parse ratings: %w", err)
	}
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, nil, fmt.Errorf("lean: parse ratings: %w", err)
	}
	ratings, problems := decodeRatings(root)
	if len(problems) > 0 {
		return nil, problems, nil
	}
	return ratings, nil, nil
}

type decoder struct {
	problems []Problem
}

func (d *decoder) add(path, rule string) {
	d.problems = append(d.problems, Problem{Path: path, Rule: rule})
}

func asObject(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func unknownKeys(m map[string]any, allowed ...string) []string {
	keep := make(map[string]bool, len(allowed))
	for _, k := range allowed {
		keep[k] = true
	}
	var keys []string
	for k := range m {
		if !keep[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func decodeRatings(root any) (*Ratings, []Problem) {
	d := &decoder{}
	obj, ok := asObject(root)
	if !ok {
		d.add("$", "must be a ratings object")
		return nil, d.problems
	}
	for _, k := range unknownKeys(obj, "source", "ratings") {
		d.add(k, "unknown field")
	}

	var source Source
	if v, ok := obj["source"]; !ok || v == nil {
		d.add("source", "must be an object")
	} else if m, ok := asObject(v); ok {
		source = d.source(m)
	} else {
		d.add("source", "must be an object")
	}

	var ratings []Rating
	if v, ok := obj["ratings"]; !ok || v == nil {
		d.add("ratings", "must be an array")
	} else if arr, ok := v.([]any); !ok {
		d.add("ratings", "must be an array")
	} else {
		ratings = make([]Rating, 0, len(arr))
		seen := make(map[string]string)
		for i, item := range arr {
			rating, ok := d.rating(fmt.Sprintf("ratings[%d]", i), item, seen)
			if ok {
				ratings = append(ratings, rating)
			}
		}
	}

	if len(d.problems) > 0 {
		return nil, d.problems
	}
	return &Ratings{Source: source, Ratings: ratings}, nil
}

func (d *decoder) source(m map[string]any) Source {
	for _, k := range unknownKeys(m, "name", "url", "license", "attribution") {
		d.add("source."+k, "unknown field")
	}
	return Source{
		Name:        d.text("source.name", m["name"]),
		URL:         d.url("source.url", m["url"]),
		License:     d.text("source.license", m["license"]),
		Attribution: d.text("source.attribution", m["attribution"]),
	}
}

func (d *decoder) text(path string, v any) string {
	if v == nil {
		d.add(path, "must be trimmed non-empty text")
		return ""
	}
	s, ok := v.(string)
	if !ok {
		d.add(path, "must be a string")
		return ""
	}
	if s == "" || strings.TrimSpace(s) != s {
		d.add(path, "must be trimmed non-empty text")
	}
	return s
}

func (d *decoder) url(path string, v any) string {
	if v == nil {
		d.add(path, "must be trimmed non-empty text")
		return ""
	}
	s, ok := v.(string)
	if !ok {
		d.add(path, "must be a string")
		return ""
	}
	if s == "" || strings.TrimSpace(s) != s {
		d.add(path, "must be trimmed non-empty text")
		return s
	}
	u, err := url.Parse(s)
	if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") ||
		u.Hostname() == "" || u.User != nil || strings.ContainsAny(s, " \t\r\n") {
		d.add(path, "must be an absolute http(s) URL with a host")
	}
	return s
}

func (d *decoder) rating(path string, v any, seen map[string]string) (Rating, bool) {
	m, ok := asObject(v)
	if !ok {
		d.add(path, "must be an object")
		return Rating{}, false
	}
	before := len(d.problems)
	for _, k := range unknownKeys(m, "outlet", "domains", "lean", "factuality") {
		d.add(path+"."+k, "unknown field")
	}

	var rating Rating
	rating.Outlet = d.text(path+".outlet", m["outlet"])

	dv, ok := m["domains"]
	if !ok || dv == nil {
		d.add(path+".domains", "must be a non-empty array of domains")
	} else if arr, ok := dv.([]any); ok {
		if len(arr) == 0 {
			d.add(path+".domains", "must be a non-empty array of domains")
		}
		for j, item := range arr {
			domainPath := fmt.Sprintf("%s.domains[%d]", path, j)
			s, ok := item.(string)
			if !ok || !validDomain(s) {
				d.add(domainPath, "must be a domain")
				continue
			}
			key := normalizeDomain(s)
			if _, exists := seen[key]; exists {
				d.add(domainPath, "must be unique")
			} else {
				seen[key] = domainPath
			}
			rating.Domains = append(rating.Domains, s)
		}
	} else {
		d.add(path+".domains", "must be a non-empty array of domains")
	}

	if lv, ok := m["lean"]; !ok || lv == nil {
		d.add(path+".lean", leanRule)
	} else if s, ok := lv.(string); !ok || !Lean(s).Valid() {
		d.add(path+".lean", leanRule)
	} else {
		rating.Lean = Lean(s)
	}

	if fv, ok := m["factuality"]; ok && fv != nil {
		if s, ok := fv.(string); !ok || !Factuality(s).Valid() {
			d.add(path+".factuality", factualityRule)
		} else {
			rating.Factuality = Factuality(s)
		}
	}

	return rating, len(d.problems) == before
}

// validateSource applies the file's source rules to a Source supplied by an
// importer caller.
func validateSource(src Source) []Problem {
	d := &decoder{}
	m := map[string]any{
		"name":        src.Name,
		"url":         src.URL,
		"license":     src.License,
		"attribution": src.Attribution,
	}
	d.source(m)
	return d.problems
}
