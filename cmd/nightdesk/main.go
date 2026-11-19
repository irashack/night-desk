package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/irashack/night-desk/internal/edition"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "usage: nightdesk validate <edition.json> [--sections key,key:required,...]")
		return 2
	}
	switch args[0] {
	case "init", "check":
		return standalone(args, out, errOut)
	case "serve", "migrate", "key", "backup", "quality":
		return backend(args, out, errOut)
	case "conformance":
		return conformanceCommand(args[1:], out, errOut)
	case "mcp":
		return mcpCommand(args[1:], out, errOut)
	case "gather":
		return gatherCommand(args[1:], out, errOut)
	case "ratings":
		return ratingsCommand(args[1:], out, errOut)
	case "prompts":
		return promptsCommand(args[1:], out, errOut)
	case "validate":
		return validate(args[1:], out, errOut)
	default:
		fmt.Fprintf(errOut, "unknown subcommand: %s\n", args[0])
		return 2
	}
}

func validate(args []string, out, errOut io.Writer) int {
	if len(args) != 1 && len(args) != 3 {
		fmt.Fprintln(errOut, "usage: nightdesk validate <edition.json> [--sections key,key:required,...]")
		return 2
	}
	var rules edition.PaperRules
	if len(args) == 3 {
		if args[1] != "--sections" {
			fmt.Fprintln(errOut, "expected --sections after edition file")
			return 2
		}
		seen := map[string]bool{}
		for _, value := range strings.Split(args[2], ",") {
			key, suffix, hasSuffix := strings.Cut(value, ":")
			if key == "" || strings.TrimSpace(key) != key || (hasSuffix && suffix != "required") || seen[key] {
				fmt.Fprintln(errOut, "sections must be unique keys optionally followed by :required")
				return 2
			}
			seen[key] = true
			rules.Sections = append(rules.Sections, edition.SectionRule{Key: key, Required: hasSuffix})
		}
	}
	f, err := os.Open(args[0])
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	defer f.Close()
	e, err := edition.Decode(f)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	// Without paper configuration, only intrinsic rules can be checked. Use the
	// edition's section order, rather than inventing a default paper configuration.
	if len(args) == 1 {
		seen := map[string]bool{}
		for _, section := range e.Sections {
			if !seen[section.Key] {
				rules.Sections = append(rules.Sections, edition.SectionRule{Key: section.Key})
				seen[section.Key] = true
			}
		}
	}
	code := 0
	for _, problem := range edition.Validate(e, rules) {
		fmt.Fprintln(out, problem)
		if problem.Severity == edition.Error {
			code = 1
		}
	}
	return code
}
