package store_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/irashack/night-desk/internal/auth"
)

func TestPublishedEditionPreservesStoredJSON(t *testing.T) {
	s, dir := open(t)
	a, b := scope(t, s, "a"), scope(t, s, "b")
	db, e := sql.Open("sqlite", filepath.Join(dir, "nightdesk.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	body := `{"schema":1,"future_optional":{"kept":true},"sections":[]}`
	at := "2026-10-30T12:00:00Z"
	if _, e = db.Exec(`INSERT INTO editions(paper_id,date,schema,body,published_at) VALUES (?,?,?,?,?)`, "a", "2026-10-30", 1, body, at); e != nil {
		t.Fatal(e)
	}
	got, publishedAt, e := s.PublishedEdition(ctx, a, "2026-10-30")
	if e != nil || string(got) != body || publishedAt != at {
		t.Fatal("stored JSON changed", string(got), publishedAt, e)
	}
	if _, _, e = s.PublishedEdition(ctx, b, "2026-10-30"); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal("edition crossed tenants", e)
	}
	if _, _, e = s.PublishedEdition(ctx, auth.PaperScope{}, "2026-10-30"); !errors.Is(e, auth.ErrUnauthorized) {
		t.Fatal("zero scope accepted", e)
	}
	if _, _, e = s.PublishedEdition(ctx, a, "bad"); e == nil {
		t.Fatal("invalid date accepted")
	}
	list, e := s.Editions(ctx, b, "", 20)
	if e != nil || list == nil || len(list) != 0 {
		t.Fatal("edition list crossed tenants", list, e)
	}
	list, e = s.Editions(ctx, a, "", 20)
	if e != nil || len(list) != 1 || list[0].Date != "2026-10-30" || list[0].PublishedAt != at {
		t.Fatal(list, e)
	}
	if _, e = s.Editions(ctx, auth.PaperScope{}, "", 20); !errors.Is(e, auth.ErrUnauthorized) {
		t.Fatal("zero scope accepted", e)
	}
	for _, tt := range []struct {
		before string
		limit  int
	}{{"bad", 20}, {"", 0}, {"", 51}} {
		if _, e = s.Editions(ctx, a, tt.before, tt.limit); e == nil {
			t.Fatal("invalid pagination accepted", tt)
		}
	}
	if e = s.InsertKey(ctx, "invalid", "a", "invalid", [32]byte{}, auth.Capability("unknown")); e == nil {
		t.Fatal("invalid capability stored")
	}
}
