package gather

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/irashack/night-desk/internal/candidate"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/drafting"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/model"
	"github.com/irashack/night-desk/internal/prompts"
	"github.com/irashack/night-desk/internal/scheduler"
	"github.com/irashack/night-desk/internal/store"
	"sigs.k8s.io/yaml"
)

// DraftConfig belongs only to the worker. Model keys never enter server config.
// Models omitted enables feeds-only drafting; all paths are relative to this file.
type DraftConfig struct {
	Models  *model.Config       `json:"models,omitempty"`
	Tracing model.TracingConfig `json:"tracing,omitempty"`
	Prompts struct {
		Directory string `json:"directory,omitempty"`
	} `json:"prompts,omitempty"`
	Limits model.Limits `json:"limits,omitempty"`
}

func LoadDraftConfig(path string) (DraftConfig, error) {
	var c DraftConfig
	f, err := os.Open(path)
	if err != nil {
		return c, model.Problem{Path: "worker_config", Rule: "must be readable"}
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return c, model.Problem{Path: "worker_config", Rule: "must be at most 1 MB"}
	}
	raw, err := yaml.YAMLToJSONStrict(b)
	if err != nil {
		return c, model.Problem{Path: "worker_config", Rule: "must contain valid YAML or JSON"}
	}
	if err = model.DecodeJSON(raw, &c); err != nil {
		return c, err
	}
	base := filepath.Dir(path)
	resolve := func(p string) string {
		if p != "" && !filepath.IsAbs(p) {
			return filepath.Join(base, p)
		}
		return p
	}
	c.Prompts.Directory = resolve(c.Prompts.Directory)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields) // DecodeJSON already validated the object.
	if fields["limits"] == nil {
		c.Limits = model.Limits{MaxCalls: 12, MaxTokens: 100000}
	}
	if _, err = model.NewBudget(c.Limits); err != nil {
		return c, workerConfigProblem("limits", err)
	}
	if c.Models != nil {
		c.Models.Fast.KeyFile = resolve(c.Models.Fast.KeyFile)
		c.Models.Writer.KeyFile = resolve(c.Models.Writer.KeyFile)
		// Validate providers and key files at startup, before fetching any sources.
		for _, tier := range []struct {
			name   string
			config model.TierConfig
		}{{"fast", c.Models.Fast}, {"writer", c.Models.Writer}} {
			if _, err = model.NewCompleter(tier.config, model.Options{}); err != nil {
				return c, workerConfigProblem("models."+tier.name, err)
			}
		}
	}
	return c, nil
}

func workerConfigProblem(prefix string, err error) error {
	var p model.Problem
	if errors.As(err, &p) {
		p.Path = prefix + "." + p.Path
		return p
	}
	return errors.New(prefix + ": invalid configuration")
}

