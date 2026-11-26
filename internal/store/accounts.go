package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/irashack/night-desk/internal/auth"
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

// Invite is an unused invite or password-reset link. A reset names its
// account; an invite may name the account it creates.
type Invite struct {
	TokenHash [32]byte
	Reset     bool
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
	SignCount  uint32
	Created    time.Time
	LastUsed   time.Time
}

var (
	ErrAccountsExist = errors.New("an account already exists")
	ErrUsernameTaken = errors.New("username taken")
	ErrInviteInvalid = errors.New("invite invalid or expired")
	// ErrAccountChanged refuses a sign-in that raced a password change, a
	// reset, the account's removal or its passkey's removal.
	ErrAccountChanged = errors.New("account changed during sign-in")
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
	kind := "invite"
	if v.Reset {
		kind = "reset"
	}
	if v.Username != "" {
		// One link per name: a new one replaces the last.
		if _, err = tx.ExecContext(ctx, `DELETE FROM local_invites WHERE username=? AND kind=?`, v.Username, kind); err != nil {
			return err
		}
	}
	// A reset link is only for an account that exists as it is stored, so
	// one cannot outlive a removal and reset a later account of that name.
	res, err := tx.ExecContext(ctx, `INSERT INTO local_invites(token_hash,kind,username,created_by,created_at,expires_at) SELECT ?,?,?,?,?,? WHERE ?='invite' OR EXISTS(SELECT 1 FROM local_accounts WHERE username=?)`, v.TokenHash[:], kind, username, v.CreatedBy, stamp(v.Created), stamp(v.Expires), kind, v.Username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
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
	var created, expires, kind string
	err := s.db.QueryRowContext(ctx, `SELECT kind,username,created_by,created_at,expires_at FROM local_invites WHERE token_hash=? AND julianday(expires_at)>julianday(?)`, hash[:], stamp(now)).Scan(&kind, &username, &v.CreatedBy, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrInviteInvalid
	}
	if err != nil {
		return v, err
	}
	v.Username, v.Reset = username.String, kind == "reset"
	v.Created, _ = time.Parse(time.RFC3339Nano, created)
	v.Expires, _ = time.Parse(time.RFC3339Nano, expires)
	return v, nil
}

// RedeemInvite uses an invite once: it creates the account username (the
// invite's own name when it has one), or for a reset link sets the named
// account's password, removes its passkeys and signs it out everywhere: a
// reset recovers an account, so nothing an intruder added may survive it.
// It returns the account's username.
func (s *Store) RedeemInvite(ctx context.Context, hash [32]byte, username, passwordHash string, webauthnID []byte, now time.Time) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var named sql.NullString
	var kind string
	err = tx.QueryRowContext(ctx, `SELECT kind,username FROM local_invites WHERE token_hash=? AND julianday(expires_at)>julianday(?)`, hash[:], stamp(now)).Scan(&kind, &named)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrInviteInvalid
	}
	if err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM local_invites WHERE token_hash=?`, hash[:]); err != nil {
		return "", err
	}
	if named.Valid {
		username = named.String
	}
	if kind == "reset" {
		res, err := tx.ExecContext(ctx, `UPDATE local_accounts SET password_hash=?,password_changed_at=? WHERE username=?`, passwordHash, stamp(now), username)
		if err != nil {
			return "", err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return "", ErrInviteInvalid
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM passkeys WHERE username=?`, username); err != nil {
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

// SetPassword changes an account's password from the hash the current
// password was checked against (any, when oldHash is empty) and signs out
// every other session of it (keep, when set, stays signed in). A password
// changed meanwhile (a reset) is not overwritten: ErrAccountChanged.
func (s *Store) SetPassword(ctx context.Context, username, oldHash, hash string, now time.Time, keep *[32]byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE local_accounts SET password_hash=?,password_changed_at=? WHERE username=? AND (?='' OR password_hash=?)`, hash, stamp(now), username, oldHash, oldHash)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAccountChanged
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
	for _, q := range []string{`DELETE FROM passkeys WHERE username=?`, `DELETE FROM local_invites WHERE username=? AND kind='reset'`, `DELETE FROM local_accounts WHERE username=?`} {
		if _, err = tx.ExecContext(ctx, q, username); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// signOutAccountTx deletes the account's sessions (but keep) and the push
// subscriptions those sessions made.
func signOutAccountTx(ctx context.Context, tx *sql.Tx, username string, keep *[32]byte) error {
	var except any
	if keep != nil {
		except = keep[:]
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE session_hash IN (SELECT id_hash FROM sessions WHERE issuer=? AND subject=? AND (? IS NULL OR id_hash<>?))`, config.LocalIssuer, username, except, except); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE issuer=? AND subject=? AND (? IS NULL OR id_hash<>?)`, config.LocalIssuer, username, except, except)
	return err
}

// ErrTooManyPasskeys refuses a passkey beyond MaxPasskeys.
var ErrTooManyPasskeys = errors.New("too many passkeys")

// ErrPasskeyExists refuses a credential that is already registered.
var ErrPasskeyExists = errors.New("passkey already registered")

// MaxPasskeys bounds an account's passkeys.
const MaxPasskeys = 20

// AddPasskey stores a new credential for the account of the still-live
// session that registered it (a reset or removal meanwhile ends that session),
// unless the account already has MaxPasskeys.
func (s *Store) AddPasskey(ctx context.Context, p Passkey, session [32]byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var live bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE id_hash=? AND issuer=? AND subject=?)`, session[:], config.LocalIssuer, p.Username).Scan(&live); err != nil {
		return err
	}
	if !live {
		return ErrAccountChanged
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM passkeys WHERE credential_id=?)`, p.ID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrPasskeyExists
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO passkeys(credential_id,username,name,credential,sign_count,created_at) SELECT ?,?,?,?,?,? WHERE (SELECT count(*) FROM passkeys WHERE username=?)<?`, p.ID, p.Username, p.Name, string(p.Credential), p.SignCount, stamp(p.Created), p.Username, MaxPasskeys)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTooManyPasskeys
	}
	return tx.Commit()
}

