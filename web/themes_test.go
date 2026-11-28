package web

import (
	"encoding/json"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
)

// themeRoles is the order the roles appear in app.css.
var themeRoles = []string{"bg", "fg", "muted", "line", "chip", "p-bg", "p-ink", "p-muted", "p-rule", "p-soft", "p-accent"}

type themeFile struct {
	Default string            `json:"default"`
	Sepia   map[string]string `json:"sepia"`
	Fonts   map[string]struct {
		Files []string `json:"files"`
	} `json:"fonts"`
	Themes map[string]struct {
		Name  string            `json:"name"`
		Light map[string]string `json:"light"`
		Dark  map[string]string `json:"dark"`
	} `json:"themes"`
}

func readThemes(t *testing.T) themeFile {
	t.Helper()
	b, err := os.ReadFile("../docs/brand/themes.json")
	if err != nil {
		t.Fatal(err)
	}
	var f themeFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func declarations(m map[string]string, roles []string) string {
	parts := make([]string, 0, len(roles))
	for _, r := range roles {
		parts = append(parts, "--"+r+":"+m[r])
	}
	return strings.Join(parts, ";")
}

// TestThemesMatchStylesheet keeps docs/brand/themes.json, the themes' source
// for other clients, and the reader's stylesheet in step.
func TestThemesMatchStylesheet(t *testing.T) {
	f := readThemes(t)
	b, err := Files.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)
	want := []string{":root[data-mode=sepia]{" + declarations(f.Sepia, themeRoles[:10]) + ";"}
	tmpl, err := Files.ReadFile("templates/reader.html")
	if err != nil {
		t.Fatal(err)
	}
	for name, th := range f.Themes {
		if !strings.Contains(string(tmpl), `data-theme-choice="`+name+`"`) {
			t.Errorf("reader.html has no theme choice for %s", name)
		}
		light, dark := declarations(th.Light, themeRoles), declarations(th.Dark, themeRoles)
		if name == f.Default {
			want = append(want,
				":root{"+light+";",
				":root:not([data-mode=light]):not([data-mode=sepia]){"+dark+";",
				":root[data-mode=dark]{"+dark+";")
			continue
		}
		sel := ":root[data-theme=" + name + "]"
		want = append(want,
			sel+":not([data-mode=sepia]){"+light+"}",
			sel+":not([data-mode=light]):not([data-mode=sepia]){"+dark+"}",
			sel+"[data-mode=dark]{"+dark+"}")
	}
	for _, w := range want {
		if !strings.Contains(css, w) {
			t.Errorf("app.css lacks %s", w)
		}
	}
	for font, v := range f.Fonts {
		for _, file := range v.Files {
			if _, err := Files.ReadFile("static/fonts/" + file); err != nil {
				t.Errorf("%s: %v", font, err)
			}
		}
	}
}

func luminance(t *testing.T, hex string) float64 {
	t.Helper()
	if len(hex) != 7 || hex[0] != '#' {
		t.Fatalf("bad colour %q", hex)
	}
	var c [3]float64
	for i := range c {
		v, err := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		if err != nil {
			t.Fatalf("bad colour %q", hex)
		}
		x := float64(v) / 255
		if x <= 0.03928 {
			c[i] = x / 12.92
		} else {
			c[i] = math.Pow((x+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2]
}

// TestThemeContrast holds every theme to WCAG 2.2 AA: 4.5:1 for text, 3:1 for
// rules.
func TestThemeContrast(t *testing.T) {
	f := readThemes(t)
	pairs := []struct {
		fg, bg string
		min    float64
	}{
		{"p-ink", "p-bg", 4.5}, {"p-muted", "p-bg", 4.5}, {"p-accent", "p-bg", 4.5}, {"p-rule", "p-bg", 3},
		{"fg", "bg", 4.5}, {"muted", "bg", 4.5}, {"fg", "chip", 4.5},
	}
	for name, th := range f.Themes {
		for mode, m := range map[string]map[string]string{"light": th.Light, "dark": th.Dark} {
			for _, p := range pairs {
				a, b := luminance(t, m[p.fg]), luminance(t, m[p.bg])
				ratio := (math.Max(a, b) + 0.05) / (math.Min(a, b) + 0.05)
				if ratio < p.min {
					t.Errorf("%s %s: %s on %s is %.2f:1, want %.1f:1", name, mode, p.fg, p.bg, ratio, p.min)
				}
			}
		}
	}
}
