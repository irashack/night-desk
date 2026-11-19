package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/irashack/night-desk/internal/edition"
)

// QualityEdition is the publish date and timestamp of one edition in a window.
// It carries no edition content.
type QualityEdition struct {
	Date        string
	PublishedAt string
}

// FeedbackWeek is one Monday-to-Sunday week of feedback counts. It deliberately
// contains no reader identity, item, or feedback row.
type FeedbackWeek struct {
	Week  string `json:"week"`
	Up    int    `json:"up"`
	Down  int    `json:"down"`
	Saves int    `json:"saves"`
}

// QualityData is the per-paper operator quality window. It has counts and dates,
// never item titles, reader identities or feedback rows.
type QualityData struct {
	DraftsSentBack               int
	WarningsByRule               map[string]int
	FailedSourceChecksByRule     map[string]int
	UnverifiedSourceChecksByRule map[string]int
	DroppedByRule                map[string]int
	FetchHealth                  []HostHealth
	DraftRuns                    int
	DraftTokens                  int
	DraftCostMicros              int64
	Start                        string
	End                          string
	Published                    int
	Editions                     []QualityEdition
	Held                         int
	Unreviewed                   int
	ReviewSamples                int
	ReviewMedianMinutes          float64
	EmptyRequiredSections        int
	Feedback                     []FeedbackWeek
}

// Quality reads one paper's operator quality counts for the inclusive local-date
// window. It performs only read-only SQL and never returns reader content.
func (s *Store) Quality(ctx context.Context, paper, start, end string, requiredSections []string) (QualityData, error) {
	data := QualityData{Start: start, End: end, Feedback: []FeedbackWeek{}}
	if paper == "" {
		return data, errors.New("paper is required")
	}
	startDate, err := edition.ParseDate(start)
	if err != nil {
		return data, err
	}
	endDate, err := edition.ParseDate(end)
	if err != nil {
		return data, err
	}
	if endDate.Before(startDate) {
		return data, errors.New("window end must not precede start")
	}
	required := map[string]bool{}
	for _, key := range requiredSections {
		if key != "" {
			required[key] = true
		}
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return data, err
	}
	defer tx.Rollback()

	data.Editions, data.Unreviewed, data.EmptyRequiredSections, err = qualityEditions(ctx, tx, paper, start, end, required)
	if err != nil {
		return data, err
	}
	data.Published = len(data.Editions)
	if data.Held, err = qualityHeld(ctx, tx, paper, start, end); err != nil {
		return data, err
	}
	if data.ReviewSamples, data.ReviewMedianMinutes, err = qualityTurnaround(ctx, tx, paper, start, end); err != nil {
		return data, err
	}
	if data.Feedback, err = qualityFeedback(ctx, tx, paper, start, end); err != nil {
		return data, err
	}
	if err = qualityDiagnostics(ctx, tx, paper, start, end, &data); err != nil {
		return data, err
	}
	if err = qualityGatherDrops(ctx, tx, paper, start, end, &data); err != nil {
		return data, err
	}
	if err = qualityFetchHealth(ctx, tx, paper, start, end, &data); err != nil {
		return data, err
	}
	// Tokens are as charged to the run budget: a lost worker report keeps its
	// whole reservation. Cost is the worker's estimate from configured prices.
	if err = tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(draft_tokens),0),coalesce(sum(draft_cost_micros),0) FROM runs WHERE paper_id=? AND date>=? AND date<=? AND draft_attempts>0`, paper, start, end).Scan(&data.DraftRuns, &data.DraftTokens, &data.DraftCostMicros); err != nil {
		return data, err
	}
	return data, tx.Commit()
}

func qualityEditions(ctx context.Context, tx *sql.Tx, paper, start, end string, required map[string]bool) ([]QualityEdition, int, int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT e.date,e.published_at,COALESCE(r.unreviewed,0),e.body
 FROM editions e LEFT JOIN edition_review r ON r.paper_id=e.paper_id AND r.date=e.date
 WHERE e.paper_id=? AND e.date>=? AND e.date<=?
 ORDER BY e.date`, paper, start, end)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	editions := []QualityEdition{}
	unreviewed, emptyRequired := 0, 0
	for rows.Next() {
		var date, publishedAt, body string
		var isUnreviewed int
		if err = rows.Scan(&date, &publishedAt, &isUnreviewed, &body); err != nil {
			return nil, 0, 0, err
		}
		editions = append(editions, QualityEdition{Date: date, PublishedAt: publishedAt})
		if isUnreviewed != 0 {
			unreviewed++
		}
		if len(required) == 0 {
			continue
		}
		// Only section keys and item presence matter here; decoding the whole
		// edition would make this report follow today's validation rules.
		var stored struct {
			Sections []struct {
				Key   string            `json:"key"`
				Items []json.RawMessage `json:"items"`
			} `json:"sections"`
		}
		if json.Unmarshal([]byte(body), &stored) != nil {
			continue
		}
		present := map[string]bool{}
		for _, section := range stored.Sections {
			if len(section.Items) > 0 {
				present[section.Key] = true
			}
		}
		for key := range required {
			if !present[key] {
				emptyRequired++
			}
		}
	}
	return editions, unreviewed, emptyRequired, rows.Err()
}

