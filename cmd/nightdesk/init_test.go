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
