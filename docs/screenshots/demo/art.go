package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/irashack/night-desk/internal/images"
)

// artFetcher stands in for the image fetcher, which only fetches public https
// images. It never touches the network: each demo image URL names a scene
// (".../bridge.jpg"), which is painted here and stored where the reader serves
// retained images from.
type artFetcher struct{ dataDir string }

func (f *artFetcher) Fetch(_ context.Context, raw string) (images.Result, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return images.Result{}, err
	}
	scene := strings.TrimSuffix(path.Base(u.Path), path.Ext(u.Path))
	const w, h = 1200, 675
	img := paint(scene, w, h)
	var out bytes.Buffer
	if err = jpeg.Encode(&out, img, &jpeg.Options{Quality: 82}); err != nil {
		return images.Result{}, err
	}
	sum := sha256.Sum256(out.Bytes())
	sha := hex.EncodeToString(sum[:])
	dir := filepath.Join(f.dataDir, "images", sha[:2])
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return images.Result{}, err
	}
	if err = os.WriteFile(filepath.Join(dir, sha+".jpg"), out.Bytes(), 0o600); err != nil {
		return images.Result{}, err
	}
	return images.Result{SHA256: sha, Width: w, Height: h, Bytes: int64(out.Len())}, nil
}

type rgb struct{ r, g, b float64 }

func mix(a, b rgb, t float64) rgb {
	t = math.Max(0, math.Min(1, t))
	return rgb{a.r + (b.r-a.r)*t, a.g + (b.g-a.g)*t, a.b + (b.b-a.b)*t}
}

// smooth is a soft step for anti-aliased edges, d being a signed distance in
// pixels (negative inside).
func smooth(d float64) float64 { return math.Max(0, math.Min(1, 0.5-d)) }

// paint draws a simple, flat illustration for a named scene.
func paint(scene string, w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fw, fh := float64(w), float64(h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			px, py := float64(x), float64(y)
			var c rgb
			switch scene {
			case "bridge":
				c = bridge(px, py, fw, fh)
			case "harbor":
				c = harbor(px, py, fw, fh)
			case "orchard":
				c = orchard(px, py, fw, fh)
			case "telescope":
				c = telescope(px, py, fw, fh)
			case "market":
				c = market(px, py, fw, fh)
			default:
				c = hills(px, py, fw, fh)
			}
			img.SetRGBA(x, y, color.RGBA{uint8(c.r), uint8(c.g), uint8(c.b), 255})
		}
	}
	return img
}

// bridge: a dusk river with a stone arch bridge and lamps.
func bridge(x, y, w, h float64) rgb {
	sky := mix(rgb{38, 52, 92}, rgb{236, 150, 96}, y/(h*0.62))
	water := mix(rgb{214, 128, 86}, rgb{24, 34, 62}, (y-h*0.62)/(h*0.38))
	c := sky
	// Sun low on the horizon.
	c = mix(c, rgb{255, 214, 150}, smooth(math.Hypot(x-w*0.72, y-h*0.58)-70))
	// Far bank.
	bank := h*0.58 + 14*math.Sin(x/140) + 8*math.Sin(x/37)
	c = mix(c, rgb{58, 52, 74}, smooth(bank-y))
	if y > h*0.62 {
		c = water
		// Reflection of the sun as broken bands.
		if math.Abs(x-w*0.72) < 90-0.15*(y-h*0.62) && math.Sin(y/3.2) > 0.2 {
			c = mix(c, rgb{255, 200, 140}, 0.55)
		}
	}
	// Deck of the bridge.
	deckTop, deckBottom := h*0.40, h*0.46
	if y > deckTop && y < deckBottom {
		c = rgb{44, 36, 44}
	}
	// Three arches below the deck, resting on the water.
	if y >= deckBottom && y < h*0.66 {
		span := w / 3
		cx := (math.Floor(x/span) + 0.5) * span
		r := span * 0.42
		inside := math.Hypot((x-cx)/r, (y-h*0.66)/(h*0.66-deckBottom)) < 1
		if !inside {
			c = rgb{52, 42, 50}
		}
	}
	// Lamps along the deck.
	for i := 0.5; i < 12; i++ {
		lx := i * w / 12
		if math.Abs(x-lx) < 2 && y > deckTop-48 && y < deckTop {
			c = rgb{40, 34, 40}
		}
		c = mix(c, rgb{255, 226, 160}, smooth(math.Hypot(x-lx, y-(deckTop-52))-6))
	}
	return c
}

