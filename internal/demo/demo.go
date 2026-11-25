// Package demo serves a fictional Night Desk paper with no configuration, no
// keys and no network access: `nightdesk demo` in the image, and the README
// screenshots. It wires the real reader and agent API the way `nightdesk
// serve` does, with no sign-in (server.auth.mode none), keeps its data in a
// temporary directory, and publishes the editions under editions/ through the
// real publish endpoint.
package demo

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/irashack/night-desk/internal/api"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/editorial"
	"github.com/irashack/night-desk/internal/keys"
	"github.com/irashack/night-desk/internal/push"
	"github.com/irashack/night-desk/internal/reader"
	"github.com/irashack/night-desk/internal/store"
)

//go:embed editions/*.json
var editions embed.FS

// Banner tells every page of the demo what it is.
const Banner = "Demo: a fictional paper with invented stories. Nothing you change is kept."

// Options configure Run.
type Options struct {
	// Listen is the address to serve on, such as ":8080" (port 0 picks one).
	Listen string
	// PublicURL is the address a browser opens; empty means
	// http://localhost:<port>.
	PublicURL string
	// Keep leaves the temporary data directory in place on exit.
	Keep bool
	// NoBanner hides the demo banner, for screenshots.
	NoBanner bool
	// Out receives the line naming the reader's address; Log the rest.
	Out io.Writer
	Log *slog.Logger
}

// Run serves the demo until ctx ends.
func Run(ctx context.Context, o Options) error {
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	dir, err := os.MkdirTemp("", "nightdesk-demo-")
	if err != nil {
		// Without a writable temporary directory (a minimal image), use the
		// working directory: the image's /data.
		if dir, err = os.MkdirTemp(".", "nightdesk-demo-"); err != nil {
			return err
		}
	}
	if o.Keep {
		o.Log.Warn("demo data directory kept", "dir", dir)
	} else {
		defer os.RemoveAll(dir)
	}
	listener, err := net.Listen("tcp", o.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	base := strings.TrimRight(o.PublicURL, "/")
	if base == "" {
		base = "http://localhost:" + port
	}
	cfg := demoConfig(base, dir)
	current := func() *config.Config { return cfg }

	db, err := store.Open(dir)
	if err != nil {
		return err
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		return err
	}
	desk, err := editorial.Open(ctx, db, current, o.Log)
	if err != nil {
		return err
	}
	readerServer, err := reader.New(ctx, db, desk.Config, "")
	if err != nil {
		return err
	}
	readerServer.Editorial = desk
	readerServer.Banner = Banner
	readerServer.NoBanner = o.NoBanner
	if readerServer.Push, err = push.LoadKey(dir); err != nil {
		return err
	}
	apiServer := &api.Server{Store: db, Config: desk.Config, Images: &artFetcher{dataDir: dir}, Log: o.Log, Editorial: desk}
	mux := http.NewServeMux()
	apiHandler := apiServer.Handler()
	mux.Handle("/api/", apiHandler)
	mux.Handle("/api", apiHandler)
	mux.Handle("/", readerServer.Handler())
	server := &http.Server{Handler: reader.Headers(mux), ReadHeaderTimeout: 5 * time.Second}
	serving := make(chan error, 1)
	go func() { serving <- server.Serve(listener) }()
	defer server.Close()

	// Publish through the loopback listener, whatever address readers use.
	if err = publishAll(ctx, db, "http://"+loopback(listener.Addr())); err != nil {
		return err
	}
	if err = storeSnapshots(ctx, db); err != nil {
		return err
	}
	if o.Out != nil {
		fmt.Fprintln(o.Out, "Night Desk demo reader: "+base+"/")
	}
	select {
	case err = <-serving:
		return err
	case <-ctx.Done():
		return nil
	}
}

// loopback is addr with an unspecified host replaced by loopback.
func loopback(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func demoConfig(base, dir string) *config.Config {
	lat, lon := 42.4406, -76.4966
	return &config.Config{
		Server: config.Server{
			Listen:          "127.0.0.1:0",
			PublicBaseURL:   base,
			DataDir:         dir,
			SessionLifetime: "720h",
			// No sign-in: the demo holds no keys and fetches nothing, and
			// anyone who reaches it may look around.
			Auth: config.Auth{Mode: config.AuthNone, None: config.NoneAuth{AcknowledgePublicAccess: true}},
		},
		Papers: []config.Paper{{
			ID:       "ledger",
			Masthead: "The Evening Ledger",
			Place:    "Riverside",
			Look:     "broadsheet",
			Timezone: "America/New_York",
			// Weather stays off: it would call Open-Meteo. Sun and solunar
			// times are computed locally.
			Latitude: &lat, Longitude: &lon,
			Boxes: config.Boxes{Sun: true, Solunar: true, Markets: true},
			Sections: []config.Section{
				{Key: "world", Title: "World", Required: true},
				{Key: "economy", Title: "Economy"},
				{Key: "science", Title: "Science"},
				{Key: "local", Title: "Local", Required: true},
				{Key: "opinion", Title: "Opinion"},
			},
			RetentionDays: 14,
		}},
	}
}

// publishAll publishes every embedded edition, oldest first, through the agent
// API with a freshly issued key, so the reader sees what an agent would publish.
func publishAll(ctx context.Context, db *store.Store, base string) error {
	_, key, err := keys.Issue(ctx, db, "ledger", "demo")
	if err != nil {
		return err
	}
	files, err := editions.ReadDir("editions")
	if err != nil {
		return err
	}
	for _, f := range files {
		body, err := editions.ReadFile("editions/" + f.Name())
		if err != nil {
			return err
		}
		date := strings.TrimSuffix(path.Base(f.Name()), ".json")
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, base+"/api/v1/editions/"+date, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		reply, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("publishing %s: HTTP %d: %s", date, resp.StatusCode, reply)
		}
	}
	return nil
}
