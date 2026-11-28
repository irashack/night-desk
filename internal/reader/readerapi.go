package reader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/store"
)

// The reader API (v1) is the reader's pages as JSON for native apps: the
// papers a reader may read, their editions, stories, search, feedback and
// images. It authenticates only with a bearer token from the app sign-in
// (appauth.go), never with the browser's cookie, so it needs no CSRF token.
// docs/reference/reader-api.md is its contract; v1 only grows.

// ReaderAPIVersion is the version in the /api/reader/v1 paths.
const ReaderAPIVersion = 1

const apiPrefix = "/api/reader/v1"

type apiErrorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func apiError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="Night Desk"`)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiErrorBody{code, message})
}

// apiJSON answers with v and a strong ETag, or 304 when the app already has
// this exact answer: apps revalidate the editions they keep offline.
func apiJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(v); err != nil {
		apiError(w, http.StatusInternalServerError, "unavailable", "Reader unavailable.")
		return
	}
	sum := sha256.Sum256(b.Bytes())
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodGet && status == http.StatusOK {
		w.Header().Set("ETag", etag)
		// The reader's own data: kept by the app, never by shared caches.
		w.Header().Set("Cache-Control", "private, no-cache")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.WriteHeader(status)
	_, _ = w.Write(b.Bytes())
}

// bearer reads the Authorization header's token.
func bearer(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

// protectAPI authenticates a reader API request by its bearer token and, for
// /papers/{paper}/ paths, selects that paper. unmappedOK lets a reader without
// a paper through (to /me, which says so).
func (s *Server) protectAPI(next http.Handler, unmappedOK bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := s.Config()
		token, ok := bearer(r)
		if !ok {
			apiError(w, http.StatusUnauthorized, "unauthorized", "Sign in from the app first.")
			return
		}
		if cfg.AuthMode() == config.AuthNone && !cfg.Server.Auth.None.AcknowledgePublicAccess && !privateClient(r) {
			// The same rule as the pages: a server without sign-in serves
			// private networks only.
			apiError(w, http.StatusForbidden, "private_only", "This Night Desk has no sign-in and serves only private networks.")
			return
		}
		scope, err := auth.AuthenticateReader(r.Context(), s.Store, cfg, token, s.now(), s.lifetime())
		if err == auth.ErrUnauthorized {
			apiError(w, http.StatusUnauthorized, "unauthorized", "This sign-in has ended. Sign in again from the app.")
			return
		}
		if err != nil && err != auth.ErrUnmapped {
			apiError(w, http.StatusInternalServerError, "unavailable", "Reader unavailable.")
			return
		}
		if err == auth.ErrUnmapped && !unmappedOK {
			apiError(w, http.StatusForbidden, "no_paper", "No paper is set up for this reader yet.")
			return
		}
		if id := r.PathValue("paper"); id != "" {
			selected, ok := scope.For(id)
			if !ok && scope.NeedsSignIn(id) {
				apiError(w, http.StatusForbidden, "sign_in_again", "Sign in again to read this paper: your group membership needs checking.")
				return
			}
			if !ok {
				apiError(w, http.StatusNotFound, "not_found", "No such paper for this reader.")
				return
			}
			scope = selected
		}
		paper, _ := cfg.Paper(scope.PaperScope().PaperID())
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{scope, paper, ""})))
	})
}

func (s *Server) apiRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+apiPrefix+"/{$}", s.apiInfo)
	mux.HandleFunc("GET "+apiPrefix, s.apiInfo)
	mux.HandleFunc("POST "+apiPrefix+"/token", s.appToken)
	mux.Handle("POST "+apiPrefix+"/signout", s.protectAPI(http.HandlerFunc(s.apiSignOut), true))
	mux.Handle("GET "+apiPrefix+"/me", s.protectAPI(http.HandlerFunc(s.apiMe), true))
	mux.Handle("GET "+apiPrefix+"/images/{image}", s.protectAPI(http.HandlerFunc(s.image), false))
	paper := apiPrefix + "/papers/{paper}"
	mux.Handle("GET "+paper+"/editions", s.protectAPI(http.HandlerFunc(s.apiEditions), false))
	mux.Handle("GET "+paper+"/editions/latest", s.protectAPI(http.HandlerFunc(s.apiEdition), false))
	mux.Handle("GET "+paper+"/editions/{date}", s.protectAPI(http.HandlerFunc(s.apiEdition), false))
	mux.Handle("GET "+paper+"/editions/{date}/items/{item}", s.protectAPI(http.HandlerFunc(s.apiStory), false))
	mux.Handle("GET "+paper+"/search", s.protectAPI(http.HandlerFunc(s.apiSearch), false))
	mux.Handle("POST "+paper+"/feedback", s.protectAPI(http.HandlerFunc(s.apiFeedback), false))
	// Anything else under the prefix is a JSON 404, not the reader's HTML.
	mux.HandleFunc(apiPrefix+"/", func(w http.ResponseWriter, r *http.Request) {
		apiError(w, http.StatusNotFound, "not_found", "No such reader API path.")
	})
}

type apiInfo struct {
	API     int    `json:"api"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	// Auth is how an app signs in: open Authorize in the system browser, then
	// exchange the code at Token.
	Auth struct {
		Mode      string   `json:"mode"`
		Authorize string   `json:"authorize"`
		Token     string   `json:"token"`
		Redirects []string `json:"redirect_uris"`
	} `json:"auth"`
	WebPush bool `json:"web_push"`
}

