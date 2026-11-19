package store

import (
	"context"
	"fmt"
	"io/fs"
	"testing"
)

func TestCandidateGroupsMigration(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.db.Exec(`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	paths, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if path >= "migrations/012" {
			continue
		}
		body, err := migrations.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.db.Exec(string(body)); err != nil {
			t.Fatal(err)
		}
		if _, err = s.db.Exec("INSERT INTO schema_migrations VALUES (?,?)", path, now()); err != nil {
			t.Fatal(err)
		}
	}
	// The old column is shared by two rows in one paper, one in another,
	// and absent on an ungrouped snapshot.
	for i, paper := range []string{"a", "a", "b", "a"} {
		var group any = "old-wire"
		if i == 3 {
			group = nil
		}
		if _, err = s.db.Exec(`INSERT INTO candidates
		 (id,paper_id,section,canonical_url,aliases,outlet,title,fetched_at,byline,text,state,wire_group)
		 VALUES (?,?, 'news',?, '[]','Desk','Original title',?, '[]','Original text','ok',?)`, i+1, paper, fmt.Sprintf("https://example.com/%d", i), now(), group); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := func() string {
		t.Helper()
		var raw string
		if err := s.db.QueryRow(`SELECT json_group_array(json_array(id,paper_id,section,canonical_url,aliases,outlet,title,published_at,fetched_at,byline,text,state,wire_group,repeat)) FROM (SELECT * FROM candidates ORDER BY id)`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := snapshot()
	for range 2 {
		if err = s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if after := snapshot(); after != before {
		t.Fatal("migration changed immutable snapshots", after, before)
	}
	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM candidate_groups g JOIN candidates c
	 ON g.paper_id=c.paper_id AND g.candidate_id=c.id WHERE g.group_id=c.wire_group`).Scan(&count); err != nil || count != 3 {
		t.Fatal("old membership not backfilled", count, err)
	}
	if err = s.db.QueryRow("SELECT count(*) FROM candidate_groups").Scan(&count); err != nil || count != 3 {
		t.Fatal("ungrouped candidate gained membership", count, err)
	}
	if _, err = s.db.Exec("INSERT INTO candidate_groups(paper_id,candidate_id,group_id) VALUES ('b',4,'cross-paper')"); err == nil {
		t.Fatal("cross-paper membership accepted")
	}
	if _, err = s.db.Exec("DELETE FROM candidates WHERE paper_id='a'"); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow("SELECT count(*) FROM candidate_groups").Scan(&count); err != nil || count != 1 {
		t.Fatal("pruning did not cascade membership", count, err)
	}
}
