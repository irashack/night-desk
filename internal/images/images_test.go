package images

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCheckAddress(t *testing.T) {
	for _, tt := range []struct {
		ip      string
		allowed bool
	}{
		{"8.8.8.8", true}, {"1.1.1.1", true}, {"100.63.255.255", true}, {"100.128.0.0", true},
		{"172.15.255.255", true}, {"172.32.0.0", true}, {"192.169.1.1", true},
		{"2001:4860:4860::8888", true}, {"2606:4700:4700::1111", true},
		{"127.0.0.1", false}, {"127.99.2.3", false}, {"10.0.0.1", false}, {"10.255.255.255", false},
		{"172.16.0.1", false}, {"172.31.255.255", false}, {"192.168.0.1", false},
		{"169.254.0.1", false}, {"169.254.169.254", false}, {"100.64.0.0", false}, {"100.127.255.255", false},
		{"0.0.0.0", false}, {"0.0.0.1", false}, {"224.0.0.1", false}, {"239.255.255.255", false}, {"255.255.255.255", false},
		{"::", false}, {"::1", false}, {"fc00::1", false}, {"fdff:ffff::1", false},
		{"fe80::1", false}, {"fe80::1%eth0", false}, {"ff02::1", false},
		{"::ffff:127.0.0.1", false}, {"::ffff:10.0.0.1", false}, {"::ffff:172.16.0.1", false},
		{"::ffff:192.168.1.1", false}, {"::ffff:169.254.1.1", false}, {"::ffff:100.64.1.1", false},
		{"::ffff:0.0.0.0", false}, {"::ffff:224.0.0.1", false}, {"::ffff:8.8.8.8", true},
		{"::127.0.0.1", false}, {"64:ff9b::a00:1", false}, {"2002:7f00:1::", false}, {"", false},
	} {
		t.Run(tt.ip, func(t *testing.T) {
			ip := netip.Addr{}
			if tt.ip != "" {
				ip = netip.MustParseAddr(tt.ip)
			}
			if got := CheckAddress(ip) == nil; got != tt.allowed {
				t.Fatalf("allowed=%v want %v", got, tt.allowed)
			}
		})
	}
}
func pngData(t *testing.T) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 1600, 800))
	im.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	if e := png.Encode(&b, im); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func TestPipeline(t *testing.T) {
	pngBytes := pngData(t)
	// A 1x1 WebP; the compressed bytes are a complete image, not an external fixture.
	webp, e := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	if e != nil {
		t.Fatal(e)
	}
	var jb bytes.Buffer
	if e = jpeg.Encode(&jb, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); e != nil {
		t.Fatal(e)
	}
	metadata := []byte("Exif\x00\x00private-location")
	exif := append([]byte{0xff, 0xd8, 0xff, 0xe1, 0, byte(len(metadata) + 2)}, metadata...)
	exif = append(exif, jb.Bytes()[2:]...)
	giant := append([]byte{}, pngBytes...)
	binary.BigEndian.PutUint32(giant[16:20], 10000)
	binary.BigEndian.PutUint32(giant[20:24], 10000)
	binary.BigEndian.PutUint32(giant[29:33], crc32.ChecksumIEEE(giant[12:29]))
	mux := http.NewServeMux()
	mux.HandleFunc("/png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write(append(pngBytes, []byte("APPENDED-POLYGLOT")...))
	})
	mux.HandleFunc("/webp", func(w http.ResponseWriter, r *http.Request) { w.Write(webp) })
	mux.HandleFunc("/exif", func(w http.ResponseWriter, r *http.Request) { w.Write(exif) })
	mux.HandleFunc("/giant", func(w http.ResponseWriter, r *http.Request) { w.Write(giant) })
	mux.HandleFunc("/non-image", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("<html>not an image</html>"))
	})
	mux.HandleFunc("/oversize", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(maxBytes+1))
		w.WriteHeader(200)
	})
	mux.HandleFunc("/chunked-oversize", func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		w.Write(bytes.Repeat([]byte("x"), maxBytes+1))
	})
	mux.HandleFunc("/private", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "https://127.0.0.2/image", 302) })
	mux.HandleFunc("/private-rfc1918", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "https://10.0.0.1/image", 302) })
	mux.HandleFunc("/downgrade", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://example.com/image", 302) })
	mux.HandleFunc("/redirect/{n}", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.PathValue("n"))
		target := "/png"
		if n > 1 {
			target = "/redirect/" + strconv.Itoa(n-1)
		}
		http.Redirect(w, r, target, 302)
	})
	listener := newMemoryListener()
	ts := &httptest.Server{Listener: listener, Config: &http.Server{Handler: mux}}
	ts.StartTLS()
	defer ts.Close()
	dir := t.TempDir()
	allowTestServer := func(ip netip.Addr) error {
		if ip.Unmap() == netip.MustParseAddr("127.0.0.1") {
			return nil
		}
		return CheckAddress(ip)
	}
	f := newFetcher(dir, allowTestServer, ts.Client().Transport.(*http.Transport))
	f.client.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		if e = allowTestServer(netip.MustParseAddr(host)); e != nil {
			return nil, e
		}
		return listener.dial(ctx)
	}

	for _, path := range []string{"/png", "/webp", "/exif", "/redirect/3"} {
		t.Run(path, func(t *testing.T) {
			result, e := f.Fetch(context.Background(), ts.URL+path)
			if e != nil {
				t.Fatal(e)
			}
			if len(result.SHA256) != 64 || result.Width > 1200 || result.Bytes < 1 {
				t.Fatal(result)
			}
			if path == "/png" && (result.Width != 1200 || result.Height != 600) {
				t.Fatal("wrong scale", result)
			}
			b, e := os.ReadFile(filepath.Join(dir, "images", result.SHA256[:2], result.SHA256+".jpg"))
			if e != nil {
				t.Fatal(e)
			}
			_, format, e := image.Decode(bytes.NewReader(b))
			if e != nil || format != "jpeg" {
				t.Fatal("not JPEG", format, e)
			}
			for _, marker := range [][]byte{metadata, []byte("APPENDED-POLYGLOT")} {
				if bytes.Contains(b, marker) {
					t.Fatal("metadata survived")
				}
			}
			again, e := f.Fetch(context.Background(), ts.URL+path)
			if e != nil || again != result {
				t.Fatal("not deduped", again, e)
			}
		})
	}
	for _, path := range []string{"/giant", "/non-image", "/oversize", "/chunked-oversize", "/private", "/private-rfc1918", "/downgrade", "/redirect/4"} {
		t.Run(path, func(t *testing.T) {
			if _, e := f.Fetch(context.Background(), ts.URL+path); e == nil {
				t.Fatal("accepted unsafe image")
			}
		})
	}
	// Production rejects both literal loopback and a hostname resolving to loopback.
	prod := New(t.TempDir())
	for _, u := range []string{ts.URL + "/png", strings.Replace(ts.URL, "127.0.0.1", "localhost", 1) + "/png", "http://example.com/image", "https://user:pass@example.com/image"} {
		if _, e := prod.Fetch(context.Background(), u); e == nil {
			t.Fatal("production allowed unsafe URL")
		}
	}
}
