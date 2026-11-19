package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConformanceCLI(t *testing.T) {
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, []byte("synthetic-cli-conformance-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		args []string
		code int
	}{
		{[]string{"list"}, 0}, {nil, 2}, {[]string{"other"}, 2},
		{[]string{"serve", "--addr", "0.0.0.0:0", "--scenario", "validation", "--key", key}, 2},
		{[]string{"serve", "--addr", "127.0.0.1:0", "--scenario", "unknown", "--key", key}, 2},
		{[]string{"serve", "--addr", "127.0.0.1:0", "--scenario", "webhooks", "--key", key}, 2},
		{[]string{"serve", "--addr", "127.0.0.1:0", "--scenario", "validation", "--key", key, "--exit-on-finish", "--timeout", "10ms"}, 1},
	} {
		var out, errOut bytes.Buffer
		if code := run(append([]string{"conformance"}, tt.args...), &out, &errOut); code != tt.code {
			t.Fatalf("%v: %d: %s", tt.args, code, errOut.String())
		}
		if strings.Contains(out.String()+errOut.String(), "synthetic-cli-conformance-key") {
			t.Fatal("key leaked")
		}
	}
}

func TestConformanceCLIExitOnReport(t *testing.T) {
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, []byte("synthetic-cli-conformance-key"), 0600); err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	result := make(chan int, 1)
	var errOut bytes.Buffer
	go func() {
		code := conformanceCommand([]string{"serve", "--addr", "127.0.0.1:0", "--scenario", "forbidden", "--key", key, "--exit-on-finish", "--timeout", "5s"}, writer, &errOut)
		writer.Close()
		result <- code
	}()
	scanner := bufio.NewScanner(reader)
	if !scanner.Scan() {
		t.Fatal("no listen address")
	}
	base := strings.TrimPrefix(scanner.Text(), "conformance listening at ")
	req, _ := http.NewRequest("PUT", base+"/api/v1/editions/2026-10-30", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer synthetic-cli-conformance-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal(resp.StatusCode)
	}
	time.Sleep(1250 * time.Millisecond)
	resp, err = http.Get(base + "/conformance/report")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var report struct{ Passed bool }
	if json.NewDecoder(resp.Body).Decode(&report) != nil || !report.Passed {
		t.Fatal("report did not pass")
	}
	for scanner.Scan() {
	} // Drain the final report before waiting for command exit.
	select {
	case code := <-result:
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errOut.String())
		}
	case <-time.After(6 * time.Second):
		t.Fatal("command did not exit")
	}
}

func TestConformanceCLIFailedChecksExitAutomatically(t *testing.T) {
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer receiver.Close()
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, []byte("synthetic-cli-conformance-key"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := conformanceCommand([]string{"serve", "--addr", "127.0.0.1:0", "--scenario", "webhooks", "--key", key, "--webhook", receiver.URL, "--exit-on-finish", "--timeout", "5s"}, &out, &errOut)
	if code != 1 || strings.Contains(errOut.String(), "timed out") || !strings.Contains(out.String(), `"name":"bad_signature_rejected","passed":false`) {
		t.Fatalf("exit %d: %s %s", code, out.String(), errOut.String())
	}
}
