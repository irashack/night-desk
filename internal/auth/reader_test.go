package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/config"
)

type readerLookup struct{ session Session }

func (l readerLookup) Session(context.Context, [32]byte) (Session, error) { return l.session, nil }
func (l readerLookup) SlideSession(context.Context, [32]byte, time.Time, time.Time) error {
	return nil
}

func TestReaderSubjectFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subject")
	if err := os.WriteFile(path, []byte(" \nreader-one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	lat, lon := 41.0, -87.0
	c := &config.Config{
		Server: config.Server{
			Listen: "127.0.0.1:8080", PublicBaseURL: "https://paper.example", DataDir: dir,
			SessionLifetime: "720h", OIDC: config.OIDC{
				Issuer: "https://id.example", ClientID: "nightdesk", ClientSecretFile: "unused", Scopes: []string{"openid"},
			},
		},
		Papers: []config.Paper{{ID: "daily", Masthead: "The Daily", Look: "broadsheet",
			Timezone: "America/Chicago", Latitude: &lat, Longitude: &lon,
			Sections: []config.Section{{Key: "economy", Title: "Economy"}},
			Readers:  []config.Reader{{Issuer: "https://id.example", SubjectFile: path}},
		}},
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	// Authentication uses the loaded snapshot, not a live file read.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	token := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("a", 32)))
	lookup := readerLookup{Session{Hash: sha256.Sum256([]byte(token)), Issuer: "https://id.example", Subject: "reader-one", Expires: now.Add(time.Hour)}}
	scope, err := AuthenticateReader(context.Background(), lookup, c, token, now, time.Hour)
	if err != nil || scope.PaperScope().PaperID() != "daily" {
		t.Fatal("file-backed identity not mapped", err)
	}
	lookup.session.Subject = "other-reader"
	if _, err = AuthenticateReader(context.Background(), lookup, c, token, now, time.Hour); err != ErrUnmapped {
		t.Fatal("wrong subject mapped", err)
	}
}
