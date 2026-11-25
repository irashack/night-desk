package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/irashack/night-desk/internal/auth"
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
		if remaining := placeholders(c); len(remaining) > 0 {
			fmt.Fprintln(errOut, "Configuration valid, but not ready to serve:")
			for _, p := range remaining {
				fmt.Fprintln(errOut, "  "+p)
			}
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
	mode := flags.String("auth", config.AuthLocal, "reader sign-in: local, oidc, proxy or none")
	publicURL := flags.String("public-url", "http://localhost:8080", "the address readers open (https, or http on localhost)")
	standalone := flags.Bool("standalone", false, "a paper Night Desk writes itself: agent_role none, a deadline, sections that find their own sources, and a worker key")
	interactive := flags.Bool("interactive", false, "ask for each setting (implies --standalone)")
	deadline := flags.String("deadline", "06:30", "with --standalone, when the paper is published (HH:MM, local time)")
	sections := flags.String("sections", "World,Science,Technology,Business", "with --standalone, comma-separated section titles")
	units := flags.String("units", "", "weather units: imperial or metric (default by --locale region)")
	locale := flags.String("locale", "en-US", "language and region of news searches, such as en-GB")
	place := flags.String("place", "", "place name shown on the masthead and weather box")
	coordinates := flags.String("coordinates", "", "LATITUDE,LONGITUDE for weather and sunrise boxes, such as 51.51,-0.13")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if *dir == "" || flags.NArg() != 0 {
		fmt.Fprintln(errOut, "usage: nightdesk init --dir PATH [--paper ID --masthead NAME --timezone TZ --auth local|oidc|proxy|none --public-url URL]\n       [--standalone --deadline HH:MM --sections A,B --units metric|imperial --locale xx-YY --place NAME --coordinates LAT,LON] [--interactive]")
		return 2
	}
	o := initOptions{paper: *paper, masthead: *masthead, timezone: *timezone, mode: *mode, publicURL: *publicURL,
		standalone: *standalone || *interactive, deadline: *deadline, units: *units, locale: *locale, place: *place, coordinates: *coordinates}
	o.sections = splitList(*sections)
	if *interactive {
		set := map[string]bool{}
		flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
		if err := ask(stdin, out, &o, set); err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
	}
	if err := initialize(*dir, o); err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	if o.standalone {
		fmt.Fprintln(out, "Created config.yaml, nightdesk.db, agent.key, vapid.key and worker/worker.key.")
		fmt.Fprintln(out, "The paper publishes at "+o.deadline+" ("+o.timezone+") from what the worker gathers: run it with the worker key")
		fmt.Fprintln(out, "(docs/install.md, \"Gathering and optional models\"). Without a model it prints headlines, excerpts and links.")
	} else {
		fmt.Fprintln(out, "Created config.yaml, nightdesk.db, agent.key and vapid.key.")
	}
	switch *mode {
	case config.AuthOIDC:
		fmt.Fprintln(out, "Edit the OIDC placeholders and add your reader identity before serving.")
	case config.AuthProxy:
		fmt.Fprintln(out, "Set server.auth.proxy.trusted_proxies to your proxy's address and add your readers before serving.")
	case config.AuthLocal:
		fmt.Fprintln(out, "Serve it, then open "+strings.TrimRight(*publicURL, "/")+"/setup with the setup code from the log to create your account.")
	case config.AuthNone:
		fmt.Fprintln(out, "Serve it and open "+*publicURL+". There is no sign-in: keep it on a private network.")
	}
	return 0
}

// placeholders lists what `init` leaves for the operator to fill in: the
// example.invalid hosts, the OIDC client placeholder and empty reader lists.
func placeholders(c *config.Config) []string {
	var found []string
	placeholderHost := func(field, raw string) {
		if u, err := url.Parse(raw); err == nil {
			host := strings.ToLower(u.Hostname())
			if host == "invalid" || strings.HasSuffix(host, ".invalid") {
				found = append(found, fmt.Sprintf("%s: replace the placeholder host %s", field, u.Hostname()))
			}
		}
	}
	placeholderHost("server.public_base_url", c.Server.PublicBaseURL)
	mode := c.AuthMode()
	if mode == config.AuthOIDC {
		placeholderHost("server.oidc.issuer", c.Server.OIDC.Issuer)
		if strings.HasPrefix(c.Server.OIDC.ClientID, "REPLACE_") {
			found = append(found, "server.oidc.client_id: replace "+c.Server.OIDC.ClientID+" with your provider's client ID")
		}
	}
	for i, p := range c.Papers {
		// Local accounts read papers without readers; none has one reader.
		if len(p.Readers) == 0 && (mode == config.AuthOIDC || mode == config.AuthProxy) {
			found = append(found, fmt.Sprintf("papers[%d].readers: add at least one verified reader identity", i))
		}
		for j, r := range p.Readers {
			placeholderHost(fmt.Sprintf("papers[%d].readers[%d].issuer", i, j), r.Issuer)
		}
	}
	return found
}

