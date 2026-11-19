package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/edition"
	"github.com/irashack/night-desk/internal/webhook"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	events   eventSettings
	db       *sql.DB
	dataDir  string
	backupMu sync.Mutex
}

func Open(dataDir string) (*Store, error) {
	if e := os.MkdirAll(dataDir, 0700); e != nil {
		return nil, e
	}
	path, e := filepath.Abs(filepath.Join(dataDir, "nightdesk.db"))
	if e != nil {
		return nil, e
	}
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{}
	for _, p := range []string{"journal_mode(WAL)", "busy_timeout(5000)", "foreign_keys(ON)"} {
		q.Add("_pragma", p)
	}
	u.RawQuery = q.Encode()
	db, e := sql.Open("sqlite", u.String())
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db, dataDir: dataDir}
	if e = s.Ping(context.Background()); e != nil {
		db.Close()
		return nil, e
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error {
	var one int
	return s.db.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func (s *Store) Migrate(ctx context.Context) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)"); e != nil {
		return e
	}
	paths, e := fs.Glob(migrations, "migrations/*.sql")
	if e != nil {
		return e
	}
	for _, p := range paths {
		var count int
		if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations WHERE version=?", p).Scan(&count); e != nil {
			return e
		}
		if count != 0 {
			continue
		}
		b, e := migrations.ReadFile(p)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, string(b)); e != nil {
			return fmt.Errorf("%s: %w", p, e)
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO schema_migrations VALUES (?,?)", p, now()); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func scopeID(scope auth.PaperScope) (string, error) {
	if !scope.Valid() {
		return "", auth.ErrUnauthorized
	}
	return scope.PaperID(), nil
}

type Image struct {
	SHA256        string
	Width, Height int
	Bytes         int64
}

// Publish replaces an edition atomically with its images and optional publication
// layout. Existing callers may omit the layout for pre-layout behavior.
func (s *Store) Publish(ctx context.Context, scope auth.PaperScope, date string, e *edition.Edition, images map[string]Image, layouts ...*EditionLayout) error {
	return s.publish(ctx, scope, date, e, images, nil, layouts...)
}

func (s *Store) publish(ctx context.Context, scope auth.PaperScope, date string, e *edition.Edition, images map[string]Image, diagnostics *PublishDiagnostics, layouts ...*EditionLayout) error {
	paper, err := scopeID(scope)
	if err != nil {
		return err
	}
	if _, err = edition.ParseDate(date); err != nil {
		return err
	}
	layout, err := encodeLayout(layouts)
	if err != nil {
		return err
	}
	eventPaper := s.eventPaper(paper)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = publishTx(ctx, tx, eventPaper, date, e, images, layout, diagnostics); err != nil {
		return err
	}
	return tx.Commit()
}