// DraftOnce drafts the upcoming publication date inside draft_lead, which may
// start on the preceding local date. The server leases each attempt and reserves
// its spending; this HTTP-only worker never approves or publishes.
func (w *Worker) DraftOnce(ctx context.Context, c DraftConfig, options model.Options, now time.Time) (bool, error) {
	var p struct {
		config.Paper
		ReaderNotes string `json:"reader_notes"`
	}
	if err := w.request(ctx, "GET", "/api/v1/paper", nil, &p); err != nil {
		return false, err
	}
	if (p.Role() != "reviewer" && p.Role() != "none") || p.Deadline == nil {
		return false, nil
	}
	deadline, err := upcomingDeadline(p.Paper, now)
	if err != nil {
		return false, errors.New("paper.deadline: must be a valid local deadline")
	}
	lead, err := p.DraftLeadTime()
	if err != nil {
		return false, err
	}
	late := false
	if now.Before(deadline.Add(-lead)) {
		// Outside the lead window, today's edition may still be drafted late if its
		// deadline passed with nothing to publish; the server only grants a late
		// claim for a missed run on the paper's current local date.
		today, err := scheduler.Deadline(p.Paper, now)
		if err != nil || now.Before(today) {
			return false, nil
		}
		deadline, late = today, true
	}
	date := deadline.Format("2006-01-02")
	var existing store.Draft
	err = w.request(ctx, "GET", "/api/v1/drafts/"+date, nil, &existing)
	editorNote := ""
	if err == nil {
		editorNote = pendingSendBack(existing)
		if existing.State != "gathering" && editorNote == "" {
			return false, nil
		}
		if existing.Body != nil && editorNote == "" {
			return false, nil
		}
	} else {
		var status responseError
		if !errors.As(err, &status) || status != 404 {
			return false, err
		}
	}
	limits := c.Limits
	if c.Models == nil {
		// The claim API requires both reservation limits to be positive.
		limits = model.Limits{MaxCalls: 1, MaxTokens: 1}
	} else if limits == (model.Limits{}) {
		limits = model.Limits{MaxCalls: 12, MaxTokens: 100000}
	}
	var lease store.DraftLease
	var run *model.Run
	leased, reported := false, false
	defer func() {
		if !leased || reported {
			return
		}
		spend := store.DraftSpend{}
		if run != nil {
			spent := run.Budget.Spending()
			spend = store.DraftSpend{Calls: spent.Calls, Tokens: spent.Tokens, CostMicros: spent.CostMicros}
		}
		report, _ := json.Marshal(map[string]any{"lease_id": lease.ID, "outcome": "failed", "spend": spend})
		// Cancellation must not erase spending. A lost report leaves the full
		// reservation charged, so retrying or restarting cannot reset the cap.
		reportCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer done()
		_ = w.request(reportCtx, "POST", "/api/v1/runs/"+date+"/report", report, nil)
	}()
	claimLease := func() (bool, error) {
		claim, _ := json.Marshal(store.DraftBudget{MaxCalls: limits.MaxCalls, MaxTokens: limits.MaxTokens})
		if err := w.request(ctx, "POST", "/api/v1/runs/"+date+"/claim", claim, &lease); err != nil {
			var status responseError
			if errors.As(err, &status) && status == http.StatusConflict {
				return false, nil
			}
			// A worker without models used to only gather, so its key may
			// lack the draft capability: then it keeps only gathering.
			if c.Models == nil && errors.As(err, &status) && status == http.StatusForbidden {
				return false, nil
			}
			return false, err
		}
		leased = true
		return true, nil
	}
	// A late edition claims first: most of the day after a deadline there is
	// nothing to draft, and a refused claim costs no candidate read.
	if late {
		if ok, err := claimLease(); !ok {
			return false, err
		}
	}
	candidates, err := w.draftCandidates(ctx, p.Paper, c.Models == nil)
	if err != nil {
		return false, err
	}
	if len(candidates) == 0 {
		return false, nil
	}
	paper := drafting.Paper{Masthead: p.Masthead, Place: p.Place, Timezone: p.Timezone, Limits: p.Rules().Limits}
	if p.Brief != nil {
		paper.Brief = *p.Brief
	}
	for _, s := range p.Sections {
		section := drafting.Section{Key: s.Key, Title: s.Title, Required: s.Required}
		if s.Brief != nil {
			section.Brief = *s.Brief
		}
		paper.Sections = append(paper.Sections, section)
	}
	if !leased {
		if ok, err := claimLease(); !ok {
			return false, err
		}
	}
	// Never start a model call that can outlive the server's exclusive lease.
	attemptCtx, cancel := context.WithDeadline(ctx, lease.ExpiresAt)
	defer cancel()
	var ed edition.Edition
	var spend store.DraftSpend
	if c.Models == nil {
		ed, _, err = drafting.FeedsOnly(paper, candidates)
	} else {
		run, err = model.NewRun(*c.Models, model.Limits{MaxCalls: lease.Remaining.MaxCalls, MaxTokens: lease.Remaining.MaxTokens}, options)
		if err != nil {
			return false, err
		}
		ed, _, err = drafting.Draft(attemptCtx, drafting.Models{Fast: run.Fast, Writer: run.Writer, Prompts: prompts.New(c.Prompts.Directory)}, paper, p.ReaderNotes, candidates, deadline, editorNote)
		spent := run.Budget.Spending()
		spend = store.DraftSpend{Calls: spent.Calls, Tokens: spent.Tokens, CostMicros: spent.CostMicros}
	}
	if err != nil {
		return false, err
	}
	body, err := json.Marshal(map[string]any{"lease_id": lease.ID, "outcome": "submitted", "spend": spend, "expected_revision": existing.Revision, "draft": ed})
	if err != nil {
		return false, errors.New("draft encoding failed")
	}
	if err = w.request(attemptCtx, "POST", "/api/v1/runs/"+date+"/report", body, nil); err != nil {
		return false, err
	}
	reported = true
	return true, nil
}

func upcomingDeadline(p config.Paper, now time.Time) (time.Time, error) {
	deadline, err := scheduler.Deadline(p, now)
	if err != nil {
		return time.Time{}, err
	}
	if !now.Before(deadline) {
		loc, err := time.LoadLocation(p.Timezone)
		if err != nil {
			return time.Time{}, err
		}
		// Calendar arithmetic from noon avoids DST gaps at midnight and 24-hour
		// assumptions. Deadline retains the scheduler's first-fold/gap policy.
		local := now.In(loc)
		tomorrow := time.Date(local.Year(), local.Month(), local.Day()+1, 12, 0, 0, 0, loc)
		return scheduler.Deadline(p, tomorrow)
	}
	return deadline, nil
}

