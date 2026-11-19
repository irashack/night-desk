package quality

import (
	"context"
	"testing"
	"time"

	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/store"
)

func TestDeadlineMissesIncludeUnpublishedDays(t *testing.T) {
	deadline := "06:30"
	p := config.Paper{ID: "daily", Timezone: "America/Chicago", Deadline: &deadline}
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		t.Fatal(err)
	}
	editions := []store.QualityEdition{
		{Date: "2026-03-07", PublishedAt: "2026-03-07T12:29:00Z"}, // before spring DST
		{Date: "2026-03-08", PublishedAt: "2026-03-08T11:31:00Z"}, // late, after spring DST
	}
	for _, tt := range []struct {
		now  string
		want int
	}{
		{"2026-03-10T11:00:00Z", 2}, // today is not yet due; March 9 is missing
		{"2026-03-10T11:30:00Z", 3}, // today now missing as well
	} {
		now, err := time.Parse(time.RFC3339, tt.now)
		if err != nil {
			t.Fatal(err)
		}
		got, err := deadlineMisses(p, loc, "2026-03-07", "2026-03-10", now, editions)
		if err != nil || got != tt.want {
			t.Fatal(got, tt.want, err)
		}
	}
	if got, err := deadlineMisses(config.Paper{}, loc, "2026-03-07", "2026-03-10", time.Now(), editions); err != nil || got != 0 {
		t.Fatal(got, err)
	}
}

func TestBuildLocalWindowAndValidation(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	papers := []config.Paper{{ID: "daily", Timezone: "America/Chicago"}, {ID: "other", Timezone: "Asia/Tokyo"}}
	now := time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC)
	report, err := Build(context.Background(), db, papers, 2, now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Papers[0].Start != "2026-10-28" || report.Papers[0].End != "2026-10-29" || report.Papers[1].End != "2026-10-30" {
		t.Fatal(report)
	}
	for _, days := range []int{0, -1, 3661} {
		if _, err = Build(context.Background(), db, papers, days, now); err == nil {
			t.Fatal("invalid days accepted", days)
		}
	}
}