// harbor: cranes and stacked containers against a pale morning sky.
func harbor(x, y, w, h float64) rgb {
	c := mix(rgb{208, 222, 230}, rgb{246, 236, 214}, y/(h*0.7))
	quay := h * 0.74
	if y > quay {
		c = mix(rgb{70, 96, 112}, rgb{30, 44, 58}, (y-quay)/(h-quay))
		if math.Sin(x/9+y/2) > 0.85 {
			c = mix(c, rgb{200, 214, 220}, 0.4)
		}
	}
	// Containers in stacks.
	colors := []rgb{{176, 62, 48}, {44, 94, 128}, {214, 160, 64}, {70, 120, 90}, {120, 60, 88}}
	cw, ch := 92.0, 34.0
	for stack := 0; stack < 9; stack++ {
		sx := 40 + float64(stack)*cw*1.25
		height := 2 + (stack*7)%4
		for level := 0; level < height; level++ {
			top := quay - float64(level+1)*ch
			if x > sx && x < sx+cw && y > top+2 && y < top+ch {
				c = colors[(stack+level*2)%len(colors)]
				if int(x-sx)%12 < 2 {
					c = mix(c, rgb{0, 0, 0}, 0.25)
				}
			}
		}
	}
	// Two gantry cranes.
	for _, cx := range []float64{w * 0.3, w * 0.78} {
		leg := math.Abs(x-cx+60) < 6 || math.Abs(x-cx-60) < 6
		if leg && y > h*0.2 && y < quay {
			c = rgb{60, 66, 74}
		}
		if y > h*0.18 && y < h*0.23 && x > cx-170 && x < cx+120 {
			c = rgb{60, 66, 74}
		}
	}
	return c
}

// orchard: rows of round trees on rolling green fields under a clear sky.
func orchard(x, y, w, h float64) rgb {
	c := mix(rgb{150, 196, 228}, rgb{232, 240, 236}, y/(h*0.5))
	c = mix(c, rgb{255, 246, 214}, smooth(math.Hypot(x-w*0.18, y-h*0.2)-50))
	horizon := h*0.48 + 26*math.Sin(x/260)
	if y > horizon {
		c = mix(rgb{140, 170, 84}, rgb{62, 104, 52}, (y-horizon)/(h-horizon))
		if math.Sin((y-horizon)/9) > 0.6 {
			c = mix(c, rgb{40, 70, 36}, 0.25)
		}
	}
	for row := 0; row < 3; row++ {
		ry := horizon + 40 + float64(row)*90
		r := 22 + float64(row)*16
		step := r * 3.2
		tx := (math.Floor(x/step) + 0.5) * step
		if math.Abs(x-tx) < r*0.18 && y > ry && y < ry+r*1.4 {
			c = rgb{82, 58, 40}
		}
		c = mix(c, rgb{46, 98, 50}, smooth(math.Hypot(x-tx, y-ry)-r))
		// Fruit.
		for _, o := range [][2]float64{{-0.4, -0.2}, {0.3, 0.1}, {0, 0.45}} {
			c = mix(c, rgb{210, 70, 48}, smooth(math.Hypot(x-tx-o[0]*r, y-ry-o[1]*r)-r*0.13))
		}
	}
	return c
}

