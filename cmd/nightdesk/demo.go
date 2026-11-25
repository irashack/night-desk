package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os/signal"
	"syscall"

	"github.com/irashack/night-desk/internal/demo"
	"os"
)

// demoCommand serves the fictional demo paper: nothing to configure, no keys,
// no network access, and nothing kept on exit.
func demoCommand(args []string, out, errOut io.Writer) int {
	flags := flag.NewFlagSet("demo", flag.ContinueOnError)
	flags.SetOutput(errOut)
	listen := flags.String("listen", ":8080", "address to serve the demo on")
	publicURL := flags.String("public-url", "", "address a browser opens (default http://localhost:<port>)")
	keep := flags.Bool("keep", false, "keep the temporary data directory on exit")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(errOut, "usage: nightdesk demo [--listen ADDR] [--public-url URL] [--keep]")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewTextHandler(errOut, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := demo.Run(ctx, demo.Options{Listen: *listen, PublicURL: *publicURL, Keep: *keep, Out: out, Log: log}); err != nil {
		fmt.Fprintln(errOut, "demo:", err)
		return 1
	}
	return 0
}
