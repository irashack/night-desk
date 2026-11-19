package reader

import (
	"time"

	"github.com/irashack/night-desk/internal/store"
)

func coverageWindow(coverage *store.EditionCoverage) string {
	format := func(value string) string {
		t, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return value
		}
		return t.UTC().Format("Jan 2, 2006 15:04 UTC")
	}
	if coverage.From != "" {
		return "Named sources fetched " + format(coverage.From) + " through publication " + format(coverage.Through)
	}
	return "As of publication " + format(coverage.Through)
}

func (item itemView) CoverageSides() []string {
	if item.Coverage == nil {
		return nil
	}
	bar := item.Coverage
	sides := make([]string, 0, bar.Left+bar.Center+bar.Right)
	for _, count := range []struct {
		side string
		n    int
	}{{"left", bar.Left}, {"center", bar.Center}, {"right", bar.Right}} {
		for i := 0; i < count.n; i++ {
			sides = append(sides, count.side)
		}
	}
	return sides
}
