package config

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const valid = `server:
  listen: "127.0.0.1:8080"
  public_base_url: https://paper.example
  data_dir: data
  oidc:
    issuer: https://id.example
    client_id: nightdesk
    client_secret_file: secret-file
papers:
- id: daily
  masthead: The Daily
  brief: Prefer primary sources.
  deadline: "06:30"
  motto: {guidance: A verified quotation.}
  look: broadsheet
  timezone: America/Chicago
  latitude: 41
  longitude: -87
  sections:
  - key: economy
    title: Economy
    brief: Explain household effects.
    required: true
  boxes:
    weather: true
  readers:
  - issuer: https://id.example
    subject: reader-one
`

func write(t *testing.T, path, s string) {
	t.Helper()
	if e := os.WriteFile(path, []byte(s), 0600); e != nil {
		t.Fatal(e)
	}
}
func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paper.yaml")
	write(t, path, valid)
	c, e := Load(path)
	if e != nil {
		t.Fatal(e)
	}
	if c.Server.SessionLifetime != "720h" || len(c.Server.OIDC.Scopes) != 1 || c.Server.OIDC.Scopes[0] != "openid" {
		t.Fatal(c.Server)
	}
	if !filepath.IsAbs(c.Server.DataDir) || !filepath.IsAbs(c.Server.OIDC.ClientSecretFile) {
		t.Fatal("paths not relative to config")
	}
	p, ok := c.Paper("daily")
	if !ok || !p.Rules().Sections[0].Required || !p.Boxes.Weather {
		t.Fatal(p)
	}
	if p.Brief == nil || *p.Brief != "Prefer primary sources." || p.Deadline == nil || *p.Deadline != "06:30" || p.Motto == nil || p.Motto.Guidance != "A verified quotation." || p.Sections[0].Brief == nil || *p.Sections[0].Brief != "Explain household effects." {
		t.Fatal("missing editorial configuration", p)
	}
	if _, ok = c.Paper("missing"); ok {
		t.Fatal("missing paper found")
	}
}
func TestValidation(t *testing.T) {
	for _, tt := range []struct{ from, to, path string }{
		{"listen: \"127.0.0.1:8080\"", "listen: broken", "server.listen"},
		{"https://paper.example", "http://paper.example", "server.public_base_url"},
		{"data_dir: data", "data_dir: ''", "server.data_dir"},
		{"issuer: https://id.example", "issuer: http://id.example", "server.oidc.issuer"},
		{"client_id: nightdesk", "client_id: ''", "server.oidc.client_id"},
		{"client_secret_file: secret-file", "client_secret_file: ''", "server.oidc.client_secret_file"},
		{"  oidc:", "  session_lifetime: -1h\n  oidc:", "server.session_lifetime"},
		{"    client_id: nightdesk", "    scopes: [profile]\n    client_id: nightdesk", "server.oidc.scopes"},
		{"id: daily", "id: Bad_ID", "papers[0].id"},
		{"masthead: The Daily", "masthead: ''", "papers[0].masthead"},
		{"look: broadsheet", "look: tabloid", "papers[0].look"},
		{"timezone: America/Chicago", "timezone: Mars/Desk", "papers[0].timezone"},
		{"latitude: 41", "latitude: 91", "papers[0].latitude"},
		{"latitude: 41", "latitude: null", "papers[0].latitude"},
		{"longitude: -87", "longitude: -181", "papers[0].longitude"},
		{"key: economy", "key: Bad_KEY", "papers[0].sections[0].key"},
		{"title: Economy", "title: ''", "papers[0].sections[0].title"},
		{"subject: reader-one", "subject: ''", "papers[0].readers[0].subject"},
		{"look: broadsheet", "look: broadsheet\n  typo: true", "typo"},
		{"  listen: \"127.0.0.1:8080\"", "  listen: \"127.0.0.1:8080\"\n  listen: x", "listen"},
	} {
		t.Run(tt.path+tt.to, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			write(t, path, strings.Replace(valid, tt.from, tt.to, 1))
			if _, e := Load(path); e == nil || !strings.Contains(e.Error(), tt.path) {
				t.Fatalf("wanted %s, got %v", tt.path, e)
			}
		})
	}
}
func TestReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paper.yaml")
	write(t, path, valid)
	m, e := NewManager(path)
	if e != nil {
		t.Fatal(e)
	}
	old := m.Current()
	write(t, path, strings.Replace(valid, "broadsheet", "invalid", 1))
	if e = m.Reload(true); e == nil {
		t.Fatal("accepted invalid reload")
	}
	if m.Current() != old {
		t.Fatal("bad reload replaced old config")
	}
	changed := strings.Replace(valid, "The Daily", "The Evening", 1)
	changed = strings.Replace(changed, "Prefer primary sources.", "Prefer local reporting.", 1)
	changed = strings.Replace(changed, "06:30", "07:00", 1)
	changed = strings.Replace(changed, "A verified quotation.", "A verified maxim.", 1)
	changed = strings.Replace(changed, "Explain household effects.", "Explain regional effects.", 1)
	write(t, path, changed)
	// Simulate the 10-second watch's mtime path, without sleeping.
	future := time.Now().Add(time.Second)
	if e = os.Chtimes(path, future, future); e != nil {
		t.Fatal(e)
	}
	if e = m.Reload(false); e != nil {
		t.Fatal(e)
	}
	if m.Current().Papers[0].Masthead != "The Evening" {
		t.Fatal("mtime reload missed")
	}
	p := m.Current().Papers[0]
	if *p.Brief != "Prefer local reporting." || *p.Deadline != "07:00" || p.Motto.Guidance != "A verified maxim." || *p.Sections[0].Brief != "Explain regional effects." {
		t.Fatal("editorial reload missed")
	}
	old = m.Current()
	write(t, path, strings.Replace(valid, "8080", "9090", 1))
	if e = m.Reload(true); e == nil || m.Current() != old {
		t.Fatal("server mutation should require restart", e)
	}
	write(t, path, valid)
	hup := make(chan os.Signal, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { m.Watch(ctx, hup, slog.New(slog.NewJSONHandler(io.Discard, nil))); close(done) }()
	hup <- os.Interrupt
	deadline := time.Now().Add(time.Second)
	for m.Current().Papers[0].Masthead != "The Daily" {
		if time.Now().After(deadline) {
			t.Fatal("signal reload missed")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
}

func TestDuplicateIdentitiesAndSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paper.yaml")
	write(t, path, valid)
	for _, tt := range []struct {
		path   string
		change func(*Config)
	}{
		{"papers[1].id", func(c *Config) { c.Papers = append(c.Papers, c.Papers[0]) }},
		{"papers[0].sections[1].key", func(c *Config) { c.Papers[0].Sections = append(c.Papers[0].Sections, c.Papers[0].Sections[0]) }},
		{"papers[0].readers[1]", func(c *Config) { c.Papers[0].Readers = append(c.Papers[0].Readers, c.Papers[0].Readers[0]) }},
	} {
		c, e := Load(path)
		if e != nil {
			t.Fatal(e)
		}
		tt.change(c)
		if e = c.Validate(); e == nil || !strings.Contains(e.Error(), tt.path) {
			t.Fatal(tt.path, e)
		}
	}
	c, e := Load(path)
	if e != nil {
		t.Fatal(e)
	}
	c.Papers[0].Readers = append(c.Papers[0].Readers, Reader{Issuer: "https://other.example", Subject: "reader-one"})
	if e = c.Validate(); e != nil {
		t.Fatal("subject uniqueness must be per issuer", e)
	}
}

func TestOptionalPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paper.yaml")
	write(t, path, strings.Replace(valid, "masthead: The Daily", "masthead: The Daily\n  place: Baton Rouge", 1))
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Papers[0].Place != "Baton Rouge" {
		t.Fatal("missing place label")
	}
	write(t, path, strings.Replace(valid, "masthead: The Daily", "masthead: The Daily\n  place: ' padded '", 1))
	if _, err = Load(path); err == nil || !strings.Contains(err.Error(), "papers[0].place") {
		t.Fatal("unvalidated place label", err)
	}
}

