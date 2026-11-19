package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/editorial"
	"github.com/irashack/night-desk/internal/images"
	"github.com/irashack/night-desk/internal/store"
)

type ImageFetcher interface {
	Fetch(context.Context, string) (images.Result, error)
}
type Server struct {
	Store  *store.Store
	Config func() *config.Config
	Images ImageFetcher
	Log    *slog.Logger
	// Editorial, when set, lets each paper's agent edit its editorial fields.
	Editorial *editorial.Desk
	// Now, when set, replaces the clock for draft run claims (tests).
	Now func() time.Time
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

type problem struct {
	Path string `json:"path"`
	Rule string `json:"rule"`
}

func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, message string) {
	jsonResponse(w, status, errorResponse{Error: message})
}
func (s *Server) Handler() http.Handler {
	s.Store.SetEventConfig(s.Config)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if s.Store.Ping(ctx) != nil {
			http.Error(w, "unhealthy", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok")
	})
	agent := http.NewServeMux()
	for _, route := range Routes() {
		if route.Editorial && s.Editorial == nil {
			continue
		}
		agent.HandleFunc(route.Method+" "+route.Path, s.requireAny(func(w http.ResponseWriter, r *http.Request) {
			route.Handler(s, w, r)
		}, append([]auth.Capability{route.Capability}, route.OrCapabilities...)...))
	}
	// Explicit fallbacks keep even routing failures JSON.
	fallback := func(w http.ResponseWriter, r *http.Request) {
		status := http.StatusNotFound
		for _, route := range Routes() {
			if route.Editorial && s.Editorial == nil {
				continue
			}
			if routeMatches(route.Path, r.URL.Path) {
				status = http.StatusMethodNotAllowed
				break
			}
		}
		fail(w, status, http.StatusText(status))
	}
	agent.HandleFunc("/api/", fallback)
	agent.HandleFunc("/api", fallback)
	authenticated := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fields := strings.Fields(r.Header.Get("Authorization"))
		if len(fields) != 2 || fields[0] != "Bearer" {
			fail(w, 401, "unauthorized")
			return
		}
		scope, e := auth.Authenticate(r.Context(), s.Store, fields[1])
		if e != nil {
			fail(w, 401, "unauthorized")
			return
		}
		// A removed paper immediately loses access, even if a key was not revoked.
		p, ok := s.Config().Paper(scope.PaperID())
		if !ok {
			fail(w, 401, "unauthorized")
			return
		}
		keyID, e := s.Store.AgentKeyID(r.Context(), fields[1])
		if e != nil {
			fail(w, 401, "unauthorized")
			return
		}
		ctx := context.WithValue(r.Context(), principalKey{}, principal{scope: scope, paper: p, keyID: keyID})
		agent.ServeHTTP(w, r.WithContext(ctx))
	})
	mux.Handle("/api", authenticated)
	mux.Handle("/api/", authenticated)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			clean := path.Clean(r.URL.Path)
			if clean != r.URL.Path && clean+"/" != r.URL.Path {
				fail(w, 400, "non-canonical API path")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

type principalKey struct{}
type principal struct {
	scope auth.PaperScope
	paper config.Paper
	keyID string
}

func current(r *http.Request) principal { return r.Context().Value(principalKey{}).(principal) }
func (s *Server) paper(w http.ResponseWriter, r *http.Request) {
	p := current(r)
	v := s.paperView(p.paper)
	urls, err := s.Store.RecentCandidateURLs(r.Context(), p.scope, time.Now().AddDate(0, 0, -7))
	if err != nil {
		s.storageFailed(w)
		return
	}
	notes, err := s.Store.ReaderNotes(r.Context(), p.scope)
	if err != nil {
		s.storageFailed(w)
		return
	}
	var keys []string
	for _, section := range p.paper.Sections {
		if section.Finds() {
			keys = append(keys, section.Key)
		}
	}
	if len(keys) > 0 {
		if v.FoundSources, err = s.Store.FoundSources(r.Context(), p.paper.ID, keys); err != nil {
			s.storageFailed(w)
			return
		}
	}
	v.RecentURLs = urls
	v.ReaderNotes = notes.Text
	jsonResponse(w, 200, v)
}

type paperView struct {
	AgentRole      string          `json:"agent_role"`
	DeadlinePolicy string          `json:"deadline_policy"`
	DraftLead      string          `json:"draft_lead"`
	ReaderNotes    string          `json:"reader_notes"`
	Sources        []config.Source `json:"sources,omitempty"`
	RetentionDays  int             `json:"retention_days"`
	Gather         *config.Gather  `json:"gather,omitempty"`
	Filters        *config.Filters `json:"filters,omitempty"`
	RecentURLs     []string        `json:"recent_urls"`
	// FoundSources lists, for each section that finds its own sources, the
	// feeds found so far and the last discovery attempt.
	FoundSources []store.SectionSources `json:"found_sources,omitempty"`
	Masthead     string                 `json:"masthead"`
	Place        string                 `json:"place,omitempty"`
	Brief        *string                `json:"brief,omitempty"`
	Deadline     *string                `json:"deadline,omitempty"`
	Motto        *config.Motto          `json:"motto,omitempty"`
	Sections     []config.Section       `json:"sections"`
	Timezone     string                 `json:"timezone"`
	Boxes        config.Boxes           `json:"boxes"`
	// Revision is the paper's current editorial revision (0: never edited);
	// Edited says whether an agent revision rather than the declaration is in effect.
	Revision         *int64    `json:"revision,omitempty"`
	Edited           *bool     `json:"edited,omitempty"`
	RevisionProblems []problem `json:"revision_problems,omitempty"`
}

func (s *Server) paperView(p config.Paper) paperView {
	v := paperView{AgentRole: p.Role(), DeadlinePolicy: p.Policy(), DraftLead: p.DraftLead, Masthead: p.Masthead, Place: p.Place, Brief: p.Brief, Deadline: p.Deadline, Motto: p.Motto, Sections: p.Sections, Timezone: p.Timezone, Boxes: p.Boxes, Sources: p.Sources, RetentionDays: p.RetentionDays, Gather: nil, Filters: p.Filters}
	if len(s.Config().Gather.AllowPrivate) > 0 {
		g := s.Config().Gather
		v.Gather = &g
	}
	if v.DraftLead == "" {
		v.DraftLead = config.DefaultDraftLead
	}
	if v.RetentionDays == 0 {
		v.RetentionDays = 14
	}
	if s.Editorial != nil {
		rev, edited := s.Editorial.Revision(p.ID)
		v.Revision, v.Edited = &rev, &edited
		v.RevisionProblems = problemList(s.Editorial.RevisionProblems(p.ID))
	}
	return v
}
func (s *Server) publish(w http.ResponseWriter, r *http.Request) {
	p := current(r)
	if p.paper.Role() != "author" {
		fail(w, 409, "paper publishes through drafts")
		return
	}
	date := r.PathValue("date")
	if _, e := edition.ParseDate(date); e != nil {
		fail(w, 400, "date must be YYYY-MM-DD")
		return
	}
	body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if e != nil {
		fail(w, 413, "body must be at most 1 MB")
		return
	}
	errors, warnings := []problem{}, []problem{}
	ed, e := edition.Decode(strings.NewReader(string(body)))
	if e != nil {
		path, rule, ok := strings.Cut(e.Error(), ": ")
		if !ok {
			path = "$"
			rule = "invalid edition JSON"
		}
		errors = append(errors, problem{path, rule})
	} else {
		for _, v := range edition.Validate(ed, p.paper.Rules()) {
			if v.Severity == edition.Error {
				errors = append(errors, problem{v.Path, v.Rule})
			} else {
				warnings = append(warnings, problem{v.Path, v.Rule})
			}
		}
	}
	if len(errors) > 0 {
		if e = s.Store.RecordPublishDiagnostics(r.Context(), p.paper.ID, date, publishDiagnostics(p.paper, "put", warnings, errors, nil)); e != nil {
			s.storageFailed(w)
			return
		}
		jsonResponse(w, 422, map[string]any{"errors": errors, "warnings": warnings})
		return
	}
	sourceWarnings, checks := s.publishSourceChecks(r.Context(), p.scope, p.paper, ed)
	warnings = append(warnings, sourceWarnings...)
	fetched, imageWarnings := s.retainImages(r.Context(), ed)
	warnings = append(warnings, imageWarnings...)
	if e = s.Store.PublishWithDiagnostics(r.Context(), p.scope, date, ed, fetched, publishDiagnostics(p.paper, "put", warnings, errors, checks), s.layoutFor(p.paper)); e != nil {
		if itemIdentityError(w, e, warnings) {
			return
		}
		if s.Log != nil {
			s.Log.Error("edition storage failed")
		}
		fail(w, 500, "storage failed")
		return
	}
	jsonResponse(w, 200, publishResponse{Date: date, Warnings: warnings})
}
func (s *Server) feedback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	after := int64(0)
	limit := 100
	var e error
	if q.Has("after") {
		after, e = strconv.ParseInt(q.Get("after"), 10, 64)
		if e != nil || after < 0 {
			fail(w, 400, "after must be a non-negative cursor")
			return
		}
	}
	if q.Has("limit") {
		limit, e = strconv.Atoi(q.Get("limit"))
		if e != nil || limit < 1 || limit > 500 {
			fail(w, 400, "limit must be 1..500")
			return
		}
	}
	events, next, e := s.Store.Feedback(r.Context(), current(r).scope, after, limit)
	if e != nil {
		fail(w, 500, "storage failed")
		return
	}
	jsonResponse(w, 200, feedbackResponse{Events: events, NextCursor: next})
}

