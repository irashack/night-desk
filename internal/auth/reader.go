package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
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
	// GroupsChecked records that sign-in asked for the groups claim, so Groups
	// is the reader's membership (possibly none) rather than unknown.
	GroupsChecked bool
}
type SessionLookup interface {
	Session(context.Context, [32]byte) (Session, error)
	SlideSession(context.Context, [32]byte, time.Time, time.Time) error
}

// AccountLookup is implemented by a SessionLookup that keeps local accounts.
type AccountLookup interface {
	LocalAccount(ctx context.Context, username string) (exists, admin bool, err error)
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
	unknown []string
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

// NeedsSignIn reports whether signing in again could let the session read
// paper: its group membership is stale or was never recorded, and a group of
// its issuer reads the paper. A session whose current groups simply do not
// include the paper gets false.
func (s ReaderScope) NeedsSignIn(paper string) bool {
	return slices.Contains(s.refresh, paper) || slices.Contains(s.unknown, paper)
}

// groupPapers lists the papers some group reader of issuer can read.
func groupPapers(cfg *config.Config, issuer string) []string {
	var out []string
	for _, paper := range cfg.Papers {
		for _, reader := range paper.Readers {
			if reader.Group != "" && reader.Issuer == issuer {
				out = append(out, paper.ID)
				break
			}
		}
	}
	return out
}

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
//
// Without sign-in (server.auth.mode none) the one reader reads every paper.
// A local account also reads every paper whose readers list is empty; for an
// administrator the first such paper is its own (editable) unless a paper
// names it, while other accounts only read open papers.
func Readable(cfg *config.Config, issuer, subject string, groups []string) []Access {
	return readable(cfg, issuer, subject, groups, false)
}

func readable(cfg *config.Config, issuer, subject string, groups []string, admin bool) []Access {
	var out []Access
	mode := cfg.AuthMode()
	if !cfg.AcceptsIssuer(issuer) {
		return nil
	}
	if mode == config.AuthNone {
		if subject != config.NoneSubject {
			return nil
		}
		for i, paper := range cfg.Papers {
			out = append(out, Access{Paper: paper.ID, BySubject: i == 0})
		}
		return out
	}
	out = configured(cfg, issuer, subject, groups)
	if mode == config.AuthLocal {
		own := len(out) > 0 && out[0].BySubject || !admin
		for _, paper := range cfg.Papers {
			if len(paper.Readers) == 0 && !slices.ContainsFunc(out, func(a Access) bool { return a.Paper == paper.ID }) {
				out = append(out, Access{Paper: paper.ID, BySubject: !own})
				own = true
			}
		}
	}
	return out
}

// configured applies the papers' readers lists.
func configured(cfg *config.Config, issuer, subject string, groups []string) []Access {
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
	if err != nil && !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, ErrUnauthorized) {
		// A storage failure is not a sign-out: the caller answers 500 and
		// keeps the reader's cookie.
		return ReaderScope{}, err
	}
	if err != nil || !session.Expires.After(now) {
		return ReaderScope{}, ErrUnauthorized
	}
	if !cfg.AcceptsIssuer(session.Issuer) {
		// Signed in under another server.auth.mode.
		return ReaderScope{}, ErrUnauthorized
	}
	admin := false
	if session.Issuer == config.LocalIssuer {
		accounts, ok := lookup.(AccountLookup)
		if !ok {
			return ReaderScope{}, ErrUnauthorized
		}
		var exists bool
		if exists, admin, err = accounts.LocalAccount(ctx, session.Subject); err != nil {
			return ReaderScope{}, err
		}
		if !exists {
			return ReaderScope{}, ErrUnauthorized
		}
	}
	// Group membership holds as of sign-in, and only for the group lifetime.
	// A sign-in that never asked for groups knows no membership at all.
	stale := cfg.HasGroupReaders() && (!session.GroupsChecked || !session.Created.Add(cfg.GroupLifetime()).After(now))
	var papers []Access
	var refresh, unknown []string
	if stale {
		papers = readable(cfg, session.Issuer, session.Subject, nil, admin)
		if len(papers) == 0 {
			// Without a paper of its own the session signs in again, to read
			// group papers it may have joined or still belongs to.
			return ReaderScope{}, ErrUnauthorized
		}
		for _, a := range readable(cfg, session.Issuer, session.Subject, session.Groups, admin) {
			if !a.BySubject {
				refresh = append(refresh, a.Paper)
			}
		}
		for _, id := range groupPapers(cfg, session.Issuer) {
			if !slices.ContainsFunc(papers, func(a Access) bool { return a.Paper == id }) && !slices.Contains(refresh, id) {
				unknown = append(unknown, id)
			}
		}
	} else {
		papers = readable(cfg, session.Issuer, session.Subject, session.Groups, admin)
	}
	if err = lookup.SlideSession(ctx, session.Hash, now, now.Add(lifetime)); err != nil {
		return ReaderScope{}, err
	}
	scope := ReaderScope{session: session, papers: papers, refresh: refresh, unknown: unknown}
	if len(papers) == 0 {
		return scope, ErrUnmapped
	}
	scope.paper = PaperScope{paperID: papers[0].Paper}
	scope.subject = papers[0].BySubject
	return scope, nil
}