func TestEditorialValidation(t *testing.T) {
	for _, tt := range []struct{ from, to, path string }{
		{"Prefer primary sources.", "''", "papers[0].brief"},
		{"Prefer primary sources.", "' padded '", "papers[0].brief"},
		{"Prefer primary sources.", "'<b>HTML</b>'", "papers[0].brief"},
		{"Prefer primary sources.", "'[link](https://example.com)'", "papers[0].brief"},
		{"Prefer primary sources.", `"bad\ntext"`, "papers[0].brief"},
		{"Prefer primary sources.", strings.Repeat("é", 2001), "papers[0].brief"},
		{"A verified quotation.", "''", "papers[0].motto.guidance"},
		{"A verified quotation.", "' padded '", "papers[0].motto.guidance"},
		{"A verified quotation.", "'<b>HTML</b>'", "papers[0].motto.guidance"},
		{"A verified quotation.", strings.Repeat("x", 2001), "papers[0].motto.guidance"},
		{"motto: {guidance: A verified quotation.}", "motto: {}", "papers[0].motto.guidance"},
		{"Explain household effects.", "''", "papers[0].sections[0].brief"},
		{"Explain household effects.", "' padded '", "papers[0].sections[0].brief"},
		{"Explain household effects.", strings.Repeat("é", 601), "papers[0].sections[0].brief"},
		{`"06:30"`, `"6:30"`, "papers[0].deadline"},
		{`"06:30"`, `"24:00"`, "papers[0].deadline"},
		{`"06:30"`, `"12:60"`, "papers[0].deadline"},
		{`"06:30"`, `" 06:30"`, "papers[0].deadline"},
		{`"06:30"`, `"06:30:00"`, "papers[0].deadline"},
		{`"06:30"`, `""`, "papers[0].deadline"},
		{`"06:30"`, `630`, "papers[0].deadline"},
		{"brief: Prefer primary sources.", "brief: null", "papers[0].brief"},
		{"motto: {guidance: A verified quotation.}", "motto: null", "papers[0].motto"},
	} {
		t.Run(tt.path+tt.to, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "paper.yaml")
			write(t, path, strings.Replace(valid, tt.from, tt.to, 1))
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), tt.path) {
				t.Fatalf("wanted %s, got %v", tt.path, err)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "paper.yaml")
	for _, clock := range []string{"00:00", "23:59"} {
		s := strings.Replace(valid, "06:30", clock, 1)
		s = strings.Replace(s, "Prefer primary sources.", strings.Repeat("é", 2000), 1)
		s = strings.Replace(s, "Explain household effects.", strings.Repeat("é", 600), 1)
		write(t, path, s)
		if _, err := Load(path); err != nil {
			t.Fatal(err)
		}
	}
	s := valid
	for _, field := range []string{"  brief: Prefer primary sources.\n", "  deadline: \"06:30\"\n", "  motto: {guidance: A verified quotation.}\n", "    brief: Explain household effects.\n"} {
		s = strings.Replace(s, field, "", 1)
	}
	write(t, path, s)
	c, err := Load(path)
	if err != nil || c.Papers[0].Brief != nil || c.Papers[0].Deadline != nil || c.Papers[0].Motto != nil || c.Papers[0].Sections[0].Brief != nil {
		t.Fatal("optional editorial fields", err)
	}
}