// apiInfo needs no sign-in: an app checks the address it was given is a
// Night Desk it can talk to before signing in.
func (s *Server) apiInfo(w http.ResponseWriter, r *http.Request) {
	v := apiInfo{API: ReaderAPIVersion, Name: "Night Desk", Version: s.Version, WebPush: s.Push != nil}
	v.Auth.Mode = string(s.Config().AuthMode())
	v.Auth.Authorize = "/app/authorize"
	v.Auth.Token = apiPrefix + "/token"
	v.Auth.Redirects = appRedirects
	apiJSON(w, r, http.StatusOK, v)
}

// apiSignOut ends this token's session and its push subscriptions.
func (s *Server) apiSignOut(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.SignOut(r.Context(), current(r).scope.Session().Hash); err != nil {
		apiError(w, http.StatusInternalServerError, "unavailable", "Sign-out failed.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type apiLatest struct {
	Date   string `json:"date"`
	Number int    `json:"number"`
	// Lead is the lead story's headline, for widgets; empty when none.
	Lead string `json:"lead,omitempty"`
}

type apiPaper struct {
	ID       string `json:"id"`
	Masthead string `json:"masthead"`
	// Default is the reader's own paper, the one at / in the browser.
	Default  bool       `json:"default"`
	Timezone string     `json:"timezone"`
	Latest   *apiLatest `json:"latest"`
}

type apiMe struct {
	// Name is who the server knows the reader as: a local username or proxy
	// user; empty for OIDC subjects (opaque) and in none mode.
	Name   string     `json:"name,omitempty"`
	Client string     `json:"client,omitempty"`
	Papers []apiPaper `json:"papers"`
	// SignInAgain lists papers this reader read through a group until the
	// membership went stale; signing in again from the app may restore them.
	SignInAgain []apiPaper `json:"sign_in_again"`
}

func (s *Server) apiMe(w http.ResponseWriter, r *http.Request) {
	scope := current(r).scope
	cfg := s.Config()
	session := scope.Session()
	v := apiMe{Client: session.Client, Papers: []apiPaper{}, SignInAgain: []apiPaper{}}
	if session.Issuer == config.LocalIssuer || session.Issuer == config.ProxyIssuer {
		v.Name = session.Subject
	}
	for i, a := range scope.Papers() {
		paper, ok := cfg.Paper(a.Paper)
		if !ok {
			continue
		}
		p := apiPaper{ID: paper.ID, Masthead: paper.Masthead, Default: i == 0, Timezone: paper.Timezone}
		selected, _ := scope.For(a.Paper)
		days, err := s.Store.ArchiveDays(r.Context(), selected.PaperScope(), 0, 1)
		if err != nil {
			apiError(w, http.StatusInternalServerError, "unavailable", "Reader unavailable.")
			return
		}
		if len(days) > 0 {
			n, err := s.Store.EditionNumber(r.Context(), selected.PaperScope(), days[0].Date)
			if err != nil {
				apiError(w, http.StatusInternalServerError, "unavailable", "Reader unavailable.")
				return
			}
			p.Latest = &apiLatest{Date: days[0].Date, Number: n, Lead: days[0].Lead}
		}
		v.Papers = append(v.Papers, p)
	}
	for _, id := range scope.Refresh() {
		if paper, ok := cfg.Paper(id); ok {
			v.SignInAgain = append(v.SignInAgain, apiPaper{ID: paper.ID, Masthead: paper.Masthead, Timezone: paper.Timezone})
		}
	}
	apiJSON(w, r, http.StatusOK, v)
}

type apiEditionDay struct {
	Date string `json:"date"`
	Lead string `json:"lead,omitempty"`
}

type apiEditions struct {
	Editions []apiEditionDay `json:"editions"`
	// NextPage is the page of older editions, 0 when there are none.
	NextPage int `json:"next_page"`
}

// apiEditions lists the paper's editions, newest first, 60 to a page.
func (s *Server) apiEditions(w http.ResponseWriter, r *http.Request) {
	page := 1
	if q := r.URL.Query().Get("page"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 1 || n > 1000000 {
			apiError(w, http.StatusBadRequest, "invalid_request", "page must be a positive number.")
			return
		}
		page = n
	}
	days, err := s.Store.ArchiveDays(r.Context(), current(r).scope.PaperScope(), (page-1)*60, 61)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "unavailable", "Reader unavailable.")
		return
	}
	v := apiEditions{Editions: []apiEditionDay{}}
	if len(days) > 60 {
		days, v.NextPage = days[:60], page+1
	}
	for _, d := range days {
		v.Editions = append(v.Editions, apiEditionDay{d.Date, d.Lead})
	}
	apiJSON(w, r, http.StatusOK, v)
}

