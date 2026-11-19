package keys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/store"
)

// Issue grants the existing four capabilities when none are specified.
func Issue(ctx context.Context, s *store.Store, paper, label string, capabilities ...auth.Capability) (id, key string, err error) {
	var raw [32]byte
	if _, err = rand.Read(raw[:]); err != nil {
		return
	}
	var ident [16]byte
	if _, err = rand.Read(ident[:]); err != nil {
		return
	}
	key = "nd_" + base64.RawURLEncoding.EncodeToString(raw[:])
	id = hex.EncodeToString(ident[:])
	err = s.InsertKey(ctx, id, paper, label, sha256.Sum256([]byte(key)), capabilities...)
	if err != nil {
		key = ""
	}
	return
}
