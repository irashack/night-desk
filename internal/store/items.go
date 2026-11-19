package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/edition"
)

// ItemIdentityError rejects reassignment of a published ID, including IDs removed
// by an intervening replacement: reader feedback may still refer to them.
type ItemIdentityError struct {
	Path string
}

const ItemIdentityRule = "already names a different story in this edition; use a new id"

func (e *ItemIdentityError) Error() string { return e.Path + ": " + ItemIdentityRule }

func itemIdentityURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	// Trim literal slashes without treating an escaped slash as a path separator.
	u.RawPath = strings.TrimRight(u.EscapedPath(), "/")
	u.Path, _ = url.PathUnescape(u.RawPath)
	// Preserve unparseable queries rather than silently dropping story parameters.
	if q, err := url.ParseQuery(u.RawQuery); err == nil {
		for key := range q {
			if strings.HasPrefix(strings.ToLower(key), "utm_") {
				q.Del(key)
			}
		}
		u.RawQuery = q.Encode()
	}
	u.ForceQuery = false
	u.Fragment, u.RawFragment = "", ""
	return u.String()
}

func checkItemIdentities(ctx context.Context, tx *sql.Tx, paper, date string, ed *edition.Edition) error {
	for i, section := range ed.Sections {
		for j, item := range section.Items {
			var oldURL string
			err := tx.QueryRowContext(ctx, "SELECT url FROM edition_items WHERE paper_id=? AND date=? AND item_id=?", paper, date, item.ID).Scan(&oldURL)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			if itemIdentityURL(oldURL) != itemIdentityURL(item.URL) {
				return &ItemIdentityError{Path: fmt.Sprintf("sections[%d].items[%d].id", i, j)}
			}
		}
	}
	return nil
}

func recordItemIdentities(ctx context.Context, tx *sql.Tx, paper, date string, ed *edition.Edition) error {
	for _, section := range ed.Sections {
		for _, item := range section.Items {
			// Keep the original URL as the identity; only the section may change.
			if _, err := tx.ExecContext(ctx, `INSERT INTO edition_items(paper_id,date,item_id,url,section) VALUES (?,?,?,?,?)
			 ON CONFLICT(paper_id,date,item_id) DO UPDATE SET section=excluded.section`, paper, date, item.ID, item.URL, section.Key); err != nil {
				return err
			}
		}
	}
	return nil
}

// PublishedItem returns the current published item JSON, section key and edition
// publication timestamp. Reading the stored JSON preserves all published fields.
// Retired identity records do not make removed items visible to this lookup.
func (s *Store) PublishedItem(ctx context.Context, scope auth.PaperScope, date, id string) (json.RawMessage, string, string, error) {
	paper, err := scopeID(scope)
	if err != nil {
		return nil, "", "", err
	}
	if _, err = edition.ParseDate(date); err != nil {
		return nil, "", "", err
	}
	var body string
	var section, at string
	err = s.db.QueryRowContext(ctx, `SELECT i.value,json_extract(s.value,'$.key'),e.published_at
	 FROM editions AS e, json_each(e.body,'$.sections') AS s, json_each(s.value,'$.items') AS i
	 WHERE e.paper_id=? AND e.date=? AND json_extract(i.value,'$.id')=?`, paper, date, id).Scan(&body, &section, &at)
	return json.RawMessage(body), section, at, err
}
