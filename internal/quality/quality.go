// Package quality builds operational aggregates shared by the CLI and operator page.
package quality

import (
	"context"
	"errors"
	"time"

	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/scheduler"
	"github.com/irashack/night-desk/internal/store"
)

type PaperReport struct {
	DraftsSentBack               int            `json:"drafts_sent_back"`
	WarningsByRule               map[string]int `json:"warnings_by_rule"`
	FailedSourceChecksByRule     map[string]int `json:"failed_source_checks_by_rule"`
	UnverifiedSourceChecksByRule map[string]int `json:"unverified_source_checks_by_rule"`
	// DroppedByRule counts gathered items excluded by ingestion filters.
	DroppedByRule map[string]int `json:"dropped_by_rule"`
	// Drafting spend: runs with a model drafting attempt, tokens charged to their
	// budgets and the estimated cost in US dollars from configured prices.
	DraftRuns    int     `json:"draft_runs"`
	DraftTokens  int     `json:"draft_tokens"`
	DraftCostUSD float64 `json:"draft_cost_usd"`
	// FetchHealth lists the hosts with the most failed article fetches.
	FetchHealth                   []store.HostHealth   `json:"fetch_health"`
	DeadlineMisses                int                  `json:"deadline_misses"`
	PaperID                       string               `json:"paper_id"`
	Days                          int                  `json:"days"`
	Start                         string               `json:"start"`
	End                           string               `json:"end"`
	Published                     int                  `json:"published"`
	DeadlinesMet                  int                  `json:"deadlines_met"`
	DeadlinesChecked              int                  `json:"deadlines_checked"`
	Held                          int                  `json:"held"`
	Unreviewed                    int                  `json:"unreviewed"`
	ReviewTurnaroundMedianMinutes float64              `json:"review_turnaround_median_minutes"`
	ReviewTurnaroundSamples       int                  `json:"review_turnaround_samples"`
	EmptyRequiredSections         int                  `json:"empty_required_sections"`
	FeedbackTrend                 []store.FeedbackWeek `json:"feedback_trend"`
}

type Report struct {
	Papers []PaperReport `json:"papers"`
}

// Build uses each paper's local calendar, including DST deadline rules.
// Published counts distinct edition dates; diagnostics count retained publish
// attempts, including replacements and rejections. FeedbackTrend is retained
// for the administrative CLI only; the operator page never renders it.
func Build(ctx context.Context, db *store.Store, papers []config.Paper, days int, now time.Time) (Report, error) {
	report := Report{Papers: make([]PaperReport, 0, len(papers))}
	if days < 1 || days > 3660 {
		return report, errors.New("days must be between 1 and 3660")
	}
	for _, p := range papers {
		loc, err := time.LoadLocation(p.Timezone)
		if err != nil {
			return report, err
		}
		end := now.In(loc).Format("2006-01-02")
		endDate, err := edition.ParseDate(end)
		if err != nil {
			return report, err
		}
		start := endDate.AddDate(0, 0, -(days - 1)).Format("2006-01-02")
		required := []string{}
		for _, section := range p.Sections {
			if section.Required {
				required = append(required, section.Key)
			}
		}
		data, err := db.Quality(ctx, p.ID, start, end, required)
		if err != nil {
			return report, err
		}
		met, checked, err := DeadlinesMet(p, loc, data.Editions)
		if err != nil {
			return report, err
		}
		misses, err := deadlineMisses(p, loc, start, end, now, data.Editions)
		if err != nil {
			return report, err
		}
		report.Papers = append(report.Papers, PaperReport{
			PaperID: p.ID, Days: days, Start: start, End: end, Published: data.Published,
			DeadlinesMet: met, DeadlinesChecked: checked, DeadlineMisses: misses, Held: data.Held, Unreviewed: data.Unreviewed,
			DraftsSentBack: data.DraftsSentBack, WarningsByRule: data.WarningsByRule,
			FailedSourceChecksByRule: data.FailedSourceChecksByRule, UnverifiedSourceChecksByRule: data.UnverifiedSourceChecksByRule, DroppedByRule: data.DroppedByRule, FetchHealth: data.FetchHealth,
			DraftRuns: data.DraftRuns, DraftTokens: data.DraftTokens, DraftCostUSD: float64(data.DraftCostMicros) / 1e6,
			ReviewTurnaroundMedianMinutes: data.ReviewMedianMinutes, ReviewTurnaroundSamples: data.ReviewSamples,
			EmptyRequiredSections: data.EmptyRequiredSections, FeedbackTrend: data.Feedback,
		})
	}
	return report, nil
}

func deadlineMisses(p config.Paper, loc *time.Location, start, end string, now time.Time, editions []store.QualityEdition) (int, error) {
	if p.Deadline == nil {
		return 0, nil
	}
	published := map[string]time.Time{}
	for _, ed := range editions {
		at, err := time.Parse(time.RFC3339Nano, ed.PublishedAt)
		if err != nil {
			return 0, err
		}
		published[ed.Date] = at
	}
	day, err := edition.ParseDate(start)
	if err != nil {
		return 0, err
	}
	misses := 0
	for ; day.Format("2006-01-02") <= end; day = day.AddDate(0, 0, 1) {
		reference := time.Date(day.Year(), day.Month(), day.Day(), 12, 0, 0, 0, loc)
		due, err := scheduler.Deadline(p, reference)
		if err != nil {
			return 0, err
		}
		if now.Before(due) {
			continue
		}
		at, ok := published[day.Format("2006-01-02")]
		if !ok || at.After(due) {
			misses++
		}
	}
	return misses, nil
}

func DeadlinesMet(p config.Paper, loc *time.Location, editions []store.QualityEdition) (int, int, error) {
	if p.Deadline == nil {
		return 0, 0, nil
	}
	met, checked := 0, 0
	for _, e := range editions {
		day, err := edition.ParseDate(e.Date)
		if err != nil {
			return 0, 0, err
		}
		// Noon anchors the paper's local calendar date without choosing a DST
		// fold; scheduler.Deadline then resolves the deadline on that date.
		reference := time.Date(day.Year(), day.Month(), day.Day(), 12, 0, 0, 0, loc)
		due, err := scheduler.Deadline(p, reference)
		if err != nil {
			return 0, 0, err
		}
		published, err := time.Parse(time.RFC3339Nano, e.PublishedAt)
		if err != nil {
			return 0, 0, err
		}
		checked++
		if !published.After(due) {
			met++
		}
	}
	return met, checked, nil
}
