package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestOperatorConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	write(t, path, strings.Replace(valid, "server:", "server:\n  operator:\n    key_hash_file: operator.sha256", 1))
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.Operator.KeyHashFile != filepath.Join(filepath.Dir(path), "operator.sha256") {
		t.Fatal(c.Server.Operator)
	}
	for _, setting := range []string{"key_hash_file: 3", "key_hash_file: true", "key_hash_file: ' '", "unknown: hash"} {
		write(t, path, strings.Replace(valid, "server:", "server:\n  operator:\n    "+setting, 1))
		if _, err = Load(path); err == nil || !strings.Contains(err.Error(), "operator") {
			t.Fatal(setting, err)
		}
	}
}
