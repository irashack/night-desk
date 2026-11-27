package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/edition"
)

// SearchResult identifies an item in a published edition, never a live source URL.
// Kind, Byline and Stance come from the item; Outlets are the other outlets that
// covered the story (edition schema 2), in edition order.
type SearchResult struct {
	Date, ItemID, Kind, Title, Summary, Why, Source, Byline, Stance string
	Outlets                                                         []string
}

// SearchIn narrows a query to some of an item's fields. The zero value searches
// every indexed field.
type SearchIn string

const (
	SearchAnywhere SearchIn = ""
	// SearchWriter matches an item's byline and the bylines of its further coverage.
	SearchWriter SearchIn = "writer"
	// SearchStance matches an opinion column's stance.
	SearchStance SearchIn = "stance"
	// SearchOutlets matches the other outlets that covered the story.
	SearchOutlets SearchIn = "outlets"
)

// searchColumns are FTS5 column filters chosen by the server, never by input.
var searchColumns = map[SearchIn]string{
	SearchWriter:  "{byline outlet_bylines}",
	SearchStance:  "{stance}",
	SearchOutlets: "{outlets}",
}

// ParseSearchIn accepts the reader's "in" parameter.
func ParseSearchIn(value string) (SearchIn, bool) {
	in := SearchIn(value)
	if _, ok := searchColumns[in]; ok || in == SearchAnywhere {
		return in, true
	}
	return SearchAnywhere, false
}

// searchTerms treats punctuation as separators and quotes every word, including
// FTS operators. User input can never introduce MATCH syntax or column filters.
func searchTerms(query string) string {
	words := strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.IsMark(r)
	})
	for i, word := range words {
		words[i] = `"` + word + `"`
	}
	return strings.Join(words, " AND ")
}

// Search searches the session's paper. Saved-only matches use the last save or
// unsave event for this exact issuer/subject, independently of subsequent votes.
// An empty query lists saved items when savedOnly is set, otherwise no results.
// Results are newest first and bounded to 100; callers should suggest refining.
func (s *Store) Search(ctx context.Context, scope auth.ReaderScope, query string, savedOnly bool) ([]SearchResult, error) {
	return s.SearchFields(ctx, scope, query, SearchAnywhere, savedOnly)
}

// SearchFields is Search narrowed to the fields that in names.
func (s *Store) SearchFields(ctx context.Context, scope auth.ReaderScope, query string, in SearchIn, savedOnly bool) ([]SearchResult, error) {
	paper, err := scopeID(scope.PaperScope())
	if err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(query) > 500 {
		return nil, errors.New("q: must be at most 500 characters")
	}
	if _, ok := searchColumns[in]; !ok && in != SearchAnywhere {
		return nil, errors.New("in: unknown search field")
	}
	match := searchTerms(query)
	if match != "" && in != SearchAnywhere {
		match = searchColumns[in] + " : (" + match + ")"
	}
	results := []SearchResult{}
	if match == "" && (!savedOnly || strings.TrimSpace(query) != "") {
		return results, nil
	}
	statement := `SELECT date,item_id,kind,title,summary,why,source,byline,stance,outlets FROM archive_items WHERE paper_id=?`
	args := []any{paper}
	if match != "" {
		statement += ` AND archive_items MATCH ?`
		args = append(args, match)
	}
	if savedOnly {
		session := scope.Session()
		statement += ` AND (SELECT kind FROM feedback AS f
 WHERE f.paper_id=archive_items.paper_id AND f.date=archive_items.date
 AND f.item_id=archive_items.item_id AND f.reader_issuer=? AND f.reader_subject=?
 AND f.kind IN ('save','unsave') ORDER BY f.id DESC LIMIT 1)='save'`
		args = append(args, session.Issuer, session.Subject)
	}
	statement += ` ORDER BY date DESC,item_id LIMIT 100`
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var result SearchResult
		var outlets string
		if err = rows.Scan(&result.Date, &result.ItemID, &result.Kind, &result.Title, &result.Summary, &result.Why, &result.Source, &result.Byline, &result.Stance, &outlets); err != nil {
			return nil, err
		}
		if outlets != "" {
			result.Outlets = strings.Split(outlets, "\n")
		}
		results = append(results, result)
	}
	return results, rows.Err()
}

func indexEdition(ctx context.Context, tx *sql.Tx, paper, date string, ed *edition.Edition) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM archive_items WHERE paper_id=? AND date=?`, paper, date); err != nil {
		return err
	}
	for _, section := range ed.Sections {
		for _, item := range section.Items {
			var outlets, outletBylines []string
			for _, source := range item.Sources {
				outlets = append(outlets, source.Outlet)
				if source.Byline != nil {
					outletBylines = append(outletBylines, *source.Byline)
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO archive_items(paper_id,date,item_id,kind,title,summary,why,source,byline,stance,outlets,outlet_bylines) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
				paper, date, item.ID, item.Kind, item.Title, item.Summary, deref(item.Why), item.Source, deref(item.Byline), deref(item.Stance),
				strings.Join(outlets, "\n"), strings.Join(outletBylines, "\n")); err != nil {
				return err
			}
		}
	}
	return nil
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
