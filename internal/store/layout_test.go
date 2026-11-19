package store_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/store"
)

func TestEditionLayout(t *testing.T) {
	s, dir := open(t)
	a, b := scope(t, s, "a"), scope(t, s, "b")
	ed := &edition.Edition{Schema: 1, Lead: "old", Sections: []edition.Section{{Key: "retired", Items: []edition.Item{{ID: "old", Title: "Old story"}}}}}
	layout := &store.EditionLayout{Masthead: "Old Gazette", Sections: []store.LayoutSection{{Key: "retired", Title: "Old section"}}}
	if err := s.Publish(ctx, a, "2026-10-27", ed, nil, layout); err != nil {
		t.Fatal(err)
	}
	got, snap, err := s.EditionWithLayout(ctx, a, "2026-10-27")
	if err != nil || !reflect.DeepEqual(got, ed) || !reflect.DeepEqual(snap, layout) {
		t.Fatalf("read: %+v %+v %v", got, snap, err)
	}
	if _, _, err := s.EditionWithLayout(ctx, b, "2026-10-27"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("other tenant: %v", err)
	}
	if _, _, err := s.EditionWithLayout(ctx, auth.PaperScope{}, "2026-10-27"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("invalid scope: %v", err)
	}
	layout.Masthead = "Replacement"
	if err := s.Publish(ctx, a, "2026-10-27", ed, nil, layout); err != nil {
		t.Fatal(err)
	}
	_, snap, err = s.EditionWithLayout(ctx, a, "2026-10-27")
	if err != nil || snap.Masthead != "Replacement" {
		t.Fatalf("replacement: %+v %v", snap, err)
	}
	if err := s.Publish(ctx, a, "2026-10-27", ed, nil); err != nil {
		t.Fatal(err)
	}
	_, snap, err = s.EditionWithLayout(ctx, a, "2026-10-27")
	if err != nil || snap != nil {
		t.Fatalf("legacy publication: %+v %v", snap, err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "nightdesk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("UPDATE editions SET layout='invalid JSON'"); err == nil {
		t.Fatal("layout JSON constraint missing")
	}
	// The database schema column, not the body's version alone, selects decoding.
	if _, err = db.Exec("UPDATE editions SET schema=99"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Edition(ctx, a, "2026-10-27"); err == nil {
		t.Fatal("unknown schema decoded")
	}
}
