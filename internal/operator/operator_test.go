package operator_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/keys"
	"github.com/irashack/night-desk/internal/operator"
	"github.com/irashack/night-desk/internal/store"
)

func TestOperatorCredentialAggregatesAndPrivacy(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	_, agentKey, err := keys.Issue(ctx, db, "daily", "test")
	if err != nil {
		t.Fatal(err)
	}
	scope, err := auth.Authenticate(ctx, db, agentKey)
	if err != nil {
		t.Fatal(err)
	}
	today := time.Now().UTC().Format("2006-01-02")
	ed := &edition.Edition{Schema: 1, Lead: "one", Sections: []edition.Section{{Key: "news", Items: []edition.Item{{ID: "one", Kind: "news", Title: "SECRET TITLE", Summary: "SECRET SUMMARY", Source: "PRIVATE OUTLET", URL: "https://example.com/private-story"}}}}}
	diagnostics := store.PublishDiagnostics{Path: "put", Warnings: []store.DiagnosticProblem{{Path: "sections[0].items[0].title", Rule: "unsupported-entity: SECRET TITLE"}}, SourceChecks: []store.SourceCheckCount{{Status: "failed", Rule: "unsupported-entity", Count: 3}, {Status: "unverified", Rule: "no source text", Count: 2}}}
	if err = db.PublishWithDiagnostics(ctx, scope, today, ed, nil, diagnostics); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"save", "up", "down"} {
		if err = db.AddFeedback(ctx, scope, store.Feedback{Date: today, ItemID: "one", Kind: kind, ReaderIssuer: "https://id.example", ReaderSubject: "PRIVATE SUBJECT"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.SaveReaderNotes(ctx, scope, "PRIVATE READER NOTE"); err != nil {
		t.Fatal(err)
	}
	if err = db.AddGatherDrops(ctx, scope, today, map[string]int{"betting": 4}); err != nil {
		t.Fatal(err)
	}
	if err = db.AddFetchHealth(ctx, scope, today, []store.HostHealth{{Host: "slow.example", OK: 2, FailedHTTP5xx: 3, CooledOff: 1}}); err != nil {
		t.Fatal(err)
	}
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	rev, err := db.SubmitDraft(ctx, scope, yesterday, "writer", ed)
	if err != nil {
		t.Fatal(err)
	}
	rev, err = db.ReviewDraft(ctx, scope, yesterday, rev, "reviewer", "send_back", "PRIVATE REVIEW NOTE", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.ApplyDeadline(ctx, "daily", yesterday, rev, true, nil); err != nil {
		t.Fatal(err)
	}
	key := "operator-test-credential-that-is-not-an-agent-key"
	digest := sha256.Sum256([]byte(key))
	file := filepath.Join(t.TempDir(), "operator.sha256")
	if err = os.WriteFile(file, []byte(hex.EncodeToString(digest[:])+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Server: config.Server{Operator: config.Operator{KeyHashFile: file}}, Papers: []config.Paper{{ID: "daily", Timezone: "UTC", Sections: []config.Section{{Key: "news", Required: true}}}}}
	handler, err := operator.New(db, func() *config.Config { return cfg })
	if err != nil {
		t.Fatal(err)
	}
	request := func(path, credential string, basic bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(&http.Cookie{Name: "nd_session", Value: "reader-session"})
		if credential != "" {
			if basic {
				r.SetBasicAuth("operator", credential)
			} else {
				r.Header.Set("Authorization", "Bearer "+credential)
			}
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, credential := range []string{"", "wrong", agentKey} {
		w := request("/operator", credential, false)
		if w.Code != 401 || w.Header().Get("WWW-Authenticate") == "" || strings.Contains(w.Body.String(), "daily") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	// Credentials in the URL never authenticate.
	if w := request("/operator?key="+key, "", false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	for _, basic := range []bool{false, true} {
		w := request("/operator", key, basic)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Code, w.Body.String())
		}
		body := w.Body.String()
		for _, want := range []string{"daily", "Editions published</dt><dd>1", "Held</dt><dd>1", "Drafts sent back</dt><dd>1", "unsupported-entity: 3", "no source text: 2", "Gathered items dropped by filter", "betting: 4", "Article fetch health", "<td>slow.example</td><td>2</td><td>3</td>", "/static/app.css"} {
			if !strings.Contains(body, want) {
				t.Fatalf("missing %q: %s", want, body)
			}
		}
		for _, private := range []string{"SECRET", "PRIVATE", "private-story", agentKey, key, "feedback", "Saves", "votes", "notes"} {
			if strings.Contains(body, private) {
				t.Fatalf("operator leaked %q: %s", private, body)
			}
		}
	}
	for _, query := range []string{"0", "-1", "bad", "3661", "1&days=2", ""} {
		if w := request("/operator?days="+query, key, false); w.Code != 400 {
			t.Fatal(query, w.Code, w.Body.String())
		}
	}
	if w := request("/operator?days=1", key, false); w.Code != 200 || !strings.Contains(w.Body.String(), "Held</dt><dd>0") {
		t.Fatal(w.Code, w.Body.String())
	}
	r := httptest.NewRequest("POST", "/operator", nil)
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 405 {
		t.Fatal(w.Code)
	}
}

func TestOperatorDisabledAndInvalidHash(t *testing.T) {
	cfg := &config.Config{}
	current := func() *config.Config { return cfg }
	handler, err := operator.New(nil, current)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/operator", nil))
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
	cfg.Server.Operator.KeyHashFile = filepath.Join(t.TempDir(), "hash")
	if _, err = operator.New(nil, current); err == nil {
		t.Fatal("missing digest accepted")
	}
	for _, content := range []string{"SECRET CONTENT", strings.Repeat("a", 63), strings.Repeat("a", 66), strings.Repeat("z", 64)} {
		if err = os.WriteFile(cfg.Server.Operator.KeyHashFile, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = operator.New(nil, current); err == nil || strings.Contains(err.Error(), content) {
			t.Fatal("invalid digest accepted or disclosed", err)
		}
	}
}
