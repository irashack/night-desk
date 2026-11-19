package main

import (
	"testing"

	"github.com/irashack/night-desk/internal/auth"
)

func TestIssueKeyOptions(t *testing.T) {
	for _, tt := range []struct {
		args         []string
		label, names string
	}{
		{nil, "", auth.DefaultCapabilities},
		{[]string{"--worker"}, "", auth.WorkerCapabilities},
		{[]string{"--label", "Worker", "--worker"}, "Worker", auth.WorkerCapabilities},
		{[]string{"--worker", "--label", "Worker"}, "Worker", auth.WorkerCapabilities},
		{[]string{"--capabilities", "read,publish", "--label", "Writer"}, "Writer", "read,publish"},
		{[]string{"--label", "Reviewer", "--capabilities", "read,review"}, "Reviewer", "read,review"},
	} {
		opts, e := parseIssueKeyOptions(tt.args)
		if e != nil || opts.label != tt.label || auth.CapabilityNames(opts.capabilities) != tt.names {
			t.Fatal(tt.args, opts, e)
		}
	}
	for _, args := range [][]string{
		{"--capabilities"}, {"--capabilities", ""}, {"--capabilities", "unknown"}, {"--capabilities", "read,read"},
		{"--capabilities", "read", "--worker"}, {"--worker", "--capabilities", "read"},
		{"--worker", "--worker"}, {"--capabilities", "read", "--capabilities", "publish"},
		{"--label"}, {"--label", "one", "--label", "two"}, {"--unknown"}, {"extra"},
	} {
		if _, e := parseIssueKeyOptions(args); e == nil {
			t.Fatal("invalid options accepted", args)
		}
	}
}
