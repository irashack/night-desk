package demo

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

type lineWriter struct{ lines chan string }

func (w lineWriter) Write(p []byte) (int, error) {
	w.lines <- string(p)
	return len(p), nil
}

func TestRunServesTheDemoPaper(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	out := lineWriter{make(chan string, 4)}
	done := make(chan error, 1)
	go func() { done <- Run(ctx, Options{Listen: "127.0.0.1:0", Out: out}) }()
	var line string
	select {
	case line = <-out.lines:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(30 * time.Second):
		t.Fatal("demo did not start")
	}
	m := regexp.MustCompile(`http://localhost:(\d+)/`).FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("no address in %q", line)
	}
	get := func(path string) string {
		r, err := http.Get("http://127.0.0.1:" + m[1] + path)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		if r.StatusCode != 200 {
			t.Fatalf("%s: %d", path, r.StatusCode)
		}
		return string(b)
	}
	page := get("/")
	for _, want := range []string{"The Evening Ledger", Banner, "Northgate Bridge"} {
		if !strings.Contains(page, want) {
			t.Fatalf("front page lacks %q", want)
		}
	}
	if !strings.Contains(get("/archive"), "2026-11-16") {
		t.Fatal("archive lacks the first edition")
	}
	// The reading view has the stored copy of the bridge story.
	if read := regexp.MustCompile(`href="(/read/[^"]+)"`).FindStringSubmatch(page); read == nil || !strings.Contains(get(read[1]), "Night Desk's copy of the page") {
		t.Fatal("no reading view")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
