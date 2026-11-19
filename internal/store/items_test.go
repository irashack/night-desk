package store_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/store"
)

func TestItemIdentityAcrossReplacements(t *testing.T) {
	s, dir := open(t)
	a, b := scope(t, s, "a"), scope(t, s, "b")
	date := "2026-10-30"
	ed := draftEdition()
	ed.Sections[0].Items[0].URL = "HTTPS://EXAMPLE.COM/story/?utm_source=feed&keep=1&utm_medium=rss"
	if err := s.Publish(ctx, a, date, ed, nil); err != nil {
		t.Fatal(err)
	}
	ed.Sections[0].Key = "moved"
	ed.Sections[0].Items[0].Title = "Edited title"
	ed.Sections[0].Items[0].URL = "https://example.com/story?keep=1&utm_campaign=other"
	if err := s.Publish(ctx, a, date, ed, nil); err != nil {
		t.Fatal("same story rejected", err)
	}
	before, at, err := s.PublishedEdition(ctx, a, date)
	if err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{"https://example.com/other?keep=1", "https://example.com/story?keep=2", "https://other.example/story?keep=1", "http://example.com/story?keep=1", "https://example.com/Story?keep=1"} {
		ed.Sections[0].Items[0].URL = url
		if err = s.Publish(ctx, a, date, ed, nil); err == nil || err.Error() != "sections[0].items[0].id: already names a different story in this edition; use a new id" {
			t.Fatalf("URL %s: %v", url, err)
		}
	}
	after, afterAt, err := s.PublishedEdition(ctx, a, date)
	if err != nil || string(before) != string(after) || at != afterAt {
		t.Fatal("failed replacement changed edition", err)
	}
	// Identity is local to a paper and date.
	if err = s.Publish(ctx, b, date, ed, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.Publish(ctx, a, "2026-10-29", ed, nil); err != nil {
		t.Fatal(err)
	}
	// Removing and later reintroducing an ID cannot transfer old feedback.
	if err = s.Publish(ctx, a, date, &edition.Edition{Schema: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.Publish(ctx, a, date, ed, nil); err == nil {
		t.Fatal("removed id reused for another story")
	}
	ed.Sections[0].Items[0].ID = "new-id"
	ed.Lead = "new-id"
	if err = s.Publish(ctx, a, date, ed, nil); err != nil {
		t.Fatal("new id rejected", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "nightdesk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var section string
	if err = db.QueryRow("SELECT section FROM edition_items WHERE paper_id=? AND date=? AND item_id=?", "a", date, "one").Scan(&section); err != nil || section != "moved" {
		t.Fatal("identity section not maintained", section, err)
	}
}

func TestItemIdentityMigrationBackfill(t *testing.T) {
	s, dir := open(t)
	a := scope(t, s, "a")
	db, err := sql.Open("sqlite", filepath.Join(dir, "nightdesk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Recreate a pre-013 database with an edition written by the old publisher.
	if _, err = db.Exec("DROP TABLE IF EXISTS edition_items"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("DELETE FROM schema_migrations WHERE version='migrations/013_edition_items.sql'"); err != nil {
		t.Fatal(err)
	}
	ed := draftEdition()
	ed.Sections[0].Items[0].URL = "https://EXAMPLE.com/story/?utm_source=old"
	raw, _ := json.Marshal(ed)
	if _, err = db.Exec("INSERT INTO editions(paper_id,date,schema,body,published_at) VALUES (?,?,?,?,?)", "a", "2026-10-30", 1, string(raw), "2026-10-30T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	ed.Sections[0].Items[0].URL = "https://example.com/story"
	if err = s.Publish(ctx, a, "2026-10-30", ed, nil); err != nil {
		t.Fatal("backfill did not normalize URLs", err)
	}
	ed.Sections[0].Items[0].URL = "https://example.com/unrelated"
	if err = s.Publish(ctx, a, "2026-10-30", ed, nil); err == nil {
		t.Fatal("backfilled identity not enforced")
	}
}

func TestPublishedItemPreservesStoredJSON(t *testing.T) {
	s, dir := open(t)
	a, b := scope(t, s, "a"), scope(t, s, "b")
	db, err := sql.Open("sqlite", filepath.Join(dir, "nightdesk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	item := `{"id":"old","url":"https://example.com/story","future_optional":{"kept":true}}`
	body := `{"schema":1,"sections":[{"key":"retired","items":[` + item + `]}]}`
	date, at := "2026-10-30", "2026-10-30T12:00:00Z"
	if _, err = db.Exec("INSERT INTO editions(paper_id,date,schema,body,published_at) VALUES (?,?,?,?,?)", "a", date, 1, body, at); err != nil {
		t.Fatal(err)
	}
	got, section, publishedAt, err := s.PublishedItem(ctx, a, date, "old")
	if err != nil || string(got) != item || section != "retired" || publishedAt != at {
		t.Fatal(string(got), section, publishedAt, err)
	}
	if _, _, _, err = s.PublishedItem(ctx, b, date, "old"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("item crossed tenants", err)
	}
	if _, _, _, err = s.PublishedItem(ctx, auth.PaperScope{}, date, "old"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("invalid scope accepted", err)
	}
	if _, _, _, err = s.PublishedItem(ctx, a, "bad", "old"); err == nil {
		t.Fatal("invalid date accepted")
	}
}

func TestItemIdentityTransactionRollback(t *testing.T) {
	s, dir := open(t)
	a := scope(t, s, "a")
	db, err := sql.Open("sqlite", filepath.Join(dir, "nightdesk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TRIGGER fail_image BEFORE INSERT ON edition_images BEGIN SELECT RAISE(ABORT, 'test rollback'); END`); err != nil {
		t.Fatal(err)
	}
	date := "2026-10-30"
	if err = s.Publish(ctx, a, date, draftEdition(), map[string]store.Image{"one": {SHA256: "test", Width: 1, Height: 1, Bytes: 1}}); err == nil {
		t.Fatal("publication should fail after recording identities")
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM edition_items WHERE paper_id='a'").Scan(&count); err != nil || count != 0 {
		t.Fatal("identity escaped failed transaction", count, err)
	}
	ed := draftEdition()
	ed.Sections[0].Items[0].URL = "https://example.com/another"
	if err = s.Publish(ctx, a, date, ed, nil); err != nil {
		t.Fatal("failed publication reserved an id", err)
	}
	if err = s.Publish(ctx, a, date, &edition.Edition{Schema: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.PublishedItem(ctx, a, date, "one"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("retired identity exposed an unpublished item", err)
	}
}
