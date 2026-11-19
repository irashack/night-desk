package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/irashack/night-desk/internal/config"
)

type Session struct {
	Hash                       [32]byte
	Issuer, Subject, CSRF      string
	Created, Expires, LastSeen time.Time
}
type SessionLookup interface {
	Session(context.Context, [32]byte) (Session, error)
	SlideSession(context.Context, [32]byte, time.Time, time.Time) error
}

// ReaderScope can only be obtained from a live session and current configuration.
type ReaderScope struct {
	session Session
	paper   PaperScope
}

func (s ReaderScope) PaperScope() PaperScope { return s.paper }
func (s ReaderScope) Session() Session       { return s.session }

var ErrUnmapped = errors.New("no paper for reader")

func AuthenticateReader(ctx context.Context, lookup SessionLookup, cfg *config.Config, token string, now time.Time, lifetime time.Duration) (ReaderScope, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != token {
		return ReaderScope{}, ErrUnauthorized
	}
	session, err := lookup.Session(ctx, sha256.Sum256([]byte(token)))
	if err != nil || !session.Expires.After(now) {
		return ReaderScope{}, ErrUnauthorized
	}
	if err = lookup.SlideSession(ctx, session.Hash, now, now.Add(lifetime)); err != nil {
		return ReaderScope{}, err
	}
	scope := ReaderScope{session: session}
	for _, paper := range cfg.Papers {
		for _, reader := range paper.Readers {
			if reader.Issuer == session.Issuer && reader.ResolvedSubject() == session.Subject {
				scope.paper = PaperScope{paperID: paper.ID}
				return scope, nil
			}
		}
	}
	return scope, ErrUnmapped
}
