package store

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/auth"
)

func TestExistingKeyCapabilitiesMigration(t *testing.T) {
	ctx := context.Background()
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e = s.db.ExecContext(ctx, `CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"migrations/001_initial.sql", "migrations/002_paper_editorial.sql"} {
		body, e := migrations.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.db.ExecContext(ctx, string(body)); e != nil {
			t.Fatal(e)
		}
		if _, e = s.db.ExecContext(ctx, `INSERT INTO schema_migrations VALUES (?,?)`, path, now()); e != nil {
			t.Fatal(e)
		}
	}
	key := "nd_" + base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("a", 32)))
	hash := sha256.Sum256([]byte(key))
	if _, e = s.db.ExecContext(ctx, `INSERT INTO agent_keys(id,paper_id,hash,label,created_at) VALUES (?,?,?,?,?)`, "old-key", "daily", hash[:], "old", now()); e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal("migration not idempotent", e)
	}
	scope, e := auth.Authenticate(ctx, s, key)
	if e != nil || scope.PaperID() != "daily" {
		t.Fatal("old key lost access", e)
	}
	for _, c := range []auth.Capability{auth.Read, auth.Publish, auth.Edit, auth.Review} {
		if !scope.Can(c) {
			t.Fatal("old key lost capability", c)
		}
	}
	if scope.Can(auth.Gather) || scope.Can(auth.Draft) {
		t.Fatal("migration granted new worker powers")
	}
	list, e := s.Keys(ctx, "daily")
	if e != nil || len(list) != 1 || auth.CapabilityNames(list[0].Capabilities) != auth.DefaultCapabilities {
		t.Fatal("incorrect key metadata", list, e)
	}
	if _, e = s.db.ExecContext(ctx, `UPDATE agent_keys SET capabilities='read,unknown' WHERE id='old-key'`); e != nil {
		t.Fatal(e)
	}
	if _, e = auth.Authenticate(ctx, s, key); !errors.Is(e, auth.ErrUnauthorized) {
		t.Fatal("invalid stored capabilities authenticated", e)
	}
	if _, e = s.Keys(ctx, "daily"); e == nil {
		t.Fatal("invalid stored capabilities listed")
	}
}
