package fetch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAddressPolicy(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.2", "169.254.169.254", "100.64.0.1", "::1", "fc00::1", "fe80::1", "::ffff:127.0.0.1", "64:ff9b::a00:1", "2002:7f00:1::"} {
		if CheckAddress(netip.MustParseAddr(address)) == nil {
			t.Errorf("accepted %s", address)
		}
	}
	for _, address := range []string{"8.8.8.8", "2606:4700:4700::1111", "::ffff:8.8.8.8"} {
		if err := CheckAddress(netip.MustParseAddr(address)); err != nil {
			t.Errorf("refused %s: %v", address, err)
		}
	}
}

func TestRefusalAndAllow(t *testing.T) {
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, "article")
	}))
	defer ts.Close()
	ctx := context.Background()
	guarded := New(nil)
	defer guarded.CloseIdleConnections()
	for _, rawURL := range []string{ts.URL, strings.Replace(ts.URL, "127.0.0.1", "localhost", 1), "http://169.254.169.254/feed", "http://10.0.0.1/article", "file:///etc/passwd", "http://user:password@example.com/feed"} {
		t.Run(rawURL, func(t *testing.T) {
			// Exercise Do directly: resolver and feed libraries do not use Read.
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
			if err != nil {
				t.Fatal(err)
			}
			if resp, err := guarded.Do(req); err == nil {
				resp.Body.Close()
				t.Fatal("accepted unsafe destination")
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatal("refused requests reached server")
	}
	u, _ := url.Parse(ts.URL)
	allow := Allow{u.Host}
	approved := New(allow)
	defer approved.CloseIdleConnections()
	allow[0] = "different.invalid:80" // A caller cannot mutate the installed policy.
	body, final, err := Read(ctx, approved, ts.URL+"/feed", 1024)
	if err != nil || string(body) != "article" || final != ts.URL+"/feed" {
		t.Fatal(string(body), final, err)
	}
	if _, _, err := Read(ctx, approved, strings.Replace(ts.URL, "127.0.0.1", "localhost", 1), 1024); err == nil {
		t.Fatal("exception also allowed a different hostname")
	}
	if (Allow{net.JoinHostPort(u.Hostname(), "1")}).permits(u.Host) {
		t.Fatal("exception also allowed a different port")
	}
}

func TestRedirectPolicy(t *testing.T) {
	var privateHits atomic.Int32
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		privateHits.Add(1)
		io.WriteString(w, "private")
	}))
	defer private.Close()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/private":
			http.Redirect(w, r, private.URL, http.StatusFound)
		case r.URL.Path == "/credentials":
			http.Redirect(w, r, "http://user:password@example.com/feed", http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/redirect/"):
			n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/redirect/"))
			next := "/article"
			if n > 1 {
				next = "/redirect/" + strconv.Itoa(n-1)
			}
			http.Redirect(w, r, next, http.StatusFound)
		default:
			io.WriteString(w, "article")
		}
	}))
	defer ts.Close()
	u, _ := url.Parse(ts.URL)
	c := New(Allow{u.Host})
	defer c.CloseIdleConnections()
	body, final, err := Read(context.Background(), c, ts.URL+"/redirect/3", 100)
	if err != nil || string(body) != "article" || final != ts.URL+"/article" {
		t.Fatal(string(body), final, err)
	}
	for _, path := range []string{"/private", "/credentials", "/redirect/4"} {
		if _, _, err := Read(context.Background(), c, ts.URL+path, 100); err == nil {
			t.Errorf("accepted %s", path)
		}
	}
	if privateHits.Load() != 0 {
		t.Fatal("redirect reached a non-approved private destination")
	}
}

func TestLimitsAndStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/declared":
			w.Header().Set("Content-Length", "100")
		case "/chunked":
			w.(http.Flusher).Flush()
			io.WriteString(w, "123456")
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
		default:
			io.WriteString(w, "12345")
		}
	}))
	defer ts.Close()
	u, _ := url.Parse(ts.URL)
	c := New(Allow{u.Host})
	defer c.CloseIdleConnections()
	for _, path := range []string{"/declared", "/chunked"} {
		if _, _, err := Read(context.Background(), c, ts.URL+path, 5); !errors.Is(err, ErrTooLarge) {
			t.Errorf("%s: %v", path, err)
		}
	}
	body, _, err := Read(context.Background(), c, ts.URL, 5)
	if err != nil || string(body) != "12345" {
		t.Fatal(string(body), err)
	}
	if _, _, err := Read(context.Background(), c, ts.URL+"/missing", 5); err == nil {
		t.Fatal("accepted HTTP 404")
	}
	for _, limit := range []int64{0, -1} {
		if _, _, err := Read(context.Background(), c, ts.URL, limit); err == nil {
			t.Fatal("accepted invalid limit")
		}
	}
	if _, _, err := Read(context.Background(), nil, ts.URL, 5); err == nil {
		t.Fatal("accepted nil client")
	}
	if _, _, err := Read(context.Background(), c, "://", 5); err == nil {
		t.Fatal("accepted malformed URL")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := Read(ctx, c, ts.URL, 5); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestReadBodyFailure(t *testing.T) {
	resp := &http.Response{ContentLength: -1, Body: io.NopCloser(io.MultiReader(bytes.NewBufferString("data"), brokenReader{}))}
	if _, err := ReadBody(resp, 10); err == nil {
		t.Fatal("ignored read error")
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("broken") }

func TestTransportGuardCannotUseTLSBypass(t *testing.T) {
	base := &http.Transport{DialTLSContext: func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("unguarded TLS dialer called")
		return nil, errors.New("unexpected")
	}}
	transport := NewTransport(CheckAddress, base)
	if transport.Proxy != nil || transport.DialTLS != nil || transport.DialTLSContext != nil {
		t.Fatal("transport inherited an unguarded dialer or proxy")
	}
	if transport.TLSHandshakeTimeout <= 0 || transport.ResponseHeaderTimeout <= 0 || New(nil).Timeout <= 0 {
		t.Fatal("missing timeout")
	}
}
