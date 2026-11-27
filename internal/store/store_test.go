package store_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/keys"
	"github.com/irashack/night-desk/internal/store"
)

var ctx = context.Background()

func open(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, e := store.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	return s, dir
}
func scope(t *testing.T, s *store.Store, paper string) auth.PaperScope {
	t.Helper()
	_, key, e := keys.Issue(ctx, s, paper, "test")
	if e != nil {
		t.Fatal(e)
	}
	p, e := auth.Authenticate(ctx, s, key)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestMigrations(t *testing.T) {
	s, dir := open(t)
	if e := s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite", filepath.Join(dir, "nightdesk.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	for _, table := range []string{"schema_migrations", "agent_keys", "editions", "edition_images", "images", "feedback", "sessions", "paper_editorial", "candidates"} {
		var n int
		if e = db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); e != nil {
			t.Fatalf("%s: %v", table, e)
		}
	}
	var n int
	if e = db.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&n); e != nil || n != migrationCount(t) {
		t.Fatalf("migrations=%d %v", n, e)
	}
	var mode string
	if e = db.QueryRow("PRAGMA journal_mode").Scan(&mode); e != nil || mode != "wal" {
		t.Fatalf("journal=%s %v", mode, e)
	}
}
func TestTenantIsolationAndReplace(t *testing.T) {
	s, _ := open(t)
	a, b := scope(t, s, "a"), scope(t, s, "b")
	ed := &edition.Edition{Schema: 1, Lead: "one", Sections: []edition.Section{{Key: "news", Items: []edition.Item{{ID: "one", Title: "A"}}}}}
	for _, d := range []string{"2026-10-27", "2026-10-28"} {
		if e := s.Publish(ctx, b, d, ed, map[string]store.Image{"one": {SHA256: strings.Repeat("a", 64), Width: 1, Height: 1, Bytes: 1}}); e != nil {
			t.Fatal(e)
		}
	}
	for _, d := range []string{"2026-10-27", "2026-10-28"} {
		if _, e := s.Edition(ctx, a, d); !errors.Is(e, sql.ErrNoRows) {
			t.Fatalf("A read B: %v", e)
		}
		im, e := s.EditionImages(ctx, a, d)
		if e != nil || len(im) != 0 {
			t.Fatal("A read B images", im, e)
		}
	}
	if e := s.Publish(ctx, a, "2026-10-27", ed, nil); e != nil {
		t.Fatal(e)
	}
	ed.Sections[0].Items[0].Title = "replacement"
	if e := s.Publish(ctx, a, "2026-10-27", ed, nil); e != nil {
		t.Fatal(e)
	}
	got, e := s.Edition(ctx, a, "2026-10-27")
	if e != nil || got.Sections[0].Items[0].Title != "replacement" {
		t.Fatal(got, e)
	}
	got, e = s.Edition(ctx, b, "2026-10-27")
	if e != nil || got.Sections[0].Items[0].Title != "A" {
		t.Fatal("replacement crossed tenants", got, e)
	}
	// Replacing also drops obsolete retained-image associations.
	if e = s.Publish(ctx, b, "2026-10-27", ed, nil); e != nil {
		t.Fatal(e)
	}
	if im, e := s.EditionImages(ctx, b, "2026-10-27"); e != nil || len(im) != 0 {
		t.Fatal(im, e)
	}
	for _, p := range []auth.PaperScope{b, a, b, a, b} {
		if e = s.AddFeedback(ctx, p, store.Feedback{Date: "2026-10-27", ItemID: "one", Kind: "up", ReaderIssuer: "https://issuer", ReaderSubject: "sub"}); e != nil {
			t.Fatal(e)
		}
	}
	events, next, e := s.Feedback(ctx, a, 0, 1)
	if e != nil || len(events) != 1 || next != 2 {
		t.Fatal(events, next, e)
	}
	events, next, e = s.Feedback(ctx, a, next, 1)
	if e != nil || len(events) != 1 || next != 4 {
		t.Fatal(events, next, e)
	}
	events, next, e = s.Feedback(ctx, a, next, 500)
	if e != nil || len(events) != 0 || next != 4 {
		t.Fatal(events, next, e)
	}
	events, _, e = s.Feedback(ctx, b, 0, 500)
	if e != nil || len(events) != 3 {
		t.Fatal(events, e)
	}
	if _, e = s.Edition(ctx, auth.PaperScope{}, "2026-10-27"); !errors.Is(e, auth.ErrUnauthorized) {
		t.Fatal("zero scope accepted", e)
	}
	if e = s.Publish(ctx, auth.PaperScope{}, "2026-10-27", ed, nil); !errors.Is(e, auth.ErrUnauthorized) {
		t.Fatal(e)
	}
	if _, _, e = s.Feedback(ctx, auth.PaperScope{}, 0, 1); !errors.Is(e, auth.ErrUnauthorized) {
		t.Fatal(e)
	}
}
func TestKeys(t *testing.T) {
	s, dir := open(t)
	id, key, e := keys.Issue(ctx, s, "a", "desk")
	if e != nil {
		t.Fatal(e)
	}
	if len(key) != 46 || !strings.HasPrefix(key, "nd_") {
		t.Fatal("wrong format")
	}
	p, e := auth.Authenticate(ctx, s, key)
	if e != nil || p.PaperID() != "a" {
		t.Fatal(p, e)
	}
	hash := sha256.Sum256([]byte(key))
	db, e := sql.Open("sqlite", filepath.Join(dir, "nightdesk.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var stored []byte
	if e = db.QueryRow("SELECT hash FROM agent_keys WHERE id=?", id).Scan(&stored); e != nil || string(stored) != string(hash[:]) {
		t.Fatal("not stored by hash", e)
	}
	list, e := s.Keys(ctx, "a")
	if e != nil || len(list) != 1 || list[0].LastUsedAt == nil || list[0].Label != "desk" {
		t.Fatal(list, e)
	}
	if list, e = s.Keys(ctx, "b"); e != nil || len(list) != 0 {
		t.Fatal(list, e)
	}
	for _, bad := range []string{"", "nd_bad", strings.Repeat("x", 46), "nd_" + strings.Repeat("A", 43), key + "="} {
		if _, e = auth.Authenticate(ctx, s, bad); !errors.Is(e, auth.ErrUnauthorized) {
			t.Fatal("bad key accepted")
		}
	}
	if e = s.RevokeKey(ctx, id); e != nil {
		t.Fatal(e)
	}
	if _, e = auth.Authenticate(ctx, s, key); !errors.Is(e, auth.ErrUnauthorized) {
		t.Fatal("revoked key accepted")
	}
	list, e = s.Keys(ctx, "")
	if e != nil || len(list) != 1 || list[0].RevokedAt == nil {
		t.Fatal(list, e)
	}
	if e = s.RevokeKey(ctx, "missing"); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal(e)
	}
}
func TestBackup(t *testing.T) {
	s, dir := open(t)
	a := scope(t, s, "a")
	ed := &edition.Edition{Schema: 1, Lead: "one", Sections: []edition.Section{}}
	if e := s.Publish(ctx, a, "2026-10-27", ed, nil); e != nil {
		t.Fatal(e)
	}
	imageDir := filepath.Join(dir, "images", "ab")
	if e := os.MkdirAll(imageDir, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(imageDir, "test.jpg"), []byte("retained"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, store.PushKeyFile), []byte("push key"), 0600); e != nil {
		t.Fatal(e)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if e := s.Backup(ctx, backup); e != nil {
		t.Fatal(e)
	}
	if info, e := os.Stat(filepath.Join(backup, store.PushKeyFile)); e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("the push key is backed up privately", info, e)
	}
	if b, _ := os.ReadFile(filepath.Join(backup, store.PushKeyFile)); string(b) != "push key" {
		t.Fatal("push key contents", string(b))
	}
	other, e := store.Open(backup)
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	if e = other.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = other.Edition(ctx, a, "2026-10-27"); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(backup, "images", "ab", "test.jpg"))
	if e != nil || string(b) != "retained" {
		t.Fatal(string(b), e)
	}
	if e = os.Remove(filepath.Join(dir, store.PushKeyFile)); e != nil {
		t.Fatal(e)
	}
	keyless := filepath.Join(t.TempDir(), "keyless")
	if e = s.Backup(ctx, keyless); e != nil {
		t.Fatal("an install without a push key still backs up", e)
	}
	if _, e = os.Stat(filepath.Join(keyless, store.PushKeyFile)); !os.IsNotExist(e) {
		t.Fatal("no push key to copy", e)
	}
	if e = s.Backup(ctx, backup); e == nil {
		t.Fatal("overwrote existing backup")
	}
	if e = s.Backup(ctx, filepath.Join(dir, "nested")); e == nil {
		t.Fatal("recursive backup accepted")
	}
}

func migrationCount(t *testing.T) int {
	t.Helper()
	paths, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	return len(paths)
}

func TestFailedBackupLeavesNoSnapshot(t *testing.T) {
	s, dir := open(t)
	imageDir := filepath.Join(dir, "images", "ab")
	if e := os.MkdirAll(imageDir, 0700); e != nil {
		t.Fatal(e)
	}
	// A non-regular file in the image store fails the backup after the
	// database copy is written.
	if e := os.Symlink("/dev/null", filepath.Join(imageDir, "odd.jpg")); e != nil {
		t.Fatal(e)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if e := s.Backup(ctx, backup); e == nil {
		t.Fatal("backup with a non-regular image succeeded")
	}
	for _, name := range []string{"nightdesk.db", "images"} {
		if _, e := os.Lstat(filepath.Join(backup, name)); !os.IsNotExist(e) {
			t.Fatalf("a failed backup left %s behind", name)
		}
	}
	if e := os.Remove(filepath.Join(imageDir, "odd.jpg")); e != nil {
		t.Fatal(e)
	}
	if e := s.Backup(ctx, backup); e != nil {
		t.Fatal("retrying into the same directory failed", e)
	}
}
