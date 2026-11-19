package auth

import (
	"errors"
	"strings"
)

type Capability string

const (
	Read    Capability = "read"
	Publish Capability = "publish"
	Edit    Capability = "edit"
	Review  Capability = "review"
	Gather  Capability = "gather"
	Draft   Capability = "draft"

	DefaultCapabilities = "read,publish,edit,review"
	WorkerCapabilities  = "read,gather,draft"
)

// ParseCapabilities accepts a non-empty comma-separated set of known names.
// Whitespace, empty names and duplicates are rejected rather than normalized.
func ParseCapabilities(names string) ([]Capability, error) {
	result := []Capability{}
	seen := map[Capability]bool{}
	for _, name := range strings.Split(names, ",") {
		c := Capability(name)
		switch c {
		case Read, Publish, Edit, Review, Gather, Draft:
		default:
			return nil, errors.New("capabilities: must be comma-separated names from read,publish,edit,review,gather,draft")
		}
		if seen[c] {
			return nil, errors.New("capabilities: must not contain duplicates")
		}
		seen[c] = true
		result = append(result, c)
	}
	return result, nil
}

func CapabilityNames(capabilities []Capability) string {
	names := make([]string, len(capabilities))
	for i, c := range capabilities {
		names[i] = string(c)
	}
	return strings.Join(names, ",")
}
