package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/irashack/night-desk/internal/conformance"
)

func conformanceCommand(args []string, out, errOut io.Writer) int {
	if len(args) == 1 && args[0] == "list" {
		for _, name := range conformance.Scenarios() {
			fmt.Fprintln(out, name)
		}
		return 0
	}
	if len(args) == 0 || args[0] != "serve" {
		fmt.Fprintln(errOut, "usage: nightdesk conformance list | serve --addr 127.0.0.1:PORT --scenario NAME --key KEY_FILE [--webhook URL] [--exit-on-finish]")
		return 2
	}
	flags := flag.NewFlagSet("conformance serve", flag.ContinueOnError)
	flags.SetOutput(errOut)
	addr := flags.String("addr", "127.0.0.1:8081", "loopback listen address (port 0 selects a free port)")
	scenario := flags.String("scenario", "", "script name; see conformance list")
	keyFile := flags.String("key", "", "fixture bearer key file; also the webhook secret")
	hook := flags.String("webhook", "", "receiver URL (required for webhooks)")
	exit := flags.Bool("exit-on-finish", false, "exit after script completion (with a one-second report grace), or timeout")
	timeout := flags.Duration("timeout", 2*time.Minute, "maximum time with --exit-on-finish")
	if flags.Parse(args[1:]) != nil {
		return 2
	}
	host, _, err := net.SplitHostPort(*addr)
	if flags.NArg() != 0 || err != nil || !net.ParseIP(host).IsLoopback() || *keyFile == "" || *timeout <= 0 || (*scenario == "webhooks") != (*hook != "") {
		fmt.Fprintln(errOut, "require a literal loopback --addr, --key file, positive --timeout, and --webhook only for the webhooks scenario")
		return 2
	}
	key, err := os.ReadFile(*keyFile)
	if err != nil {
		fmt.Fprintln(errOut, "conformance key file is unreadable")
		return 1
	}
	kit, err := conformance.New(*scenario, bytes.TrimSpace(key))
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintln(errOut, "conformance listen failed")
		return 1
	}
	server := &http.Server{Handler: kit, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serving := make(chan error, 1)
	go func() { serving <- server.Serve(listener) }()
	fmt.Fprintln(out, "conformance listening at http://"+listener.Addr().String())
	if *scenario == "webhooks" {
		go kit.DeliverWebhooks(ctx, *hook)
	}
	var finished <-chan struct{}
	var deadline <-chan time.Time
	if *exit {
		finished = kit.Done()
		timer := time.NewTimer(*timeout)
		defer timer.Stop()
		deadline = timer.C
	}
	var polls <-chan time.Time
	if *exit {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		polls = ticker.C
	}
	var reportGrace time.Time
	code := 0
waiting:
	for {
		select {
		case <-ctx.Done():
			break waiting
		case err := <-serving:
			if err != http.ErrServerClosed {
				fmt.Fprintln(errOut, "conformance server failed")
				code = 1
			}
			break waiting
		case <-finished:
			break waiting
		case <-polls:
			if !kit.Finished() {
				reportGrace = time.Time{}
				continue
			}
			if reportGrace.IsZero() {
				reportGrace = time.Now().Add(time.Second)
			}
			if !time.Now().Before(reportGrace) {
				break waiting
			}
		case <-deadline:
			fmt.Fprintln(errOut, "conformance scenario timed out")
			code = 1
			break waiting
		}
	}
	// Cancel webhook deliveries before draining HTTP handlers.
	stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if server.Shutdown(shutdown) != nil {
		_ = server.Close()
		code = 1
	}
	report := kit.Report()
	if *exit && !report.Passed {
		code = 1
	}
	_ = json.NewEncoder(out).Encode(report)
	return code
}
