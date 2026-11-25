package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/irashack/night-desk/internal/push"
)

func TestInitCreatesVAPIDKey(t *testing.T) {
	dir := t.TempDir()
	if code := run([]string{"init", "--dir", dir}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatal("init failed")
	}
	path := filepath.Join(dir, push.KeyFile)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("vapid.key: %v %v", info, err)
	}
	if _, err = push.ReadKey(path); err != nil {
		t.Fatal(err)
	}
	// A second init refuses before touching anything, the key included.
	other := t.TempDir()
	if err = os.WriteFile(filepath.Join(other, push.KeyFile), []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"init", "--dir", other}, &bytes.Buffer{}, &bytes.Buffer{}); code == 0 {
		t.Fatal("init replaced an existing vapid.key")
	}
	if _, err = os.Stat(filepath.Join(other, "config.yaml")); err == nil {
		t.Fatal("refused init left files behind")
	}
}

func TestInitAuthModes(t *testing.T) {
	for _, tt := range []struct {
		mode  string
		ready bool
		want  string // in check's output
	}{
		{"local", true, "Configuration valid."},
		{"none", true, "Configuration valid."},
		{"proxy", false, "papers[0].readers: add at least one verified reader identity"},
		{"oidc", false, "server.oidc.client_id: replace REPLACE_WITH_OIDC_CLIENT_ID"},
	} {
		t.Run(tt.mode, func(t *testing.T) {
			dir := t.TempDir()
			var out, errOut bytes.Buffer
			if code := run([]string{"init", "--dir", dir, "--auth", tt.mode}, &out, &errOut); code != 0 {
				t.Fatalf("init: %s", errOut.String())
			}
			out.Reset()
			errOut.Reset()
			code := run([]string{"check", "--config", filepath.Join(dir, "config.yaml")}, &out, &errOut)
			if (code == 0) != tt.ready || !bytes.Contains(append(out.Bytes(), errOut.Bytes()...), []byte(tt.want)) {
				t.Fatalf("check exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
		})
	}
	if code := run([]string{"init", "--dir", t.TempDir(), "--auth", "magic"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 1 {
		t.Fatal("init accepted an unknown sign-in mode")
	}
}
