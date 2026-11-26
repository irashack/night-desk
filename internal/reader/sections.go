package reader

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/editorial"
)

// sectionRow is one section as the Sections page shows and edits it.
type sectionRow struct {
	Key, Title, Brief string
	First, Last       bool
}

// sectionNotices are the confirmations a redirect may name; nothing the reader
// typed is reflected through the query string.
var sectionNotices = map[string]string{"added": "Section added", "saved": "Section saved", "moved": "Section moved", "removed": "Section removed"}

// staleSections explains a refused edit computed from an older revision.
const staleSections = "The sections changed since you opened this page, so your change was not saved. They are shown below as they stand now; make your change again if you still want it."

func (s *Server) sections(w http.ResponseWriter, r *http.Request) {
	if s.Editorial == nil || !current(r).scope.CanEdit() {
		s.notFound(w, r)
		return
	}
	paper, rev, ok := s.Editorial.Current(current(r).paper.ID)
	if !ok {
		s.notFound(w, r)
		return
	}
	v := s.sectionsView(r, paper.Sections, rev)
	v.Message = sectionNotices[r.URL.Query().Get("done")]
	s.render(w, "sections", v)
}

func (s *Server) sectionsView(r *http.Request, sections []config.Section, revision int64) view {
	v := s.base(r)
	v.Revision = revision
	for i, sec := range sections {
		row := sectionRow{Key: sec.Key, Title: sec.Title, First: i == 0, Last: i == len(sections)-1}
		if sec.Brief != nil {
			row.Brief = *sec.Brief
		}
		v.Rows = append(v.Rows, row)
	}
	return v
}

// editSections applies one reader action to the paper's effective editorial
// fields and saves the result as one revision, exactly as the agent API's paper
// patch does, so both share validation and history.
func (s *Server) editSections(w http.ResponseWriter, r *http.Request) {
	if s.Editorial == nil || !current(r).scope.CanEdit() {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if r.ParseForm() != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	if !validCSRF(r) {
		http.Error(w, "Invalid CSRF token", 403)
		return
	}
	p := current(r)
	form := r.PostForm
	action, key := form.Get("action"), form.Get("key")
	title, brief := strings.TrimSpace(form.Get("title")), strings.TrimSpace(form.Get("brief"))
	// The page posts the revision it showed; a page from before revisions were
	// posted saves against whatever is current, as before.
	expected := editorial.AnyRevision
	if raw := form.Get("revision"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			http.Error(w, "Invalid form", 400)
			return
		}
		expected = n
	}
	// Edits build on the newest revision, never the paper as of authentication,
	// and are saved only if that revision is still current.
	paper, rev, ok := s.Editorial.Current(p.paper.ID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	stale := func() {
		paper, rev, _ := s.Editorial.Current(p.paper.ID)
		v := s.sectionsView(r, paper.Sections, rev)
		v.Problems = []string{staleSections}
		if action == "add" {
			v.AddTitle, v.AddBrief = title, brief
		}
		s.renderStatus(w, http.StatusConflict, "sections", v)
	}
	if expected != editorial.AnyRevision && expected != rev {
		stale()
		return
	}
	ed := paper.Editorial()
	sections := append([]config.Section(nil), ed.Sections...)
	at := -1
	for i, sec := range sections {
		if sec.Key == key {
			at = i
		}
	}
	if action != "add" && at < 0 {
		v := s.sectionsView(r, sections, rev)
		v.Problems = []string{"sections: that section is no longer in the paper; reload the page"}
		s.renderStatus(w, http.StatusConflict, "sections", v)
		return
	}
	var done string
	switch action {
	case "add":
		// New sections carry no sources of their own: gathering finds them.
		sections = append(sections, config.Section{Key: sectionKey(title, sections), Title: title, Brief: optionalText(brief), FindSources: true})
		done = "added"
	case "save":
		sections[at].Title, sections[at].Brief = title, optionalText(brief)
		done = "saved"
	case "up", "down":
		to := at - 1
		if action == "down" {
			to = at + 1
		}
		if to < 0 || to >= len(sections) {
			http.Redirect(w, r, "/sections", http.StatusSeeOther) // sections are only ever the default paper's
			return
		}
		sections[at], sections[to] = sections[to], sections[at]
		done = "moved"
	case "remove":
		if form.Get("confirm") != "1" {
			v := s.sectionsView(r, sections, rev)
			v.Confirm = &v.Rows[at]
			s.render(w, "sections", v)
			return
		}
		sections = append(sections[:at], sections[at+1:]...)
		done = "removed"
	default:
		http.Error(w, "Invalid action", 400)
		return
	}
	ed.Sections = sections
	_, problems, err := s.Editorial.Save(r.Context(), p.scope.PaperScope(), ed, rev)
	if errors.Is(err, editorial.ErrConflict) {
		stale()
		return
	}
	if err != nil {
		http.Error(w, "Reader unavailable", 500)
		return
	}
	if len(problems) > 0 {
		// Show the paper as it stands, with the reader's input kept in place.
		v := s.sectionsView(r, paper.Sections, rev)
		v.Problems = problems
		switch action {
		case "add":
			v.AddTitle, v.AddBrief = title, brief
		case "save":
			v.Rows[at].Title, v.Rows[at].Brief = title, brief
		}
		s.renderStatus(w, http.StatusUnprocessableEntity, "sections", v)
		return
	}
	http.Redirect(w, r, "/sections?done="+done, http.StatusSeeOther)
}

func optionalText(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// sectionKey derives a slug from title that no section in sections uses. Keys
// never change once made: stories and history refer to them.
func sectionKey(title string, sections []config.Section) string {
	var b strings.Builder
	dash := false
	for _, c := range strings.ToLower(title) {
		switch {
		case c >= 'a' && c <= 'z' || c >= '0' && c <= '9':
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(c)
			dash = false
		default:
			dash = true
		}
		if b.Len() >= 48 {
			break
		}
	}
	base := b.String()
	if base == "" {
		base = "section"
	}
	used := map[string]bool{}
	for _, sec := range sections {
		used[sec.Key] = true
	}
	key := base
	for n := 2; used[key]; n++ {
		key = fmt.Sprintf("%s-%d", base, n)
	}
	return key
}
