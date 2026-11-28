package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/irashack/night-desk/internal/api"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/editorial"
	"github.com/irashack/night-desk/internal/images"
	"github.com/irashack/night-desk/internal/keys"
	"github.com/irashack/night-desk/internal/operator"
	"github.com/irashack/night-desk/internal/push"
	"github.com/irashack/night-desk/internal/reader"
	"github.com/irashack/night-desk/internal/scheduler"
	"github.com/irashack/night-desk/internal/store"
	"github.com/irashack/night-desk/internal/webhook/delivery"
)

func backend(args []string, out, errOut io.Writer) int {
	log := slog.New(slog.NewJSONHandler(errOut, nil))
	configPath := "nightdesk.yaml"
	hasConfig := false
	rest := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "--config" {
			if i+1 == len(args) || hasConfig {
				fmt.Fprintln(errOut, "usage: --config <file>")
				return 2
			}
			i++
			configPath = args[i]
			hasConfig = true
		} else {
			rest = append(rest, args[i])
		}
	}
	args = rest
	if len(args) == 0 {
		return 2
	}
	usage := func() int {
		fmt.Fprintln(errOut, "usage: nightdesk serve --config <file> | migrate | key issue <paper> [--label L] [--capabilities a,b,... | --worker] | key revoke <id> | key list [paper] | backup <dir> | accounts ... | quality ... (administrative commands accept --config <file>, default nightdesk.yaml)")
		return 2
	}
	var keyOptions issueKeyOptions
	switch args[0] {
	case "serve":
		if len(args) != 1 || !hasConfig {
			return usage()
		}
	case "migrate":
		if len(args) != 1 {
			return usage()
		}
	case "backup":
		if len(args) != 2 {
			return usage()
		}
	case "accounts":
		ok := len(args) == 2 && (args[1] == "list" || args[1] == "invite") || len(args) == 3 && (args[1] == "invite" || args[1] == "reset" || args[1] == "remove")
		if !ok {
			fmt.Fprintln(errOut, "usage: nightdesk accounts list | invite [username] | reset <username> | remove <username> [--config <file>]")
			return 2
		}
	case "key":
		if len(args) < 2 {
			return usage()
		}
		switch args[1] {
		case "issue":
			if len(args) < 3 {
				return usage()
			}
			opts, e := parseIssueKeyOptions(args[3:])
			if e != nil {
				fmt.Fprintln(errOut, e)
				return usage()
			}
			keyOptions = opts
		case "revoke":
			if len(args) != 3 {
				return usage()
			}
		case "list":
			if len(args) > 3 {
				return usage()
			}
		default:
			return usage()
		}
	}
	manager, e := config.NewManager(configPath)
	if e != nil {
		log.Error("configuration failed", "error", e)
		return 1
	}
	c := manager.Current()
	if args[0] == "backup" {
		// Open would create an empty database at a mistyped data_dir, and the
		// backup of it would succeed.
		if _, e := os.Stat(filepath.Join(c.Server.DataDir, "nightdesk.db")); e != nil {
			log.Error("backup failed: no nightdesk.db in server.data_dir", "data_dir", c.Server.DataDir)
			return 1
		}
	}
	db, e := store.Open(c.Server.DataDir)
	if e != nil {
		log.Error("database open failed")
		return 1
	}
	defer db.Close()
	ctx := context.Background()
	if e = db.Migrate(ctx); e != nil {
		log.Error("database migration failed", "error", e)
		return 1
	}
	switch args[0] {
	case "migrate":
		return 0
	case "backup":
		if e = db.Backup(ctx, args[1]); e != nil {
			log.Error("backup failed", "error", e)
			return 1
		}
		return 0
	case "key":
		switch args[1] {
		case "issue":
			if _, ok := c.Paper(args[2]); !ok {
				log.Error("unknown paper")
				return 1
			}
			label := keyOptions.label
			if strings.ContainsAny(label, "\r\n\x00") {
				log.Error("label must not contain control characters")
				return 1
			}
			id, key, e := keys.Issue(ctx, db, args[2], label, keyOptions.capabilities...)
			if e != nil {
				log.Error("key issue failed")
				return 1
			}
			fmt.Fprintln(out, key)
			log.Info("key issued", "id", id)
		case "revoke":
			if e = db.RevokeKey(ctx, args[2]); e != nil {
				log.Error("key revoke failed")
				return 1
			}
		case "list":
			paper := ""
			if len(args) == 3 {
				paper = args[2]
			}
			list, e := db.Keys(ctx, paper)
			if e != nil {
				log.Error("key list failed")
				return 1
			}
			if e = json.NewEncoder(out).Encode(list); e != nil {
				return 1
			}
		}
		return 0
	case "accounts":
		return accountsCommand(ctx, c, db, args[1:], out, errOut)
	case "quality":
		return qualityCommand(ctx, manager, db, log, args[1:], out, errOut)
	case "serve":
		return serve(manager, db, log)
	}
	return usage()
}
func serve(manager *config.Manager, db *store.Store, log *slog.Logger) int {
	c := manager.Current()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go manager.Watch(ctx, hup, log)
	var secret []byte
	if c.AuthMode() == config.AuthOIDC {
		var e error
		if secret, e = os.ReadFile(c.Server.OIDC.ClientSecretFile); e != nil {
			log.Error("OIDC client secret unavailable")
			return 1
		}
	}
	// Agent edits overlay the declared papers for both the reader and the agent API.
	desk, e := editorial.Open(ctx, db, manager.Current, log)
	if e != nil {
		log.Error("paper editorial load failed")
		return 1
	}
	readerServer, e := reader.New(ctx, db, desk.Config, strings.TrimSpace(string(secret)))
	if e != nil {
		log.Error("reader initialization failed", "error", e)
		return 1
	}
	readerServer.Editorial = desk
	readerServer.Log = log
	readerServer.Version = versionString()
	// Sessions from another sign-in mode never count; delete them so they
	// cannot come back if that mode is chosen again.
	if n, e := db.DeleteSessionsNotFrom(ctx, c.AcceptsIssuer); e != nil {
		log.Error("session cleanup failed")
		return 1
	} else if n > 0 {
		log.Info("signed out sessions from another sign-in mode", "sessions", n)
	}
	log.Info("reader sign-in", "mode", c.AuthMode())
	if c.AuthMode() == config.AuthNone {
		log.Warn("server.auth.mode is none: anyone who can reach this server reads and edits its papers")
	}
	if readerServer.PasskeyProblem != "" {
		log.Warn(readerServer.PasskeyProblem)
	}
	if code := readerServer.SetupCode(); code != "" {
		log.Warn("no accounts yet: open "+strings.TrimRight(c.Server.PublicBaseURL, "/")+"/setup and enter the setup code to create the administrator", "setup_code", code)
	}
	// The VAPID key signs web push requests; it is created on first serve.
	pushKey, e := push.LoadKey(c.Server.DataDir)
	if e != nil {
		log.Error("web push key unavailable")
		return 1
	}
	readerServer.Push = pushKey
	apiServer := &api.Server{Store: db, Config: desk.Config, Images: images.New(c.Server.DataDir), Log: log, Editorial: desk}
	operatorHandler, e := operator.New(db, desk.Config)
	if e != nil {
		log.Error("operator initialization failed", "error", e)
		return 1
	}
	apiHandler := apiServer.Handler()
	mux := http.NewServeMux()
	mux.Handle("/api/", apiHandler)
	mux.Handle("/api", apiHandler)
	mux.Handle("GET /healthz", apiHandler)
	mux.Handle("/operator", operatorHandler)
	mux.Handle("/", readerServer.Handler())
	handler := reader.Headers(mux)
	server := &http.Server{Addr: c.Server.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 75 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10, ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelError)}
	done := make(chan error, 1)
	listener, e := net.Listen("tcp", c.Server.Listen)
	if e != nil {
		log.Error("server listen failed", "error", e)
		return 1
	}
	schedulerDone := make(chan struct{})
	go func() {
		defer close(schedulerDone)
		(&scheduler.Scheduler{Store: db, Config: desk.Config, Publisher: apiServer, Log: log}).Run(ctx)
	}()
	deliveryDone := make(chan struct{})
	go func() {
		defer close(deliveryDone)
		(&delivery.Loop{Store: db, Config: manager.Current, Log: log}).Run(ctx)
	}()
	pushDone := make(chan struct{})
	go func() {
		defer close(pushDone)
		(&push.Loop{Key: pushKey, Store: db, Config: desk.Config, Log: log}).Run(ctx)
	}()
	defer func() { stop(); <-schedulerDone; <-deliveryDone; <-pushDone }()
	go func() { done <- server.Serve(listener) }()
	log.Info("server started", "listen", c.Server.Listen)
	select {
	case e := <-done:
		if !errors.Is(e, http.ErrServerClosed) {
			log.Error("server failed", "error", e)
			return 1
		}
		return 0
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if e := server.Shutdown(shutdown); e != nil {
			_ = server.Close()
			log.Error("shutdown timed out")
			return 1
		}
		if e := <-done; !errors.Is(e, http.ErrServerClosed) {
			log.Error("server failed")
			return 1
		}
		log.Info("server stopped")
		return 0
	}
}
