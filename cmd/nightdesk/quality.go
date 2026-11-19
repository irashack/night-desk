package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/editorial"
	"github.com/irashack/night-desk/internal/quality"
	"github.com/irashack/night-desk/internal/store"
)

type qualityPaperReport = quality.PaperReport
type qualityReport = quality.Report

func qualityUsage(errOut io.Writer) int {
	fmt.Fprintln(errOut, "usage: nightdesk quality --config <file> [--paper ID] [--days 14] [--json]")
	return 2
}

func qualityCommand(ctx context.Context, manager *config.Manager, db *store.Store, log *slog.Logger, args []string, out, errOut io.Writer) int {
	flags := flag.NewFlagSet("quality", flag.ContinueOnError)
	flags.SetOutput(errOut)
	paperID := flags.String("paper", "", "paper ID (default: every paper)")
	days := flags.Int("days", 14, "window length in local calendar days")
	asJSON := flags.Bool("json", false, "print JSON instead of a text table")
	if flags.Parse(args) != nil {
		return 2
	}
	if flags.NArg() != 0 || (*days < 1 || *days > 3660) {
		return qualityUsage(errOut)
	}

	// The quality report describes the paper as its writing agent has edited it.
	desk, err := editorial.Open(ctx, db, manager.Current, log)
	if err != nil {
		fmt.Fprintln(errOut, "quality report failed")
		return 1
	}
	c := desk.Config()
	papers := c.Papers
	if *paperID != "" {
		p, ok := c.Paper(*paperID)
		if !ok {
			fmt.Fprintln(errOut, "unknown paper")
			return 1
		}
		papers = []config.Paper{p}
	}

	report, err := quality.Build(ctx, db, papers, *days, time.Now())
	if err != nil {
		fmt.Fprintln(errOut, "quality report failed")
		return 1
	}

	if *asJSON {
		if err := json.NewEncoder(out).Encode(report); err != nil {
			return 1
		}
		return 0
	}
	if err := writeQualityTable(out, report.Papers); err != nil {
		return 1
	}
	return 0
}

func deadlinesMet(p config.Paper, loc *time.Location, editions []store.QualityEdition) (int, int, error) {
	return quality.DeadlinesMet(p, loc, editions)
}

func writeQualityTable(out io.Writer, papers []qualityPaperReport) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PAPER\tDAYS\tPUBLISHED/DAYS\tDEADLINES MET/CHECKED\tHELD\tUNREVIEWED\tREVIEW TURNAROUND MEDIAN (MIN)\tEMPTY REQUIRED SECTIONS\tSENT BACK\tDEADLINE MISSES")
	for _, p := range papers {
		median := "-"
		if p.ReviewTurnaroundSamples > 0 {
			median = strconv.FormatFloat(p.ReviewTurnaroundMedianMinutes, 'f', 1, 64)
		}
		fmt.Fprintf(w, "%s\t%d\t%d/%d\t%d/%d\t%d\t%d\t%s\t%d\t%d\t%d\n",
			p.PaperID, p.Days, p.Published, p.Days, p.DeadlinesMet, p.DeadlinesChecked,
			p.Held, p.Unreviewed, median, p.EmptyRequiredSections, p.DraftsSentBack, p.DeadlineMisses)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	for _, p := range papers {
		for _, group := range []struct {
			name   string
			counts map[string]int
		}{
			{"WARNINGS", p.WarningsByRule}, {"FAILED SOURCE CHECKS", p.FailedSourceChecksByRule}, {"UNVERIFIED SOURCE CHECKS", p.UnverifiedSourceChecksByRule}, {"DROPPED BY FILTER", p.DroppedByRule},
		} {
			rules := make([]string, 0, len(group.counts))
			for rule := range group.counts {
				rules = append(rules, rule)
			}
			sort.Strings(rules)
			for _, rule := range rules {
				if _, err := fmt.Fprintf(out, "%s %s: %s: %d\n", p.PaperID, group.name, rule, group.counts[rule]); err != nil {
					return err
				}
			}
		}
	}
	if _, err := fmt.Fprintln(out, "\nDRAFTING"); err != nil {
		return err
	}
	w = tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PAPER\tRUNS\tTOKENS\tEST. COST")
	for _, p := range papers {
		fmt.Fprintf(w, "%s\t%d\t%d\t$%.2f\n", p.PaperID, p.DraftRuns, p.DraftTokens, p.DraftCostUSD)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "\nFETCH HEALTH"); err != nil {
		return err
	}
	w = tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PAPER\tHOST\tOK\tFAILED\t4XX\t429\t5XX\tNETWORK\tTOO LARGE\tCOOLED OFF\tREADABILITY\tJSON-LD\tAMP")
	for _, p := range papers {
		for _, h := range p.FetchHealth {
			fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n", p.PaperID, h.Host, h.OK, h.Failed(), h.FailedHTTP4xx, h.FailedHTTP429, h.FailedHTTP5xx, h.FailedNetwork, h.FailedTooLarge, h.CooledOff, h.Readability, h.JSONLD, h.AMP)
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "\nFEEDBACK"); err != nil {
		return err
	}
	w = tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PAPER\tWEEK\tUP\tDOWN\tSAVES")
	for _, p := range papers {
		for _, week := range p.FeedbackTrend {
			fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\n", p.PaperID, week.Week, week.Up, week.Down, week.Saves)
		}
	}
	return w.Flush()
}
