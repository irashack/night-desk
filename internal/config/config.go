package config

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"
	"unicode"
	"unicode/utf8"

	"github.com/irashack/night-desk/internal/edition"
	"sigs.k8s.io/yaml"
)

type Config struct {
	Server Server  `json:"server"`
	Papers []Paper `json:"papers"`
	Gather Gather  `json:"gather"`
	Lean   Lean    `json:"lean"`
}
type Server struct {
	Operator      Operator `json:"operator,omitempty"`
	Listen        string   `json:"listen"`
	PublicBaseURL string   `json:"public_base_url"`
	DataDir       string   `json:"data_dir"`
	// Auth selects how readers sign in: oidc, proxy, local or none.
	Auth            Auth   `json:"auth,omitzero"`
	OIDC            OIDC   `json:"oidc,omitzero"`
	SessionLifetime string `json:"session_lifetime"`
	// GroupSessionLifetime bounds how long group membership read from the ID
	// token at sign-in grants a paper: a member removed from the group loses
	// that paper by then, whatever the session lifetime.
	GroupSessionLifetime string `json:"group_session_lifetime"`
}
type OIDC struct {
	Issuer           string   `json:"issuer"`
	ClientID         string   `json:"client_id"`
	ClientSecretFile string   `json:"client_secret_file"`
	Scopes           []string `json:"scopes"`
}
type Paper struct {
	Webhooks       []Webhook `json:"webhooks,omitempty"`
	Filters        *Filters  `json:"filters,omitempty"`
	AgentRole      string    `json:"agent_role,omitempty"`
	DeadlinePolicy string    `json:"deadline_policy,omitempty"`
	DraftLead      string    `json:"draft_lead,omitempty"`
	ID             string    `json:"id"`
	Masthead       string    `json:"masthead"`
	Place          string    `json:"place,omitempty"`
	Brief          *string   `json:"brief,omitempty"`
	Deadline       *string   `json:"deadline,omitempty"`
	Motto          *Motto    `json:"motto,omitempty"`
	Look           string    `json:"look"`
	Timezone       string    `json:"timezone"`
	Latitude       *float64  `json:"latitude"`
	Longitude      *float64  `json:"longitude"`
	Sections       []Section `json:"sections"`
	Boxes          Boxes     `json:"boxes"`
	Readers        []Reader  `json:"readers"`
	Sources        []Source  `json:"sources"`
	RetentionDays  int       `json:"retention_days"`
}
type Section struct {
	Lean     bool     `json:"lean"`
	Key      string   `json:"key"`
	Title    string   `json:"title"`
	Required bool     `json:"required"`
	Brief    *string  `json:"brief,omitempty"`
	Sources  []Source `json:"sources"`
	// FindSources lets gathering find this section's sources when it has none of
	// its own: a topic search feed at once, then outlet feeds a model suggests and
	// the worker verifies. Sections added from the reader set it.
	FindSources bool `json:"find_sources,omitempty"`
}
type Motto struct {
	Guidance string `json:"guidance"`
}
type Boxes struct {
	Weather bool `json:"weather"`
	Sun     bool `json:"sun"`
	Solunar bool `json:"solunar"`
	Markets bool `json:"markets"`
}
type Reader struct {
	Issuer      string `json:"issuer"`
	Subject     string `json:"subject,omitempty"`
	SubjectFile string `json:"subject_file,omitempty"`
	// Group admits every account whose ID token, at sign-in, lists this name in
	// its groups claim.
	Group   string `json:"group,omitempty"`
	subject string
}

// GroupsScope is the scope that asks the provider for the groups claim.
const GroupsScope = "groups"

// HasGroupReaders reports whether any paper admits readers by group.
func (c *Config) HasGroupReaders() bool {
	for _, p := range c.Papers {
		for _, r := range p.Readers {
			if r.Group != "" {
				return true
			}
		}
	}
	return false
}

// SignInScopes are the scopes sign-in asks for: the configured ones, plus
// groups when a paper has a group reader.
func (c *Config) SignInScopes() []string {
	scopes := append([]string{}, c.Server.OIDC.Scopes...)
	if len(scopes) == 0 {
		scopes = []string{"openid"}
	}
	if c.HasGroupReaders() {
		for _, s := range scopes {
			if s == GroupsScope {
				return scopes
			}
		}
		scopes = append(scopes, GroupsScope)
	}
	return scopes
}

// WantsGroups reports whether sign-in should keep the groups claim.
func (c *Config) WantsGroups() bool {
	for _, s := range c.SignInScopes() {
		if s == GroupsScope {
			return true
		}
	}
	return false
}

