package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/auth"
)

// holdWriteLock takes SQLite's write lock from a second connection, as another
// process would, until the returned release runs. Anything the store does under
// it that needs a write transaction waits out busy_timeout and fails, so an
// operation that completes promptly and correctly did not try to write.
func holdWriteLock(t *testing.T, s *Store) (release func()) {
	t.Helper()
	ctx := context.Background()
	u := url.URL{Scheme: "file", Path: filepath.Join(s.dataDir, "nightdesk.db"), RawQuery: url.Values{"_pragma": {"busy_timeout(0)"}}.Encode()}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	done := false
	release = func() {
		if done {
			return
		}
		done = true
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		conn.Close()
		db.Close()
	}
	t.Cleanup(release)
	return release
}

// readOnly runs op under another connection's write lock and fails the test if
// op needed the lock.
func readOnly(t *testing.T, s *Store, name string, op func(context.Context) error) {
	t.Helper()
	release := holdWriteLock(t, s)
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	err := op(ctx)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("%s waited %v for the write lock", name, elapsed)
	}
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

func TestIdleClaimsTakeNoWriteLock(t *testing.T) {
	s, _ := eventStore(t)
	at := time.Now().Add(time.Second)
	readOnly(t, s, "ClaimDelivery", func(ctx context.Context) error {
		if _, err := s.ClaimDelivery(ctx, at); !errors.Is(err, sql.ErrNoRows) {
			return errors.Join(errors.New("want no rows"), err)
		}
		return nil
	})
	readOnly(t, s, "ClaimPush", func(ctx context.Context) error {
		if _, err := s.ClaimPush(ctx, at); !errors.Is(err, sql.ErrNoRows) {
			return errors.Join(errors.New("want no rows"), err)
		}
		return nil
	})
}

func TestDueWorkIsStillClaimed(t *testing.T) {
	s, scope := eventStore(t)
	ctx := context.Background()
	if err := s.Publish(ctx, scope, "2026-10-30", eventEdition(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimDelivery(ctx, time.Now().Add(time.Second)); err != nil {
		t.Fatal("due delivery not claimed:", err)
	}
	// Exhausted attempts are still marked failed although nothing is claimable.
	if _, err := s.db.Exec("UPDATE deliveries SET attempts=12,next_attempt_at=0"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimDelivery(ctx, time.Now()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	var open int
	if err := s.db.QueryRow("SELECT count(*) FROM deliveries WHERE failed_at IS NULL").Scan(&open); err != nil || open != 0 {
		t.Fatal("exhausted deliveries not failed", open, err)
	}
}

func TestSlideSessionWritesAtMostOncePerMinute(t *testing.T) {
	s, _ := eventStore(t)
	ctx := context.Background()
	hash := sha256.Sum256([]byte("session"))
	now := time.Now().UTC()
	lifetime := 30 * 24 * time.Hour
	if err := s.InsertSession(ctx, auth.Session{Hash: hash, Issuer: "https://id.example", Subject: "one", CSRF: "c", Created: now, LastSeen: now, Expires: now.Add(lifetime)}); err != nil {
		t.Fatal(err)
	}
	later := now.Add(30 * time.Second)
	readOnly(t, s, "SlideSession within a minute", func(ctx context.Context) error {
		return s.SlideSession(ctx, hash, later, later.Add(lifetime))
	})
	v, err := s.Session(ctx, hash)
	if err != nil || !v.Expires.Equal(now.Add(lifetime)) {
		t.Fatal("expiry rewritten within a minute", v.Expires, err)
	}
	// After a minute the expiry slides; after an hour last_seen moves too.
	later = now.Add(2 * time.Minute)
	if err = s.SlideSession(ctx, hash, later, later.Add(lifetime)); err != nil {
		t.Fatal(err)
	}
	if v, err = s.Session(ctx, hash); err != nil || !v.Expires.Equal(later.Add(lifetime)) || !v.LastSeen.Equal(now) {
		t.Fatal("expiry did not slide", v, err)
	}
	later = now.Add(61 * time.Minute)
	if err = s.SlideSession(ctx, hash, later, later.Add(lifetime)); err != nil {
		t.Fatal(err)
	}
	if v, err = s.Session(ctx, hash); err != nil || !v.LastSeen.Equal(later) {
		t.Fatal("last_seen did not move after an hour", v, err)
	}
	// A shortened lifetime takes effect at once.
	if err = s.SlideSession(ctx, hash, later, later.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if v, err = s.Session(ctx, hash); err != nil || !v.Expires.Equal(later.Add(time.Hour)) {
		t.Fatal("shorter lifetime not applied", v, err)
	}
	// Expired and unknown sessions are refused without writing.
	expired := later.Add(2 * time.Hour)
	readOnly(t, s, "SlideSession expired", func(ctx context.Context) error {
		if err := s.SlideSession(ctx, hash, expired, expired.Add(lifetime)); !errors.Is(err, auth.ErrUnauthorized) {
			return errors.Join(errors.New("want unauthorized"), err)
		}
		if err := s.SlideSession(ctx, sha256.Sum256([]byte("none")), now, now.Add(lifetime)); !errors.Is(err, auth.ErrUnauthorized) {
			return errors.Join(errors.New("want unauthorized"), err)
		}
		return nil
	})
}

func TestAgentKeyLastUsedWritesAtMostOncePerMinute(t *testing.T) {
	s, _ := eventStore(t)
	ctx := context.Background()
	key := "nd_" + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	hash := sha256.Sum256([]byte(key))
	if _, _, err := s.LookupAgentHash(ctx, hash); err != nil {
		t.Fatal(err)
	}
	var first string
	if err := s.db.QueryRow("SELECT last_used_at FROM agent_keys WHERE id='key'").Scan(&first); err != nil {
		t.Fatal(err)
	}
	readOnly(t, s, "LookupAgentHash within a minute", func(ctx context.Context) error {
		paper, _, err := s.LookupAgentHash(ctx, hash)
		if err == nil && paper != "daily" {
			err = errors.New("wrong paper " + paper)
		}
		return err
	})
	old := time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err := s.db.Exec("UPDATE agent_keys SET last_used_at=? WHERE id='key'", old); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LookupAgentHash(ctx, hash); err != nil {
		t.Fatal(err)
	}
	var used string
	if err := s.db.QueryRow("SELECT last_used_at FROM agent_keys WHERE id='key'").Scan(&used); err != nil || used == old {
		t.Fatal("last_used_at not refreshed after a minute", used, err)
	}
	if err := s.RevokeKey(ctx, "key"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LookupAgentHash(ctx, hash); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("revoked key accepted", err)
	}
}
