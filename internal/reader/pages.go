package reader

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/irashack/night-desk/internal/almanac"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/lean"
	"github.com/irashack/night-desk/internal/store"
	"github.com/irashack/night-desk/internal/weather"
)

type itemView struct {
	edition.Item
	Image, Date     string
	Front           bool
	Up, Down, Saved bool
	Coverage        *lean.Bar
	CoverageWindow  string
	// Opinions are the columns that name this story, in edition order.
	Opinions []opinionView
	Later    []followUp
}

// opinionView is an opinion column shown beside the story it argues about. Lean
// is the outlet's rating label when the edition's ratings cover it.
type opinionView struct {
	Title, URL, Source, Stance, Lean string
}

var leanLabels = map[lean.Lean]string{lean.Left: "Left", lean.LeanLeft: "Lean left", lean.Center: "Center", lean.LeanRight: "Lean right", lean.Right: "Right"}

type pageNavigation struct {
	Previous, Next, Title string
	Page, Total           int
}

type sectionView struct {
	Key, Title     string
	Page           int
	Items          []itemView
	Navigation     pageNavigation
	Markets        *edition.Markets
	Attribution    string
	CoverageWindow string
}
type view struct {
	Paper                                config.Paper
	CSRF, Owner, Date, Dateline, Message string
	Number                               int
	Motto                                *edition.Motto
	Lead                                 *itemView
	Sections                             []sectionView
	Facts                                almanac.Day
	Weather                              *weather.Day
	Dates                                []string
	Previous, Next                       int
	Unreviewed                           bool
	Logout                               bool
	FrontNavigation                      pageNavigation
	Query                                string
	SavedOnly                            bool
	Results                              []store.SearchResult
	Editing                              bool
	Rows                                 []sectionRow
	Confirm                              *sectionRow
	Problems                             []string
	AddTitle, AddBrief                   string
	PushKey                              string
	// Base prefixes the paper's own links: empty for the session's default
	// paper, /p/{paper} for the others it reads.
	Base   string
	Papers []paperLink
}

// paperLink is one entry of the masthead's paper switcher.
type paperLink struct {
	Masthead, Href string
	Current        bool
}

