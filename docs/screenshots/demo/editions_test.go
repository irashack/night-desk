package main

import (
	"bytes"
	"testing"

	"github.com/irashack/night-desk/internal/edition"
)

// The demo editions must stay publishable as the schema and validator change.
func TestEditionsValidate(t *testing.T) {
	paper := demoConfig("http://127.0.0.1:1", "http://127.0.0.1:2", t.TempDir()).Papers[0]
	files, err := editions.ReadDir("editions")
	if err != nil || len(files) < 3 {
		t.Fatalf("want at least three demo editions: %v", err)
	}
	for _, f := range files {
		body, err := editions.ReadFile("editions/" + f.Name())
		if err != nil {
			t.Fatal(err)
		}
		e, err := edition.Decode(bytes.NewReader(body))
		if err != nil {
			t.Fatalf("%s: %v", f.Name(), err)
		}
		for _, p := range edition.Validate(e, paper.Rules()) {
			t.Errorf("%s: %s (%s)", f.Name(), p, p.Severity)
		}
	}
}
