package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGatherCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/paper" || r.Header.Get("Authorization") != "Bearer file-key" {
			t.Error("wrong request")
		}
		fmt.Fprint(w, `{"sections":[{"key":"news"}]}`)
	}))
	defer server.Close()
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyFile, []byte("file-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"gather", "--server", server.URL, "--key-file", keyFile, "--once"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "gathered 0") {
		t.Fatal(code, out.String(), errOut.String())
	}
	for _, args := range [][]string{{"gather"}, {"gather", "--server", server.URL, "--key-file", keyFile, "--interval", "0s"}, {"gather", "--key", "must-not-accept"}} {
		if code := run(args, &out, &errOut); code != 2 {
			t.Fatal(code)
		}
	}
	errOut.Reset()
	if code := run([]string{"gather", "--server", server.URL, "--key-file", keyFile + "missing", "--once"}, &out, &errOut); code != 1 || strings.Contains(errOut.String(), keyFile) {
		t.Fatal(code, errOut.String())
	}
}

func TestGatherWorkerConfig(t *testing.T) {
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/paper" {
			t.Error("unexpected request")
		}
		reads++
		fmt.Fprint(w, `{"agent_role":"author","sections":[{"key":"news"}]}`)
	}))
	defer server.Close()
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")
	configFile := filepath.Join(dir, "worker.yaml")
	if err := os.WriteFile(keyFile, []byte("worker-key"), 0600); err != nil {
		t.Fatal(err)
	}
	// A valid provider config enables drafting checks; author papers stay agent-written.
	cfg := `models:
  fast:
    base_url: http://localhost:11434/v1
    model: fast
  writer:
    base_url: http://localhost:11434/v1
    model: writer
`
	if err := os.WriteFile(configFile, []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"gather", "--server", server.URL, "--key-file", keyFile, "--worker-config", configFile, "--once"}
	var out, errOut bytes.Buffer
	// Source discovery, gathering and drafting each read the paper.
	if code := run(args, &out, &errOut); code != 0 || reads != 3 {
		t.Fatal(code, reads, out.String(), errOut.String())
	}
	if err := os.WriteFile(configFile, []byte(`{"models":{"fast":{"base_url":"private-provider-key"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	errOut.Reset()
	if code := run(args, &out, &errOut); code != 1 || reads != 3 || strings.Contains(errOut.String(), "private-provider-key") {
		t.Fatal(code, reads, errOut.String())
	}
}