func clock(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("3:04 pm")
}
func period(p almanac.Period) string { return clock(p.Start) + "–" + clock(p.End) }
func (s *Server) base(r *http.Request) view {
	p := current(r)
	owner := sha256.Sum256([]byte(p.scope.Session().CSRF + "\x00" + p.paper.ID))
	v := view{Paper: p.paper, CSRF: p.scope.Session().CSRF, Owner: base64.RawURLEncoding.EncodeToString(owner[:]), Base: p.base,
		Editing: s.Editorial != nil && p.scope.BySubject() && p.scope.Default()}
	if papers := p.scope.Papers(); len(papers) > 1 {
		cfg := s.Config()
		for i, a := range papers {
			paper, ok := cfg.Paper(a.Paper)
			if !ok {
				continue
			}
			href := "/"
			if i > 0 {
				href = paperBase(a.Paper) + "/"
			}
			v.Papers = append(v.Papers, paperLink{Masthead: paper.Masthead, Href: href, Current: a.Paper == p.paper.ID})
		}
	}
	if s.Push != nil {
		v.PushKey = s.Push.PublicKey()
	}
	return v
}
func (s *Server) edition(w http.ResponseWriter, r *http.Request) {
	v := s.base(r)
	scope := current(r).scope.PaperScope()
	date := r.PathValue("date")
	if date == "" {
		dates, err := s.Store.Dates(r.Context(), scope, 0, 1)
		if err != nil {
			http.Error(w, "Reader unavailable", 500)
			return
		}
		if len(dates) == 0 {
			v.Message = "Your first paper arrives tomorrow morning"
			s.render(w, "message", v)
			return
		}
		date = dates[0]
	}
	parsed, err := edition.ParseDate(date)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ed, layout, err := s.Store.EditionWithLayout(r.Context(), scope, date)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "Reader unavailable", 500)
		return
	}
	coverage, err := s.Store.EditionCoverage(r.Context(), scope, date)
	if err != nil {
		http.Error(w, "Reader unavailable", 500)
		return
	}
	images, err := s.Store.EditionImages(r.Context(), scope, date)
	if err != nil {
		http.Error(w, "Reader unavailable", 500)
		return
	}
	feedback, err := s.Store.ReaderFeedback(r.Context(), current(r).scope, date)
	if err != nil {
		http.Error(w, "Reader unavailable", 500)
		return
	}
	v.Number, err = s.Store.EditionNumber(r.Context(), scope, date)
	if err != nil {
		http.Error(w, "Reader unavailable", 500)
		return
	}
	v.Unreviewed, err = s.Store.EditionUnreviewed(r.Context(), scope, date)
	if err != nil {
		http.Error(w, "Reader unavailable", 500)
		return
	}
	later, err := s.followUps(r.Context(), scope, v.Paper, date, ed)
	if err != nil {
		http.Error(w, "Reader unavailable", 500)
		return
	}
	v.Date = date
	v.Dateline = parsed.Format("Monday, January 2, 2006")
	v.Motto = ed.Motto
	sections := editionSections(v.Paper, ed, layout)
	if layout != nil {
		v.Paper.Masthead = layout.Masthead
	}
	// Opinion columns that name a story in this edition are shown beside it.
	kinds := map[string]string{}
	for _, sec := range ed.Sections {
		for _, it := range sec.Items {
			kinds[it.ID] = it.Kind
		}
	}
	attached := map[string][]opinionView{}
	for _, sec := range ed.Sections {
		for _, it := range sec.Items {
			if it.Kind != "opinion" || it.About == nil || kinds[*it.About] == "" || kinds[*it.About] == "opinion" {
				continue
			}
			o := opinionView{Title: it.Title, URL: it.URL, Source: it.Source}
			if it.Stance != nil {
				o.Stance = *it.Stance
			}
			if coverage != nil {
				if rating, ok := coverage.Ratings.Lookup(it.Source, it.URL); ok {
					o.Lean = leanLabels[rating.Lean]
				}
			}
			attached[*it.About] = append(attached[*it.About], o)
		}
	}
	for _, cfg := range sections {
		section := sectionView{Key: cfg.Key, Title: cfg.Title, Page: len(v.Sections) + 2}
		var opinions []itemView
		for _, sec := range ed.Sections {
			if sec.Key != cfg.Key {
				continue
			}
			for _, it := range sec.Items {
				iv := itemView{Item: it, Image: images[it.ID], Date: date, Up: feedback[it.ID].Vote == "up", Down: feedback[it.ID].Vote == "down", Saved: feedback[it.ID].Saved, Later: later[it.ID]}
				if coverage != nil {
					if bar, ok := coverage.Items[it.ID]; ok {
						iv.Coverage = &bar
						iv.CoverageWindow = coverageWindow(coverage)
						section.Attribution = coverage.Ratings.Source.Attribution
						section.CoverageWindow = coverageWindow(coverage)
					}
				}
				iv.Opinions = attached[it.ID]
				if it.Kind == "opinion" {
					if it.About == nil || kinds[*it.About] == "" || kinds[*it.About] == "opinion" {
						opinions = append(opinions, iv)
					}
				} else {
					section.Items = append(section.Items, iv)
				}
				if it.ID == ed.Lead && it.Kind != "opinion" {
					lead := iv
					lead.Front = true
					v.Lead = &lead
				}
			}
		}
		section.Items = append(section.Items, opinions...)
		if cfg.Key == "economy" && v.Paper.Boxes.Markets && ed.Boxes != nil && ed.Boxes.Markets != nil && ed.Boxes.Markets.Source != "" && ed.Boxes.Markets.AsOf != "" {
			section.Markets = ed.Boxes.Markets
		}
		v.Sections = append(v.Sections, section)
	}
	targets, titles := []string{"#front"}, []string{"Front page"}
	for _, section := range v.Sections {
		targets = append(targets, "#section-"+section.Key)
		titles = append(titles, section.Title)
	}
	for i := range targets {
		nav := pageNavigation{Title: titles[i], Page: i + 1, Total: len(targets)}
		if i > 0 {
			nav.Previous = targets[i-1]
		}
		if i+1 < len(targets) {
			nav.Next = targets[i+1]
		}
		if i == 0 {
			v.FrontNavigation = nav
		} else {
			v.Sections[i-1].Navigation = nav
		}
	}
	if v.Paper.Latitude != nil && v.Paper.Longitude != nil {
		loc, err := time.LoadLocation(v.Paper.Timezone)
		if err != nil {
			http.Error(w, "Reader unavailable", 500)
			return
		}
		if v.Paper.Boxes.Sun || v.Paper.Boxes.Solunar {
			v.Facts = almanac.Compute(parsed, *v.Paper.Latitude, *v.Paper.Longitude, loc)
		}
		if v.Paper.Boxes.Weather && s.Weather != nil {
			v.Weather = s.Weather.Forecast(r.Context(), v.Paper.ID, *v.Paper.Latitude, *v.Paper.Longitude, v.Paper.Timezone, date)
		}
	}
	s.render(w, "edition", v)
}
func (s *Server) archive(w http.ResponseWriter, r *http.Request) {
	page := 1
	if q := r.URL.Query().Get("page"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 1 || n > 1000000 {
			http.NotFound(w, r)
			return
		}
		page = n
	}
	dates, err := s.Store.Dates(r.Context(), current(r).scope.PaperScope(), (page-1)*60, 61)
	if err != nil {
		http.Error(w, "Reader unavailable", 500)
		return
	}
	v := s.base(r)
	v.Dates = dates
	if page > 1 {
		v.Previous = page - 1
	}
	if len(dates) > 60 {
		v.Dates = dates[:60]
		v.Next = page + 1
	}
	s.render(w, "archive", v)
}
func (s *Server) feedback(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if r.ParseForm() != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	if !validCSRF(r) {
		http.Error(w, "Invalid CSRF token", 403)
		return
	}
	kind := r.PostForm.Get("kind")
	if kind != "up" && kind != "down" && kind != "save" && kind != "unsave" {
		http.Error(w, "Invalid feedback", 400)
		return
	}
	date, item := r.PostForm.Get("date"), r.PostForm.Get("item")
	if _, err := edition.ParseDate(date); err != nil {
		http.NotFound(w, r)
		return
	}
	p := current(r)
	ed, err := s.Store.Edition(r.Context(), p.scope.PaperScope(), date)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "Reader unavailable", 500)
		return
	}
	found := false
	for _, sec := range ed.Sections {
		for _, it := range sec.Items {
			if it.ID == item {
				found = true
			}
		}
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	session := p.scope.Session()
	err = s.Store.AddFeedback(r.Context(), p.scope.PaperScope(), store.Feedback{Date: date, ItemID: item, Kind: kind, ReaderIssuer: session.Issuer, ReaderSubject: session.Subject})
	if err != nil {
		http.Error(w, "Feedback unavailable", 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
