// Package scheduler applies local-date deadline policy. It never calls models or
// gathers web content in the server. Workers poll the paper and draft endpoints.
package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/store"
)

type PolicyApplier interface {
	ApplyDeadline(context.Context, config.Paper, string) error
}
type Scheduler struct {
	Store     *store.Store
	Config    func() *config.Config
	Publisher PolicyApplier
	Now       func() time.Time
	Log       *slog.Logger
}

// Deadline chooses the first occurrence of an ambiguous local minute. On a spring
// gap it chooses the first real local minute at or after the requested wall time.
// Scanning UTC minutes avoids time.Date's unspecified DST gap/fold choice.
func Deadline(p config.Paper, now time.Time) (time.Time, error) {
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	if p.Deadline == nil || len(*p.Deadline) != 5 || (*p.Deadline)[2] != ':' {
		return time.Time{}, fmt.Errorf("deadline must be HH:MM")
	}
	local := now.In(loc)
	h, err := strconv.Atoi((*p.Deadline)[:2])
	if err != nil {
		return time.Time{}, err
	}
	m, err := strconv.Atoi((*p.Deadline)[3:])
	if err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return time.Time{}, fmt.Errorf("deadline must be HH:MM")
	}
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	for t := midnight; t.Before(midnight.Add(27 * time.Hour)); t = t.Add(time.Minute) {
		v := t.In(loc)
		if v.Year() != local.Year() || v.YearDay() != local.YearDay() {
			continue
		}
		if v.Hour()*60+v.Minute() >= h*60+m {
			return t, nil
		}
	}
	return midnight, nil
}

// Run catches up today's policy immediately, then checks once per minute. Only
// today's local date is considered; old missed editions are never backfilled.
// A run missed at its deadline is published (or held) by the same policy once a
// worker's late draft for today has a body.
func (s *Scheduler) Run(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Tick is exported for deterministic tests with an injected Now function.
func (s *Scheduler) Tick(ctx context.Context) {
	now := s.now()
	for _, p := range s.Config().Papers {
		if ctx.Err() != nil {
			return
		}
		if p.Deadline == nil {
			continue
		}
		deadline, err := Deadline(p, now)
		if err != nil {
			s.failed(p.ID)
			continue
		}
		loc, _ := time.LoadLocation(p.Timezone)
		date := now.In(loc).Format("2006-01-02")
		if err = s.paper(ctx, p, date, !now.Before(deadline)); err != nil {
			s.failed(p.ID)
		}
	}
}
func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
func (s *Scheduler) failed(paper string) {
	if s.Log != nil {
		s.Log.Error("paper scheduler operation failed", "paper", paper)
	}
}
func (s *Scheduler) paper(ctx context.Context, p config.Paper, date string, due bool) error {
	run, err := s.Store.EnsureRun(ctx, p.ID, date)
	if err != nil {
		return err
	}
	if run.State == "published" || run.State == "held" || run.State == "failed" {
		return nil
	}
	// A worker is drafting; apply the policy to its result, not to its absence.
	if live, err := s.Store.LiveDraftLease(ctx, p.ID, date, s.now()); err != nil || live {
		return err
	}
	if run.State == "missed" {
		d, err := s.Store.RunDraft(ctx, p.ID, date)
		if errors.Is(err, sql.ErrNoRows) || err == nil && (d.Body == nil || d.State != "drafted" && d.State != "in_review") {
			return nil
		}
		if err != nil {
			return err
		}
	}
	published, err := s.Store.RunPublished(ctx, p.ID, date)
	if err != nil {
		return err
	}
	if published {
		return s.Store.FinishRun(ctx, p.ID, date, "published")
	}
	if !due {
		return nil
	}
	if run.Attempts >= store.MaxRunAttempts {
		return s.Store.FinishRun(ctx, p.ID, date, "failed")
	}
	ok, err := s.Store.BeginRunAttempt(ctx, p.ID, date, false)
	if err != nil || !ok {
		return err
	}
	err = s.Publisher.ApplyDeadline(ctx, p, date)
	if err != nil && run.Attempts+1 >= store.MaxRunAttempts {
		_ = s.Store.FinishRun(ctx, p.ID, date, "failed")
	}
	return err
}