type apiFeedbackState struct {
	// Vote is up, down or empty.
	Vote  string `json:"vote,omitempty"`
	Saved bool   `json:"saved"`
}

type apiOpinion struct {
	Title  string `json:"title"`
	URL    string `json:"url"`
	Source string `json:"source"`
	Stance string `json:"stance,omitempty"`
	Lean   string `json:"lean,omitempty"`
}

type apiFollowUp struct {
	Title    string `json:"title"`
	URL      string `json:"url"`
	Outlet   string `json:"outlet"`
	Gathered string `json:"gathered"`
}

type apiCoverage struct {
	Left        int    `json:"left"`
	Center      int    `json:"center"`
	Right       int    `json:"right"`
	Unrated     int    `json:"unrated"`
	Opinion     int    `json:"opinion"`
	Blindspot   string `json:"blindspot,omitempty"`
	Window      string `json:"window,omitempty"`
	Attribution string `json:"attribution,omitempty"`
}

type apiProvenance struct {
	// Status is checked (nothing to report) or notes (findings to double-check).
	Status   string   `json:"status"`
	Summary  string   `json:"summary"`
	Notes    []string `json:"notes"`
	Checked  string   `json:"checked"`
	Compared int      `json:"compared"`
	Cited    int      `json:"cited"`
}

// apiItem is an edition item as the edition schema v2 defines it, plus what
// the reader shows beside it.
type apiItem struct {
	edition.Item
	// Image is the paper's own copy of the item's image (an API path), or empty.
	Image string `json:"image,omitempty"`
	// Readable says the paper holds the story's text for the reading view.
	Readable   bool             `json:"readable"`
	Feedback   apiFeedbackState `json:"feedback"`
	Opinions   []apiOpinion     `json:"opinions,omitempty"`
	Later      []apiFollowUp    `json:"later,omitempty"`
	Coverage   *apiCoverage     `json:"coverage,omitempty"`
	Provenance *apiProvenance   `json:"provenance,omitempty"`
}

type apiSection struct {
	Key     string           `json:"key"`
	Title   string           `json:"title"`
	Items   []apiItem        `json:"items"`
	Markets *edition.Markets `json:"markets,omitempty"`
}

type apiWeather struct {
	Date  string  `json:"date"`
	High  float64 `json:"high"`
	Low   float64 `json:"low"`
	Wind  float64 `json:"wind"`
	Code  int     `json:"code"`
	Units string  `json:"units"`
}

type apiAlmanac struct {
	Sunrise      *time.Time `json:"sunrise,omitempty"`
	Sunset       *time.Time `json:"sunset,omitempty"`
	Moonrise     *time.Time `json:"moonrise,omitempty"`
	Moonset      *time.Time `json:"moonset,omitempty"`
	Phase        string     `json:"phase,omitempty"`
	Illumination float64    `json:"illumination"`
}

type apiEdition struct {
	Paper    apiPaper       `json:"paper"`
	Date     string         `json:"date"`
	Dateline string         `json:"dateline"`
	Number   int            `json:"number"`
	Motto    *edition.Motto `json:"motto,omitempty"`
	Lead     *apiItem       `json:"lead"`
	Sections []apiSection   `json:"sections"`
	Weather  *apiWeather    `json:"weather,omitempty"`
	Almanac  *apiAlmanac    `json:"almanac,omitempty"`
	Checked  string         `json:"checked,omitempty"`
	Pending  bool           `json:"unreviewed"`
	Previous *apiEditionAt  `json:"previous"`
}

