package store_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/editorial"
	"github.com/irashack/night-desk/internal/reader"
	"github.com/irashack/night-desk/internal/store"
	"github.com/irashack/night-desk/internal/testhttp"
	"golang.org/x/oauth2"
)

// Exercise the frozen pre-release schema fixture against all migrations embedded in this build.
// No list of newer migrations is kept here: future migrations run automatically.
func TestUpgradePrereleaseSchema(t *testing.T) {
	dir := t.TempDir()
	dump, err := os.ReadFile("testdata/prerelease-schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", filepath.Join(dir, "nightdesk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err = raw.Exec(string(dump)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := raw.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&n); err != nil || n != 2 {
		t.Fatalf("fixture migrations: %d %v", n, err)
	}
	// Enumerate every edition before upgrading, rather than selecting a sample.
	type archived struct {
		paper, date string
		schema      int
		body        []byte
	}
	rows, err := raw.Query("SELECT paper_id,date,schema,body FROM editions ORDER BY paper_id,date")
	if err != nil {
		t.Fatal(err)
	}
	var before []archived
	for rows.Next() {
		var a archived
		if err := rows.Scan(&a.paper, &a.date, &a.schema, &a.body); err != nil {
			t.Fatal(err)
		}
		before = append(before, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(before) != 3 {
		t.Fatalf("fixture editions: %d", len(before))
	}
	raw.Close()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}

	cfg := &config.Config{Server: config.Server{DataDir: dir, PublicBaseURL: "http://localhost", SessionLifetime: "720h"}}
	scopes := map[string]auth.PaperScope{}
	for i, paper := range []string{"a", "b"} {
		// Deterministic synthetic keys match the fixture's hashes; no new key is issued.
		key := "nd_" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{byte(i + 1)}, 32))
		s, err := auth.Authenticate(ctx, db, key)
		if err != nil || s.PaperID() != paper {
			t.Fatalf("fixture key for %s: %v", paper, err)
		}
		scopes[paper] = s
		cfg.Papers = append(cfg.Papers, config.Paper{ID: paper, Masthead: "Declared " + paper, Timezone: "UTC", Look: "broadsheet", Sections: []config.Section{{Key: "economy", Title: "Declared economy"}}, Readers: []config.Reader{{Issuer: "https://fixture.example", Subject: "reader-" + paper}}})
	}
	desk, err := editorial.Open(ctx, db, func() *config.Config { return cfg }, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, paper := range []string{"a", "b"} {
		expected, revision := "Current A", int64(2)
		if paper == "b" {
			expected, revision = "Archive B", 1
		}
		p, _ := desk.Config().Paper(paper)
		if rev, edited := desk.Revision(paper); rev != revision || !edited || p.Masthead != expected || len(desk.RevisionProblems(paper)) != 0 {
			t.Fatalf("revision %s: %d %v %+v", paper, rev, edited, p)
		}
	}
	// Discovery uses an in-process transport; edition requests use stored sessions.
	network := testhttp.New()
	issuer := network.Unstarted(nil)
	issuerURL := "http://" + issuer.Listener.Addr().String()
	issuer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuerURL, "authorization_endpoint": issuerURL + "/auth", "token_endpoint": issuerURL + "/token", "jwks_uri": issuerURL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	issuer.Start()
	defer issuer.Close()
	nextConfig := *cfg
	nextConfig.Server.OIDC = config.OIDC{Issuer: issuerURL, ClientID: "fixture-reader"}
	cfg = &nextConfig
	srv, err := reader.New(context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Transport: network.Transport()}), db, desk.Config, "")
	if err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler()
	tokens := map[string]string{}
	for i, paper := range []string{"a", "b"} {
		token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{byte(10 + i)}, 32))
		tokens[paper] = token
		now := time.Now()
		if err := db.InsertSession(ctx, auth.Session{Hash: sha256.Sum256([]byte(token)), Issuer: "https://fixture.example", Subject: "reader-" + paper, CSRF: "fixture-csrf", Created: now, LastSeen: now, Expires: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range before {
		ed, layout, err := db.EditionWithLayout(ctx, scopes[a.paper], a.date)
		original, decodeErr := edition.DecodeStored(a.schema, a.body)
		if err != nil || decodeErr != nil || layout != nil || !reflect.DeepEqual(ed, original) {
			t.Fatalf("edition %s/%s: %+v %v %v", a.paper, a.date, ed, err, decodeErr)
		}
		req := httptest.NewRequest("GET", "http://localhost/e/"+a.date, nil)
		req.AddCookie(&http.Cookie{Name: "nd_session", Value: tokens[a.paper]})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("render %s/%s: %d %s", a.paper, a.date, w.Code, w.Body)
		}
		for _, sec := range ed.Sections {
			for _, item := range sec.Items {
				if !strings.Contains(w.Body.String(), html.EscapeString(item.Title)) {
					t.Errorf("lost story %s/%s/%s", a.paper, a.date, item.ID)
				}
			}
		}
		images, err := db.EditionImages(ctx, scopes[a.paper], a.date)
		if err != nil {
			t.Fatal(err)
		}
		if a.date == "2026-10-28" {
			want := strings.Repeat("1", 64)
			if a.paper == "b" {
				want = strings.Repeat("2", 64)
			}
			if images["economy-"+a.paper] != want {
				t.Fatalf("image lost: %v", images)
			}
		}
	}
	// Cursors retain their original global IDs and paper isolation after migration.
	events, cursor, err := db.Feedback(ctx, scopes["a"], 7, 500)
	if err != nil || cursor != 19 || len(events) != 1 || events[0].ID != 19 || events[0].Kind != "save" {
		t.Fatalf("feedback cursor: %+v %d %v", events, cursor, err)
	}
	events, next, err := db.Feedback(ctx, scopes["a"], cursor, 500)
	if err != nil || next != cursor || len(events) != 0 {
		t.Fatalf("empty cursor: %+v %d %v", events, next, err)
	}
	events, next, err = db.Feedback(ctx, scopes["b"], 0, 500)
	if err != nil || next != 12 || len(events) != 1 || events[0].ItemID != "economy-b" {
		t.Fatalf("other feedback: %+v %d %v", events, next, err)
	}
	if err := db.AddFeedback(ctx, scopes["a"], store.Feedback{Date: "2026-10-28", ItemID: "economy-a", Kind: "down", ReaderIssuer: "https://fixture.example", ReaderSubject: "reader-a"}); err != nil {
		t.Fatal(err)
	}
	events, next, err = db.Feedback(ctx, scopes["a"], cursor, 500)
	if err != nil || next != 20 || len(events) != 1 || events[0].ID != 20 {
		t.Fatalf("continued cursor: %+v %d %v", events, next, err)
	}
	if rev, err := desk.Restore(ctx, scopes["a"], 1, editorial.AnyRevision); err != nil || rev != 3 {
		t.Fatalf("restore old revision: %d %v", rev, err)
	}
	again, err := editorial.Open(ctx, db, func() *config.Config { return cfg }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := again.Config().Paper("a"); p.Masthead != "Archive A" || len(p.Sections) != 2 {
		t.Fatalf("restored revision: %+v", p)
	}
	if _, err := desk.Restore(ctx, scopes["b"], 0, editorial.AnyRevision); err != nil {
		t.Fatal(err)
	}
	if p, _ := desk.Config().Paper("b"); p.Masthead != "Declared b" {
		t.Fatalf("restore declaration: %+v", p)
	}
}
