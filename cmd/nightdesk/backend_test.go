package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/store"
)

func TestAdministrativeCommands(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nightdesk.yaml")
	config := `server:
  listen: "127.0.0.1:8080"
  public_base_url: https://paper.example
  data_dir: data
  oidc:
    issuer: https://id.example
    client_id: nightdesk
    client_secret_file: secret-file
papers:
- id: daily
  masthead: The Daily
  look: broadsheet
  timezone: America/Chicago
  latitude: 41
  longitude: -87
  sections:
  - key: economy
    title: Economy
    required: true
  readers:
  - issuer: https://id.example
    subject: sub
`
	if e := os.WriteFile(path, []byte(config), 0600); e != nil {
		t.Fatal(e)
	}
	invoke := func(args ...string) (int, string, string) {
		t.Helper()
		var out, errOut bytes.Buffer
		args = append(args, "--config", path)
		code := run(args, &out, &errOut)
		return code, out.String(), errOut.String()
	}
	for i := 0; i < 2; i++ {
		if code, _, errOut := invoke("migrate"); code != 0 {
			t.Fatal(code, errOut)
		}
	}
	code, key, errOut := invoke("key", "issue", "daily", "--label", "Writer")
	if code != 0 || !strings.HasPrefix(key, "nd_") || strings.Contains(errOut, "nd_") {
		t.Fatal("key issue failed or leaked")
	}
	key = strings.TrimSpace(key)
	code, list, errOut := invoke("key", "list", "daily")
	if code != 0 || strings.Contains(list, key) || strings.Contains(list, "hash") {
		t.Fatal("key list failed or leaked", errOut)
	}
	var metadata []store.Key
	if e := json.Unmarshal([]byte(list), &metadata); e != nil || len(metadata) != 1 || metadata[0].Label != "Writer" || auth.CapabilityNames(metadata[0].Capabilities) != auth.DefaultCapabilities {
		t.Fatal(metadata, e)
	}
	db, e := store.Open(filepath.Join(dir, "data"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = auth.Authenticate(context.Background(), db, key); e != nil {
		t.Fatal(e)
	}
	db.Close()
	if code, _, errOut = invoke("key", "revoke", metadata[0].ID); code != 0 {
		t.Fatal(code, errOut)
	}
	db, e = store.Open(filepath.Join(dir, "data"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = auth.Authenticate(context.Background(), db, key); e == nil {
		t.Fatal("CLI revoke did not revoke")
	}
	db.Close()
	backup := filepath.Join(dir, "backup")
	if code, _, errOut = invoke("backup", backup); code != 0 {
		t.Fatal(code, errOut)
	}
	// A configuration whose data_dir has no database is not backed up as an
	// empty install.
	moved := filepath.Join(dir, "data-moved")
	if e := os.Rename(filepath.Join(dir, "data"), moved); e != nil {
		t.Fatal(e)
	}
	if code, _, _ := invoke("backup", filepath.Join(dir, "backup-empty")); code == 0 {
		t.Fatal("backup of a missing database succeeded")
	}
	if _, e := os.Stat(filepath.Join(dir, "data", "nightdesk.db")); !os.IsNotExist(e) {
		t.Fatal("backup created a database at the missing data_dir")
	}
	if e := os.RemoveAll(filepath.Join(dir, "data")); e != nil {
		t.Fatal(e)
	}
	if e := os.Rename(moved, filepath.Join(dir, "data")); e != nil {
		t.Fatal(e)
	}
	copy, e := store.Open(backup)
	if e != nil {
		t.Fatal(e)
	}
	defer copy.Close()
	keys, e := copy.Keys(context.Background(), "daily")
	if e != nil || len(keys) != 1 || keys[0].RevokedAt == nil {
		t.Fatal(keys, e)
	}
	for _, args := range [][]string{{"key", "issue", "missing"}, {"key", "revoke", "missing"}} {
		if code, _, _ = invoke(args...); code != 1 {
			t.Fatal("bad administrative command accepted", args, code)
		}
	}
	for _, args := range [][]string{{"serve", "extra"}, {"migrate", "extra"}, {"backup"}, {"key"}, {"key", "issue"}, {"key", "issue", "daily", "--other", "x"}, {"key", "revoke"}, {"key", "list", "daily", "extra"}, {"key", "other"}} {
		if code, _, _ = invoke(args...); code != 2 {
			t.Fatal("bad arguments accepted", args, code)
		}
	}
	for _, tt := range []struct {
		options []string
		names   string
	}{
		{[]string{"--worker", "--label", "Worker"}, auth.WorkerCapabilities},
		{[]string{"--capabilities", "read,publish"}, "read,publish"},
	} {
		args := append([]string{"key", "issue", "daily"}, tt.options...)
		code, issued, errOut := invoke(args...)
		if code != 0 || strings.Contains(errOut, "nd_") {
			t.Fatal("restricted key issue failed or leaked")
		}
		db, e := store.Open(filepath.Join(dir, "data"))
		if e != nil {
			t.Fatal(e)
		}
		p, e := auth.Authenticate(context.Background(), db, strings.TrimSpace(issued))
		if e != nil {
			db.Close()
			t.Fatal(e)
		}
		for _, c := range []auth.Capability{auth.Read, auth.Publish, auth.Edit, auth.Review, auth.Gather, auth.Draft} {
			if p.Can(c) != strings.Contains(tt.names, string(c)) {
				t.Error("CLI granted wrong capabilities", c)
			}
		}
		db.Close()
	}
	for _, options := range [][]string{{"--capabilities", "unknown"}, {"--capabilities", ""}, {"--worker", "--capabilities", "read"}} {
		if code, _, _ := invoke(append([]string{"key", "issue", "daily"}, options...)...); code != 2 {
			t.Fatal("bad capability options accepted", code)
		}
	}
}