// publishTx is shared by legacy publication and revision-checked draft publication.
func publishTx(ctx context.Context, tx *sql.Tx, eventPaper config.Paper, date string, e *edition.Edition, images map[string]Image, layout *EditionLayout, diagnostics ...*PublishDiagnostics) error {
	paper := eventPaper.ID
	if err := checkItemIdentities(ctx, tx, paper, date, e); err != nil {
		return err
	}
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	publishedAt := now()
	coverage, err := coverageFor(ctx, tx, paper, e, layout, publishedAt)
	if err != nil {
		return err
	}
	var rawLayout any
	if layout != nil {
		raw, err := json.Marshal(layout)
		if err != nil {
			return err
		}
		rawLayout = string(raw)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO editions(paper_id,date,schema,body,published_at,layout,coverage) VALUES (?,?,?,?,?,?,?) ON CONFLICT(paper_id,date) DO UPDATE SET schema=excluded.schema,body=excluded.body,published_at=excluded.published_at,layout=excluded.layout,coverage=excluded.coverage`, paper, date, e.Schema, string(body), publishedAt, rawLayout, coverage)
	if err != nil {
		return err
	}
	if err = recordItemIdentities(ctx, tx, paper, date, e); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM edition_review WHERE paper_id=? AND date=?", paper, date); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM edition_images WHERE paper_id=? AND date=?", paper, date); err != nil {
		return err
	}
	for item, im := range images {
		if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO images VALUES (?,?,?,?,?)", im.SHA256, im.Width, im.Height, im.Bytes, now()); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO edition_images VALUES (?,?,?,?)", paper, date, item, im.SHA256); err != nil {
			return err
		}
	}
	if err = indexEdition(ctx, tx, paper, date, e); err != nil {
		return err
	}
	d := PublishDiagnostics{Path: "put", RetentionDays: eventPaper.RetentionDays}
	if len(diagnostics) > 0 && diagnostics[0] != nil {
		d = *diagnostics[0]
	}
	if err = diagnosticsTx(ctx, tx, paper, date, true, d); err != nil {
		return err
	}
	if err = enqueuePushTx(ctx, tx, paper, date, time.Now()); err != nil {
		return err
	}
	return enqueueEventTx(ctx, tx, eventPaper, webhook.EventEditionPublished, map[string]any{"date": date})
}

func (s *Store) Edition(ctx context.Context, scope auth.PaperScope, date string) (*edition.Edition, error) {
	ed, _, err := s.EditionWithLayout(ctx, scope, date)
	return ed, err
}

func (s *Store) EditionImages(ctx context.Context, scope auth.PaperScope, date string) (map[string]string, error) {
	paper, e := scopeID(scope)
	if e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(ctx, "SELECT item_id,image_sha256 FROM edition_images WHERE paper_id=? AND date=?", paper, date)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var id, sha string
		if e = rows.Scan(&id, &sha); e != nil {
			return nil, e
		}
		result[id] = sha
	}
	return result, rows.Err()
}

type Feedback struct {
	ID            int64  `json:"id"`
	Date          string `json:"date"`
	ItemID        string `json:"item_id"`
	Kind          string `json:"kind"`
	ReaderIssuer  string `json:"reader_issuer"`
	ReaderSubject string `json:"reader_subject"`
	At            string `json:"at"`
}

func (s *Store) AddFeedback(ctx context.Context, scope auth.PaperScope, f Feedback) error {
	paper, e := scopeID(scope)
	if e != nil {
		return e
	}
	eventPaper := s.eventPaper(paper)
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	_, e = tx.ExecContext(ctx, "INSERT INTO feedback(paper_id,date,item_id,kind,reader_issuer,reader_subject,at) VALUES (?,?,?,?,?,?,?)", paper, f.Date, f.ItemID, f.Kind, f.ReaderIssuer, f.ReaderSubject, now())
	if e != nil {
		return e
	}
	if e = enqueueEventTx(ctx, tx, eventPaper, webhook.EventFeedbackNew, map[string]any{}); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Feedback(ctx context.Context, scope auth.PaperScope, after int64, limit int) ([]Feedback, int64, error) {
	paper, e := scopeID(scope)
	if e != nil {
		return nil, after, e
	}
	if after < 0 || limit < 1 || limit > 500 {
		return nil, after, errors.New("invalid cursor or limit")
	}
	rows, e := s.db.QueryContext(ctx, "SELECT id,date,item_id,kind,reader_issuer,reader_subject,at FROM feedback WHERE paper_id=? AND id>? ORDER BY id LIMIT ?", paper, after, limit)
	if e != nil {
		return nil, after, e
	}
	defer rows.Close()
	result := []Feedback{}
	for rows.Next() {
		var f Feedback
		if e = rows.Scan(&f.ID, &f.Date, &f.ItemID, &f.Kind, &f.ReaderIssuer, &f.ReaderSubject, &f.At); e != nil {
			return nil, after, e
		}
		result = append(result, f)
		after = f.ID
	}
	return result, after, rows.Err()
}

// Key administration is deliberately separate from paper-scoped reader operations.
type Key struct {
	ID           string            `json:"id"`
	PaperID      string            `json:"paper_id"`
	Label        string            `json:"label"`
	CreatedAt    string            `json:"created_at"`
	LastUsedAt   *string           `json:"last_used_at"`
	RevokedAt    *string           `json:"revoked_at"`
	Capabilities []auth.Capability `json:"capabilities"`
}

func (s *Store) InsertKey(ctx context.Context, id, paper, label string, hash [32]byte, capabilities ...auth.Capability) error {
	names := auth.DefaultCapabilities
	if len(capabilities) > 0 {
		names = auth.CapabilityNames(capabilities)
	}
	if _, e := auth.ParseCapabilities(names); e != nil {
		return e
	}
	_, e := s.db.ExecContext(ctx, "INSERT INTO agent_keys(id,paper_id,hash,label,created_at,capabilities) VALUES (?,?,?,?,?,?)", id, paper, hash[:], label, now(), names)
	return e
}
func (s *Store) LookupAgentHash(ctx context.Context, hash [32]byte) (string, string, error) {
	var paper, capabilities string
	e := s.db.QueryRowContext(ctx, "UPDATE agent_keys SET last_used_at=? WHERE hash=? AND revoked_at IS NULL RETURNING paper_id,capabilities", now(), hash[:]).Scan(&paper, &capabilities)
	return paper, capabilities, e
}
func (s *Store) RevokeKey(ctx context.Context, id string) error {
	r, e := s.db.ExecContext(ctx, "UPDATE agent_keys SET revoked_at=COALESCE(revoked_at,?) WHERE id=?", now(), id)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e == nil && n == 0 {
		return sql.ErrNoRows
	}
	return e
}
func (s *Store) Keys(ctx context.Context, paper string) ([]Key, error) {
	rows, e := s.db.QueryContext(ctx, "SELECT id,paper_id,label,created_at,last_used_at,revoked_at,capabilities FROM agent_keys WHERE (?='' OR paper_id=?) ORDER BY created_at,id", paper, paper)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := []Key{}
	for rows.Next() {
		var k Key
		var names string
		if e = rows.Scan(&k.ID, &k.PaperID, &k.Label, &k.CreatedAt, &k.LastUsedAt, &k.RevokedAt, &names); e != nil {
			return nil, e
		}
		if k.Capabilities, e = auth.ParseCapabilities(names); e != nil {
			return nil, e
		}
		result = append(result, k)
	}
	return result, rows.Err()
}

// PushKeyFile is the web push (VAPID) private key's name in the data directory.
// Backup copies it: a restore without it silently stops every device's pushes.
const PushKeyFile = "vapid.key"

func (s *Store) Backup(ctx context.Context, dir string) error {
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	abs, e := filepath.Abs(dir)
	if e != nil {
		return e
	}
	source, e := filepath.Abs(s.dataDir)
	if e != nil {
		return e
	}
	if abs == source || strings.HasPrefix(abs, source+string(os.PathSeparator)) {
		return errors.New("backup destination must be outside data directory")
	}
	if e = os.MkdirAll(abs, 0700); e != nil {
		return e
	}
	dest := filepath.Join(abs, "nightdesk.db")
	if _, e = s.db.ExecContext(ctx, "VACUUM INTO ?", dest); e != nil {
		return e
	}
	// Content-addressed images are immutable and never removed during publication.
	root := filepath.Join(s.dataDir, "images")
	e = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if os.IsNotExist(walkErr) && path == root {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		target := filepath.Join(abs, "images", rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		if strings.HasPrefix(d.Name(), ".image-") {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("non-regular image file")
		}
		if e = os.Link(path, target); e == nil {
			return nil
		}
		in, e := os.Open(path)
		if e != nil {
			return e
		}
		defer in.Close()
		out, e := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		_, e = io.Copy(out, in)
		closeErr := out.Close()
		if e != nil {
			return e
		}
		return closeErr
	})
	if e != nil {
		return e
	}
	return copyPrivate(filepath.Join(s.dataDir, PushKeyFile), filepath.Join(abs, PushKeyFile))
}

// copyPrivate copies a private file, if it exists, to a new file at mode 0600.
func copyPrivate(from, to string) error {
	in, e := os.Open(from)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	defer in.Close()
	out, e := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	_, e = io.Copy(out, in)
	if closeErr := out.Close(); e == nil {
		e = closeErr
	}
	return e
}

// encodeLayout accepts at most one optional publication layout. Serialization
// happens in publishTx alongside the coverage snapshot.
func encodeLayout(layouts []*EditionLayout) (*EditionLayout, error) {
	if len(layouts) > 1 {
		return nil, errors.New("expected at most one edition layout")
	}
	if len(layouts) == 0 || layouts[0] == nil {
		return nil, nil
	}
	return layouts[0], nil
}