func qualityHeld(ctx context.Context, tx *sql.Tx, paper, start, end string) (int, error) {
	var held int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM (
 SELECT date FROM drafts WHERE paper_id=? AND state='held' AND date>=? AND date<=?
 UNION
 SELECT date FROM runs WHERE paper_id=? AND state='held' AND date>=? AND date<=?
)`, paper, start, end, paper, start, end).Scan(&held)
	return held, err
}

func qualityTurnaround(ctx context.Context, tx *sql.Tx, paper, start, end string) (int, float64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT date,action,at FROM draft_actions
 WHERE paper_id=? AND date>=? AND date<=? AND action IN ('submit','approve')
 ORDER BY date,revision,at`, paper, start, end)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	submitted := map[string]time.Time{}
	samples := []float64{}
	for rows.Next() {
		var date, action, at string
		if err = rows.Scan(&date, &action, &at); err != nil {
			return 0, 0, err
		}
		when, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			continue
		}
		switch action {
		case "submit":
			submitted[date] = when
		case "approve":
			started, ok := submitted[date]
			if !ok || when.Before(started) {
				delete(submitted, date)
				continue
			}
			samples = append(samples, when.Sub(started).Minutes())
			delete(submitted, date)
		}
	}
	if err = rows.Err(); err != nil {
		return 0, 0, err
	}
	if len(samples) == 0 {
		return 0, 0, nil
	}
	sort.Float64s(samples)
	middle := len(samples) / 2
	median := samples[middle]
	if len(samples)%2 == 0 {
		median = (samples[middle-1] + samples[middle]) / 2
	}
	return len(samples), median, nil
}

func qualityFeedback(ctx context.Context, tx *sql.Tx, paper, start, end string) ([]FeedbackWeek, error) {
	rows, err := tx.QueryContext(ctx, `SELECT date,kind,count(*) FROM feedback
 WHERE paper_id=? AND date>=? AND date<=? AND kind IN ('up','down','save')
 GROUP BY date,kind ORDER BY date`, paper, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	weeks := map[string]*FeedbackWeek{}
	for rows.Next() {
		var date, kind string
		var count int
		if err = rows.Scan(&date, &kind, &count); err != nil {
			return nil, err
		}
		when, err := time.Parse("2006-01-02", date)
		if err != nil {
			continue
		}
		monday := when.AddDate(0, 0, -((int(when.Weekday()) + 6) % 7)).Format("2006-01-02")
		week := weeks[monday]
		if week == nil {
			week = &FeedbackWeek{Week: monday}
			weeks[monday] = week
		}
		switch kind {
		case "up":
			week.Up += count
		case "down":
			week.Down += count
		case "save":
			week.Saves += count
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	order := make([]string, 0, len(weeks))
	for monday := range weeks {
		order = append(order, monday)
	}
	sort.Strings(order)
	out := make([]FeedbackWeek, 0, len(order))
	for _, monday := range order {
		out = append(out, *weeks[monday])
	}
	return out, nil
}
