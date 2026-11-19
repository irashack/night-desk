package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/edition"
)

type EditionSummary struct {
	Date        string `json:"date"`
	PublishedAt string `json:"published_at"`
}

// Editions lists a paper's published dates, newest first. Before is exclusive;
// an empty value starts at the newest edition.
func (s *Store) Editions(ctx context.Context, scope auth.PaperScope, before string, limit int) ([]EditionSummary, error) {
	paper, e := scopeID(scope)
	if e != nil {
		return nil, e
	}
	if before != "" {
		if _, e = edition.ParseDate(before); e != nil {
			return nil, e
		}
	}
	if limit < 1 || limit > 50 {
		return nil, errors.New("limit must be 1..50")
	}
	rows, e := s.db.QueryContext(ctx, `SELECT date,published_at FROM editions WHERE paper_id=? AND (?='' OR date<?) ORDER BY date DESC LIMIT ?`, paper, before, before, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []EditionSummary{}
	for rows.Next() {
		var v EditionSummary
		if e = rows.Scan(&v.Date, &v.PublishedAt); e != nil {
			return nil, e
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

// PublishedEdition returns the stored JSON without decoding it through the
// current edition schema, so archive reads preserve the published document.
func (s *Store) PublishedEdition(ctx context.Context, scope auth.PaperScope, date string) (json.RawMessage, string, error) {
	paper, e := scopeID(scope)
	if e != nil {
		return nil, "", e
	}
	if _, e = edition.ParseDate(date); e != nil {
		return nil, "", e
	}
	var body, publishedAt string
	e = s.db.QueryRowContext(ctx, `SELECT body,published_at FROM editions WHERE paper_id=? AND date=?`, paper, date).Scan(&body, &publishedAt)
	if e != nil {
		return nil, "", e
	}
	return json.RawMessage(body), publishedAt, nil
}
