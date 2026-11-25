package demo

import (
	"context"
	"embed"
	"strings"
	"time"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/candidate"
	"github.com/irashack/night-desk/internal/keys"
	"github.com/irashack/night-desk/internal/store"
)

//go:embed snapshots/*.txt
var snapshots embed.FS

// storeSnapshots stands in for the gathering worker's stored page text, so the
// reading view has a full story (Northgate Bridge) and a paywalled teaser (the
// comet) to show. Both are invented, like the rest of the demo paper.
func storeSnapshots(ctx context.Context, db *store.Store) error {
	_, key, err := keys.Issue(ctx, db, "ledger", "demo-gather")
	if err != nil {
		return err
	}
	scope, err := auth.Authenticate(ctx, db, key)
	if err != nil {
		return err
	}
	fetched := time.Date(2026, 11, 19, 9, 40, 0, 0, time.UTC)
	var batch []candidate.Candidate
	for _, s := range []struct{ file, section, outlet, url, title string }{
		{"northgate-bridge-reopens.txt", "local", "Riverside Courier", "https://riverside-courier.example/2026/11/19/northgate-bridge-reopens", "Northgate Bridge reopens to walkers and cyclists"},
		{"comet-brightening-faster.txt", "science", "Lattice Science", "https://lattice-science.example/2026/11/19/comet-brightening-faster", "A comet is brightening faster than forecast"},
	} {
		text, err := snapshots.ReadFile("snapshots/" + s.file)
		if err != nil {
			return err
		}
		batch = append(batch, candidate.Candidate{Section: s.section, CanonicalURL: s.url, Outlet: s.outlet, Title: s.title, FetchedAt: fetched, Text: strings.TrimSpace(string(text)), State: "ok"})
	}
	return db.UpsertCandidates(ctx, scope, batch)
}
