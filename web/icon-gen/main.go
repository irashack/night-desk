// Run from the repository root: go run ./web/icon-gen
//
// It renders every app icon from the brand masters in web/brand: nd-mark.png,
// the "ND" mark with its red rule, and nd-mark-small.png, the heavier cut drawn
// for 32 pixels and below. Both are renders of the SVG sources beside them:
//
//	rsvg-convert -w 2048 web/brand/nd-mark.svg -o web/brand/nd-mark.png
//	rsvg-convert -w 1024 web/brand/nd-mark-small.svg -o web/brand/nd-mark-small.png
//
// It also renders the sign-in page's artwork from docs/brand/hero.png.
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"

	"golang.org/x/image/draw"
)

// iconColour is the tile behind the mark, the brand's ink (#171719).
var iconColour = color.RGBA{23, 23, 25, 255}

const (
	masterPath = "web/brand/nd-mark.png"
	smallPath  = "web/brand/nd-mark-small.png"
	heroPath   = "docs/brand/hero.png"
	signInArt  = "web/static/signin-art.png"
)

// Icons, their master and the share of the icon's width the mark spans.
// Maskable icons keep the mark inside the central safe circle Android may crop
// to.
var icons = []struct {
	path   string
	master string
	size   int
	width  float64
}{
	{"web/static/icon-32.png", smallPath, 32, 0.86},
	{"web/static/apple-touch-icon.png", masterPath, 180, 0.72},
	{"web/static/icon-192.png", masterPath, 192, 0.72},
	{"web/static/icon-512.png", masterPath, 512, 0.72},
	{"web/static/icon-maskable-512.png", masterPath, 512, 0.56},
	{"docs/brand/night-desk-icon-512.png", masterPath, 512, 0.72},
}

func main() {
	masters := map[string]image.Image{}
	for _, icon := range icons {
		if masters[icon.master] == nil {
			masters[icon.master] = read(icon.master)
		}
		write(icon.path, render(masters[icon.master], icon.size, icon.width))
	}
	// The sign-in page shows the hero's artwork without its ground: the dark
	// ground becomes transparent, so type, lamp and glow sit on the page.
	art := trim(lightOnDark(read(heroPath)), 32)
	write(signInArt, scale(art, 640, 640*art.Bounds().Dy()/art.Bounds().Dx()))
}

func render(master image.Image, size int, width float64) image.Image {
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(out, out.Bounds(), image.NewUniform(iconColour), image.Point{}, draw.Src)
	b := master.Bounds()
	w := int(float64(size) * width)
	h := w * b.Dy() / b.Dx()
	at := image.Rect((size-w)/2, (size-h)/2, (size-w)/2+w, (size-h)/2+h)
	draw.CatmullRom.Scale(out, at, master, b, draw.Over, nil)
	return out
}

func scale(src image.Image, w, h int) image.Image {
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(out, out.Bounds(), src, src.Bounds(), draw.Src, nil)
	return out
}

// trim crops transparent edges, keeping margin pixels around the artwork.
func trim(src *image.NRGBA, margin int) image.Image {
	b := src.Bounds()
	box := image.Rectangle{Min: b.Max, Max: b.Min}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if src.NRGBAAt(x, y).A > 8 {
				box = box.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	box = image.Rect(box.Min.X-margin, box.Min.Y-margin, box.Max.X+margin, box.Max.Y+margin).Intersect(b)
	return src.SubImage(box)
}

// lightOnDark treats artwork drawn on a dark ground as light over
// transparency: brightness above the ground becomes opacity, and colour is
// recovered so that compositing over the ground reproduces the original. The
// ground may be a faint gradient, so its level is interpolated from the
// four corners; opacity eases in from just above it, so the grain disappears
// without a hard edge around the lamp's glow.
func lightOnDark(src image.Image) *image.NRGBA {
	b := src.Bounds()
	level := func(x, y int) float64 {
		c := color.RGBAModel.Convert(src.At(x, y)).(color.RGBA)
		return math.Max(float64(c.R), math.Max(float64(c.G), float64(c.B)))
	}
	tl, tr := level(b.Min.X+2, b.Min.Y+2), level(b.Max.X-3, b.Min.Y+2)
	bl, br := level(b.Min.X+2, b.Max.Y-3), level(b.Max.X-3, b.Max.Y-3)
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		v := float64(y-b.Min.Y) / float64(b.Dy()-1)
		for x := b.Min.X; x < b.Max.X; x++ {
			u := float64(x-b.Min.X) / float64(b.Dx()-1)
			ground := (tl*(1-u)+tr*u)*(1-v) + (bl*(1-u)+br*u)*v + 6
			c := color.RGBAModel.Convert(src.At(x, y)).(color.RGBA)
			r, g, bv := float64(c.R), float64(c.G), float64(c.B)
			raw := (math.Max(r, math.Max(g, bv)) - ground) / (255 - ground)
			a := math.Min(math.Max((raw-0.03)/0.97, 0), 1)
			if a < 1.0/255 {
				continue
			}
			un := func(v float64) uint8 { return uint8(math.Min(math.Max((v-ground*(1-a))/a, 0), 255) + 0.5) }
			out.SetNRGBA(x-b.Min.X, y-b.Min.Y, color.NRGBA{un(r), un(g), un(bv), uint8(a*255 + 0.5)})
		}
	}
	return out
}

func read(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		fail(err)
	}
	defer f.Close()
	im, err := png.Decode(f)
	if err != nil {
		fail(fmt.Errorf("%s: %w", path, err))
	}
	return im
}

func write(path string, im image.Image) {
	if err := os.MkdirAll(dir(path), 0o755); err != nil {
		fail(err)
	}
	f, err := os.Create(path)
	if err != nil {
		fail(err)
	}
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err = enc.Encode(f, im); err == nil {
		err = f.Close()
	}
	if err != nil {
		fail(err)
	}
}

func dir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
