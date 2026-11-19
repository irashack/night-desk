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
type SearchResult struct {
	Date, ItemID, Title, Summary, Why, Source string
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
	paper, err := scopeID(scope.PaperScope())
	if err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(query) > 500 {
		return nil, errors.New("q: must be at most 500 characters")
	}
	match := searchTerms(query)
	results := []SearchResult{}
	if match == "" && (!savedOnly || strings.TrimSpace(query) != "") {
		return results, nil
	}
	statement := `SELECT date,item_id,title,summary,why,source FROM archive_items WHERE paper_id=?`
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
		if err = rows.Scan(&result.Date, &result.ItemID, &result.Title, &result.Summary, &result.Why, &result.Source); err != nil {
			return nil, err
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
			why := ""
			if item.Why != nil {
				why = *item.Why
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO archive_items(paper_id,date,item_id,title,summary,why,source) VALUES (?,?,?,?,?,?,?)`, paper, date, item.ID, item.Title, item.Summary, why, item.Source); err != nil {
				return err
			}
		}
	}
	return nil
}
