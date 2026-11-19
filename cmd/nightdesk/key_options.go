package main

import (
	"errors"

	"github.com/irashack/night-desk/internal/auth"
)

type issueKeyOptions struct {
	label        string
	capabilities []auth.Capability
}

func parseIssueKeyOptions(args []string) (issueKeyOptions, error) {
	var opts issueKeyOptions
	names := auth.DefaultCapabilities
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		flag := args[i]
		if seen[flag] {
			return opts, errors.New("key issue options must not be repeated")
		}
		seen[flag] = true
		switch flag {
		case "--label", "--capabilities":
			if i+1 == len(args) {
				return opts, errors.New("key issue option requires a value")
			}
			i++
			if flag == "--label" {
				opts.label = args[i]
			} else {
				names = args[i]
			}
		case "--worker":
			names = auth.WorkerCapabilities
		default:
			return opts, errors.New("unknown key issue option")
		}
	}
	if seen["--worker"] && seen["--capabilities"] {
		return opts, errors.New("--worker and --capabilities are mutually exclusive")
	}
	var e error
	opts.capabilities, e = auth.ParseCapabilities(names)
	return opts, e
}
