package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
	"testing"
)

func TestMCPCommandArguments(t *testing.T) {
	for _, args := range [][]string{
		{"mcp"},
		{"mcp", "--server", "https://example.com"},
		{"mcp", "--server", "https://example.com", "--key-file", "key", "extra"},
		{"mcp", "--server", "https://example.com", "--key", "forbidden-command-line-key"},
	} {
		var out, errOut bytes.Buffer
		if status := run(args, &out, &errOut); status != 2 || out.Len() != 0 {
			t.Fatalf("MCP invalid arguments: status %d, stdout %q", status, out.String())
		}
	}
	var out, errOut bytes.Buffer
	if status := run([]string{"mcp", "--server", "https://example.com", "--key-file", "/does-not-exist"}, &out, &errOut); status != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "key file unavailable") {
		t.Fatalf("MCP missing key file: status %d, stdout %q, stderr %q", status, out.String(), errOut.String())
	}
}

// Run the real command with pipes to verify stdio framing, a clean EOF and
// shutdown while stdin is still open. No HTTP request or database is needed.
func TestMCPStdioProcess(t *testing.T) {
	if os.Getenv("NIGHTDESK_MCP_TEST_HELPER") == "1" {
		os.Exit(run(os.Args[3:], os.Stdout, os.Stderr))
	}
	for _, shutdown := range []string{"eof", "signal"} {
		t.Run(shutdown, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "key")
			if err := os.WriteFile(path, []byte("test-key"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPStdioProcess$", "--", "mcp", "--server", "http://127.0.0.1:1", "--key-file", path)
			cmd.Env = append(os.Environ(), "NIGHTDESK_MCP_TEST_HELPER=1")
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var diagnostics bytes.Buffer
			cmd.Stderr = &diagnostics
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			client := mcp.NewClient(&mcp.Implementation{Name: "stdio-test", Version: "1"}, nil)
			session, err := client.Connect(ctx, &mcp.IOTransport{Reader: stdout, Writer: stdin}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			tools, err := session.ListTools(ctx, nil)
			if err != nil || len(tools.Tools) == 0 {
				t.Fatal("stdio tools/list failed", err)
			}
			if shutdown == "eof" {
				_ = session.Close()
			} else if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("stdio command did not exit cleanly: %v; %s", err, diagnostics.String())
			}
		})
	}
}
