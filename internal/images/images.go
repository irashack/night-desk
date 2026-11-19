package images

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	stddraw "image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/irashack/night-desk/internal/fetch"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const maxBytes = 5 * 1024 * 1024

// CheckAddress retains the image fetcher's public-address policy.
func CheckAddress(ip netip.Addr) error { return fetch.CheckAddress(ip) }

type Result struct {
	SHA256        string
	Width, Height int
	Bytes         int64
}
type Fetcher struct {
	client  *http.Client
	dataDir string
	check   func(netip.Addr) error
}

func New(dataDir string) *Fetcher { return newFetcher(dataDir, CheckAddress, nil) }

// The address policy and TLS transport are unexported test seams; New always
// installs the public-address policy, with no environment/configuration bypass.
func newFetcher(dataDir string, check func(netip.Addr) error, tlsConfigTransport *http.Transport) *Fetcher {
	transport := fetch.NewTransport(check, tlsConfigTransport)
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > fetch.RedirectLimit {
			return errors.New("too many redirects")
		}
		return checkURL(req.URL, check)
	}}
	return &Fetcher{client: client, dataDir: dataDir, check: check}
}
func checkURL(u *url.URL, check func(netip.Addr) error) error {
	if u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return errors.New("image URL must be https without credentials")
	}
	// Fail before dialing too, including when a connection could be reused.
	if ip, e := netip.ParseAddr(u.Hostname()); e == nil {
		return check(ip)
	}
	return nil
}
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (Result, error) {
	var zero Result
	u, e := url.Parse(rawURL)
	if e != nil {
		return zero, errors.New("invalid image URL")
	}
	if e = checkURL(u, f.check); e != nil {
		return zero, e
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if e != nil {
		return zero, e
	}
	resp, e := f.client.Do(req)
	if e != nil {
		return zero, errors.New("image request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return zero, fmt.Errorf("image returned HTTP %d", resp.StatusCode)
	}
	b, e := fetch.ReadBody(resp, maxBytes)
	if errors.Is(e, fetch.ErrTooLarge) {
		return zero, errors.New("image exceeds 5 MB")
	}
	if e != nil {
		return zero, errors.New("image read failed")
	}
	switch http.DetectContentType(b) {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
	default:
		return zero, errors.New("unsupported image type")
	}
	cfg, _, e := image.DecodeConfig(bytes.NewReader(b))
	if e != nil {
		return zero, errors.New("invalid image")
	}
	// Bound decoded allocation as well as compressed bytes.
	if cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 8_000_000 {
		return zero, errors.New("image exceeds decoded pixel limit")
	}
	src, _, e := image.Decode(bytes.NewReader(b))
	if e != nil {
		return zero, errors.New("invalid image")
	}
	w, h := cfg.Width, cfg.Height
	if w > 1200 {
		h = max(1, int(int64(h)*1200/int64(w)))
		w = 1200
	}
	flattened := image.NewRGBA(image.Rect(0, 0, w, h))
	stddraw.Draw(flattened, flattened.Bounds(), image.NewUniform(color.White), image.Point{}, stddraw.Src)
	draw.CatmullRom.Scale(flattened, flattened.Bounds(), src, src.Bounds(), draw.Over, nil)
	var out bytes.Buffer
	if e = jpeg.Encode(&out, flattened, &jpeg.Options{Quality: 82}); e != nil {
		return zero, e
	}
	hash := sha256.Sum256(out.Bytes())
	sha := hex.EncodeToString(hash[:])
	dir := filepath.Join(f.dataDir, "images", sha[:2])
	if e = os.MkdirAll(dir, 0700); e != nil {
		return zero, e
	}
	tmp, e := os.CreateTemp(dir, ".image-")
	if e != nil {
		return zero, e
	}
	name := tmp.Name()
	defer os.Remove(name)
	_, e = tmp.Write(out.Bytes())
	if e != nil {
		tmp.Close()
		return zero, e
	}
	if e = tmp.Close(); e != nil {
		return zero, e
	}
	dest := filepath.Join(dir, sha+".jpg")
	// Link is atomic and does not replace an existing content-addressed object.
	if e = os.Link(name, dest); e != nil && !os.IsExist(e) {
		return zero, e
	}
	return Result{SHA256: sha, Width: w, Height: h, Bytes: int64(out.Len())}, nil
}
