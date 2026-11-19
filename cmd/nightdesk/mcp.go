package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	bridge "github.com/irashack/night-desk/internal/mcp"
)

func mcpCommand(args []string, out, errOut io.Writer) int {
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.SetOutput(errOut)
	serverURL := flags.String("server", "", "Night Desk server origin")
	keyFile := flags.String("key-file", "", "agent key file")
	if flags.Parse(args) != nil {
		return 2
	}
	if flags.NArg() != 0 || *serverURL == "" || *keyFile == "" {
		fmt.Fprintln(errOut, "usage: nightdesk mcp --server URL --key-file PATH")
		return 2
	}
	bridge.Version = versionString()
	server, err := bridge.New(*serverURL, *keyFile, nil)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// stdout belongs exclusively to the MCP transport; diagnostics use stderr.
	if err := server.Run(ctx, &mcp.IOTransport{Reader: os.Stdin, Writer: mcpWriter{out}}); err != nil && ctx.Err() == nil {
		fmt.Fprintln(errOut, "MCP transport failed")
		return 1
	}
	return 0
}

type mcpWriter struct{ io.Writer }

func (mcpWriter) Close() error { return nil }