// apiEditionAt points at a neighbouring edition.
type apiEditionAt struct {
	Date string `json:"date"`
}

func imagePath(name string) string {
	if name == "" {
		return ""
	}
	if !strings.HasSuffix(name, ".jpg") {
		name += ".jpg"
	}
	return apiPrefix + "/images/" + name
}

func toAPIItem(iv itemView) apiItem {
	it := apiItem{Item: iv.Item, Readable: iv.Read != "", Feedback: apiFeedbackState{Saved: iv.Saved}}
	it.Image = imagePath(iv.Image)
	switch {
	case iv.Up:
		it.Feedback.Vote = "up"
	case iv.Down:
		it.Feedback.Vote = "down"
	}
	for _, o := range iv.Opinions {
		it.Opinions = append(it.Opinions, apiOpinion(o))
	}
	for _, f := range iv.Later {
		it.Later = append(it.Later, apiFollowUp{Title: f.Title, URL: f.URL, Outlet: f.Outlet, Gathered: f.Gathered})
	}
	if b := iv.Coverage; b != nil {
		it.Coverage = &apiCoverage{Left: b.Left, Center: b.Center, Right: b.Right, Unrated: b.Unrated, Opinion: b.Opinion, Blindspot: b.Blindspot, Window: iv.CoverageWindow, Attribution: b.Attribution}
	}
	it.Provenance = toAPIProvenance(iv.Provenance)
	return it
}

func toAPIProvenance(p *provenanceView) *apiProvenance {
	if p == nil {
		return nil
	}
	notes := p.Notes
	if notes == nil {
		notes = []string{}
	}
	return &apiProvenance{Status: p.Status, Summary: p.Summary, Notes: notes, Checked: p.Checked, Compared: p.Compared, Cited: p.Cited}
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// apiEdition is one edition (or the newest) with everything the edition page
// shows.
func (s *Server) apiEdition(w http.ResponseWriter, r *http.Request) {
	date := r.PathValue("date")
	v, err := s.editionView(r, date)
	switch {
	case errors.Is(err, errNoEdition):
		apiError(w, http.StatusNotFound, "no_edition", "Your first paper arrives tomorrow morning.")
		return
	case errors.Is(err, errNotInPaper):
		apiError(w, http.StatusNotFound, "not_found", "This edition is not in the paper.")
		return
	case err != nil:
		apiError(w, http.StatusInternalServerError, "unavailable", "Reader unavailable.")
		return
	}
	p := current(r)
	out := apiEdition{Paper: apiPaper{ID: v.Paper.ID, Masthead: v.Paper.Masthead, Default: p.scope.Default(), Timezone: v.Paper.Timezone}, Date: v.Date, Dateline: v.Dateline, Number: v.Number, Sections: []apiSection{}, Checked: v.Checked, Pending: v.Unreviewed}
	out.Motto = v.Motto
	if v.Lead != nil {
		lead := toAPIItem(*v.Lead)
		out.Lead = &lead
	}
	for _, sec := range v.Sections {
		as := apiSection{Key: sec.Key, Title: sec.Title, Items: []apiItem{}, Markets: sec.Markets}
		for _, iv := range sec.Items {
			as.Items = append(as.Items, toAPIItem(iv))
		}
		out.Sections = append(out.Sections, as)
	}
	if d := v.Weather; d != nil {
		units := "imperial"
		if v.Paper.Metric() {
			units = "metric"
		}
		out.Weather = &apiWeather{Date: d.Date, High: d.High, Low: d.Low, Wind: d.Wind, Code: d.Code, Units: units}
	}
	if f := v.Facts; !f.Sunrise.IsZero() || !f.Sunset.IsZero() || f.Phase != "" {
		out.Almanac = &apiAlmanac{Sunrise: timePtr(f.Sunrise), Sunset: timePtr(f.Sunset), Moonrise: timePtr(f.Moonrise), Moonset: timePtr(f.Moonset), Phase: f.Phase, Illumination: f.Illumination}
	}
	if previous, err := s.Store.PreviousDate(r.Context(), p.scope.PaperScope(), v.Date); err != nil {
		apiError(w, http.StatusInternalServerError, "unavailable", "Reader unavailable.")
		return
	} else if previous != "" {
		out.Previous = &apiEditionAt{previous}
	}
	apiJSON(w, r, http.StatusOK, out)
}

type apiStory struct {
	Item apiItem `json:"item"`
	Date string  `json:"date"`
	// Paragraphs is the stored text, or null when the paper keeps only the link.
	Paragraphs []string `json:"paragraphs"`
	Partial    bool     `json:"partial"`
	// Gathered is when the text was fetched (RFC 3339).
	Gathered *time.Time `json:"gathered,omitempty"`
}

// apiStory is the reading view of one item.
func (s *Server) apiStory(w http.ResponseWriter, r *http.Request) {
	v, err := s.editionView(r, r.PathValue("date"))
	if errors.Is(err, errNoEdition) || errors.Is(err, errNotInPaper) {
		apiError(w, http.StatusNotFound, "not_found", "This story is not in the paper.")
		return
	}
	if err != nil {
		apiError(w, http.StatusInternalServerError, "unavailable", "Reader unavailable.")
		return
	}
	var found *itemView
	for _, sec := range v.Sections {
		for i := range sec.Items {
			if sec.Items[i].ID == r.PathValue("item") {
				found = &sec.Items[i]
			}
		}
	}
	if found == nil {
		apiError(w, http.StatusNotFound, "not_found", "This story is not in the paper.")
		return
	}
	out := apiStory{Item: toAPIItem(*found), Date: v.Date}
	snap, ok, err := s.Store.ReadableSnapshot(r.Context(), current(r).scope.PaperScope(), found.URL)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "unavailable", "Reader unavailable.")
		return
	}
	if ok {
		if out.Paragraphs = paragraphs(snap.Text); len(out.Paragraphs) == 0 {
			out.Paragraphs = nil
		}
		out.Partial = partial(snap.Text, found.Minutes)
		out.Gathered = timePtr(snap.FetchedAt.UTC())
	}
	apiJSON(w, r, http.StatusOK, out)
}

