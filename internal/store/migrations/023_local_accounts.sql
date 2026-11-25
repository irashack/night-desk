-- Local accounts for server.auth.mode local: a password (an argon2id hash),
-- whether the account administers the others, and the random WebAuthn user
-- handle its passkeys are registered under. Sessions, feedback and push
-- subscriptions name an account by issuer "local" and its username.
CREATE TABLE local_accounts (
 username TEXT PRIMARY KEY,
 password_hash TEXT NOT NULL,
 admin INTEGER NOT NULL DEFAULT 0,
 webauthn_id BLOB NOT NULL UNIQUE CHECK(length(webauthn_id)=32),
 created_at TEXT NOT NULL,
 password_changed_at TEXT NOT NULL
);
-- Invite and password-reset links, stored as SHA-256 hashes. An invite creates
-- an account (named by username when the administrator chose one); a reset
-- link sets the password of the account username names.
CREATE TABLE local_invites (
 token_hash BLOB PRIMARY KEY CHECK(length(token_hash)=32),
 kind TEXT NOT NULL CHECK(kind IN ('invite','reset')),
 username TEXT,
 created_by TEXT NOT NULL,
 created_at TEXT NOT NULL,
 expires_at TEXT NOT NULL
);
-- Passkeys: the go-webauthn credential as JSON, keyed by its credential ID.
CREATE TABLE passkeys (
 credential_id BLOB PRIMARY KEY,
 username TEXT NOT NULL REFERENCES local_accounts(username) ON DELETE CASCADE,
 name TEXT NOT NULL,
 credential TEXT NOT NULL,
 sign_count INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL,
 last_used_at TEXT
);
CREATE INDEX passkeys_username ON passkeys(username);
