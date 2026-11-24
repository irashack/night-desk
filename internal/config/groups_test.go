package config

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestGroupReaders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "paper.yaml")
	subjectPath := filepath.Join(dir, "subject")
	write(t, subjectPath, "reader-two\n")
	// Without a group reader, sign-in asks for the configured scopes only.
	write(t, path, valid)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.HasGroupReaders() || c.WantsGroups() || !slices.Equal(c.SignInScopes(), []string{"openid"}) {
		t.Fatal("groups asked for without a group reader", c.SignInScopes())
	}
	if c.Server.GroupSessionLifetime != "24h" || c.GroupLifetime() != 24*time.Hour {
		t.Fatal("group session lifetime default", c.Server.GroupSessionLifetime)
	}
	// All three reader forms side by side.
	three := strings.Replace(valid, "    subject: reader-one\n", "    subject: reader-one\n  - issuer: https://id.example\n    subject_file: "+subjectPath+"\n  - issuer: https://id.example\n    group: arcadia-family\n", 1)
	write(t, path, strings.Replace(three, "  data_dir: data\n", "  data_dir: data\n  group_session_lifetime: 2h\n", 1))
	if c, err = Load(path); err != nil {
		t.Fatal(err)
	}
	readers := c.Papers[0].Readers
	if len(readers) != 3 || readers[0].ResolvedSubject() != "reader-one" || readers[1].ResolvedSubject() != "reader-two" || readers[2].Group != "arcadia-family" {
		t.Fatal("reader forms", readers)
	}
	if !c.HasGroupReaders() || !slices.Equal(c.SignInScopes(), []string{"openid", "groups"}) || c.GroupLifetime() != 2*time.Hour {
		t.Fatal("group reader scopes or lifetime", c.SignInScopes(), c.GroupLifetime())
	}
	// Listing groups in oidc.scopes keeps it once, and keeps the claim without group readers.
	write(t, path, strings.Replace(valid, "    client_secret_file: secret-file\n", "    client_secret_file: secret-file\n    scopes: [openid, groups]\n", 1))
	if c, err = Load(path); err != nil {
		t.Fatal(err)
	}
	if !c.WantsGroups() || !slices.Equal(c.SignInScopes(), []string{"openid", "groups"}) {
		t.Fatal("configured groups scope", c.SignInScopes())
	}
	// Exactly one of the three forms, no duplicate groups, a valid lifetime.
	for _, tc := range []struct{ from, to, want string }{
		{"    subject: reader-one\n", "    subject: reader-one\n    group: family\n", "papers[0].readers[0].subject_file"},
		{"    subject: reader-one\n", "    group: family\n    subject_file: " + subjectPath + "\n", "papers[0].readers[0].subject_file"},
		{"    subject: reader-one\n", "    group: ''\n", "papers[0].readers[0].subject_file"},
		{"    subject: reader-one\n", "    group: family\n  - issuer: https://id.example\n    group: family\n", "duplicate issuer/group pair"},
		{"    subject: reader-one\n", "    group: \"bad\\u0001\"\n", "papers[0].readers[0].group"},
		{"  data_dir: data\n", "  data_dir: data\n  group_session_lifetime: -1h\n", "server.group_session_lifetime"},
		{"  data_dir: data\n", "  data_dir: data\n  group_session_lifetime: soon\n", "server.group_session_lifetime"},
	} {
		write(t, path, strings.Replace(valid, tc.from, tc.to, 1))
		if _, err = Load(path); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: %v", tc.to, err)
		}
	}
}
