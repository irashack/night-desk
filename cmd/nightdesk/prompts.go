package main

import (
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/irashack/night-desk/internal/gather"
	"github.com/irashack/night-desk/internal/prompts"
)

const promptsUsage = "usage: nightdesk prompts list --worker-config FILE | nightdesk prompts reset --worker-config FILE <name>"

// promptsCommand lists the drafting prompts and removes overrides. It touches
// only <prompts.directory>/<name>.md for a known name.
func promptsCommand(args []string, out, errOut io.Writer) int {
	if len(args) == 0 || (args[0] != "list" && args[0] != "reset") {
		fmt.Fprintln(errOut, promptsUsage)
		return 2
	}
	flags := flag.NewFlagSet("prompts "+args[0], flag.ContinueOnError)
	flags.SetOutput(errOut)
	workerConfig := flags.String("worker-config", "", "worker configuration with prompts.directory")
	if flags.Parse(args[1:]) != nil {
		return 2
	}
	wantArgs := 0
	if args[0] == "reset" {
		wantArgs = 1
	}
	if *workerConfig == "" || flags.NArg() != wantArgs {
		fmt.Fprintln(errOut, promptsUsage)
		return 2
	}
	c, err := gather.LoadDraftConfig(*workerConfig)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	store := prompts.New(c.Prompts.Directory)
	if args[0] == "list" {
		for _, name := range prompts.Names {
			p, err := store.Get(name)
			if err != nil {
				fmt.Fprintln(errOut, err)
				return 1
			}
			state := "default"
			if strings.HasPrefix(p.Version, "override-") {
				state = "overridden"
			}
			fmt.Fprintf(out, "%s\t%s\t%s\n", name, p.Version, state)
		}
		return 0
	}
	name := flags.Arg(0)
	if !slices.Contains(prompts.Names, name) {
		fmt.Fprintf(errOut, "unknown prompt %q; prompts are %s\n", name, strings.Join(prompts.Names, ", "))
		return 1
	}
	if c.Prompts.Directory == "" {
		fmt.Fprintln(out, "No prompts.directory is configured; every prompt is the default.")
		return 0
	}
	before, err := store.Get(name)
	if err != nil && !strings.Contains(err.Error(), "prompts."+name) {
		fmt.Fprintln(errOut, err)
		return 1
	}
	p, err := store.Reset(name)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	if before.Version == p.Version {
		fmt.Fprintf(out, "%s already uses the default (%s).\n", name, p.Version)
		return 0
	}
	fmt.Fprintf(out, "Reset %s to the default (%s).\n", name, p.Version)
	return 0
}
