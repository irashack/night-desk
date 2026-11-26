// Package edition defines and validates Night Desk edition schemas v1 and v2.
package edition

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

//go:generate go run ./schema-gen

type Edition struct {
	Schema   int       `json:"schema" const:"1"`
	Lead     string    `json:"lead"`
	Motto    *Motto    `json:"motto,omitempty"`
	Sections []Section `json:"sections"`
	Boxes    *Boxes    `json:"boxes,omitempty"`
}

type Motto struct {
	Kind   string `json:"kind" enum:"scripture,quotation"`
	Text   string `json:"text" maxLength:"400"`
	Source string `json:"source" maxLength:"120"`
}

type Section struct {
	Key   string `json:"key"`
	Items []Item `json:"items" maxItems:"6"`
}

type Item struct {
	ID       string   `json:"id" pattern:"^[a-z0-9][a-z0-9-]{0,63}$"`
	Kind     string   `json:"kind" enum:"news,seasonal,evergreen,brief" enumV2:"news,seasonal,evergreen,brief,opinion"`
	Title    string   `json:"title" maxLength:"160"`
	Summary  string   `json:"summary" maxLength:"600"`
	Why      *string  `json:"why,omitempty" maxLength:"300"`
	Source   string   `json:"source" maxLength:"80"`
	Byline   *string  `json:"byline,omitempty" maxLength:"120" since:"2"`
	Sources  []Source `json:"sources,omitempty" maxItems:"12" since:"2"`
	URL      string   `json:"url" format:"uri"`
	Minutes  *int     `json:"minutes,omitempty" minimum:"1" maximum:"240"`
	ImageURL *string  `json:"image_url,omitempty" format:"uri"`
	// About names the story, by its item ID in this edition, that an opinion
	// item argues about; Stance is the column's position in one line.
	About  *string `json:"about,omitempty" pattern:"^[a-z0-9][a-z0-9-]{0,63}$" since:"2"`
	Stance *string `json:"stance,omitempty" maxLength:"140" since:"2"`
}

// Source is further coverage of an item, available in schema v2.
type Source struct {
	Outlet string  `json:"outlet" maxLength:"80"`
	URL    string  `json:"url" format:"uri"`
	Byline *string `json:"byline,omitempty" maxLength:"120"`
}

type Boxes struct {
	Markets *Markets `json:"markets,omitempty"`
}

type Markets struct {
	Source string  `json:"source" maxLength:"80"`
	AsOf   string  `json:"as_of" format:"date-time"`
	Quotes []Quote `json:"quotes" maxItems:"12"`
}

type Quote struct {
	Symbol    string `json:"symbol" maxLength:"16"`
	Label     string `json:"label" maxLength:"40"`
	Value     string `json:"value" maxLength:"24"`
	Change    string `json:"change" maxLength:"24"`
	Direction string `json:"direction" enum:"up,down,flat"`
}

// Decode rejects unknown fields, non-schema JSON shapes, and trailing data.
// Semantic rules, including paper-specific rules, are checked by Validate.
func Decode(r io.Reader) (*Edition, error) {
	var raw json.RawMessage
	d := json.NewDecoder(r)
	if err := d.Decode(&raw); err != nil {
		return nil, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("trailing data: expected one JSON value")
		}
		return nil, fmt.Errorf("trailing data: %w", err)
	}
	// The shape check runs first so that every structural error, unknown
	// fields included, names its JSON path.
	var shape any
	if err := json.Unmarshal(raw, &shape); err != nil {
		return nil, err
	}
	version := 2
	// Unknown versions retain their value for Validate's schema error. Only
	// v1 needs to restrict the shape to the original set of fields.
	if obj, ok := shape.(map[string]any); ok && obj["schema"] == float64(1) {
		version = 1
	}
	if err := checkShape(shape, editionType, "", version); err != nil {
		return nil, err
	}
	var e Edition
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&e); err != nil {
		return nil, err
	}
	return &e, nil
}

// ParseDate parses the calendar date used in edition URLs, without normalization.
func ParseDate(s string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, err
	}
	if t.Format("2006-01-02") != s {
		return time.Time{}, fmt.Errorf("date must be YYYY-MM-DD")
	}
	return t, nil
}

// FirstLead is the edition's first item that may lead it (any but an
// opinion), for when the lead was cut or dropped; "" when there is none.
func FirstLead(e *Edition) string {
	for _, section := range e.Sections {
		for _, item := range section.Items {
			if item.Kind != "opinion" {
				return item.ID
			}
		}
	}
	return ""
}
