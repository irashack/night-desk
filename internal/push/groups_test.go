package push

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/keys"
)

// withShared adds a paper read by the "family" group.
func (d *desk) withShared(t *testing.T) {
	t.Helper()
	d.cfg.Papers = append(d.cfg.Papers, config.Paper{ID: "shared", Sections: []config.Section{{Key: "news", Title: "News"}}, Readers: []config.Reader{{Issuer: "https://id.example.com", Group: "family"}}})
	_, k, err := keys.Issue(context.Background(), d.db, "shared", "test")
	if err != nil {
		t.Fatal(err)
	}
	if d.papers["shared"], err = auth.Authenticate(context.Background(), d.db, k); err != nil {
		t.Fatal(err)
	}
}

// signIn records a sign-in with groups, as the reader's callback does.
func (d *desk) signIn(t *testing.T, subject, seed string, groups ...string) string {
	t.Helper()
	raw := sha256.Sum256([]byte(subject + "\x00" + seed))
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	now := time.Now()
	if err := d.db.InsertSession(context.Background(), auth.Session{Hash: sha256.Sum256([]byte(token)), Issuer: "https://id.example.com", Subject: subject, CSRF: "c", Created: now, LastSeen: now, Expires: now.Add(time.Hour), Groups: groups}); err != nil {
		t.Fatal(err)
	}
	return token
}

// subscribeTo registers endpoint for one paper the session reads.
func (d *desk) subscribeTo(t *testing.T, token, paper, endpoint string) {
	t.Helper()
	ctx := context.Background()
	scope, err := auth.AuthenticateReader(ctx, d.db, d.cfg, token, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if scope, ok := scope.For(paper); !ok {
		t.Fatalf("session does not read %s", paper)
	} else if err = d.db.AddPushSubscription(ctx, scope, endpoint, nil); err != nil {
		t.Fatal(err)
	}
}

func TestGroupPushRechecksReadership(t *testing.T) {
	svc := service(t)
	d := setup(t, svc.Client())
	d.withShared(t)
	// A subject reader in the group, and a reader known only by the group.
	one := d.signIn(t, "one", "a", "family")
	d.subscribeTo(t, one, "daily", svc.URL+"/one-daily")
	d.subscribeTo(t, one, "shared", svc.URL+"/one-shared")
	four := d.signIn(t, "four", "a", "family", "other")
	d.subscribeTo(t, four, "shared", svc.URL+"/four")
	d.publish(t, "shared", "2026-11-03")
	d.tick(t)
	if got := svc.paths(); !same(got, "/four", "/one-shared") && !same(got, "/one-shared", "/four") {
		t.Fatalf("shared paper pushed to %v", got)
	}
	d.publish(t, "daily", "2026-11-03")
	d.tick(t)
	if got := svc.paths(); !same(got, "/one-daily") {
		t.Fatalf("own paper pushed to %v", got)
	}
	// Leaving the group shows at the next sign-in: it refreshes every
	// subscription's groups, so the shared paper stops notifying.
	d.signIn(t, "four", "b", "other")
	d.publish(t, "shared", "2026-11-04")
	d.tick(t)
	if got := svc.paths(); !same(got, "/one-shared") {
		t.Fatalf("removed member pushed: %v", got)
	}
	// Dropping the group reader from the paper stops the rest at once.
	d.cfg.Papers[2].Readers = []config.Reader{{Issuer: "https://id.example.com", Group: "someone-else"}}
	d.publish(t, "shared", "2026-11-05")
	d.tick(t)
	if got := svc.paths(); len(got) != 0 {
		t.Fatalf("paper without the group pushed: %v", got)
	}
	// A subject reader's own subscription does not depend on groups.
	d.signIn(t, "one", "b")
	d.publish(t, "daily", "2026-11-04")
	d.tick(t)
	if got := svc.paths(); !same(got, "/one-daily") {
		t.Fatalf("subject push lost with groups: %v", got)
	}
}
