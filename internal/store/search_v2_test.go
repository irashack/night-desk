package store_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/irashack/night-desk/internal/edition"
)

// Migration 025 rebuilds the archive index from stored editions, including the
// schema 2 fields earlier indexes did not cover.
func TestArchiveSearchMigrationBackfillsSchema2Fields(t *testing.T) {
	s, dir := open(t)
	a := scope(t, s, "a")
	byline, stance, writer := "Ana Ruiz", "The levees should go higher", "Lee Park"
	ed := &edition.Edition{Schema: 2, Lead: "story", Sections: []edition.Section{{Key: "news", Items: []edition.Item{
		{ID: "story", Kind: "news", Title: "River plan", Summary: "Approved.", Source: "Wire", URL: "https://example.test/a",
			Byline: &byline, Sources: []edition.Source{{Outlet: "County Herald", URL: "https://herald.example/a", Byline: &writer}, {Outlet: "Valley Radio", URL: "https://radio.example/a"}}},
		{ID: "column", Kind: "opinion", Title: "Too timid", Summary: "A column.", Source: "Wire", URL: "https://example.test/b", Stance: &stance},
		{ID: "plain", Kind: "brief", Title: "Weather", Summary: "Rain.", Source: "Wire", URL: "https://example.test/c"},
	}}}}
	if err := s.Publish(ctx, a, "2026-10-27", ed, nil); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "nightdesk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Forget the index and the migration, as a database from 0.9 would be.
	if _, err = db.Exec(`DELETE FROM archive_items; DELETE FROM schema_migrations WHERE version='migrations/025_archive_search_v2.sql'`); err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for match, want := range map[string]string{
		`{byline outlet_bylines} : ("ruiz")`: "story",
		`{byline outlet_bylines} : ("park")`: "story",
		`{stance} : ("levees")`:              "column",
		`{outlets} : ("valley")`:             "story",
		`{outlets} : ("herald")`:             "story",
		`"rain"`:                             "plain",
	} {
		var got, kind, outlets string
		if err := db.QueryRow(`SELECT item_id,kind,outlets FROM archive_items WHERE paper_id='a' AND archive_items MATCH ?`, match).Scan(&got, &kind, &outlets); err != nil || got != want {
			t.Errorf("%s: got %q %v, want %q", match, got, err, want)
		}
		if got == "story" && (kind != "news" || outlets != "County Herald\nValley Radio") {
			t.Errorf("story row kind %q outlets %q", kind, outlets)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM archive_items`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("index rows %d %v", n, err)
	}
}