type initOptions struct {
	paper, masthead, timezone, mode, publicURL string
	// A standalone paper: Night Desk drafts and publishes it at deadline from
	// sections that find their own sources.
	standalone                                  bool
	deadline, units, locale, place, coordinates string
	sections                                    []string
}

func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// imperialRegions use imperial weather units by default.
var imperialRegions = map[string]bool{"US": true, "LR": true, "MM": true}

// defaultUnits picks weather units from a locale's region.
func defaultUnits(locale string) string {
	_, region, _ := strings.Cut(locale, "-")
	if imperialRegions[region] {
		return "imperial"
	}
	return "metric"
}

// sectionKey turns a section title into a key: lowercase letters, digits and
// dashes.
func sectionKey(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// paperDetails validates the standalone and place settings and returns the
// paper's coordinates (nil when none were given).
func (o *initOptions) paperDetails() (lat, lon *float64, err error) {
	if o.locale == "" {
		o.locale = "en-US"
	}
	if o.units == "" {
		o.units = defaultUnits(o.locale)
	}
	if o.coordinates != "" {
		a, b, ok := strings.Cut(o.coordinates, ",")
		x, e1 := strconv.ParseFloat(strings.TrimSpace(a), 64)
		y, e2 := strconv.ParseFloat(strings.TrimSpace(b), 64)
		if !ok || e1 != nil || e2 != nil {
			return nil, nil, errors.New("--coordinates must be LATITUDE,LONGITUDE, such as 51.51,-0.13")
		}
		lat, lon = &x, &y
	}
	if o.standalone {
		if len(o.sections) == 0 {
			return nil, nil, errors.New("--sections must name at least one section")
		}
		seen := map[string]bool{}
		for _, title := range o.sections {
			key := sectionKey(title)
			if key == "" || seen[key] {
				return nil, nil, fmt.Errorf("--sections: %q needs letters or digits, and a key of its own", title)
			}
			seen[key] = true
		}
	}
	return lat, lon, nil
}

// authYAML is the server.auth (and server.oidc) part of a new configuration.
func authYAML(mode string) (string, error) {
	switch mode {
	case config.AuthLocal:
		return `  auth:
    mode: local # password accounts; the first is created at /setup
    local:
      passkeys: true
`, nil
	case config.AuthOIDC:
		return `  auth:
    mode: oidc
  oidc:
    issuer: https://identity.example.invalid # REPLACE with your provider's issuer
    client_id: REPLACE_WITH_OIDC_CLIENT_ID
    client_secret_file: oidc-client-secret # Create separately; never commit secrets
`, nil
	case config.AuthProxy:
		return `  auth:
    mode: proxy
    proxy:
      user_header: Remote-User
      # groups_header: Remote-Groups
      trusted_proxies: ["127.0.0.1/32"] # REPLACE with your reverse proxy's address
`, nil
	case config.AuthNone:
		return `  auth:
    mode: none # no sign-in: one reader, on a private network only
`, nil
	}
	return "", fmt.Errorf("--auth must be one of local, oidc, proxy, none")
}

func initialize(dir string, o initOptions) error {
	paper, masthead, timezone := o.paper, o.masthead, o.timezone
	if o.mode == "" {
		o.mode = config.AuthLocal
	}
	if o.publicURL == "" {
		o.publicURL = "http://localhost:8080"
	}
	authText, err := authYAML(o.mode)
	if err != nil {
		return err
	}
	lat, lon, err := o.paperDetails()
	if err != nil {
		return err
	}
	zero := 0.0
	p := config.Paper{
		ID: paper, Masthead: masthead, Look: "broadsheet", Timezone: timezone, Place: o.place,
		Units: o.units, Locale: o.locale,
		Latitude: &zero, Longitude: &zero,
		Sections: []config.Section{{Key: "news", Title: "News", Required: true}},
		Readers:  []config.Reader{},
	}
	if lat != nil {
		p.Latitude, p.Longitude = lat, lon
		p.Boxes = config.Boxes{Weather: true, Sun: true}
	}
	if o.standalone {
		p.AgentRole, p.Deadline = "none", &o.deadline
		p.Sections = nil
		for _, title := range o.sections {
			p.Sections = append(p.Sections, config.Section{Key: sectionKey(title), Title: title, FindSources: true})
		}
	}
	c := config.Config{
		Server: config.Server{
			Listen: "0.0.0.0:8080", PublicBaseURL: o.publicURL, DataDir: ".",
			SessionLifetime: "720h",
			Auth:            config.Auth{Mode: o.mode},
		},
		Papers: []config.Paper{p},
	}
	switch o.mode {
	case config.AuthOIDC:
		c.Server.OIDC = config.OIDC{Issuer: "https://identity.example.invalid", ClientID: "REPLACE_WITH_OIDC_CLIENT_ID", ClientSecretFile: "oidc-client-secret", Scopes: []string{"openid"}}
	case config.AuthProxy:
		c.Server.Auth.Proxy = config.ProxyAuth{UserHeader: "Remote-User", TrustedProxies: []string{"127.0.0.1/32"}}
	case config.AuthLocal:
		c.Server.Auth.Local.Passkeys = true
	case config.AuthNone:
		// A container's 0.0.0.0 is checked when serving, not here.
		c.Server.Listen = "127.0.0.1:8080"
	}
	if err := c.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// Check all destinations before reserving any. O_EXCL below also prevents
	// concurrent initializers from replacing a file between the check and open.
	for _, name := range []string{"config.yaml", "agent.key", "nightdesk.db", "nightdesk.db-wal", "nightdesk.db-shm", "nightdesk.db-journal", push.KeyFile, "worker"} {
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
	readers := "    # Every local account reads a paper whose readers list is empty;\n    # name usernames here to limit it: [{subject: alice}]\n    readers: []\n"
	switch o.mode {
	case config.AuthOIDC:
		readers = "    # Add verified OIDC identities: [{issuer: \"https://your-issuer\", subject: \"your-subject\"}]\n    readers: []\n"
	case config.AuthProxy:
		readers = "    # Add the users your proxy signs in: [{subject: alice}] or [{group: family}]\n    readers: []\n"
	case config.AuthNone:
		readers = ""
	}
	text := fmt.Sprintf(`# Night Desk configuration. docs/configuration.md describes every setting.
server:
  listen: %q
  public_base_url: %q # the address readers open: https, or http on localhost
  data_dir: .
%spapers:
  - id: %q
    masthead: %q
    look: broadsheet
    timezone: %q
%s%s`, c.Server.Listen, o.publicURL, authText, paper, masthead, timezone, paperYAML(p), readers)
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
	if o.standalone {
		// The worker's own key, in a directory of its own to mount into the
		// worker alone: it gathers and drafts, and cannot publish or edit.
		workerDir := filepath.Join(dir, "worker")
		if err := os.Mkdir(workerDir, 0700); err != nil {
			return err
		}
		created = append(created, workerDir)
		capabilities, err := auth.ParseCapabilities(auth.WorkerCapabilities)
		if err != nil {
			return err
		}
		_, workerKey, err := keys.Issue(ctx, db, paper, "Worker", capabilities...)
		if err != nil {
			return errors.New("worker key creation failed")
		}
		path := filepath.Join(workerDir, "worker.key")
		if err := os.WriteFile(path, []byte(workerKey+"\n"), 0600); err != nil {
			return errors.New("worker.key write failed")
		}
		created = append([]string{path}, created...)
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

// paperYAML writes a new paper's settings after its timezone.
func paperYAML(p config.Paper) string {
	var b strings.Builder
	if p.Place != "" {
		fmt.Fprintf(&b, "    place: %q\n", p.Place)
	}
	fmt.Fprintf(&b, "    units: %s # imperial (°F, mph) or metric (°C, km/h)\n", p.Units)
	fmt.Fprintf(&b, "    locale: %s # language and region of news searches\n", p.Locale)
	if p.AgentRole != "" {
		b.WriteString("    agent_role: none # Night Desk drafts and publishes the paper itself\n")
		fmt.Fprintf(&b, "    deadline: %q # published by this local time\n", *p.Deadline)
	}
	if p.Boxes.Weather {
		fmt.Fprintf(&b, "    latitude: %g\n    longitude: %g\n    boxes: {weather: true, sun: true}\n", *p.Latitude, *p.Longitude)
	} else {
		b.WriteString("    # Set your coordinates before enabling weather, sun or solunar boxes.\n    latitude: 0\n    longitude: 0\n")
	}
	b.WriteString("    sections:\n")
	for _, s := range p.Sections {
		fmt.Fprintf(&b, "      - key: %s\n        title: %q\n", s.Key, s.Title)
		if s.Required {
			b.WriteString("        required: true\n")
		}
		if s.FindSources {
			b.WriteString("        find_sources: true # a news search for the title until feeds are found\n")
		}
	}
	return b.String()
}