// telescope: a domed observatory under a starry night sky.
func telescope(x, y, w, h float64) rgb {
	c := mix(rgb{12, 16, 40}, rgb{58, 52, 96}, y/h)
	// Deterministic stars.
	gx, gy := math.Floor(x/23), math.Floor(y/23)
	n := math.Abs(math.Sin(gx*12.9898+gy*78.233) * 43758.5453)
	n -= math.Floor(n)
	if n > 0.86 {
		sx := gx*23 + 11 + 8*math.Sin(n*40)
		sy := gy*23 + 11 + 8*math.Cos(n*40)
		c = mix(c, rgb{250, 246, 230}, smooth(math.Hypot(x-sx, y-sy)-1.4*(n-0.8)*10))
	}
	// Milky way haze.
	band := math.Abs(y - (h*0.9 - x*0.55))
	c = mix(c, rgb{120, 116, 160}, 0.22*math.Exp(-band*band/9000))
	ground := h*0.8 + 10*math.Sin(x/90)
	if y > ground {
		c = rgb{18, 20, 26}
	}
	// Observatory dome with an open slit.
	cx, cy, r := w*0.62, h*0.8, 120.0
	if y < cy && math.Hypot(x-cx, y-cy) < r {
		c = rgb{196, 198, 206}
		if math.Abs(x-cx-(cy-y)*0.25) < 14 {
			c = rgb{20, 22, 40}
		}
	}
	if y >= cy && y < cy+70 && math.Abs(x-cx) < r {
		c = rgb{150, 152, 162}
	}
	return c
}

// market: a covered market hall with striped awnings.
func market(x, y, w, h float64) rgb {
	c := mix(rgb{238, 226, 204}, rgb{220, 200, 176}, y/h)
	// Roof truss arcs.
	if y < h*0.3 {
		d := math.Abs(math.Hypot(x-w/2, y-h*1.2) - h*1.0)
		if d < 4 || int(x)%150 < 4 {
			c = rgb{90, 80, 76}
		}
	}
	// Stalls with striped awnings.
	stalls := []rgb{{178, 54, 46}, {42, 102, 120}, {204, 150, 50}, {64, 116, 76}}
	sw := w / 4
	i := int(x / sw)
	if i > 3 {
		i = 3
	}
	sx := float64(i) * sw
	if y > h*0.42 && y < h*0.52 {
		if int((x-sx)/28)%2 == 0 {
			c = stalls[i]
		} else {
			c = rgb{244, 238, 226}
		}
		// Scalloped edge.
		if y > h*0.5 && math.Hypot(math.Mod(x-sx, 28)-14, y-h*0.5) > 14 {
			c = mix(rgb{238, 226, 204}, rgb{220, 200, 176}, y/h)
		}
	}
	if y > h*0.66 && y < h*0.74 && x > sx+20 && x < sx+sw-20 {
		c = rgb{120, 84, 56}
	}
	// Produce piles on the counters.
	for k := 0.0; k < 5; k++ {
		cx := sx + 44 + k*(sw-88)/4
		c = mix(c, mix(stalls[(i+1)%4], rgb{255, 255, 255}, 0.15), smooth(math.Hypot(x-cx, (y-h*0.66)*1.6)-26))
	}
	if y > h*0.74 {
		c = rgb{96, 88, 84}
	}
	return c
}

// hills: the fallback, layered hills at dawn.
func hills(x, y, w, h float64) rgb {
	c := mix(rgb{246, 206, 160}, rgb{240, 236, 226}, 1-y/h)
	c = mix(c, rgb{255, 240, 200}, smooth(math.Hypot(x-w*0.5, y-h*0.45)-80))
	layers := []rgb{{180, 150, 160}, {130, 104, 124}, {84, 66, 88}, {44, 36, 52}}
	for i, col := range layers {
		f := float64(i)
		top := h*(0.45+0.12*f) + 30*math.Sin(x/(180+40*f)+f*2)
		if y > top {
			c = col
		}
	}
	return c
}
