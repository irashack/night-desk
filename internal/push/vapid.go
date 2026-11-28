// Package push sends Web Push notifications (RFC 8030) signed with
// the server's VAPID key (RFC 8292) when an edition is published.
package push

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/irashack/night-desk/internal/store"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// KeyFile is the VAPID private key's name in the server's data directory.
const KeyFile = store.PushKeyFile

// Key is the server's VAPID identity: an ECDSA P-256 key pair.
type Key struct {
	private *ecdsa.PrivateKey
	public  string
}

// String keeps the private key out of structured log fallbacks.
func (k *Key) String() string   { return "VAPID key (redacted)" }
func (k *Key) GoString() string { return k.String() }

// PublicKey is the uncompressed public point, base64url without padding: the
// browser's applicationServerKey and the k= parameter of each request.
func (k *Key) PublicKey() string { return k.public }

func newKey(private *ecdsa.PrivateKey) (*Key, error) {
	if private.Curve != elliptic.P256() {
		return nil, errors.New("VAPID key must be P-256")
	}
	raw, err := private.PublicKey.Bytes()
	if err != nil {
		return nil, err
	}
	return &Key{private: private, public: base64.RawURLEncoding.EncodeToString(raw)}, nil
}

// CreateKey writes a new key to path (mode 0600), refusing an existing file.
func CreateKey(path string) (*Key, error) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err = pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
		f.Close()
		os.Remove(path)
		return nil, errors.New("VAPID key write failed")
	}
	if err = f.Close(); err != nil {
		os.Remove(path)
		return nil, errors.New("VAPID key write failed")
	}
	return newKey(private)
}

// ReadKey loads a key written by CreateKey.
func ReadKey(path string) (*Key, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("VAPID key: not a PEM private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("VAPID key: invalid private key")
	}
	private, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("VAPID key must be ECDSA P-256")
	}
	return newKey(private)
}

// LoadKey reads the data directory's key, creating it on first use.
func LoadKey(dataDir string) (*Key, error) {
	path := filepath.Join(dataDir, KeyFile)
	k, err := ReadKey(path)
	if errors.Is(err, os.ErrNotExist) {
		k, err = CreateKey(path)
		if errors.Is(err, os.ErrExist) {
			k, err = ReadKey(path)
		}
	}
	return k, err
}

// Authorization returns the RFC 8292 header value for one push endpoint: an
// ES256 JWT for the endpoint's origin, valid for 12 hours, naming subject (the
// paper's public URL) as the contact.
func (k *Key) Authorization(endpoint, subject string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("invalid push endpoint")
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, err := json.Marshal(map[string]any{"aud": u.Scheme + "://" + u.Host, "exp": now.Add(12 * time.Hour).Unix(), "sub": subject})
	if err != nil {
		return "", err
	}
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, k.private, digest[:])
	if err != nil {
		return "", err
	}
	// JWS ES256 signatures are the fixed-width R || S, not ASN.1.
	var sig [64]byte
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return fmt.Sprintf("vapid t=%s.%s, k=%s", unsigned, base64.RawURLEncoding.EncodeToString(sig[:]), k.public), nil
}