func TestSubjectFile(t *testing.T) {
	dir := t.TempDir()
	path, subjectPath := filepath.Join(dir, "paper.yaml"), filepath.Join(dir, "subject")
	write(t, subjectPath, " \nreader-one\n")
	withFile := strings.Replace(valid, "subject: reader-one", "subject_file: "+subjectPath, 1)
	write(t, path, withFile)
	m, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.Current().Papers[0].Readers[0].ResolvedSubject() != "reader-one" {
		t.Fatal("subject file not resolved")
	}
	old := m.Current()
	write(t, subjectPath, "reader-two\n")
	if err = m.Reload(true); err != nil || m.Current().Papers[0].Readers[0].ResolvedSubject() != "reader-two" || old.Papers[0].Readers[0].ResolvedSubject() != "reader-one" {
		t.Fatal("subject file reload", err)
	}
	old = m.Current()
	for _, value := range []string{"", " \n ", "reader\none", "\xff"} {
		write(t, subjectPath, value)
		if err = m.Reload(true); err == nil || !strings.Contains(err.Error(), "papers[0].readers[0].subject_file") || m.Current() != old {
			t.Fatal("invalid subject file reload", err)
		}
		if value != "" && strings.Contains(err.Error(), value) {
			t.Fatal("subject content in error")
		}
	}
	if err = os.Remove(subjectPath); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{subjectPath, dir, "relative-subject"} {
		write(t, path, strings.Replace(valid, "subject: reader-one", "subject_file: "+file, 1))
		if _, err = Load(path); err == nil || !strings.Contains(err.Error(), "papers[0].readers[0].subject_file") {
			t.Fatal("invalid subject path", err)
		}
	}
	write(t, subjectPath, "reader-one\n")
	for _, mapping := range []string{
		"subject: reader-one\n    subject_file: " + subjectPath,
		"subject: ''\n    subject_file: " + subjectPath,
		"subject: reader-one\n    subject_file: ''",
		"subject_file: ''",
		"",
	} {
		write(t, path, strings.Replace(valid, "subject: reader-one", mapping, 1))
		if _, err = Load(path); err == nil || !strings.Contains(err.Error(), "papers[0].readers[0].subject_file") {
			t.Fatal("subject choice", err)
		}
	}
	otherPath := filepath.Join(dir, "other-subject")
	write(t, otherPath, "reader-one")
	for _, mapping := range []string{"subject: reader-one", "subject_file: " + otherPath} {
		write(t, path, withFile+"  - issuer: https://id.example\n    "+mapping+"\n")
		if _, err = Load(path); err == nil || !strings.Contains(err.Error(), "papers[0].readers[1]: duplicate issuer/subject pair") || strings.Contains(err.Error(), "reader-one") {
			t.Fatal("duplicate resolved identity", err)
		}
	}
}
