package store

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"github.com/irashack/night-desk/internal/auth"
)

func TestReaderNotesKeepsFifty(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	key := "nd_" + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	err = s.InsertKey(ctx, "test-notes", "daily", "", sha256.Sum256([]byte(key)))
	if err != nil {
		t.Fatal(err)
	}
	scope, err := auth.Authenticate(ctx, s, key)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 55; i++ {
		if _, err = s.SaveReaderNotes(ctx, scope, "Notes."); err != nil {
			t.Fatal(err)
		}
	}
	var count, first, last int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*),min(revision),max(revision) FROM paper_notes WHERE paper_id='daily'").Scan(&count, &first, &last); err != nil || count != 50 || first != 6 || last != 55 {
		t.Fatal(count, first, last, err)
	}
}