// GroupLifetime is how long sign-in's group membership grants papers.
func (c *Config) GroupLifetime() time.Duration {
	d, err := time.ParseDuration(c.Server.GroupSessionLifetime)
	if err != nil || d <= 0 {
		return 24 * time.Hour
	}
	return d
}

func (r Reader) ResolvedSubject() string {
	if r.SubjectFile != "" {
		return r.subject
	}
	return r.Subject
}

func (c *Config) Paper(id string) (Paper, bool) {
	for _, p := range c.Papers {
		if p.ID == id {
			return p, true
		}
	}
	return Paper{}, false
}
func (p Paper) Rules() edition.PaperRules {
	r := edition.PaperRules{}
	for _, s := range p.Sections {
		r.Sections = append(r.Sections, edition.SectionRule{Key: s.Key, Required: s.Required})
	}
	return r
}

const readerRule = "exactly one of subject, subject_file and group is required"

var slug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
var htmlTag = regexp.MustCompile(`<[/!?]?[a-zA-Z]`)
var deadline = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw, err := yaml.YAMLToJSONStrict(b)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	var shape any
	if err = json.Unmarshal(raw, &shape); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if err = configShape(shape, reflect.TypeFor[Config](), ""); err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.UnmarshalStrict(b, &c); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	for i := range c.Papers {
		if c.Papers[i].AgentRole == "" {
			c.Papers[i].AgentRole = "author"
		}
		if c.Papers[i].DeadlinePolicy == "" {
			c.Papers[i].DeadlinePolicy = "hold"
		}
	}
	if c.Server.SessionLifetime == "" {
		c.Server.SessionLifetime = "720h"
	}
	if c.Server.GroupSessionLifetime == "" {
		c.Server.GroupSessionLifetime = "24h"
	}
	if c.Server.OIDC.Scopes == nil && c.AuthMode() == AuthOIDC {
		c.Server.OIDC.Scopes = []string{"openid"}
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if err := c.loadWebhookSecrets(); err != nil {
		return nil, err
	}
	if err := c.expandSources(base); err != nil {
		return nil, err
	}
	if err := c.loadRatings(base); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(c.Server.DataDir) {
		c.Server.DataDir = filepath.Join(base, c.Server.DataDir)
	}
	if c.Server.OIDC.ClientSecretFile != "" && !filepath.IsAbs(c.Server.OIDC.ClientSecretFile) {
		c.Server.OIDC.ClientSecretFile = filepath.Join(base, c.Server.OIDC.ClientSecretFile)
	}
	if c.Server.Operator.KeyHashFile != "" && !filepath.IsAbs(c.Server.Operator.KeyHashFile) {
		c.Server.Operator.KeyHashFile = filepath.Join(base, c.Server.Operator.KeyHashFile)
	}
	return &c, nil
}
func configShape(v any, t reflect.Type, path string) error {
	if path == "" {
		path = "config"
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	bad := func(want string) error { return fmt.Errorf("%s: must be %s", path, want) }
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return bad("an object")
		}
		if t == reflect.TypeFor[Reader]() {
			n := 0
			for _, k := range []string{"subject", "subject_file", "group"} {
				if _, ok := obj[k]; ok {
					n++
				}
			}
			if n != 1 {
				return fmt.Errorf("%s.subject_file: %s", path, readerRule)
			}
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			if f.Tag.Get("json") == "-" {
				continue
			}
			fields[strings.Split(f.Tag.Get("json"), ",")[0]] = f.Type
		}
		for key, child := range obj {
			childPath := key
			if path != "config" {
				childPath = path + "." + key
			}
			typ, ok := fields[key]
			if !ok {
				return fmt.Errorf("%s: unknown field", childPath)
			}
			if e := configShape(child, typ, childPath); e != nil {
				return e
			}
		}
	case reflect.Slice:
		values, ok := v.([]any)
		if !ok {
			return bad("an array")
		}
		for i, value := range values {
			if e := configShape(value, t.Elem(), fmt.Sprintf("%s[%d]", path, i)); e != nil {
				return e
			}
		}
	case reflect.String:
		if v == "" && strings.HasSuffix(path, ".draft_lead") {
			return fmt.Errorf("%s: %s", path, draftLeadRule)
		}
		if v == "" && path == "lean.ratings_file" {
			return fmt.Errorf("%s: must be trimmed non-empty text", path)
		}
		if v == "" && strings.HasSuffix(path, ".agent_role") {
			return fmt.Errorf("%s: must be one of author, reviewer, none", path)
		}
		if v == "" && strings.HasSuffix(path, ".deadline_policy") {
			return fmt.Errorf("%s: must be one of hold, publish-unreviewed", path)
		}
		if _, ok := v.(string); !ok {
			return bad("a string")
		}
	case reflect.Bool:
		if _, ok := v.(bool); !ok {
			return bad("a boolean")
		}
	case reflect.Int:
		n, ok := v.(float64)
		if !ok || n != math.Trunc(n) {
			return bad("an integer")
		}
		if strings.HasSuffix(path, ".retention_days") && (n < 1 || n > 90) {
			return fmt.Errorf("%s: must be between 1 and 90", path)
		}
	case reflect.Float64:
		if _, ok := v.(float64); !ok {
			return bad("a number")
		}
	}
	return nil
}

