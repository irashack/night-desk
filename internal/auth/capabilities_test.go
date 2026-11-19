package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseCapabilities(t *testing.T) {
	for _, tt := range []struct {
		names string
		want  []Capability
	}{
		{DefaultCapabilities, []Capability{Read, Publish, Edit, Review}},
		{WorkerCapabilities, []Capability{Read, Gather, Draft}},
		{"draft,review,gather,edit,publish,read", []Capability{Draft, Review, Gather, Edit, Publish, Read}},
		{"read", []Capability{Read}},
	} {
		got, e := ParseCapabilities(tt.names)
		if e != nil || !reflect.DeepEqual(got, tt.want) || CapabilityNames(got) != tt.names {
			t.Fatal(tt.names, got, e)
		}
	}
	for _, names := range []string{"", "admin", "Read", " read", "read ", "read, publish", "read,", ",read", "read,,publish", "read,read", "read\npublish"} {
		if _, e := ParseCapabilities(names); e == nil {
			t.Fatalf("accepted invalid capabilities %q", names)
		}
	}
}

type capabilityLookup struct{ paper, names string }

func (l capabilityLookup) LookupAgentHash(context.Context, [32]byte) (string, string, error) {
	return l.paper, l.names, nil
}

func TestCapabilityScope(t *testing.T) {
	key := "nd_" + base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("a", 32)))
	for _, tt := range []struct {
		names string
		want  []Capability
	}{
		{DefaultCapabilities, []Capability{Read, Publish, Edit, Review}},
		{WorkerCapabilities, []Capability{Read, Gather, Draft}},
	} {
		scope, e := Authenticate(context.Background(), capabilityLookup{"daily", tt.names}, key)
		if e != nil || !scope.Valid() || scope.PaperID() != "daily" {
			t.Fatal("authentication failed", e)
		}
		for _, c := range []Capability{Read, Publish, Edit, Review, Gather, Draft, "unknown"} {
			want := false
			for _, granted := range tt.want {
				want = want || granted == c
			}
			if scope.Can(c) != want || (PaperScope{}).Can(c) {
				t.Fatal("wrong capability grant", c)
			}
		}
	}
	for _, l := range []capabilityLookup{{"", DefaultCapabilities}, {"daily", ""}, {"daily", "read,unknown"}} {
		if _, e := Authenticate(context.Background(), l, key); !errors.Is(e, ErrUnauthorized) {
			t.Fatal("invalid lookup authenticated", e)
		}
	}
}
