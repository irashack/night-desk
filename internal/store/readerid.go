package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// readerIDKey reads the installation's reader-id key (migration 024).
func (s *Store) readerIDKey(ctx context.Context) ([]byte, error) {
	var key []byte
	err := s.db.QueryRowContext(ctx, "SELECT key FROM reader_id_key WHERE id=1").Scan(&key)
	return key, err
}

// readerID is the opaque reader id agents see in feedback: stable for one
// reader of one paper, different between papers, and not reversible to the
// identity provider's issuer and subject without the database's key.
func readerID(key []byte, paper, issuer, subject string) string {
	mac := hmac.New(sha256.New, key)
	for _, part := range []string{paper, issuer, subject} {
		mac.Write([]byte(part))
		mac.Write([]byte{0})
	}
	return "r_" + hex.EncodeToString(mac.Sum(nil)[:16])
}