// Validate resolves reader subject files before a configuration is published.
func (c *Config) Validate() error {
	var errs []string
	add := func(p, r string) { errs = append(errs, p+": "+r) }
	text := func(p, v string) { checkText(add, p, v) }
	issuer := func(p, v string) {
		u, e := url.Parse(v)
		if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			add(p, "must be an absolute https URL without credentials, query or fragment")
		}
	}
	_, port, e := net.SplitHostPort(c.Server.Listen)
	n, pe := strconv.Atoi(port)
	if e != nil || pe != nil || n < 1 || n > 65535 {
		add("server.listen", "must be host:port with port 1..65535")
	}
	u, e := url.Parse(c.Server.PublicBaseURL)
	if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback()))) {
		add("server.public_base_url", "must be an https origin (http allowed on loopback)")
	}
	if c.Server.Operator.KeyHashFile != "" {
		text("server.operator.key_hash_file", c.Server.Operator.KeyHashFile)
	}
	text("server.data_dir", c.Server.DataDir)
	c.checkAuth(add)
	mode := c.AuthMode()
	if mode == AuthOIDC {
		issuer("server.oidc.issuer", c.Server.OIDC.Issuer)
		text("server.oidc.client_id", c.Server.OIDC.ClientID)
		text("server.oidc.client_secret_file", c.Server.OIDC.ClientSecretFile)
		scopes := map[string]bool{}
		for i, s := range c.Server.OIDC.Scopes {
			if s == "" || strings.ContainsFunc(s, func(r rune) bool { return r < 0x21 || r > 0x7e || r == '"' || r == '\\' }) || scopes[s] {
				add(fmt.Sprintf("server.oidc.scopes[%d]", i), "must be a unique non-empty scope")
			}
			scopes[s] = true
		}
		if !scopes["openid"] {
			add("server.oidc.scopes", "must include openid")
		}
	} else if len(c.Server.OIDC.Scopes) > 0 {
		add("server.oidc", "is only read when server.auth.mode is oidc; remove it")
	}
	readerIssuer := c.ReaderIssuer()
	d, e := time.ParseDuration(c.Server.SessionLifetime)
	if e != nil || d <= 0 {
		add("server.session_lifetime", "must be a positive duration")
	}
	if d, e := time.ParseDuration(c.Server.GroupSessionLifetime); c.Server.GroupSessionLifetime != "" && (e != nil || d <= 0) {
		add("server.group_session_lifetime", "must be a positive duration")
	}
	if len(c.Papers) == 0 {
		add("papers", "must contain at least one paper")
	}
	c.Gather.check(add)
	if c.Lean.RatingsFile != "" {
		text("lean.ratings_file", c.Lean.RatingsFile)
	}
	ids := map[string]bool{}
	for i, p := range c.Papers {
		b := fmt.Sprintf("papers[%d].", i)
		if !slug.MatchString(p.ID) || ids[p.ID] {
			add(b+"id", "must be a unique slug")
		}
		ids[p.ID] = true
		if p.Role() != "author" && p.Role() != "reviewer" && p.Role() != "none" {
			add(b+"agent_role", "must be one of author, reviewer, none")
		}
		if p.Policy() != "hold" && p.Policy() != "publish-unreviewed" {
			add(b+"deadline_policy", "must be one of hold, publish-unreviewed")
		}
		if p.DraftLead == "" {
			p.DraftLead = DefaultDraftLead
			c.Papers[i].DraftLead = DefaultDraftLead
		}
		if _, err := p.DraftLeadTime(); err != nil {
			add(b+"draft_lead", draftLeadRule)
		}
		if p.RetentionDays == 0 {
			p.RetentionDays = 14
			c.Papers[i].RetentionDays = 14
		}
		if p.RetentionDays < 1 || p.RetentionDays > 90 {
			add(b+"retention_days", "must be between 1 and 90")
		}
		checkSources(add, b+"sources", p.Sources, true)
		if p.Filters != nil {
			p.Filters.check(add, b+"filters.")
		}
		checkWebhooks(add, b+"webhooks", p.Webhooks)
		p.Editorial().check(add, b, true)
		if p.Look != "broadsheet" {
			add(b+"look", "must be broadsheet")
		}
		if _, e := time.LoadLocation(p.Timezone); e != nil || p.Timezone == "" || p.Timezone == "Local" {
			add(b+"timezone", "must be an IANA timezone")
		}
		for _, v := range []struct {
			name  string
			value *float64
			cap   float64
		}{{"latitude", p.Latitude, 90}, {"longitude", p.Longitude, 180}} {
			if v.value == nil || math.IsNaN(*v.value) || math.IsInf(*v.value, 0) || math.Abs(*v.value) > v.cap {
				add(b+v.name, fmt.Sprintf("must be between -%g and %g", v.cap, v.cap))
			}
		}
		readers := map[[2]string]bool{}
		groups := map[[2]string]bool{}
		for j, r := range p.Readers {
			path := fmt.Sprintf("%sreaders[%d].", b, j)
			switch {
			case mode == AuthNone:
				add(strings.TrimSuffix(path, "."), "server.auth.mode none has one reader, who reads every paper; remove readers")
				continue
			case readerIssuer == "":
				issuer(path+"issuer", r.Issuer)
			case r.Issuer == "" || r.Issuer == readerIssuer:
				// Proxy and local readers name only a user or group; the
				// mode's issuer is filled in, so sessions match them.
				c.Papers[i].Readers[j].Issuer = readerIssuer
				r.Issuer = readerIssuer
			default:
				add(path+"issuer", "must be omitted when server.auth.mode is "+mode)
			}
			set := 0
			for _, v := range []string{r.Subject, r.SubjectFile, r.Group} {
				if v != "" {
					set++
				}
			}
			if set != 1 {
				add(path+"subject_file", readerRule)
				continue
			}
			if r.Group != "" {
				switch {
				case mode == AuthLocal:
					add(path+"group", "local accounts have no groups; name each reader's subject, or leave readers empty so every account reads the paper")
				case mode == AuthProxy && c.Server.Auth.Proxy.GroupsHeader == "":
					add(path+"group", "needs server.auth.proxy.groups_header")
				}
				text(path+"group", r.Group)
				if len(r.Group) > 256 {
					add(path+"group", "must be at most 256 bytes")
				}
				identity := [2]string{r.Issuer, r.Group}
				if groups[identity] {
					add(strings.TrimSuffix(path, "."), "duplicate issuer/group pair")
				}
				groups[identity] = true
				continue
			}
			subject := r.Subject
			if r.SubjectFile != "" {
				text(path+"subject_file", r.SubjectFile)
				if !filepath.IsAbs(r.SubjectFile) {
					add(path+"subject_file", "must be an absolute path")
					continue
				}
				data, err := os.ReadFile(r.SubjectFile)
				if err != nil {
					add(path+"subject_file", "must name a readable subject file")
					continue
				}
				subject = strings.TrimSpace(string(data))
				if subject == "" || !utf8.ValidString(subject) || strings.ContainsFunc(subject, unicode.IsControl) {
					add(path+"subject_file", "must contain non-empty subject text")
					continue
				}
			} else {
				text(path+"subject", subject)
			}
			c.Papers[i].Readers[j].subject = subject
			identity := [2]string{r.Issuer, subject}
			if readers[identity] {
				add(strings.TrimSuffix(path, "."), "duplicate issuer/subject pair")
			}
			readers[identity] = true
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// Published configurations are immutable snapshots.
type Manager struct {
	mu      sync.RWMutex
	path    string
	current *Config
	mtime   time.Time
}

func NewManager(path string) (*Manager, error) {
	st, e := os.Stat(path)
	if e != nil {
		return nil, e
	}
	c, e := Load(path)
	if e != nil {
		return nil, e
	}
	return &Manager{path: path, current: c, mtime: st.ModTime()}, nil
}
func (m *Manager) Current() *Config { m.mu.RLock(); defer m.mu.RUnlock(); return m.current }
func (m *Manager) Reload(force bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, e := os.Stat(m.path)
	if e != nil {
		return e
	}
	if !force && st.ModTime().Equal(m.mtime) {
		return nil
	}
	c, e := Load(m.path)
	if e != nil {
		return e
	}
	// A listener and database cannot be swapped by a paper configuration reload.
	if !reflect.DeepEqual(c.Server, m.current.Server) {
		return fmt.Errorf("server: settings changed; restart required")
	}
	m.current = c
	m.mtime = st.ModTime()
	return nil
}
func (m *Manager) Watch(ctx context.Context, hup <-chan os.Signal, log *slog.Logger) {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		force := false
		select {
		case <-ctx.Done():
			return
		case <-hup:
			force = true
		case <-tick.C:
		}
		if e := m.Reload(force); e != nil {
			log.Error("config reload rejected", "error", e)
		}
	}
}
