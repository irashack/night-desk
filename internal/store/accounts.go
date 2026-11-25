package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/irashack/night-desk/internal/config"
)

// Account is a local account (server.auth.mode local).
type Account struct {
	Username        string
	PasswordHash    string
	Admin           bool
	WebAuthnID      []byte
	Created         time.Time
	PasswordChanged time.Time
	Passkeys        int
}

// Invite is an unused invite (Username empty) or password-reset link.
type Invite struct {
	TokenHash [32]byte
	Username  string
	CreatedBy string
	Created   time.Time
	Expires   time.Time
}

// Passkey is one stored WebAuthn credential.
type Passkey struct {
	ID         []byte
	Username   string
	Name       string
	Credential []byte
	Created    time.Time
	LastUsed   time.Time
}

var (
	ErrAccountsExist = errors.New("an account already exists")
	ErrUsernameTaken = errors.New("username taken")
	ErrInviteInvalid = errors.New("invite invalid or expired")
)

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// CountAccounts reports how many local accounts exist.
func (s *Store) CountAccounts(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM local_accounts`).Scan(&n)
	return n, err
}

// CreateFirstAdmin creates the first account, an administrator, only while
// there is none.
func (s *Store) CreateFirstAdmin(ctx context.Context, username, hash string, webauthnID []byte, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM local_accounts`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrAccountsExist
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO local_accounts(username,password_hash,admin,webauthn_id,created_at,password_changed_at) VALUES (?,?,1,?,?,?)`, username, hash, webauthnID, stamp(now), stamp(now)); err != nil {
		return err
	}
	return tx.Commit()
}

func scanAccount(row interface{ Scan(...any) error }) (Account, error) {
	var a Account
	var created, changed string
	if err := row.Scan(&a.Username, &a.PasswordHash, &a.Admin, &a.WebAuthnID, &created, &changed, &a.Passkeys); err != nil {
		return a, err
	}
	a.Created, _ = time.Parse(time.RFC3339Nano, created)
	a.PasswordChanged, _ = time.Parse(time.RFC3339Nano, changed)
	return a, nil
}

const accountColumns = `username,password_hash,admin,webauthn_id,created_at,password_changed_at,(SELECT count(*) FROM passkeys p WHERE p.username=local_accounts.username)`

// Account returns one account; sql.ErrNoRows when there is none.
func (s *Store) Account(ctx context.Context, username string) (Account, error) {
	return scanAccount(s.db.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM local_accounts WHERE username=?`, username))
}

// AccountByWebAuthnID finds the account a passkey's user handle names.
func (s *Store) AccountByWebAuthnID(ctx context.Context, id []byte) (Account, error) {
	return scanAccount(s.db.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM local_accounts WHERE webauthn_id=?`, id))
}

// Accounts lists every account by username.
func (s *Store) Accounts(ctx context.Context) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+accountColumns+` FROM local_accounts ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CreateInvite stores an invite or reset link and drops expired ones.
func (s *Store) CreateInvite(ctx context.Context, v Invite) error {
	var username any
	if v.Username != "" {
		username = v.Username
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM local_invites WHERE julianday(expires_at)<=julianday(?)`, stamp(v.Created)); err != nil {
		return err
	}
	if v.Username != "" {
		// One reset link per account: a new one replaces the last.
		if _, err = tx.ExecContext(ctx, `DELETE FROM local_invites WHERE username=?`, v.Username); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO local_invites(token_hash,username,created_by,created_at,expires_at) VALUES (?,?,?,?,?)`, v.TokenHash[:], username, v.CreatedBy, stamp(v.Created), stamp(v.Expires)); err != nil {
		return err
	}
	return tx.Commit()
}

// PendingInvites counts unexpired invites and reset links.
func (s *Store) PendingInvites(ctx context.Context, now time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM local_invites WHERE julianday(expires_at)>julianday(?)`, stamp(now)).Scan(&n)
	return n, err
}

// RevokeInvites deletes every unused invite and reset link.
func (s *Store) RevokeInvites(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM local_invites`)
	return err
}

// LookupInvite returns an unexpired invite or reset link.
func (s *Store) LookupInvite(ctx context.Context, hash [32]byte, now time.Time) (Invite, error) {
	v := Invite{TokenHash: hash}
	var username sql.NullString
	var created, expires string
	err := s.db.QueryRowContext(ctx, `SELECT username,created_by,created_at,expires_at FROM local_invites WHERE token_hash=? AND julianday(expires_at)>julianday(?)`, hash[:], stamp(now)).Scan(&username, &v.CreatedBy, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrInviteInvalid
	}
	if err != nil {
		return v, err
	}
	v.Username = username.String
	v.Created, _ = time.Parse(time.RFC3339Nano, created)
	v.Expires, _ = time.Parse(time.RFC3339Nano, expires)
	return v, nil
}

// RedeemInvite uses an invite once: it creates the account username, or for a
// reset link sets the named account's password and signs it out everywhere.
// It returns the account's username.
func (s *Store) RedeemInvite(ctx context.Context, hash [32]byte, username, passwordHash string, webauthnID []byte, now time.Time) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var reset sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT username FROM local_invites WHERE token_hash=? AND julianday(expires_at)>julianday(?)`, hash[:], stamp(now)).Scan(&reset)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrInviteInvalid
	}
	if err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM local_invites WHERE token_hash=?`, hash[:]); err != nil {
		return "", err
	}
	if reset.Valid {
		username = reset.String
		if _, err = tx.ExecContext(ctx, `UPDATE local_accounts SET password_hash=?,password_changed_at=? WHERE username=?`, passwordHash, stamp(now), username); err != nil {
			return "", err
		}
		if err = signOutAccountTx(ctx, tx, username, nil); err != nil {
			return "", err
		}
	} else {
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM local_accounts WHERE username=?)`, username).Scan(&exists); err != nil {
			return "", err
		}
		if exists {
			return "", ErrUsernameTaken
		}
		// The first account administers the others, however it was made.
		if _, err = tx.ExecContext(ctx, `INSERT INTO local_accounts(username,password_hash,admin,webauthn_id,created_at,password_changed_at) VALUES (?,?,NOT EXISTS(SELECT 1 FROM local_accounts),?,?,?)`, username, passwordHash, webauthnID, stamp(now), stamp(now)); err != nil {
			return "", err
		}
	}
	return username, tx.Commit()
}

// SetPassword changes an account's password and signs out every other
// session of it (keep, when set, stays signed in).
func (s *Store) SetPassword(ctx context.Context, username, hash string, now time.Time, keep *[32]byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE local_accounts SET password_hash=?,password_changed_at=? WHERE username=?`, hash, stamp(now), username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	if err = signOutAccountTx(ctx, tx, username, keep); err != nil {
		return err
	}
	return tx.Commit()
}