type apiResult struct {
	Date    string   `json:"date"`
	Item    string   `json:"item"`
	Kind    string   `json:"kind"`
	Title   string   `json:"title"`
	Summary string   `json:"summary"`
	Why     string   `json:"why,omitempty"`
	Source  string   `json:"source"`
	Byline  string   `json:"byline,omitempty"`
	Stance  string   `json:"stance,omitempty"`
	Outlets []string `json:"outlets,omitempty"`
}

// apiSearch searches the paper's archive like /search: q, in and saved=1.
func (s *Server) apiSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	saved := q.Get("saved")
	if saved != "" && saved != "1" {
		apiError(w, http.StatusBadRequest, "invalid_request", "saved must be 1 or omitted.")
		return
	}
	in, ok := store.ParseSearchIn(q.Get("in"))
	if !ok {
		apiError(w, http.StatusBadRequest, "invalid_request", "in must be writer, stance, outlets or omitted.")
		return
	}
	if utf8.RuneCountInString(q.Get("q")) > 500 {
		apiError(w, http.StatusBadRequest, "invalid_request", "q must be at most 500 characters.")
		return
	}
	results, err := s.Store.SearchFields(r.Context(), current(r).scope, q.Get("q"), in, saved == "1")
	if err != nil {
		apiError(w, http.StatusInternalServerError, "unavailable", "Search unavailable.")
		return
	}
	out := struct {
		Results []apiResult `json:"results"`
	}{Results: []apiResult{}}
	for _, v := range results {
		out.Results = append(out.Results, apiResult{v.Date, v.ItemID, v.Kind, v.Title, v.Summary, v.Why, v.Source, v.Byline, v.Stance, v.Outlets})
	}
	apiJSON(w, r, http.StatusOK, out)
}

// apiFeedback records 👍, 👎, save or unsave on an item, as the edition
// page's buttons do. The body is JSON {"date", "item", "kind"}.
func (s *Server) apiFeedback(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var in struct {
		Date string `json:"date"`
		Item string `json:"item"`
		Kind string `json:"kind"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		apiError(w, http.StatusBadRequest, "invalid_request", "The body must be JSON with date, item and kind.")
		return
	}
	switch s.recordFeedback(r, in.Date, in.Item, in.Kind) {
	case nil:
		w.WriteHeader(http.StatusNoContent)
	case errBadFeedback:
		apiError(w, http.StatusBadRequest, "invalid_request", "kind must be up, down, save or unsave.")
	case errNotInPaper:
		apiError(w, http.StatusNotFound, "not_found", "This story is not in the paper.")
	default:
		apiError(w, http.StatusInternalServerError, "unavailable", "Feedback unavailable.")
	}
}
