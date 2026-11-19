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
   Save the corresponding config, OIDC secret and reader subject files separately.
   Keep an independent off-device copy; hard-linked image backups share disk risk.
3. Stop the server and workers, select the new image digest, run `nightdesk check
   --config /data/config.yaml`, then start it. Startup applies migrations. Verify
   sign-in, archived editions and images, and the agent or worker's access before
   discarding the backup.

Rollback means **restore the pre-upgrade backup**, then run its matching old
image and configuration. Stop every writer, restore `nightdesk.db` and `images/`
(when present) into a fresh directory, restore matching config and secret files,
and repoint the persistent mount. Never overlay an active WAL database or carry
its old sidecars into the restore. Merely selecting an older image does not undo
a migration. Changes made since the snapshot are absent after rollback.

See [installation](install.md) for container mounts, backup commands and restore
checks. Preserve backups until the new release has been verified on your papers.
