package reader

import (
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/irashack/night-desk/internal/edition"
)

// storyView is the reading view: one story set in the paper's type from the
// gathering worker's stored snapshot, or only the link when there is none.
type storyView struct {
	Item       edition.Item
	Date       string
	Back       string
	Paragraphs []string
	Gathered   string
	// Partial says the snapshot looks shorter than the piece: a paywall or a
	// page that needs script usually leaves only the opening.
	Partial bool
}

var (
	blankLines = regexp.MustCompile(`\n[ \t\r]*\n`)
	words      = regexp.MustCompile(`\S+`)
)

// paragraphs splits extracted text on blank lines, or on single line breaks
// when the extractor left no blank lines at all.
func paragraphs(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	parts := blankLines.Split(text, -1)
	if len(parts) == 1 {
		parts = strings.Split(text, "\n")
	}
	out := []string{}
	for _, p := range parts {
		if p = strings.Join(strings.Fields(p), " "); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// partial guesses that a snapshot holds only part of a piece: very short text,
// or under half of what the edition's reading time promises.
func partial(text string, minutes *int) bool {
	n := len(words.FindAllStringIndex(text, -1))
	if n < 250 {
		return true
	}
	return minutes != nil && n < *minutes*230/2
}

func (s *Server) story(w http.ResponseWriter, r *http.Request) {
	date, id := r.PathValue("date"), r.PathValue("item")
	parsed, err := edition.ParseDate(date)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p := current(r)
	scope := p.scope.PaperScope()
	ed, layout, err := s.Store.EditionWithLayout(r.Context(), scope, date)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "Reader unavailable", 500)
		return
	}
	var item *edition.Item
	for _, sec := range ed.Sections {
		for i := range sec.Items {
			if sec.Items[i].ID == id {
				item = &sec.Items[i]
			}
		}
	}
	if item == nil {
		http.NotFound(w, r)
		return
	}
	snap, ok, err := s.Store.ReadableSnapshot(r.Context(), scope, item.URL)
	if err != nil {
		http.Error(w, "Reader unavailable", 500)
		return
	}
	v := s.base(r)
	if layout != nil {
		v.Paper.Masthead = layout.Masthead
	}
	v.Date = date
	v.Dateline = parsed.Format("Monday, January 2, 2006")
	sv := &storyView{Item: *item, Date: date, Back: p.base + "/e/" + date + "#item-" + item.ID}
	if ok {
		sv.Paragraphs = paragraphs(snap.Text)
		sv.Partial = partial(snap.Text, item.Minutes)
		when := snap.FetchedAt
		if loc, err := time.LoadLocation(v.Paper.Timezone); err == nil {
			when = when.In(loc)
		}
		sv.Gathered = when.Format("January 2 at 3:04 pm")
	}
	if len(sv.Paragraphs) == 0 {
		sv.Paragraphs = nil
	}
	v.Story = sv
	// The snapshot is the reader's own copy: never cached by shared caches,
	// and the page is not indexed.
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Robots-Tag", "noindex")
	s.render(w, "story", v)
}
