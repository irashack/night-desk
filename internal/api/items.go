package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/store"
)

type itemPath struct {
	Date string `json:"date" format:"date"`
	ID   string `json:"id" pattern:"^[a-z0-9][a-z0-9-]{0,63}$"`
}

type publishedItem struct {
	edition.Item
	Date        string `json:"date" format:"date"`
	Section     string `json:"section"`
	PublishedAt string `json:"published_at" format:"date-time"`
}

// Use the schema-v2 item shape, which also contains all v1 item fields.
func publishedItemSchema() map[string]any {
	raw, err := edition.JSONSchemaVersion(2)
	if err != nil {
		panic(err)
	}
	var document map[string]any
	if err = json.Unmarshal(raw, &document); err != nil {
		panic(err)
	}
	sections := document["properties"].(map[string]any)["sections"].(map[string]any)
	section := sections["items"].(map[string]any)
	items := section["properties"].(map[string]any)["items"].(map[string]any)
	schema := items["items"].(map[string]any)
	properties := schema["properties"].(map[string]any)
	for _, name := range []string{"date", "section", "published_at"} {
		properties[name] = map[string]any{"type": "string"}
		schema["required"] = append(schema["required"].([]any), name)
	}
	properties["date"].(map[string]any)["format"] = "date"
	properties["published_at"].(map[string]any)["format"] = "date-time"
	return schema
}

func (s *Server) readItem(w http.ResponseWriter, r *http.Request) {
	date := r.PathValue("date")
	if _, err := edition.ParseDate(date); err != nil {
		fail(w, 400, "date must be YYYY-MM-DD")
		return
	}
	body, section, at, err := s.Store.PublishedItem(r.Context(), current(r).scope, date, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 404, "no such item")
		return
	}
	if err != nil {
		s.storageFailed(w)
		return
	}
	var document map[string]json.RawMessage
	if json.Unmarshal(body, &document) != nil || document == nil {
		s.storageFailed(w)
		return
	}
	document["date"], _ = json.Marshal(date)
	document["section"], _ = json.Marshal(section)
	document["published_at"], _ = json.Marshal(at)
	jsonResponse(w, 200, document)
}

func itemIdentityError(w http.ResponseWriter, err error, warnings []problem) bool {
	var identity *store.ItemIdentityError
	if !errors.As(err, &identity) {
		return false
	}
	jsonResponse(w, 422, validationResponse{Errors: []problem{{identity.Path, store.ItemIdentityRule}}, Warnings: warnings})
	return true
}
