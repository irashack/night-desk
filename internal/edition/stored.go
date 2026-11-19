package edition

import (
	"encoding/json"
	"fmt"
)

// DecodeStored dispatches by the database schema version, independently of the
// incoming decoder and today's validation rules. Already published editions must
// remain readable when incoming shape or semantic rules become stricter.
func DecodeStored(schema int, body []byte) (*Edition, error) {
	switch schema {
	case 1, 2:
		var e Edition
		if err := json.Unmarshal(body, &e); err != nil {
			return nil, err
		}
		if e.Schema != schema {
			return nil, fmt.Errorf("stored edition schema: body %d differs from column %d", e.Schema, schema)
		}
		return &e, nil
	default:
		return nil, fmt.Errorf("unsupported stored edition schema %d", schema)
	}
}
