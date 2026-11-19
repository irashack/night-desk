package edition_test

import (
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/edition"
)

func TestDecodeStoredIndependentOfIncomingRules(t *testing.T) {
	// Missing current required fields, and a field no longer accepted on input,
	// must not make a previously published edition unreadable.
	body := `{"schema":1,"lead":"old","legacy_note":"kept","sections":[{"key":"retired","items":[{"id":"old","title":"Old story"}]}]}`
	if _, err := edition.Decode(strings.NewReader(body)); err == nil {
		t.Fatal("incoming decoder accepted the legacy shape")
	}
	ed, err := edition.DecodeStored(1, []byte(body))
	if err != nil || ed.Sections[0].Items[0].Title != "Old story" {
		t.Fatalf("stored decode: %+v %v", ed, err)
	}
	for _, tc := range []struct {
		schema int
		body   string
	}{
		{99, body}, {1, `{"schema":2}`}, {1, `{"schema":1} {}`}, {1, `null`}, {1, `{"schema":1,"sections":"bad"}`},
	} {
		if _, err := edition.DecodeStored(tc.schema, []byte(tc.body)); err == nil {
			t.Errorf("accepted schema %d: %s", tc.schema, tc.body)
		}
	}
}

func TestDecodeStoredV2(t *testing.T) {
	e, err := edition.DecodeStored(2, []byte(`{"schema":2,"sections":[{"key":"news","items":[{"id":"a","kind":"opinion","title":"T","url":"https://example.com/a","source":"Example","byline":"A. Writer","sources":[{"outlet":"Other","url":"https://example.org/a"}]}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if it := e.Sections[0].Items[0]; it.Byline == nil || len(it.Sources) != 1 {
		t.Fatalf("v2 fields lost: %+v", it)
	}
}
