package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/push"
	"github.com/irashack/night-desk/internal/store"
)

func TestInitCreatesVAPIDKey(t *testing.T) {
	dir := t.TempDir()
	if code := run([]string{"init", "--dir", dir}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatal("init failed")
	}
	path := filepath.Join(dir, push.KeyFile)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("vapid.key: %v %v", info, err)
	}
	if _, err = push.ReadKey(path); err != nil {
		t.Fatal(err)
	}
	// A second init refuses before touching anything, the key included.
	other := t.TempDir()
	if err = os.WriteFile(filepath.Join(other, push.KeyFile), []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"init", "--dir", other}, &bytes.Buffer{}, &bytes.Buffer{}); code == 0 {
		t.Fatal("init replaced an existing vapid.key")
	}
	if _, err = os.Stat(filepath.Join(other, "config.yaml")); err == nil {
		t.Fatal("refused init left files behind")
	}
}

func TestInitAuthModes(t *testing.T) {
	for _, tt := range []struct {
		mode  string
		ready bool
		want  string // in check's output
	}{
		{"local", true, "Configuration valid."},
		{"none", true, "Configuration valid."},
		{"proxy", false, "papers[0].readers: add at least one verified reader identity"},
		{"oidc", false, "server.oidc.client_id: replace REPLACE_WITH_OIDC_CLIENT_ID"},
	} {
		t.Run(tt.mode, func(t *testing.T) {
			dir := t.TempDir()
			var out, errOut bytes.Buffer
			if code := run([]string{"init", "--dir", dir, "--auth", tt.mode}, &out, &errOut); code != 0 {
				t.Fatalf("init: %s", errOut.String())
			}
			out.Reset()
			errOut.Reset()
			code := run([]string{"check", "--config", filepath.Join(dir, "config.yaml")}, &out, &errOut)
			if (code == 0) != tt.ready || !bytes.Contains(append(out.Bytes(), errOut.Bytes()...), []byte(tt.want)) {
				t.Fatalf("check exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
		})
	}
	if code := run([]string{"init", "--dir", t.TempDir(), "--auth", "magic"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 1 {
		t.Fatal("init accepted an unknown sign-in mode")
	}
}

func TestInitStandalone(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	if code := run([]string{"init", "--dir", dir, "--standalone", "--locale", "de-DE", "--timezone", "Europe/Berlin", "--sections", "Welt, Wissenschaft & Technik", "--deadline", "07:15", "--coordinates", "52.52,13.40"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	c, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	p := c.Papers[0]
	if p.Role() != "none" || *p.Deadline != "07:15" || p.Units != "metric" || p.Locale != "de-DE" || !p.Boxes.Weather || *p.Latitude != 52.52 {
		t.Fatalf("%+v", p)
	}
	if len(p.Sections) != 2 || p.Sections[1].Key != "wissenschaft-technik" || !p.Sections[1].Finds() || p.Sections[0].Required {
		t.Fatalf("%+v", p.Sections)
	}
	if code := run([]string{"check", "--config", filepath.Join(dir, "config.yaml")}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatal("standalone config is not ready to serve")
	}
	// The worker key can gather and draft, but not publish.
	b, err := os.ReadFile(filepath.Join(dir, "worker", "worker.key"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	scope, err := auth.Authenticate(context.Background(), db, strings.TrimSpace(string(b)))
	if err != nil || !scope.Can(auth.Draft) || !scope.Can(auth.Gather) || scope.Can(auth.Publish) {
		t.Fatal("worker key capabilities", err)
	}
	// US English defaults to imperial units.
	us := t.TempDir()
	run([]string{"init", "--dir", us, "--standalone"}, &bytes.Buffer{}, &bytes.Buffer{})
	if c, _ := config.Load(filepath.Join(us, "config.yaml")); c.Papers[0].Units != "imperial" || c.Papers[0].Locale != "en-US" {
		t.Fatal("default units")
	}
	// Bad settings are refused before anything is written.
	for _, args := range [][]string{{"--coordinates", "north"}, {"--sections", " , "}, {"--locale", "english"}, {"--units", "kelvin"}, {"--deadline", "7am"}} {
		bad := filepath.Join(t.TempDir(), "x")
		if code := run(append([]string{"init", "--dir", bad, "--standalone"}, args...), &bytes.Buffer{}, &bytes.Buffer{}); code == 0 {
			t.Errorf("%v accepted", args)
		}
		if _, err := os.Stat(bad); err == nil {
			t.Errorf("%v left files", args)
		}
	}
}

func TestInitInteractive(t *testing.T) {
	saved := stdin
	t.Cleanup(func() { stdin = saved })
	// Answers in order; an empty line keeps the default, a bad one asks again.
	stdin = strings.NewReader(strings.Join([]string{
		"The Harbour Gazette", // masthead
		"Mars/Olympus",        // timezone: refused
		"Australia/Sydney",    // timezone
		"Sydney",              // place
		"",                    // coordinates
		"en-AU",               // locale
		"",                    // units: metric, from en-AU
		"6am",                 // deadline: refused
		"06:00",               // deadline
		"News, Sport",         // sections
		"",                    // sign-in: local
		"",                    // public URL
	}, "\n") + "\n")
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	if code := run([]string{"init", "--dir", dir, "--interactive"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String(), out.String())
	}
	if !strings.Contains(out.String(), "an IANA timezone") || !strings.Contains(out.String(), "a time such as 06:30") {
		t.Fatalf("no correction prompts: %s", out.String())
	}
	c, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	p := c.Papers[0]
	if p.Masthead != "The Harbour Gazette" || p.Timezone != "Australia/Sydney" || p.Place != "Sydney" || p.Units != "metric" || p.Locale != "en-AU" || *p.Deadline != "06:00" || len(p.Sections) != 2 || p.Sections[1].Title != "Sport" || c.AuthMode() != "local" {
		t.Fatalf("%+v", p)
	}
	// Input that ends early fails without writing anything.
	stdin = strings.NewReader("Only a masthead\n")
	short := filepath.Join(t.TempDir(), "x")
	if code := run([]string{"init", "--dir", short, "--interactive"}, &bytes.Buffer{}, &bytes.Buffer{}); code == 0 {
		t.Fatal("short input accepted")
	}
}