func pendingSendBack(d store.Draft) string {
	if (d.State != "drafted" && d.State != "in_review") || d.ApprovedRevision != nil {
		return ""
	}
	var latest store.DraftAction
	for _, action := range d.Actions {
		if (action.Action == "send_back" || action.Action == "submit" || action.Action == "approve") && action.Revision > latest.Revision {
			latest = action
		}
	}
	if latest.Action == "send_back" {
		return latest.Note
	}
	return ""
}

const (
	maxDraftCandidates      = 48
	maxDraftPerSection      = 8
	maxDraftTextBytes       = 16 << 10
	maxDraftItemBytes       = 2048
	minDraftItemBytes       = 256
	maxDraftPagesPerSection = 10
)

// draftCandidates reads newest-first pages independently for each section. The
// selection has hard count bounds and model input also has UTF-8 byte bounds.
// Feeds-only drafting uses the stored text to keep excerpt boundaries intact.
func (w *Worker) draftCandidates(ctx context.Context, p config.Paper, feedsOnly bool) ([]drafting.Candidate, error) {
	out := []drafting.Candidate{}
	if len(p.Sections) == 0 {
		return out, nil
	}
	sectionBytes := maxDraftTextBytes / len(p.Sections)
	sectionCount := min(maxDraftPerSection, maxDraftCandidates/len(p.Sections))
	// Each candidate gets an equal share of its section's text budget, so a
	// paper with many sections still offers the writer several stories in each
	// rather than one long one; the total budget is unchanged.
	itemBytes := min(maxDraftItemBytes, max(sectionBytes/max(sectionCount, 1), minDraftItemBytes))
	totalBytes := 0
	// Gathering places each item from the paper's sources in one inheriting
	// section; section-fit may move it to another of them.
	inheriting := []string{}
	for _, section := range p.Sections {
		if section.Sources == nil {
			inheriting = append(inheriting, section.Key)
		}
	}
	for _, section := range p.Sections {
		var others []string
		if section.Sources == nil && len(inheriting) > 1 {
			for _, key := range inheriting {
				if key != section.Key {
					others = append(others, key)
				}
			}
		}
		count, used := 0, 0
		cursor := ""
		seen := map[string]bool{}
		for page := 0; page < maxDraftPagesPerSection; page++ {
			path := "/api/v1/candidates?section=" + url.QueryEscape(section.Key) + "&limit=10"
			if cursor != "" {
				path += "&cursor=" + url.QueryEscape(cursor)
			}
			var pool struct {
				Candidates []candidate.Candidate `json:"candidates"`
				Next       string                `json:"next_cursor"`
			}
			if err := w.request(ctx, "GET", path, nil, &pool); err != nil {
				return nil, err
			}
			for _, c := range pool.Candidates {
				if c.State != "ok" || c.Repeat || c.Section != section.Key || !strings.HasPrefix(c.CanonicalURL, "https://") {
					continue
				}
				text := c.Text
				if !feedsOnly {
					text = capText(text, min(itemBytes, sectionBytes-used, maxDraftTextBytes-totalBytes))
				}
				if text == "" {
					continue
				}
				d := drafting.Candidate{ID: strconv.FormatInt(c.ID, 10), Section: c.Section, URL: c.CanonicalURL, Outlet: c.Outlet, Title: c.Title, Text: text, Byline: strings.Join(c.Byline, ", "), Aliases: c.Aliases, Sections: others}
				if c.WireGroup != nil {
					d.WireGroup = *c.WireGroup
				}
				out = append(out, d)
				if !feedsOnly {
					used += len(text)
					totalBytes += len(text)
				}
				count++
				if count >= sectionCount || used >= sectionBytes || len(out) >= maxDraftCandidates || totalBytes >= maxDraftTextBytes {
					break
				}
			}
			if count >= sectionCount || used >= sectionBytes || len(out) >= maxDraftCandidates || totalBytes >= maxDraftTextBytes || pool.Next == "" {
				break
			}
			if seen[pool.Next] {
				return nil, errors.New("candidate cursor did not advance")
			}
			seen[pool.Next] = true
			cursor = pool.Next
		}
		if len(out) >= maxDraftCandidates || totalBytes >= maxDraftTextBytes {
			break
		}
	}
	return out, nil
}