// SetAdmin grants or removes administration; the last administrator stays.
func (s *Store) SetAdmin(ctx context.Context, username string, admin bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !admin {
		var others int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM local_accounts WHERE admin=1 AND username<>?`, username).Scan(&others); err != nil {
			return err
		}
		if others == 0 {
			return errors.New("the last administrator cannot be removed")
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE local_accounts SET admin=? WHERE username=?`, admin, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// RemoveAccount deletes an account with its passkeys, sessions and push
// subscriptions. Its feedback stays with the paper. The last administrator
// cannot be removed.
func (s *Store) RemoveAccount(ctx context.Context, username string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var admin bool
	err = tx.QueryRowContext(ctx, `SELECT admin FROM local_accounts WHERE username=?`, username).Scan(&admin)
	if err != nil {
		return err
	}
	if admin {
		var others int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM local_accounts WHERE admin=1 AND username<>?`, username).Scan(&others); err != nil {
			return err
		}
		if others == 0 {
			return errors.New("the last administrator cannot be removed")
		}
	}
	if err = signOutAccountTx(ctx, tx, username, nil); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE reader_issuer=? AND reader_subject=?`, config.LocalIssuer, username); err != nil {
		return err
	}
	for _, q := range []string{`DELETE FROM passkeys WHERE username=?`, `DELETE FROM local_invites WHERE username=?`, `DELETE FROM local_accounts WHERE username=?`} {
		if _, err = tx.ExecContext(ctx, q, username); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// signOutAccountTx deletes the account's sessions (but keep) and the push
// subscriptions those sessions made.
func signOutAccountTx(ctx context.Context, tx *sql.Tx, username string, keep *[32]byte) error {
	var except []byte
	if keep != nil {
		except = keep[:]
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE session_hash IN (SELECT id_hash FROM sessions WHERE issuer=? AND subject=? AND (? IS NULL OR id_hash<>?))`, config.LocalIssuer, username, except, except); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE issuer=? AND subject=? AND (? IS NULL OR id_hash<>?)`, config.LocalIssuer, username, except, except)
	return err
}

// AddPasskey stores a new credential.
func (s *Store) AddPasskey(ctx context.Context, p Passkey) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO passkeys(credential_id,username,name,credential,created_at) VALUES (?,?,?,?,?)`, p.ID, p.Username, p.Name, string(p.Credential), stamp(p.Created))
	return err
}

func scanPasskeys(rows *sql.Rows) ([]Passkey, error) {
	defer rows.Close()
	var out []Passkey
	for rows.Next() {
		var p Passkey
		var cred, created string
		var used sql.NullString
		if err := rows.Scan(&p.ID, &p.Username, &p.Name, &cred, &created, &used); err != nil {
			return nil, err
		}
		p.Credential = []byte(cred)
		p.Created, _ = time.Parse(time.RFC3339Nano, created)
		if used.Valid {
			p.LastUsed, _ = time.Parse(time.RFC3339Nano, used.String)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Passkeys lists an account's credentials, oldest first.
func (s *Store) Passkeys(ctx context.Context, username string) ([]Passkey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT credential_id,username,name,credential,created_at,last_used_at FROM passkeys WHERE username=? ORDER BY created_at`, username)
	if err != nil {
		return nil, err
	}
	return scanPasskeys(rows)
}

// UsePasskey records a sign-in: the credential's updated state (its signature
// counter) and when it was used.
func (s *Store) UsePasskey(ctx context.Context, id, credential []byte, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE passkeys SET credential=?,last_used_at=? WHERE credential_id=?`, string(credential), stamp(now), id)
	return err
}

// RemovePasskey deletes one of an account's credentials.
func (s *Store) RemovePasskey(ctx context.Context, username string, id []byte) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM passkeys WHERE username=? AND credential_id=?`, username, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// NormalUsername lowercases a username and reports whether it is allowed:
// 1 to 64 of a-z, 0-9, '.', '_' and '-', starting with a letter or digit.
func NormalUsername(v string) (string, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" || len(v) > 64 {
		return v, false
	}
	for i, r := range v {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || i > 0 && (r == '.' || r == '_' || r == '-')
		if !ok {
			return v, false
		}
	}
	return v, true
}
