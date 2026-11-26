# Upgrading Night Desk

The upgrade promise: a minor release never breaks stored configuration,
editorial revisions or reader notes. Breaking changes belong only in a major
release and must be named in its release notes. Compatibility fixtures built from
existing databases, and restore tests, enforce this promise.
Edition schema v1 remains accepted alongside v2 permanently.

Database migrations are embedded and **forward-only**. `serve`, `migrate`, `key`
and `backup` open the database and apply pending migrations. `check` validates
configuration without opening the database; it does not prove that an older
binary can use a database migrated by a newer one.

For each upgrade:

1. Read the release notes; record the running image digest and configuration.
2. Using the **old image**, run `nightdesk backup /backups/pre-upgrade --config
   /data/config.yaml` with a fresh destination mounted outside the data directory.
   Save the corresponding config, OIDC secret (if any) and reader subject files separately.
   Keep an independent off-device copy; hard-linked image backups share disk risk.
3. Stop the server and workers, select the new image digest, run `nightdesk check
   --config /data/config.yaml`, then start it. Startup applies migrations. Verify
   sign-in, archived editions and images, and the agent or worker's access before
   discarding the backup.

Rollback means **restore the pre-upgrade backup**, then run its matching old
image and configuration. Stop every writer, restore `nightdesk.db`, `images/` and
`vapid.key` (when present, at mode 0600; without it readers must turn notifications
on again) into a fresh directory, restore matching config and secret files,
and repoint the persistent mount. Never overlay an active WAL database or carry
its old sidecars into the restore. Merely selecting an older image does not undo
a migration. Changes made since the snapshot are absent after rollback.

## Release notes that need action

### 0.9: feedback names readers by an opaque id

`GET /api/v1/feedback` (MCP `get_feedback`) no longer returns `reader_issuer`
and `reader_subject`: any `read` key could see readers' sign-in identities, which
the agent API must never serve. Each event now carries `reader_id`, an opaque id
that is stable for one reader of one paper, differs between papers and cannot be
turned back into the identity. This is a deliberate exception to the upgrade
promise, made for privacy. If your agent grouped feedback by issuer and subject,
group it by `reader_id` instead. Migration `024_reader_ids` stores the random key
behind the ids in the database, so restoring a backup keeps them stable.

### 0.4: reader sign-in modes

Nothing changes for an existing installation. A configuration with `server.oidc`
and no `server.auth` keeps signing in through OIDC exactly as before, and its
sessions stay valid. Migration `023_local_accounts` adds empty tables for local
accounts, invites and passkeys. Expired sessions are now deleted hourly.

To move an existing installation to another mode:

- **To `local`.** Replace `server.oidc` with `auth: {mode: local}` (and optionally
  `local: {passkeys: true}`), remove each reader's `issuer`, and either name the
  usernames you will create (`readers: [{subject: alice}]`) or leave `readers: []`
  so every account reads the paper. Restart, open `/setup` with the code from the
  log, and invite the others. Readers' past feedback and notification subscriptions
  belonged to their OIDC identities, so they do not carry over; each reader turns
  notifications on again.
- **To `proxy`.** Replace `server.oidc` with the `proxy` settings, and change each
  reader to the user name (or group) your proxy sends, without `issuer`.
- **To `none`.** Replace `server.oidc` with `auth: {mode: none}` and remove
  `readers`. Keep the server on a private address.

Changing the mode needs a restart and signs everyone out.

See [installation](install.md) for container mounts, backup commands and restore
checks. Preserve backups until the new release has been verified on your papers.
