package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/irashack/night-desk/internal/gather"
	"github.com/irashack/night-desk/internal/model"
)

func gatherCommand(args []string, out, errOut io.Writer) int {
	flags := flag.NewFlagSet("gather", flag.ContinueOnError)
	flags.SetOutput(errOut)
	server := flags.String("server", "", "Night Desk server origin")
	keyFile := flags.String("key-file", "", "worker key file")
	workerConfig := flags.String("worker-config", "", "worker-only models, tracing and prompts configuration")
	once := flags.Bool("once", false, "gather once")
	interval := flags.Duration("interval", 30*time.Minute, "time between runs")
	if flags.Parse(args) != nil {
		return 2
	}
	if flags.NArg() != 0 || *server == "" || *keyFile == "" || *interval <= 0 {
		fmt.Fprintln(errOut, "usage: nightdesk gather --server URL --key-file PATH [--worker-config PATH] [--once] [--interval 30m]")
		return 2
	}
	f, err := os.Open(*keyFile)
	if err != nil {
		fmt.Fprintln(errOut, "worker key file unavailable")
		return 1
	}
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	f.Close()
	if err != nil || len(b) > 4096 {
		fmt.Fprintln(errOut, "worker key file invalid")
		return 1
	}
	worker, err := gather.New(*server, strings.TrimSpace(string(b)), nil)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var draftConfig gather.DraftConfig
	var options model.Options
	if *workerConfig != "" {
		draftConfig, err = gather.LoadDraftConfig(*workerConfig)
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		if draftConfig.Models != nil {
			provider, shutdown, err := model.NewTracing(ctx, draftConfig.Tracing)
			if err != nil {
				fmt.Fprintln(errOut, err)
				return 1
			}
			defer func() {
				flush, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if shutdown(flush) != nil {
					fmt.Fprintln(errOut, "worker trace flush failed")
				}
			}()
			options.Tracer = provider.Tracer("github.com/irashack/night-desk/internal/model")
			options.RecordContent = draftConfig.Tracing.RecordContent
		}
	}
	for {
		// New sections find feeds before gathering, so this run already uses them.
		found, findErr := worker.FindSourcesOnce(ctx, draftConfig, options, time.Now())
		if findErr != nil {
			fmt.Fprintln(errOut, findErr)
		}
		if found > 0 {
			fmt.Fprintf(out, "found %d new section feeds\n", found)
		}
		result, err := worker.RunOnce(ctx)
		if err != nil {
			fmt.Fprintln(errOut, err)
		}
		fmt.Fprintf(out, "gathered %d candidates (%d unavailable, %d feed failures)\n", result.Candidates, result.Unavailable, result.FeedFailures)
		drafted, draftErr := worker.DraftOnce(ctx, draftConfig, options, time.Now())
		if draftErr != nil {
			fmt.Fprintln(errOut, draftErr)
		}
		if drafted {
			fmt.Fprintln(out, "submitted today's draft")
		}
		if *once {
			if findErr != nil || err != nil || draftErr != nil {
				return 1
			}
			return 0
		}
		timer := time.NewTimer(*interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0
		case <-timer.C:
		}
	}
}
