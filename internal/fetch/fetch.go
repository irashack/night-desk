// Package fetch provides bounded HTTP fetching with a public-address policy.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const RedirectLimit = 3

var ErrTooLarge = errors.New("response exceeds byte limit")

// ErrRefused is a destination the guard refuses: a non-public address or a
// URL that is not http(s) without credentials. Retrying cannot succeed.
var ErrRefused = errors.New("non-public address")

// ErrRequest is a request that got no HTTP response: a refused address, DNS,
// connection or timeout failure.
var ErrRequest = errors.New("request failed")

// StatusError is a response other than 200. RetryAfter is the server's
// Retry-After delay in seconds, when it gave one.
type StatusError struct {
	Code       int
	RetryAfter time.Duration
}

func (e *StatusError) Error() string { return fmt.Sprintf("response returned HTTP %d", e.Code) }

// Allow names explicit operator-approved destinations as host:port pairs. An
// exception applies only to that destination, never to a redirect's destination.
type Allow []string

// CheckAddress sees the numeric destination supplied by net.Dialer after DNS.
// Unmap before checking so IPv4-in-IPv6 cannot bypass any IPv4 exclusion.
func CheckAddress(ip netip.Addr) error {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.Zone() != "" || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() || !ip.IsGlobalUnicast() {
		return ErrRefused
	}
	for _, cidr := range []string{"100.64.0.0/10", "0.0.0.0/8", "240.0.0.0/4", "::/96", "64:ff9b::/96", "64:ff9b:1::/48", "2002::/16"} {
		if netip.MustParsePrefix(cidr).Contains(ip) {
			return ErrRefused
		}
	}
	return nil
}

func (a Allow) permits(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || port == "" {
		return false
	}
	for _, entry := range a {
		h, p, err := net.SplitHostPort(entry)
		if err == nil && strings.EqualFold(host, h) && port == p {
			return true
		}
	}
	return false
}

func checkURL(u *url.URL, allow Allow) error {
	if u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return fmt.Errorf("URL must be http or https without credentials: %w", ErrRefused)
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	if allow.permits(net.JoinHostPort(u.Hostname(), port)) {
		return nil
	}
	// Check literals before dialing as well, including reused connections.
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
		return CheckAddress(ip)
	}
	return nil
}

// NewTransport builds the shared dial-time guard. The address policy and optional
// base transport support consumers with stricter URL policies and transport tests.
// Consumers must validate initial URLs and redirects too; New does both.
func NewTransport(check func(netip.Addr) error, base *http.Transport) *http.Transport {
	return newTransport(nil, check, base)
}

func newTransport(allow Allow, check func(netip.Addr) error, base *http.Transport) *http.Transport {
	if check == nil {
		check = CheckAddress
	}
	transport := &http.Transport{}
	if base != nil {
		transport = base.Clone()
	}
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		allowed := allow.permits(address)
		dialer := &net.Dialer{Timeout: 10 * time.Second, Control: func(network, address string, c syscall.RawConn) error {
			if allowed {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil {
				return err
			}
			return check(ip)
		}}
		return dialer.DialContext(ctx, network, address)
	}
	// Do not inherit a custom TLS dialer that could bypass DialContext.
	transport.DialTLS = nil
	transport.DialTLSContext = nil
	transport.MaxIdleConns = 8
	transport.IdleConnTimeout = 30 * time.Second
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = 10 * time.Second
	transport.MaxResponseHeaderBytes = 1 << 20
	return transport
}

type guardedTransport struct {
	transport *http.Transport
	allow     Allow
}

func (t *guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := checkURL(req.URL, t.allow); err != nil {
		return nil, err
	}
	return t.transport.RoundTrip(req)
}

func (t *guardedTransport) CloseIdleConnections() { t.transport.CloseIdleConnections() }

// New returns a client guarding every request, including initial requests made by
// resolver libraries. HTTP and HTTPS are supported; proxies are disabled.
func New(allow Allow) *http.Client {
	allow = append(Allow(nil), allow...)
	return &http.Client{
		Transport: &guardedTransport{transport: newTransport(allow, CheckAddress, nil), allow: allow},
		Timeout:   10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > RedirectLimit {
				return errors.New("too many redirects")
			}
			return checkURL(req.URL, allow)
		},
	}
}

// ReadBody bounds both declared and streamed response bodies. The caller owns
// closing the response body. Nonpositive limits are refused.
func ReadBody(resp *http.Response, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, errors.New("byte limit must be positive")
	}
	if resp.ContentLength > maxBytes {
		return nil, ErrTooLarge
	}
	// Avoid overflowing maxBytes+1, even for a caller-supplied extreme limit.
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, errors.New("response read failed")
	}
	if int64(len(b)) == maxBytes {
		var extra [1]byte
		if n, err := io.ReadFull(resp.Body, extra[:]); n > 0 {
			return nil, ErrTooLarge
		} else if err != nil && err != io.EOF {
			return nil, errors.New("response read failed")
		}
	}
	return b, nil
}

// Read fetches a successful response and returns its bounded bytes and final URL.
// The caller supplies a guarded client from New (or a stricter client).
func Read(ctx context.Context, c *http.Client, rawURL string, maxBytes int64) ([]byte, string, error) {
	if maxBytes <= 0 {
		return nil, "", errors.New("byte limit must be positive")
	}
	if c == nil {
		return nil, "", errors.New("fetch client is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", errors.New("invalid fetch URL")
	}
	resp, err := c.Do(req)
	if errors.Is(err, ErrRefused) {
		return nil, "", ErrRefused
	}
	if err != nil {
		return nil, "", ErrRequest
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		e := &StatusError{Code: resp.StatusCode}
		if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
			e.RetryAfter = time.Duration(min(seconds, 3600)) * time.Second
		}
		return nil, "", e
	}
	b, err := ReadBody(resp, maxBytes)
	if err != nil {
		return nil, "", err
	}
	return b, resp.Request.URL.String(), nil
}
