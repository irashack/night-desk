package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/sourcecheck"
	"github.com/irashack/night-desk/internal/store"
)

// Source checks are advisory and use stored snapshots only, never web fetching.
func (s *Server) sourceWarnings(ctx context.Context, scope auth.PaperScope, p config.Paper, ed *edition.Edition) []problem {
	return sourceWarnings(p, ed, func(link string) (string, bool, error) {
		return s.Store.SnapshotText(ctx, scope, link)
	})
}

type namedSource struct {
	url  string
	path string
}

func sourceWarnings(p config.Paper, ed *edition.Edition, textFor func(string) (string, bool, error)) []problem {
	warnings, _ := sourceWarningsWithChecks(p, ed, textFor)
	return warnings
}

func sourceWarningsWithChecks(p config.Paper, ed *edition.Edition, textFor func(string) (string, bool, error)) ([]problem, []store.SourceCheckCount) {
	var warnings []problem
	counts := []store.SourceCheckCount{}
	if ed == nil {
		return warnings, counts
	}
	for i, section := range ed.Sections {
		for j, item := range section.Items {
			path := fmt.Sprintf("sections[%d].items[%d]", i, j)
			named := make([]namedSource, 0, 1+len(item.Sources))
			named = append(named, namedSource{url: item.URL, path: path + ".url"})
			for k, source := range item.Sources {
				named = append(named, namedSource{
					url:  source.URL,
					path: fmt.Sprintf("%s.sources[%d].url", path, k),
				})
			}

			// A warning is per named source, so one unavailable source cannot
			// hide behind another source's text. Text is fetched once per URL.
			// "" is available; otherwise the unverified rule: a source never gathered
			// for this paper, or one gathered whose fetch failed.
			known := make(map[string]string, len(named))
			var texts []string
			for _, source := range named {
				if source.url == "" {
					continue
				}
				rule, seen := known[source.url]
				if !seen {
					raw, ok, err := textFor(source.url)
					switch {
					case errors.Is(err, store.ErrNoSnapshot):
						rule = "not gathered"
					case err != nil || !ok || strings.TrimSpace(raw) == "":
						rule = "source unavailable"
					default:
						texts = append(texts, raw)
					}
					known[source.url] = rule
				}
				switch rule {
				case "not gathered":
					warnings = append(warnings, problem{source.path, "no stored snapshot of this source; claim unverified"})
				case "source unavailable":
					warnings = append(warnings, problem{source.path, "source fetch failed; claim unverified"})
				}
				if rule != "" {
					counts = append(counts, store.SourceCheckCount{Status: "unverified", Rule: rule, Count: 1})
				}
			}
			if len(texts) == 0 {
				continue
			}

			for _, field := range itemText(item) {
				check := sourcecheck.Check
				if field.headline {
					check = sourcecheck.CheckTitle
				}
				findings := check(field.text, texts)
				if len(findings) == 0 {
					counts = append(counts, store.SourceCheckCount{Status: "passed", Rule: "source text", Count: 1})
				}
				for _, finding := range findings {
					counts = append(counts, store.SourceCheckCount{Status: "failed", Rule: finding.Rule, Count: 1})
					warnings = append(warnings, problem{path + "." + field.name, finding.Rule + ": " + finding.Value})
				}
			}
		}
	}
	return warnings, counts
}

type itemField struct {
	name, text string
	headline   bool
}

func itemText(item edition.Item) []itemField {
	fields := []itemField{{"title", item.Title, true}, {"summary", item.Summary, false}}
	if item.Why != nil {
		fields = append(fields, itemField{"why", *item.Why, false})
	}
	return fields
}

// cutOffProblems refuses publication of a title ending on a function word or a
// summary or why that stops mid-sentence, after MuckScraper discarding cut-off
// summaries. Agents' own editions and draft submissions keep these as source
// check warnings; only publishing a draft refuses them.
func cutOffProblems(ed *edition.Edition) []problem {
	var out []problem
	if ed == nil {
		return out
	}
	for i, section := range ed.Sections {
		for j, item := range section.Items {
			for _, field := range itemText(item) {
				if reason := sourcecheck.CutOff(field.text, field.headline); reason != "" {
					out = append(out, problem{fmt.Sprintf("sections[%d].items[%d].%s", i, j, field.name), "is cut off: " + reason})
				}
			}
		}
	}
	return out
}

func diagnosticProblems(problems []problem) []store.DiagnosticProblem {
	out := make([]store.DiagnosticProblem, 0, len(problems))
	for _, p := range problems {
		out = append(out, store.DiagnosticProblem{Path: p.Path, Rule: p.Rule})
	}
	return out
}

func publishDiagnostics(p config.Paper, path string, warnings, problems []problem, checks []store.SourceCheckCount) store.PublishDiagnostics {
	return store.PublishDiagnostics{Path: path, RetentionDays: p.RetentionDays, Warnings: diagnosticProblems(warnings), Errors: diagnosticProblems(problems), SourceChecks: checks}
}

func (s *Server) publishSourceChecks(ctx context.Context, scope auth.PaperScope, p config.Paper, ed *edition.Edition) ([]problem, []store.SourceCheckCount) {
	return sourceWarningsWithChecks(p, ed, func(link string) (string, bool, error) { return s.Store.SnapshotText(ctx, scope, link) })
}
