package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"slices"
	"time"

	"github.com/irashack/night-desk/internal/config"
)

type Session struct {
	Hash                       [32]byte
	Issuer, Subject, CSRF      string
	Created, Expires, LastSeen time.Time
	// Groups is the ID token's groups claim at sign-in (empty when no paper
	// admits readers by group).
	Groups []string
}
type SessionLookup interface {
	Session(context.Context, [32]byte) (Session, error)
	SlideSession(context.Context, [32]byte, time.Time, time.Time) error
}

// Access is one paper a session may read, and whether it reads it by subject
// (its own paper) or through a group.
type Access struct {
	Paper     string
	BySubject bool
}

// ReaderScope can only be obtained from a live session and current configuration.
type ReaderScope struct {
	session Session
	paper   PaperScope
	papers  []Access
	subject bool
	refresh []string
}

func (s ReaderScope) PaperScope() PaperScope { return s.paper }
func (s ReaderScope) Session() Session       { return s.session }

// Papers lists every paper the session reads: its subject paper first, then
// group papers in configuration order.
func (s ReaderScope) Papers() []Access { return slices.Clone(s.papers) }

// BySubject reports whether the selected paper is read by subject.
func (s ReaderScope) BySubject() bool { return s.subject }

// CanEdit reports whether the reader may edit the selected paper's sections:
// only a subject reader, on their own paper (always the default one).
func (s ReaderScope) CanEdit() bool { return s.subject && s.Default() }

// Refresh lists group papers the session read until its group membership
// went stale; signing in again restores them if the reader is still a member.
func (s ReaderScope) Refresh() []string { return slices.Clone(s.refresh) }

// Default reports whether the selected paper is the session's first paper,
// the one served at /.
func (s ReaderScope) Default() bool {
	return len(s.papers) > 0 && s.papers[0].Paper == s.paper.paperID
}

// For selects another paper the session reads.
func (s ReaderScope) For(paper string) (ReaderScope, bool) {
	for _, a := range s.papers {
		if a.Paper == paper {
			s.paper = PaperScope{paperID: a.Paper}
			s.subject = a.BySubject
			return s, true
		}
	}
	return s, false
}

var ErrUnmapped = errors.New("no paper for reader")

// Readable lists the papers an identity reads under cfg: the first paper that
// names its subject (the same first-match rule as before group readers), then
// every paper with a group reader for one of groups.
func Readable(cfg *config.Config, issuer, subject string, groups []string) []Access {
	var out []Access
	seen := map[string]bool{}
subjects:
	for _, paper := range cfg.Papers {
		for _, reader := range paper.Readers {
			if reader.Group == "" && reader.Issuer == issuer && reader.ResolvedSubject() == subject {
				out = append(out, Access{Paper: paper.ID, BySubject: true})
				seen[paper.ID] = true
				break subjects
			}
		}
	}
	for _, paper := range cfg.Papers {
		if seen[paper.ID] {
			continue
		}
		for _, reader := range paper.Readers {
			if reader.Group != "" && reader.Issuer == issuer && slices.Contains(groups, reader.Group) {
				out = append(out, Access{Paper: paper.ID})
				seen[paper.ID] = true
				break
			}
		}
	}
	return out
}

func AuthenticateReader(ctx context.Context, lookup SessionLookup, cfg *config.Config, token string, now time.Time, lifetime time.Duration) (ReaderScope, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != token {
		return ReaderScope{}, ErrUnauthorized
	}
	session, err := lookup.Session(ctx, sha256.Sum256([]byte(token)))
	if err != nil || !session.Expires.After(now) {
		return ReaderScope{}, ErrUnauthorized
	}
	// Group membership holds as of sign-in, and only for the group lifetime.
	stale := cfg.HasGroupReaders() && !session.Created.Add(cfg.GroupLifetime()).After(now)
	var papers []Access
	var refresh []string
	if stale {
		papers = Readable(cfg, session.Issuer, session.Subject, nil)
		if len(papers) == 0 {
			// Without a paper of its own the session signs in again, to read
			// group papers it may have joined or still belongs to.
			return ReaderScope{}, ErrUnauthorized
		}
		for _, a := range Readable(cfg, session.Issuer, session.Subject, session.Groups) {
			if !a.BySubject {
				refresh = append(refresh, a.Paper)
			}
		}
	} else {
		papers = Readable(cfg, session.Issuer, session.Subject, session.Groups)
	}
	if err = lookup.SlideSession(ctx, session.Hash, now, now.Add(lifetime)); err != nil {
		return ReaderScope{}, err
	}
	scope := ReaderScope{session: session, papers: papers, refresh: refresh}
	if len(papers) == 0 {
		return scope, ErrUnmapped
	}
	scope.paper = PaperScope{paperID: papers[0].Paper}
	scope.subject = papers[0].BySubject
	return scope, nil
}
