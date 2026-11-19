package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

type PaperScope struct {
	paperID      string
	capabilities []Capability
}

func (s PaperScope) PaperID() string { return s.paperID }
func (s PaperScope) Valid() bool     { return s.paperID != "" }

func (s PaperScope) Can(c Capability) bool {
	for _, granted := range s.capabilities {
		if granted == c {
			return true
		}
	}
	return false
}

type KeyLookup interface {
	LookupAgentHash(context.Context, [32]byte) (paper, capabilities string, err error)
}

var ErrUnauthorized = errors.New("unauthorized")

func Authenticate(ctx context.Context, lookup KeyLookup, key string) (PaperScope, error) {
	if !strings.HasPrefix(key, "nd_") {
		return PaperScope{}, ErrUnauthorized
	}
	raw, e := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, "nd_"))
	if e != nil || len(raw) != 32 || "nd_"+base64.RawURLEncoding.EncodeToString(raw) != key {
		return PaperScope{}, ErrUnauthorized
	}
	paper, names, e := lookup.LookupAgentHash(ctx, sha256.Sum256([]byte(key)))
	if e != nil || paper == "" {
		return PaperScope{}, ErrUnauthorized
	}
	capabilities, e := ParseCapabilities(names)
	if e != nil {
		return PaperScope{}, ErrUnauthorized
	}
	return PaperScope{paperID: paper, capabilities: capabilities}, nil
}
