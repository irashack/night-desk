package store

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	"github.com/irashack/night-desk/internal/auth"
)

// A conditional editorial save commits only against the revision it expected.
func TestSaveEditorialAtComparesAndSwaps(t *testing.T) {
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
	if err = s.InsertKey(ctx, "test-editorial", "daily", "", sha256.Sum256([]byte(key))); err != nil {
		t.Fatal(err)
	}
	scope, err := auth.Authenticate(ctx, s, key)
	if err != nil {
		t.Fatal(err)
	}
	if rev, err := s.SaveEditorialAt(ctx, scope, 0, json.RawMessage(`{"masthead":"A"}`)); err != nil || rev != 1 {
		t.Fatal(rev, err)
	}
	for _, expected := range []int64{0, 2, -1} {
		if _, err := s.SaveEditorialAt(ctx, scope, expected, json.RawMessage(`{"masthead":"B"}`)); !errors.Is(err, ErrEditorialConflict) {
			t.Fatalf("expected %d: %v", expected, err)
		}
	}
	if rev, err := s.SaveEditorialAt(ctx, scope, 1, nil); err != nil || rev != 2 {
		t.Fatal(rev, err)
	}
	revs, err := s.EditorialRevisions(ctx, scope, 50)
	if err != nil || len(revs) != 2 || revs[1].Revision != 1 || string(revs[1].Body) != `{"masthead":"A"}` {
		t.Fatalf("revisions: %+v %v", revs, err)
	}
}
