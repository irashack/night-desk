// Command demo runs the Night Desk reader on loopback with a fictional demo
// paper, for screenshots and local previews. It is a development tool, not part
// of the nightdesk binary: it wires the real reader and agent API the way
// `nightdesk serve` does, signs readers in through a fake OIDC provider that
// approves everyone, keeps its data in a temporary directory, and publishes the
// editions under editions/ through the real publish endpoint.
//
//	go run ./docs/screenshots/demo
//
// Open the printed URL; it signs you in and shows the latest edition.
package main

import (
	"bytes"
	"context"
	"embed"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
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

const subject = "demo-reader"

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "loopback listen address for the reader (port 0 picks a free port)")
	keep := flag.Bool("keep", false, "keep the temporary data directory on exit")
	flag.Parse()
	if err := run(*addr, *keep); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

func run(addr string, keep bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("-addr must be a literal loopback address, such as 127.0.0.1:8080")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	dir, err := os.MkdirTemp("", "nightdesk-demo-")
	if err != nil {
		return err
	}
	if keep {
		fmt.Fprintln(os.Stderr, "demo: data directory", dir)
	} else {
		defer os.RemoveAll(dir)
	}

	issuer, err := startProvider(ctx)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	base := "http://" + listener.Addr().String()
	cfg := demoConfig(issuer, base, dir)
	current := func() *config.Config { return cfg }

	db, err := store.Open(dir)
	if err != nil {
		return err
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		return err
	}
	desk, err := editorial.Open(ctx, db, current, log)
	if err != nil {
		return err
	}
	readerServer, err := reader.New(ctx, db, desk.Config, clientSecret)
	if err != nil {
		return err
	}
	readerServer.Editorial = desk
	if readerServer.Push, err = push.LoadKey(dir); err != nil {
		return err
	}
	apiServer := &api.Server{Store: db, Config: desk.Config, Images: &artFetcher{dataDir: dir}, Log: log, Editorial: desk}
	mux := http.NewServeMux()
	apiHandler := apiServer.Handler()
	mux.Handle("/api/", apiHandler)
	mux.Handle("/api", apiHandler)
	mux.Handle("/", readerServer.Handler())
	server := &http.Server{Handler: reader.Headers(mux), ReadHeaderTimeout: 5 * time.Second}
	serving := make(chan error, 1)
	go func() { serving <- server.Serve(listener) }()
	defer server.Close()

	if err = publishAll(ctx, db, base); err != nil {
		return err
	}
	if err = storeSnapshots(ctx, db); err != nil {
		return err
	}
	fmt.Println("Night Desk demo reader: " + base + "/login")
	select {
	case err = <-serving:
		return err
	case <-ctx.Done():
		return nil
	}
}

func demoConfig(issuer, base, dir string) *config.Config {
	lat, lon := 42.4406, -76.4966
	return &config.Config{
		Server: config.Server{
			Listen:          strings.TrimPrefix(base, "http://"),
			PublicBaseURL:   base,
			DataDir:         dir,
			SessionLifetime: "720h",
			OIDC:            config.OIDC{Issuer: issuer, ClientID: clientID, Scopes: []string{"openid"}},
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
			Readers:       []config.Reader{{Issuer: issuer, Subject: subject}},
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
