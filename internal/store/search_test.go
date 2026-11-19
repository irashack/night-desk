package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSearchTerms(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{`economy OR source:news*`, `"economy" AND "OR" AND "source" AND "news"`},
		{`"one two" NOT (three)`, `"one" AND "two" AND "NOT" AND "three"`},
		{"café 日本 42", `"café" AND "日本" AND "42"`},
		{"***\x00", ""},
	} {
		if got := searchTerms(tc.input); got != tc.want {
			t.Errorf("%q: got %s, want %s", tc.input, got, tc.want)
		}
	}
}

func TestSearchMigrationBackfillsPublishedEditions(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// Recreate the pre-005 state with a historical edition. Running the real
	// migration proves backfill without depending on the new publish helper.
	for _, statement := range []string{`DROP TABLE archive_items`, `DROP INDEX feedback_reader_saves`, `DELETE FROM schema_migrations WHERE version='migrations/005_archive_search.sql'`} {
		if _, err = s.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	body := `{"schema":1,"lead":"old","sections":[{"key":"news","items":[{"id":"old","title":"Historical report","summary":"Old summary","source":"Wire"},{"id":"other","title":"Other report","summary":"Context","why":"Relevance","source":"Local"}]}]}`
	if !json.Valid([]byte(body)) {
		t.Fatal("invalid historical fixture")
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO editions(paper_id,date,schema,body,published_at) VALUES (?,?,?,?,?)`, "old-paper", "2026-10-27", 1, body, now()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT paper_id,date,item_id,title,summary,why,source FROM archive_items ORDER BY item_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got [][7]string
	for rows.Next() {
		var row [7]string
		if err = rows.Scan(&row[0], &row[1], &row[2], &row[3], &row[4], &row[5], &row[6]); err != nil {
			t.Fatal(err)
		}
		got = append(got, row)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := [][7]string{
		{"old-paper", "2026-10-27", "old", "Historical report", "Old summary", "", "Wire"},
		{"old-paper", "2026-10-27", "other", "Other report", "Context", "Relevance", "Local"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backfill got %v, want %v", got, want)
	}
}
