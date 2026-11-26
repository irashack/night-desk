// Package editorial keeps the paper fields a writing agent edits through the agent
// API and overlays them on the declared configuration.
package editorial

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/store"
)

// ErrNoRevision is returned when a restore names a revision that was never kept.
var ErrNoRevision = errors.New("no such revision")

// ErrConflict is returned when a save expected another revision than the
// paper's current one: the edit was computed from a stale paper.
var ErrConflict = errors.New("the paper changed since that revision")

// AnyRevision, as an expected revision, saves over whatever revision is current.
const AnyRevision int64 = -1

// Desk is the source of the effective configuration: the declaration, with each
// paper's latest agent revision (if any) in place of its editorial fields.
type Desk struct {
	store *store.Store
	base  func() *config.Config
	log   *slog.Logger

	mu        sync.Mutex
	current   map[string]state
	gen       uint64
	cacheBase *config.Config
	cacheGen  uint64
	cache     *config.Config
}

type state struct {
	revision  int64
	editorial *config.Editorial // nil: the paper follows its declaration
	problems  []string
}

// Open loads every paper's latest revision. A stored revision that no longer
// decodes or validates is logged and the paper follows its declaration.
func Open(ctx context.Context, st *store.Store, base func() *config.Config, log *slog.Logger) (*Desk, error) {
	revs, err := st.CurrentEditorials(ctx)
	if err != nil {
		return nil, err
	}
	d := &Desk{store: st, base: base, log: log, current: map[string]state{}}
	for paper, r := range revs {
		s := state{revision: r.Revision}
		if r.Body != nil {
			e, problems := Decode(r.Body)
			if len(problems) == 0 {
				s.editorial = &e
			} else {
				s.problems = problems
				if log != nil {
					log.Error("stored paper editorial rejected; following the declaration", "paper", paper, "revision", r.Revision)
				}
			}
		}
		d.current[paper] = s
	}
	return d, nil
}

// Decode strictly parses and validates a stored or submitted editorial document.
func Decode(body []byte) (config.Editorial, []string) {
	var e config.Editorial
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return e, []string{"$: " + err.Error()}
	}
	// More misses a stray closing bracket; only EOF proves nothing follows.
	if _, err := dec.Token(); err != io.EOF {
		return e, []string{"$: trailing data"}
	}
	return e, e.Problems()
}

// Config returns the effective configuration snapshot (never modify it).
func (d *Desk) Config() *config.Config {
	base := d.base()
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.configLocked(base)
}

func (d *Desk) configLocked(base *config.Config) *config.Config {
	if d.cache != nil && d.cacheBase == base && d.cacheGen == d.gen {
		return d.cache
	}
	edits := map[string]config.Editorial{}
	for paper, s := range d.current {
		if s.editorial != nil {
			edits[paper] = *s.editorial
		}
	}
	d.cache, d.cacheBase, d.cacheGen = base.WithEditorials(edits), base, d.gen
	return d.cache
}

// Revision reports a paper's current revision (0: never edited) and whether an
// agent revision, rather than the declaration, is in effect.
func (d *Desk) Revision(paper string) (int64, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.current[paper]
	return s.revision, s.editorial != nil
}

// Current returns a paper's effective configuration together with the revision
// it reflects, read at one instant, so an edit computed from the paper can be
// saved against that revision. ok is false when the paper is not configured.
func (d *Desk) Current(paper string) (p config.Paper, revision int64, ok bool) {
	base := d.base()
	d.mu.Lock()
	defer d.mu.Unlock()
	p, ok = d.configLocked(base).Paper(paper)
	return p, d.current[paper].revision, ok
}

// RevisionProblems reports why the latest stored revision could not be used.
// Saving or restoring a valid revision clears these problems.
func (d *Desk) RevisionProblems(paper string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.current[paper].problems...)
}

// Save validates e and makes it the paper's new revision, provided the paper is
// still at expected, the revision e was computed from (AnyRevision: whatever is
// current); otherwise it returns ErrConflict and saves nothing.
func (d *Desk) Save(ctx context.Context, scope auth.PaperScope, e config.Editorial, expected int64) (int64, []string, error) {
	if problems := e.Problems(); len(problems) > 0 {
		return 0, problems, nil
	}
	body, err := json.Marshal(e)
	if err != nil {
		return 0, nil, err
	}
	rev, err := d.save(ctx, scope, expected, body, &e)
	return rev, nil, err
}

// Restore saves a copy of an earlier revision as the newest one; revision 0
// returns the paper to its declaration. Like Save, it refuses with ErrConflict
// when the paper is no longer at expected (AnyRevision: no expectation).
func (d *Desk) Restore(ctx context.Context, scope auth.PaperScope, revision, expected int64) (int64, error) {
	if revision == 0 {
		return d.save(ctx, scope, expected, nil, nil)
	}
	r, err := d.store.EditorialRevision(ctx, scope, revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNoRevision
	}
	if err != nil {
		return 0, err
	}
	if r.Body == nil {
		return d.save(ctx, scope, expected, nil, nil)
	}
	e, problems := Decode(r.Body)
	if len(problems) > 0 {
		return 0, fmt.Errorf("revision %d no longer validates: %s", revision, strings.Join(problems, "; "))
	}
	return d.save(ctx, scope, expected, r.Body, &e)
}

func (d *Desk) save(ctx context.Context, scope auth.PaperScope, expected int64, body json.RawMessage, e *config.Editorial) (int64, error) {
	// One writer at a time keeps the stored and in-memory revision in step.
	d.mu.Lock()
	defer d.mu.Unlock()
	current := d.current[scope.PaperID()].revision
	if expected == AnyRevision {
		expected = current
	}
	if expected != current {
		return 0, ErrConflict
	}
	// The store checks the revision again, atomically with the insert.
	rev, err := d.store.SaveEditorialAt(ctx, scope, expected, body)
	if errors.Is(err, store.ErrEditorialConflict) {
		return 0, ErrConflict
	}
	if err != nil {
		return 0, err
	}
	d.current[scope.PaperID()] = state{revision: rev, editorial: e}
	d.gen++
	return rev, nil
}

// Revisions lists a paper's kept revisions, newest first.
func (d *Desk) Revisions(ctx context.Context, scope auth.PaperScope, limit int) ([]store.EditorialRevision, error) {
	return d.store.EditorialRevisions(ctx, scope, limit)
}
