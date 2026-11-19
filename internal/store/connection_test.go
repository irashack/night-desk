package store

import (
	"context"
	"testing"
	"time"
)

func TestConnectionPragmas(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	check := func() {
		t.Helper()
		for _, p := range []struct {
			sql  string
			want int
		}{{"PRAGMA foreign_keys", 1}, {"PRAGMA busy_timeout", 5000}} {
			var value int
			if e = s.db.QueryRowContext(context.Background(), p.sql).Scan(&value); e != nil || value != p.want {
				t.Fatal(p.sql, value, e)
			}
		}
	}
	check()
	s.db.SetConnMaxLifetime(time.Nanosecond)
	time.Sleep(time.Millisecond)
	check()
}
