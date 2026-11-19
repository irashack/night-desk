package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

func (s *Server) leanOutlets(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, 200, map[string]any{"outlets": s.Config().Lean.Ratings.Outlets()})
}

// Preserve the indexed lean path, including null (which encoding/json would
// otherwise silently accept as a false boolean).
func sectionLeanShape(raw json.RawMessage) *problem {
	var sections []map[string]json.RawMessage
	if json.Unmarshal(raw, &sections) != nil {
		return nil // Existing section validation reports the surrounding shape.
	}
	for i, section := range sections {
		if value, ok := section["lean"]; ok {
			value = bytes.TrimSpace(value)
			if string(value) != "true" && string(value) != "false" {
				return &problem{fmt.Sprintf("sections[%d].lean", i), "must be a boolean"}
			}
		}
	}
	return nil
}
