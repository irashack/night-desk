package lean

import (
	"encoding/csv"
	"fmt"
	"io"
	"strings"
)

// ImportCSV reads a simple ratings CSV and validates it against the same rules
// as Load. The columns are outlet,domain,lean[,factuality]; an optional header
// row with those names is accepted.
//
// Lean labels are matched case-insensitively and tolerate common spellings:
// "Far Left" and "Left" are left, "Lean Left", "Left-Center" and "Center-Left"
// are lean-left, "Center" and "Centrist" are center, and likewise on the right.
// Syntax failures are returned as err; validation failures are Problems with the
// result nil and err nil.
func ImportCSV(r io.Reader, src Source) (*Ratings, []Problem, error) {
	if r == nil {
		return nil, nil, fmt.Errorf("lean: nil CSV reader")
	}
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	records, err := reader.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("lean: parse ratings CSV: %w", err)
	}

	d := &decoder{}
	d.problems = append(d.problems, validateSource(src)...)
	seen := make(map[string]string)
	start := 0
	if len(records) > 0 && csvHeader(records[0]) {
		start = 1
	}
	out := make([]Rating, 0, len(records)-start)
	for i, record := range records[start:] {
		path := fmt.Sprintf("ratings[%d]", i)
		if len(record) != 3 && len(record) != 4 {
			d.add(path, "must have 3 or 4 fields")
			continue
		}
		before := len(d.problems)
		rating := Rating{
			Outlet:  strings.TrimSpace(record[0]),
			Domains: []string{strings.TrimSpace(record[1])},
		}
		if i == 0 {
			rating.Outlet = strings.TrimPrefix(rating.Outlet, "\ufeff")
		}
		if rating.Outlet == "" {
			d.add(path+".outlet", "must be trimmed non-empty text")
		}
		if !validDomain(rating.Domains[0]) {
			d.add(path+".domains[0]", "must be a domain")
		} else {
			key := normalizeDomain(rating.Domains[0])
			if _, exists := seen[key]; exists {
				d.add(path+".domains[0]", "must be unique")
			} else {
				seen[key] = path + ".domains[0]"
			}
		}

		label := strings.TrimSpace(record[2])
		lean, ok := parseLean(label)
		if !ok {
			d.add(path+".lean", leanRule)
		} else {
			rating.Lean = lean
		}

		if len(record) == 4 {
			factuality := strings.TrimSpace(record[3])
			if factuality != "" {
				if value, ok := parseFactuality(factuality); ok {
					rating.Factuality = value
				} else {
					d.add(path+".factuality", factualityRule)
				}
			}
		}

		if len(d.problems) == before {
			out = append(out, rating)
		}
	}

	if len(d.problems) > 0 {
		return nil, d.problems, nil
	}
	return &Ratings{Source: src, Ratings: out}, nil, nil
}

func csvHeader(record []string) bool {
	if len(record) != 3 && len(record) != 4 {
		return false
	}
	names := []string{"outlet", "domain", "lean"}
	for i, name := range names {
		if labelKey(strings.TrimPrefix(record[i], "\ufeff")) != name {
			return false
		}
	}
	if len(record) == 4 {
		name := labelKey(record[3])
		return name == "factuality" || name == "fact"
	}
	return true
}

// labelKey normalizes a human label for matching: case, surrounding
// whitespace, and hyphen/underscore/slash/dot separators do not matter.
func labelKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("_", " ", "-", " ", "/", " ", ".", " ").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

func parseLean(s string) (Lean, bool) {
	switch labelKey(s) {
	case "left", "far left", "farleft", "extreme left", "extremely left",
		"hard left", "strong left", "very left", "left wing", "leftwing",
		"leftist", "liberal", "very liberal", "progressive", "socialist":
		return Left, true
	case "lean left", "leans left", "leaning left", "left lean", "left leaning",
		"lean liberal", "liberal lean", "slight left", "slightly left",
		"somewhat left", "moderate left", "center left", "centre left",
		"left center", "left centre", "left of center", "left of centre",
		"left of middle":
		return LeanLeft, true
	case "center", "centre", "centrist", "centerist", "moderate", "balanced",
		"middle", "middle of the road", "middle of road", "neutral",
		"nonpartisan", "non partisan", "unbiased", "independent":
		return Center, true
	case "lean right", "leans right", "leaning right", "right lean", "right leaning",
		"lean conservative", "conservative lean", "slight right", "slightly right",
		"somewhat right", "moderate right", "center right", "centre right",
		"right center", "right centre", "right of center", "right of centre",
		"right of middle":
		return LeanRight, true
	case "right", "far right", "farright", "extreme right", "extremely right",
		"hard right", "strong right", "very right", "right wing", "rightwing",
		"rightist", "conservative", "very conservative", "reactionary":
		return Right, true
	}
	return "", false
}

func parseFactuality(s string) (Factuality, bool) {
	switch labelKey(s) {
	case "very low", "verylow", "very low factual", "extremely low":
		return VeryLow, true
	case "low", "low factual":
		return Low, true
	case "mixed", "mixed factual", "mixed factuality":
		return Mixed, true
	case "mostly factual", "mostlyfactual", "mostly factual reporting", "mostly true":
		return MostlyFactual, true
	case "high", "high factual", "factual":
		return High, true
	case "very high", "veryhigh", "very high factual", "extremely high":
		return VeryHigh, true
	}
	return "", false
}
