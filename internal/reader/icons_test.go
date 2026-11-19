package reader

import (
	"encoding/json"
	"fmt"
	"image/png"
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/irashack/night-desk/web"
)

// Every icon the manifest, service worker and pages name must ship, at the
// size it claims, so installing the app never finds a broken icon.
func TestAppIconsShip(t *testing.T) {
	b, err := fs.ReadFile(web.Files, "manifest.webmanifest")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		BackgroundColor string `json:"background_color"`
		Icons           []struct{ Src, Sizes, Type, Purpose string }
	}
	if err = json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	maskable := false
	for _, icon := range m.Icons {
		maskable = maskable || icon.Purpose == "maskable"
		f, err := web.Files.Open(strings.TrimPrefix(icon.Src, "/"))
		if err != nil {
			t.Fatalf("manifest icon %s: %v", icon.Src, err)
		}
		cfg, err := png.DecodeConfig(f)
		f.Close()
		if err != nil || icon.Type != "image/png" || icon.Sizes != fmt.Sprintf("%dx%d", cfg.Width, cfg.Height) {
			t.Errorf("%s: %+v %v", icon.Src, cfg, err)
		}
	}
	if !maskable {
		t.Error("Android needs a maskable icon")
	}
	sw, _ := fs.ReadFile(web.Files, "static/sw.js")
	pages, _ := fs.ReadFile(web.Files, "templates/reader.html")
	named := regexp.MustCompile(`/static/(icon[\w-]*\.png|apple-touch-icon\.png)|asset "((?:icon|apple-touch-icon)[\w-]*\.png)"`)
	for _, src := range []string{string(sw), string(pages), signIn} {
		for _, match := range named.FindAllStringSubmatch(src, -1) {
			name := match[1] + match[2]
			if _, err := fs.Stat(web.Files, "static/"+name); err != nil {
				t.Errorf("%s is referenced but not shipped", name)
			}
		}
	}
	if !strings.Contains(signIn, `href="/login"`) || !strings.Contains(signIn, `src="/static/signin-art.png"`) {
		t.Error("the sign-in page shows the logo artwork and the sign-in link")
	}
	if _, err := fs.Stat(web.Files, "static/signin-art.png"); err != nil {
		t.Error("the sign-in artwork ships")
	}
}
