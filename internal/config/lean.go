package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/irashack/night-desk/internal/lean"
)

// Lean is install policy. Ratings is loaded once per configuration snapshot;
// neither the file path nor the file is exposed in the paper API.
type Lean struct {
	RatingsFile string        `json:"ratings_file,omitempty"`
	Ratings     *lean.Ratings `json:"-"`
}

func (c *Config) loadRatings(base string) error {
	if c.Lean.RatingsFile == "" {
		return nil
	}
	if !filepath.IsAbs(c.Lean.RatingsFile) {
		c.Lean.RatingsFile = filepath.Join(base, c.Lean.RatingsFile)
	}
	f, err := os.Open(c.Lean.RatingsFile)
	if err != nil {
		return fmt.Errorf("lean.ratings_file: must name a readable ratings file")
	}
	defer f.Close()
	ratings, problems, err := lean.Load(f)
	if err != nil {
		return fmt.Errorf("lean.ratings_file: must contain valid YAML or JSON ratings")
	}
	if len(problems) > 0 {
		out := make([]string, 0, len(problems))
		for _, p := range problems {
			path := "lean.ratings_file"
			if p.Path != "$" {
				path += "." + p.Path
			}
			out = append(out, path+": "+p.Rule)
		}
		return fmt.Errorf("%s", strings.Join(out, "; "))
	}
	c.Lean.Ratings = ratings
	return nil
}
