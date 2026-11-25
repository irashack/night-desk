// Command demo runs the Night Desk demo reader on loopback, for screenshots
// and local previews: the same as `nightdesk demo --listen 127.0.0.1:8080`.
//
//	go run ./docs/screenshots/demo
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/irashack/night-desk/internal/demo"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "loopback listen address for the reader (port 0 picks a free port)")
	keep := flag.Bool("keep", false, "keep the temporary data directory on exit")
	shots := flag.Bool("screenshots", false, "hide the demo banner, for the README screenshots")
	flag.Parse()
	host, _, err := net.SplitHostPort(*addr)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		fmt.Fprintln(os.Stderr, "demo: -addr must be a literal loopback address, such as 127.0.0.1:8080")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	_, port, _ := net.SplitHostPort(*addr)
	public := ""
	if port != "0" {
		public = "http://" + *addr
	}
	if err := demo.Run(ctx, demo.Options{Listen: *addr, PublicURL: public, Keep: *keep, NoBanner: *shots, Out: os.Stdout}); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}