func (s *Server) retainImages(parent context.Context, ed *edition.Edition) (map[string]store.Image, []problem) {
	warnings := []problem{}
	fetched := map[string]store.Image{}
	// Results are collected in item order; parallel completion never changes warnings.
	type task struct {
		item   *edition.Item
		path   string
		result images.Result
		err    error
	}
	tasks := []*task{}
	for i := range ed.Sections {
		for j := range ed.Sections[i].Items {
			item := &ed.Sections[i].Items[j]
			if item.ImageURL != nil {
				tasks = append(tasks, &task{item: item, path: fmt.Sprintf("sections[%d].items[%d].image_url", i, j)})
			}
		}
	}
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, t := range tasks {
		wg.Add(1)
		go func(t *task) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				t.err = ctx.Err()
				return
			}
			if s.Images == nil {
				t.err = fmt.Errorf("no image fetcher")
				return
			}
			t.result, t.err = s.Images.Fetch(ctx, *t.item.ImageURL)
		}(t)
	}
	wg.Wait()
	for _, t := range tasks {
		// Reader pages must only use retained local images, never the agent URL.
		t.item.ImageURL = nil
		if t.err != nil {
			warnings = append(warnings, problem{t.path, "image could not be retained; omitted"})
			continue
		}
		im := t.result
		fetched[t.item.ID] = store.Image{SHA256: im.SHA256, Width: im.Width, Height: im.Height, Bytes: im.Bytes}
	}
	return fetched, warnings
}

// layoutFor records the masthead and section titles an edition is published with.
func (s *Server) layoutFor(p config.Paper) *store.EditionLayout {
	layout := &store.EditionLayout{Masthead: p.Masthead}
	// The scheduler may use a store-only Server for deadline policy application.
	if s.Config != nil {
		if cfg := s.Config(); cfg != nil {
			layout.Ratings = cfg.Lean.Ratings
		}
	}
	for _, section := range p.Sections {
		layout.Sections = append(layout.Sections, store.LayoutSection{Key: section.Key, Title: section.Title, Lean: section.Lean})
	}
	return layout
}
