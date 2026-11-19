package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQualityIncludesPublishDiagnostics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := `server:
  listen: "127.0.0.1:8080"
  public_base_url: https://paper.example
  data_dir: data
  oidc: {issuer: https://id.example, client_id: nightdesk, client_secret_file: secret}
papers:
- id: daily
  masthead: Daily
  look: broadsheet
  timezone: UTC
  latitude: 0
  longitude: 0
  sections: [{key: news, title: News}]
`
	if err := os.WriteFile(path, []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"quality", "--config", path, "--days", "1", "--json"}, &out, &errOut); code != 0 {
		t.Fatal(code, errOut.String())
	}
	var report struct {
		Papers []map[string]json.RawMessage `json:"papers"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"drafts_sent_back", "warnings_by_rule", "failed_source_checks_by_rule", "unverified_source_checks_by_rule", "deadline_misses"} {
		if _, ok := report.Papers[0][field]; !ok {
			t.Errorf("missing %s", field)
		}
	}
}

func TestQualityTextIncludesRuleCounts(t *testing.T) {
	var out bytes.Buffer
	report := qualityPaperReport{PaperID: "daily", Days: 1, Published: 1, DraftsSentBack: 2, DeadlineMisses: 3, WarningsByRule: map[string]int{"unsupported-number": 4}, FailedSourceChecksByRule: map[string]int{"unsupported-number": 4}, UnverifiedSourceChecksByRule: map[string]int{"no source text": 5}}
	if err := writeQualityTable(&out, []qualityPaperReport{report}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SENT BACK", "DEADLINE MISSES", "daily WARNINGS: unsupported-number: 4", "daily FAILED SOURCE CHECKS: unsupported-number: 4", "daily UNVERIFIED SOURCE CHECKS: no source text: 5"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(want, out.String())
		}
	}
}
