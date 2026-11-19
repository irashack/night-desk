package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/gather"
	"github.com/irashack/night-desk/internal/keys"
	"github.com/irashack/night-desk/internal/model"
	"github.com/irashack/night-desk/internal/push"
	"github.com/irashack/night-desk/internal/store"
)

func standalone(args []string, out, errOut io.Writer) int {
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(errOut)
	if args[0] == "check" {
		path := flags.String("config", "", "configuration file (required)")
		workerPath := flags.String("worker-config", "", "worker configuration file to validate and estimate drafting cost")
		if err := flags.Parse(args[1:]); err != nil {
			return 2
		}
		if *path == "" || flags.NArg() != 0 {
			fmt.Fprintln(errOut, "usage: nightdesk check --config FILE [--worker-config FILE]")
			return 2
		}
		c, err := config.Load(*path)
		if err == nil {
			err = c.Validate()
		}
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		fmt.Fprintln(out, "Configuration valid.")
		if *workerPath == "" {
			return 0
		}
		w, err := gather.LoadDraftConfig(*workerPath)
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		fmt.Fprintln(out, "Worker configuration valid.")
		if w.Models == nil {
			return 0
		}
		drafted := 0
		for _, p := range c.Papers {
			if p.Role() != "author" {
				drafted++
			}
		}
		ceiling, typical, ok := model.MonthlyCost(*w.Models, w.Limits, drafted)
		if !ok {
			fmt.Fprintln(out, "Set models.fast.price and models.writer.price to estimate drafting cost.")
			return 0
		}
		plural := "s"
		if drafted == 1 {
			plural = ""
		}
		fmt.Fprintf(out, "Drafting %d paper%s: at most $%.2f a month (31 runs of %d tokens each, every token at the dearest configured price); about $%.2f if the writer spends each budget at 80%% input and 20%% output.\n", drafted, plural, ceiling, w.Limits.MaxTokens, typical)
		return 0
	}
	dir := flags.String("dir", "", "new installation directory (required)")
	paper := flags.String("paper", "daily", "paper ID")
	masthead := flags.String("masthead", "The Daily", "paper masthead")
	timezone := flags.String("timezone", "UTC", "IANA timezone")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if *dir == "" || flags.NArg() != 0 {
		fmt.Fprintln(errOut, "usage: nightdesk init --dir PATH [--paper ID --masthead NAME --timezone TZ]")
		return 2
	}
	if err := initialize(*dir, *paper, *masthead, *timezone); err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	fmt.Fprintln(out, "Created config.yaml, nightdesk.db, agent.key and vapid.key. Edit the OIDC placeholders before serving.")
	return 0
}

func initialize(dir, paper, masthead, timezone string) error {
	const issuer = "https://identity.example.invalid"
	zero := 0.0
	c := config.Config{
		Server: config.Server{
			Listen: "0.0.0.0:8080", PublicBaseURL: "https://paper.example.invalid", DataDir: ".",
			SessionLifetime: "720h",
			OIDC:            config.OIDC{Issuer: issuer, ClientID: "REPLACE_WITH_OIDC_CLIENT_ID", ClientSecretFile: "oidc-client-secret", Scopes: []string{"openid"}},
		},
		Papers: []config.Paper{{
			ID: paper, Masthead: masthead, Look: "broadsheet", Timezone: timezone,
			Latitude: &zero, Longitude: &zero,
			Sections: []config.Section{{Key: "news", Title: "News", Required: true}},
			Readers:  []config.Reader{},
		}},
	}
	if err := c.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// Check all destinations before reserving any. O_EXCL below also prevents
	// concurrent initializers from replacing a file between the check and open.
	for _, name := range []string{"config.yaml", "agent.key", "nightdesk.db", "nightdesk.db-wal", "nightdesk.db-shm", "nightdesk.db-journal", push.KeyFile} {
		path := filepath.Join(dir, name)
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("refusing to overwrite %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	var created []string
	success := false
	defer func() {
		if !success {
			for _, path := range created {
				_ = os.Remove(path)
			}
		}
	}()
	files := make(map[string]*os.File)
	for _, name := range []string{"config.yaml", "agent.key", "nightdesk.db"} {
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		created = append(created, path)
		files[name] = f
		defer f.Close()
	}
	if err := files["nightdesk.db"].Close(); err != nil {
		return err
	}
	// Quoted user values remain scalar YAML strings, including punctuation and
	// Unicode. The template contains no credentials or guessed reader identity.
	text := fmt.Sprintf(`# Replace the example.invalid public URL and OIDC placeholders before serving.
server:
  listen: "0.0.0.0:8080"
  public_base_url: https://paper.example.invalid
  data_dir: .
  oidc:
    issuer: https://identity.example.invalid # REPLACE with your provider's issuer
    client_id: REPLACE_WITH_OIDC_CLIENT_ID
    client_secret_file: oidc-client-secret # Create separately; never commit secrets
papers:
  - id: %q
    masthead: %q
    look: broadsheet
    timezone: %q
    # Set your coordinates before enabling weather, sun or solunar boxes.
    latitude: 0
    longitude: 0
    sections:
      - key: news
        title: News
        required: true
    # Add verified OIDC identities: [{issuer: "https://your-issuer", subject: "your-subject"}]
    readers: []
`, paper, masthead, timezone)
	if _, err := io.WriteString(files["config.yaml"], text); err != nil {
		return err
	}
	if err := files["config.yaml"].Close(); err != nil {
		return err
	}
	// Use the same loader as serve, including path resolution and strict decoding.
	loaded, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		return err
	}
	db, err := store.Open(loaded.Server.DataDir)
	if err != nil {
		return fmt.Errorf("database initialization failed: %w", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		return fmt.Errorf("database migration failed: %w", err)
	}
	_, key, err := keys.Issue(ctx, db, paper, "Initial agent")
	if err != nil {
		return errors.New("initial agent key creation failed")
	}
	if _, err := io.WriteString(files["agent.key"], key+"\n"); err != nil {
		return errors.New("agent.key write failed")
	}
	if err := files["agent.key"].Close(); err != nil {
		return errors.New("agent.key close failed")
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("database close failed: %w", err)
	}
	vapid := filepath.Join(loaded.Server.DataDir, push.KeyFile)
	if _, err := push.CreateKey(vapid); err != nil {
		return errors.New("web push key creation failed")
	}
	created = append(created, vapid)
	success = true
	return nil
}
