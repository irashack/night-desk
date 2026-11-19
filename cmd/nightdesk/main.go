package main

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/irashack/night-desk/internal/edition"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// version is set at build time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

type command struct {
	name, summary string
	run           func(args []string, out, errOut io.Writer) int
}

// commands is the dispatcher table; the top-level usage is generated from it.
// Each run function receives the arguments after the command name.
var commands = []command{
	{"init", "create a first configuration, migrated database and agent key", func(args []string, out, errOut io.Writer) int {
		return standalone(append([]string{"init"}, args...), out, errOut)
	}},
	{"check", "validate a configuration (and a worker configuration) before serving", func(args []string, out, errOut io.Writer) int {
		return standalone(append([]string{"check"}, args...), out, errOut)
	}},
	{"serve", "run the server: agent API, reader, scheduler and webhook delivery", backendCommand("serve")},
	{"migrate", "apply the embedded, forward-only database migrations", backendCommand("migrate")},
	{"key", "issue, list or revoke paper-scoped keys", backendCommand("key")},
	{"backup", "write a consistent copy of the database and image store", backendCommand("backup")},
	{"quality", "print the operator quality report", backendCommand("quality")},
	{"gather", "run the gathering and drafting worker", gatherCommand},
	{"mcp", "run the stdio MCP client for an agent", mcpCommand},
	{"prompts", "list the drafting prompts or reset an override", promptsCommand},
	{"ratings", "import an outlet ratings CSV as a ratings file", ratingsCommand},
	{"conformance", "list or serve the scenarios that test an integration", conformanceCommand},
	{"validate", "validate an edition offline with the server's rules", validate},
	{"version", "print the version", func(args []string, out, errOut io.Writer) int {
		if len(args) != 0 {
			fmt.Fprintln(errOut, "usage: nightdesk version")
			return 2
		}
		fmt.Fprintln(out, "nightdesk", versionString())
		return 0
	}},
}

func backendCommand(name string) func(args []string, out, errOut io.Writer) int {
	return func(args []string, out, errOut io.Writer) int {
		return backend(append([]string{name}, args...), out, errOut)
	}
}

func run(args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		usage(errOut)
		return 2
	}
	switch args[0] {
	case "help", "-h", "-help", "--help":
		usage(out)
		return 0
	case "--version", "-version":
		args = append([]string{"version"}, args[1:]...)
	}
	for _, c := range commands {
		if c.name == args[0] {
			return c.run(args[1:], out, errOut)
		}
	}
	fmt.Fprintf(errOut, "unknown subcommand: %s\n", args[0])
	fmt.Fprintln(errOut, "Run 'nightdesk help' for the list of subcommands.")
	return 2
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "Night Desk: a self-hosted personal daily newspaper.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "usage: nightdesk <subcommand> [arguments]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Subcommands:")
	width := 0
	for _, c := range commands {
		width = max(width, len(c.name))
	}
	for _, c := range commands {
		fmt.Fprintf(w, "  %-*s  %s\n", width, c.name, c.summary)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "serve, migrate, key, backup and quality read --config FILE (default nightdesk.yaml;")
	fmt.Fprintln(w, "serve requires it). The README and docs/install.md describe each subcommand.")
}

// versionString reports the -ldflags version, else the module version recorded
// by go install (with the VCS revision when the build has one).
func versionString() string {
	if version != "dev" {
		return version
	}
	v := version
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return v
	}
	if mv := info.Main.Version; mv != "" && mv != "(devel)" {
		v = mv
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && s.Value != "" {
			rev := s.Value
			if len(rev) > 12 {
				rev = rev[:12]
			}
			if !strings.Contains(v, rev) {
				v += " (" + rev + ")"
			}
		}
	}
	return v
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
