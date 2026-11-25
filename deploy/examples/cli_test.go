package examples_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/store"
)

// These acceptance tests live with the installation examples. They exercise the
// shipped CLI binary.
func TestStandaloneCommands(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "nightdesk")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", binary, "./cmd/nightdesk")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	invoke := func(t *testing.T, args ...string) (int, string, string) {
		t.Helper()
		cmd := exec.Command(binary, args...)
		var out, errOut bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		err := cmd.Run()
		if err == nil {
			return 0, out.String(), errOut.String()
		}
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode(), out.String(), errOut.String()
		}
		t.Fatal(err)
		return -1, "", ""
	}

	for _, custom := range []bool{false, true} {
		name := "defaults"
		if custom {
			name = "custom"
		}
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "install")
			args := []string{"init", "--dir", dir, "--auth", "oidc", "--public-url", "https://paper.example.invalid"}
			paper, masthead, timezone := "daily", "The Daily", "UTC"
			if custom {
				paper, masthead, timezone = "morning", `L'été: "Morning" #1`, "America/Chicago"
				args = append(args, "--paper", paper, "--masthead", masthead, "--timezone", timezone)
			}
			code, out, errOut := invoke(t, args...)
			if code != 0 || errOut != "" {
				t.Fatalf("init exit=%d stderr=%s", code, errOut)
			}
			c, err := config.Load(filepath.Join(dir, "config.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if len(c.Papers) != 1 || c.Papers[0].ID != paper || c.Papers[0].Masthead != masthead || c.Papers[0].Timezone != timezone || c.Server.DataDir != dir {
				t.Fatal("generated configuration differs from requested paper")
			}
			secret, err := os.ReadFile(filepath.Join(dir, "agent.key"))
			if err != nil {
				t.Fatal(err)
			}
			key := strings.TrimSpace(string(secret))
			if !strings.HasPrefix(key, "nd_") || strings.Contains(out+errOut, key) || strings.ContainsAny(key, "\r\n ") {
				t.Fatal("initial key missing, malformed or disclosed")
			}
			for _, file := range []string{"agent.key", "config.yaml", "nightdesk.db"} {
				st, err := os.Stat(filepath.Join(dir, file))
				if err != nil || st.Mode().Perm() != 0600 {
					t.Fatalf("%s must exist with mode 0600", file)
				}
			}
			db, err := store.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()
			scope, err := auth.Authenticate(ctx, db, key)
			if err != nil || scope.PaperID() != paper {
				t.Fatal("initial key does not authenticate for its paper")
			}
			metadata, err := db.Keys(ctx, paper)
			if err != nil || len(metadata) != 1 {
				t.Fatal("init must create exactly one key")
			}
			if err := db.Migrate(ctx); err != nil {
				t.Fatal("migrations must already be applied and repeatable:", err)
			}
			// The unedited template is valid but not ready: check names the placeholders.
			if code, out, errOut := invoke(t, "check", "--config", filepath.Join(dir, "config.yaml")); code != 1 || out != "" ||
				!strings.HasPrefix(errOut, "Configuration valid, but not ready to serve:\n") || !strings.Contains(errOut, "REPLACE_WITH_OIDC_CLIENT_ID") {
				t.Fatalf("check exit=%d stdout=%s stderr=%s", code, out, errOut)
			}
			template, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			ready := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(ready, readyConfig(template), 0600); err != nil {
				t.Fatal(err)
			}
			if code, out, errOut := invoke(t, "check", "--config", ready); code != 0 || out != "Configuration valid.\n" || errOut != "" {
				t.Fatalf("check exit=%d stdout=%s stderr=%s", code, out, errOut)
			}
			if code, out, errOut := invoke(t, args...); code != 1 || !strings.Contains(errOut, "refusing to overwrite") || strings.Contains(out+errOut, key) {
				t.Fatal("reinitialization must refuse without disclosing the key")
			}
			after, err := os.ReadFile(filepath.Join(dir, "agent.key"))
			if err != nil || !bytes.Equal(secret, after) {
				t.Fatal("reinitialization changed the key")
			}
			metadata, err = db.Keys(ctx, paper)
			if err != nil || len(metadata) != 1 {
				t.Fatal("reinitialization created another key")
			}
		})
	}

	t.Run("refuse existing files and symlinks", func(t *testing.T) {
		for _, name := range []string{"config.yaml", "agent.key", "nightdesk.db", "nightdesk.db-wal", "nightdesk.db-shm", "nightdesk.db-journal"} {
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, name)
				if err := os.WriteFile(path, []byte("untouched"), 0600); err != nil {
					t.Fatal(err)
				}
				code, _, errOut := invoke(t, "init", "--dir", dir)
				if code != 1 || !strings.Contains(errOut, "refusing to overwrite "+path) {
					t.Fatalf("exit=%d stderr=%s", code, errOut)
				}
				b, err := os.ReadFile(path)
				entries, listErr := os.ReadDir(dir)
				if err != nil || string(b) != "untouched" || listErr != nil || len(entries) != 1 {
					t.Fatal("refused init modified the installation")
				}
			})
		}
		dir := t.TempDir()
		if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "agent.key")); err != nil {
			t.Fatal(err)
		}
		if code, _, errOut := invoke(t, "init", "--dir", dir); code != 1 || !strings.Contains(errOut, "refusing to overwrite") {
			t.Fatal("dangling symlink must count as an existing file")
		}
	})

	t.Run("reject invalid init settings before writing", func(t *testing.T) {
		for _, tc := range []struct{ flag, value, problem string }{
			{"--paper", "Not a slug", "papers[0].id: must be a unique slug"},
			{"--masthead", "   ", "papers[0].masthead: must be trimmed non-empty text"},
			{"--timezone", "No/SuchZone", "papers[0].timezone: must be an IANA timezone"},
		} {
			dir := filepath.Join(t.TempDir(), "install")
			code, _, errOut := invoke(t, "init", "--dir", dir, tc.flag, tc.value)
			if code != 1 || errOut != tc.problem+"\n" {
				t.Fatalf("exit=%d stderr=%q", code, errOut)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("invalid settings created installation files")
			}
		}
	})

	t.Run("check reports exact problems without initializing storage", func(t *testing.T) {
		dir := t.TempDir()
		if code, _, errOut := invoke(t, "init", "--dir", dir, "--auth", "oidc", "--public-url", "https://paper.example.invalid"); code != 0 {
			t.Fatal(errOut)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		template := readyConfig(raw)
		for _, tc := range []struct{ name, text, problem string }{
			{"valid", string(template), ""},
			{"semantic", strings.ReplaceAll(strings.ReplaceAll(string(template), `"0.0.0.0:8080"`, `"bad"`), `"UTC"`, `"No/SuchZone"`), "server.listen: must be host:port with port 1..65535; papers[0].timezone: must be an IANA timezone"},
			{"unknown", string(template) + "mystery: true\n", "mystery: unknown field"},
			{"shape", "papers: wrong\n", "papers: must be an array"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				clean := t.TempDir()
				path := filepath.Join(clean, "config.yaml")
				if err := os.WriteFile(path, []byte(tc.text), 0600); err != nil {
					t.Fatal(err)
				}
				code, _, errOut := invoke(t, "check", "--config", path)
				wantCode, wantErr := 0, ""
				if tc.problem != "" {
					wantCode, wantErr = 1, tc.problem+"\n"
				}
				if code != wantCode || errOut != wantErr {
					t.Fatalf("exit=%d stderr=%q want exit=%d stderr=%q", code, errOut, wantCode, wantErr)
				}
				entries, err := os.ReadDir(clean)
				if err != nil || len(entries) != 1 {
					t.Fatal("check created storage")
				}
			})
		}
		if code, _, errOut := invoke(t, "check", "--config", filepath.Join(t.TempDir(), "missing")); code != 1 || !strings.Contains(errOut, "no such file") {
			t.Fatal("missing configuration must fail")
		}
	})

	t.Run("usage", func(t *testing.T) {
		for _, args := range [][]string{
			{"init"}, {"init", "--dir"}, {"init", "--unknown"}, {"init", "--dir", t.TempDir(), "extra"},
			{"check"}, {"check", "--config"}, {"check", "--unknown"}, {"check", "--config", "x", "extra"},
		} {
			if code, _, _ := invoke(t, args...); code != 2 {
				t.Fatalf("%v: expected usage exit 2, got %d", args, code)
			}
		}
	})
}

// readyConfig fills in the placeholders `init` leaves in its template.
func readyConfig(template []byte) []byte {
	return []byte(strings.NewReplacer(
		"paper.example.invalid", "paper.example.com",
		"identity.example.invalid", "identity.example.com",
		"REPLACE_WITH_OIDC_CLIENT_ID", "nightdesk",
		"readers: []", `readers: [{issuer: "https://identity.example.com", subject: "reader-1"}]`,
	).Replace(string(template)))
}
