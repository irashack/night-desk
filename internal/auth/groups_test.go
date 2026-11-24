package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/config"
)

func groupConfig() *config.Config {
	const id = "https://id.example"
	return &config.Config{Server: config.Server{GroupSessionLifetime: "24h"}, Papers: []config.Paper{
		{ID: "own", Readers: []config.Reader{{Issuer: id, Subject: "reader-one"}}},
		{ID: "second", Readers: []config.Reader{{Issuer: id, Subject: "reader-one"}}},
		{ID: "shared", Readers: []config.Reader{{Issuer: id, Group: "family"}, {Issuer: id, Group: "friends"}}},
		{ID: "friends", Readers: []config.Reader{{Issuer: id, Group: "friends"}, {Issuer: "https://other.example", Group: "family"}}},
	}}
}

func papers(s ReaderScope) string {
	var ids []string
	for _, a := range s.Papers() {
		mark := ""
		if a.BySubject {
			mark = "*"
		}
		ids = append(ids, a.Paper+mark)
	}
	return strings.Join(ids, ",")
}

func TestReaderForms(t *testing.T) {
	cfg := groupConfig()
	now := time.Now()
	token := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("g", 32)))
	session := func(subject string, groups ...string) readerLookup {
		return readerLookup{Session{Hash: sha256.Sum256([]byte(token)), Issuer: "https://id.example", Subject: subject, Groups: groups, Created: now, Expires: now.Add(720 * time.Hour)}}
	}
	for _, tc := range []struct {
		name, subject string
		groups        []string
		want          string
		err           error
	}{
		// Subject first (only the first paper naming it), then group papers in order.
		{"subject only", "reader-one", nil, "own*", nil},
		{"subject and group", "reader-one", []string{"family"}, "own*,shared", nil},
		{"group only", "reader-z", []string{"friends"}, "shared,friends", nil},
		{"other issuer's group", "reader-z", []string{"nobody"}, "", ErrUnmapped},
		{"unmapped", "reader-z", nil, "", ErrUnmapped},
	} {
		scope, err := AuthenticateReader(context.Background(), session(tc.subject, tc.groups...), cfg, token, now, time.Hour)
		if err != tc.err || papers(scope) != tc.want {
			t.Errorf("%s: %q %v", tc.name, papers(scope), err)
		}
		if err == nil && !scope.Default() {
			t.Errorf("%s: first paper not the default", tc.name)
		}
	}
	scope, _ := AuthenticateReader(context.Background(), session("reader-one", "family"), cfg, token, now, time.Hour)
	if !scope.BySubject() || scope.PaperScope().PaperID() != "own" {
		t.Fatal("subject paper not selected first")
	}
	shared, ok := scope.For("shared")
	if !ok || shared.BySubject() || shared.Default() || shared.PaperScope().PaperID() != "shared" {
		t.Fatal("group paper selection")
	}
	if _, ok = scope.For("friends"); ok {
		t.Fatal("a paper of another group selected")
	}
	if _, ok = scope.For("second"); ok {
		t.Fatal("a second subject mapping selected (first match only)")
	}
}

func TestGroupMembershipExpires(t *testing.T) {
	cfg := groupConfig()
	signedIn := time.Now()
	token := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("h", 32)))
	lookup := readerLookup{Session{Hash: sha256.Sum256([]byte(token)), Issuer: "https://id.example", Subject: "reader-one", Groups: []string{"family"}, Created: signedIn, Expires: signedIn.Add(720 * time.Hour)}}
	later := signedIn.Add(24 * time.Hour)
	// A subject reader keeps their own paper and loses the group paper.
	scope, err := AuthenticateReader(context.Background(), lookup, cfg, token, later, time.Hour)
	if err != nil || papers(scope) != "own*" || strings.Join(scope.Refresh(), ",") != "shared" || !scope.CanEdit() {
		t.Fatal("stale groups still granted", papers(scope), scope.Refresh(), err)
	}
	if _, ok := scope.For("shared"); ok {
		t.Fatal("stale group paper selected")
	}
	// A reader admitted only by group must sign in again.
	lookup.session.Subject = "reader-z"
	if _, err = AuthenticateReader(context.Background(), lookup, cfg, token, later, time.Hour); err != ErrUnauthorized {
		t.Fatal("group-only reader kept a stale session", err)
	}
	if scope, err = AuthenticateReader(context.Background(), lookup, cfg, token, later.Add(-time.Minute), time.Hour); err != nil || papers(scope) != "shared" {
		t.Fatal("group lifetime cut short", err)
	}
	// A shorter configured lifetime applies.
	cfg.Server.GroupSessionLifetime = "1h"
	if _, err = AuthenticateReader(context.Background(), lookup, cfg, token, signedIn.Add(time.Hour), time.Hour); err != ErrUnauthorized {
		t.Fatal("group_session_lifetime ignored", err)
	}
	// An unmapped session is offered sign-in again once stale, but only when
	// some paper has group readers; otherwise unmapped behavior is unchanged.
	lookup.session.Groups = nil
	if _, err = AuthenticateReader(context.Background(), lookup, cfg, token, later, time.Hour); err != ErrUnauthorized {
		t.Fatal("stale unmapped session", err)
	}
	plain := &config.Config{Papers: cfg.Papers[:2]}
	if _, err = AuthenticateReader(context.Background(), lookup, plain, token, later.Add(600*time.Hour), 720*time.Hour); err != ErrUnmapped {
		t.Fatal("unmapped behavior changed without group readers", err)
	}
	lookup.session.Groups = []string{"family"}
	// Removing the group reader from the paper takes effect at once.
	cfg.Server.GroupSessionLifetime = "24h"
	cfg.Papers[2].Readers = cfg.Papers[2].Readers[1:]
	if _, err = AuthenticateReader(context.Background(), lookup, cfg, token, signedIn, time.Hour); err != ErrUnmapped {
		t.Fatal("group reader removed from config still reads", err)
	}
}
