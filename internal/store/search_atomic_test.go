package store_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/store"
)

func TestPublishRollsBackWhenSearchIndexFails(t *testing.T) {
	s, dir := open(t)
	a := scope(t, s, "a")
	ed := &edition.Edition{Schema: 1, Lead: "one", Sections: []edition.Section{{Key: "news", Items: []edition.Item{{ID: "one", Title: "Original"}}}}}
	sha := strings.Repeat("a", 64)
	if err := s.Publish(ctx, a, "2026-10-27", ed, map[string]store.Image{"one": {SHA256: sha, Width: 1, Height: 1, Bytes: 1}}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "nightdesk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`DROP TABLE archive_items`); err != nil {
		t.Fatal(err)
	}
	ed.Sections[0].Items[0].Title = "Failed replacement"
	if err = s.Publish(ctx, a, "2026-10-27", ed, nil); err == nil {
		t.Fatal("publish accepted a failed index update")
	}
	got, err := s.Edition(ctx, a, "2026-10-27")
	if err != nil || got.Sections[0].Items[0].Title != "Original" {
		t.Fatal("failed index update changed edition", got, err)
	}
	images, err := s.EditionImages(ctx, a, "2026-10-27")
	if err != nil || images["one"] != sha {
		t.Fatal("failed index update changed images", images, err)
	}
}

func TestSearchRejectsMissingReaderScope(t *testing.T) {
	s, _ := open(t)
	if _, err := s.Search(ctx, auth.ReaderScope{}, "news", false); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatal("search accepted missing scope", err)
	}
}