// LocalAccount reports whether a local account exists and whether it
// administers the others; reader authentication asks on every request, so a
// removed account's session never reads again.
func (s *Store) LocalAccount(ctx context.Context, username string) (exists, admin bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT admin FROM local_accounts WHERE username=?`, username).Scan(&admin)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	return err == nil, admin, err
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

// PasskeyUse is a passkey sign-in to record with its session: the
// credential's new state and signature counter.
type PasskeyUse struct {
	ID         []byte
	Credential []byte
	SignCount  uint32
}

// InsertLocalSession signs a local account in: in one transaction it checks
// that the account still exists and, for a password sign-in, that its password
// hash is still verifiedHash (the one the password was checked against: a
// change or reset replaces it), records a passkey's use, and inserts the
// session. A passkey removed meanwhile, or whose counter did not move forward
// (an authenticator that counts never repeats a value), refuses it too.
func (s *Store) InsertLocalSession(ctx context.Context, v auth.Session, verifiedHash string, use *PasskeyUse) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ok bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM local_accounts WHERE username=? AND (?='' OR password_hash=?))`, v.Subject, verifiedHash, verifiedHash).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrAccountChanged
	}
	if use != nil {
		res, err := tx.ExecContext(ctx, `UPDATE passkeys SET credential=?,sign_count=?,last_used_at=? WHERE credential_id=? AND username=? AND (?=0 AND sign_count=0 OR sign_count<?)`, string(use.Credential), use.SignCount, stamp(v.Created), use.ID, v.Subject, use.SignCount, use.SignCount)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrAccountChanged
		}
	}
	groups, err := encodeGroups(v.Groups)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sessions(id_hash,issuer,subject,created_at,expires_at,last_seen_at,csrf,groups,groups_checked) VALUES (?,?,?,?,?,?,?,?,?)`, v.Hash[:], v.Issuer, v.Subject, stamp(v.Created), stamp(v.Expires), stamp(v.LastSeen), v.CSRF, groups, v.GroupsChecked); err != nil {
		return err
	}
	return tx.Commit()
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
