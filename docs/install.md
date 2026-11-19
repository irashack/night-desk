# Install Night Desk

Night Desk needs persistent local storage, an HTTPS reverse proxy, and an OIDC
provider that supports authorization code flow with PKCE. SQLite is embedded;
no external database or other service is required. Use a released image from
any registry you trust, pinned by digest, or build one with
`docker build -f Containerfile -t nightdesk:local .` (Podman works too).

## First paper

Run the following as an unprivileged host user. Set `ENGINE=podman` to use Podman.
Choose an actual image reference in place of the registry/digest placeholders:

```sh
ENGINE=docker
export NIGHTDESK_IMAGE='registry.example/your-project/nightdesk@sha256:REPLACE_WITH_DIGEST'
export NIGHTDESK_DIR="$PWD/nightdesk"
export NIGHTDESK_UID="$(id -u)" NIGHTDESK_GID="$(id -g)"
install -d -m 0700 "$NIGHTDESK_DIR"
"$ENGINE" run --rm --user "$NIGHTDESK_UID:$NIGHTDESK_GID" \
  --mount "type=bind,src=$NIGHTDESK_DIR,dst=/data" \
  "$NIGHTDESK_IMAGE" init --dir /data \
  --paper daily --masthead 'The Daily' --timezone America/Chicago
```

`init` writes `config.yaml`, migrates `nightdesk.db`, and creates exactly
one paper-scoped agent key in `agent.key` (0600), without printing it. The key has
the default `read,publish,edit,review` capabilities. Defaults are paper `daily`,
masthead `The Daily`, timezone `UTC`. Existing config, key, database or SQLite
sidecar files are refused. Provision once; do not run `init` against an existing
installation. Transfer `agent.key` directly into your agent's secret store, without
putting its contents in commands, logs or git.

Edit `config.yaml` before starting:

- Set `server.public_base_url` to your HTTPS origin, such as
  `https://paper.example`. Register `https://paper.example/auth/callback` as the
  OIDC client's redirect URL. If your provider shows an icon for each application
  on its login and dashboard pages (Authentik, Pocket ID, Keycloak and others do),
  upload `docs/brand/night-desk-icon-512.png` as the client's icon.
- Set `server.oidc.issuer` to the provider's exact issuer and `client_id` to the
  registered client ID. `scopes` defaults to `[openid]`. Provision its client
  secret as a private file; `client_secret_file: oidc-client-secret` refers to
  `/data/oidc-client-secret` in this example. Never place the secret in YAML.
- Add each reader's verified issuer and stable subject, never their email:
  `readers: [{issuer: "https://your-issuer.example", subject: "verified-subject"}]`.
  Alternatively, use an absolute `subject_file` path accessible inside the
  container. The generated empty reader list grants nobody access.
- Choose `agent_role` (`author`, `reviewer` or `none`) and `deadline_policy`
  (`hold` or `publish-unreviewed`); the defaults are `author` and `hold`.
- Adjust the paper's sections, timezone and coordinates. The generated zero
  coordinates are placeholders; weather, sun and solunar boxes default off.

Keep `server.listen: "0.0.0.0:8080"` inside the container and `data_dir: .` when
the config is in `/data`. Relative server paths resolve beside the config file.
The default image user is UID/GID 65532; this guide overrides it with the host
user so bind-mounted files remain writable. For rootless Podman, use
`--userns=keep-id` on these commands to preserve that identity. On SELinux hosts,
use an appropriately labelled bind mount (for example `-v "$NIGHTDESK_DIR:/data:Z"`).

Validate, then start:

```sh
"$ENGINE" run --rm --user "$NIGHTDESK_UID:$NIGHTDESK_GID" \
  --mount "type=bind,src=$NIGHTDESK_DIR,dst=/data,readonly" \
  "$NIGHTDESK_IMAGE" check --config /data/config.yaml
"$ENGINE" run -d --name nightdesk --restart unless-stopped \
  --user "$NIGHTDESK_UID:$NIGHTDESK_GID" --read-only --cap-drop ALL \
  --security-opt no-new-privileges --publish 127.0.0.1:8080:8080 \
  --mount "type=bind,src=$NIGHTDESK_DIR,dst=/data" \
  "$NIGHTDESK_IMAGE" serve --config /data/config.yaml
```

`check` strictly loads and validates the whole configuration and reports
exact paths and rules on failure. It creates no database, contacts no provider,
and does not test client-secret readability or OIDC discovery; `serve` checks
those at startup. Point your HTTPS proxy at port 8080; `/healthz` is the health
endpoint. Open `/` to sign in. Keep `/data` on persistent storage across restarts.

Web push needs no setup. `init` creates the VAPID key `vapid.key` (0600) in the data
directory, and `serve` creates it there if it is missing; keep it with your
configuration and secrets, since a new key means readers turn notifications on
again. Each reader enables "Notify me when the paper lands" in the Menu on each
device and allows notifications when the browser asks; on iPhone and iPad this
works only in the app added to the Home Screen.

For Compose, use [compose.yaml](../deploy/examples/compose.yaml). With the exports
above, run `docker compose -f deploy/examples/compose.yaml up -d`; a compatible
Podman Compose provider can use the same file. Put the proxy on the same host,
or adapt the private container network instead of exposing port 8080 publicly.

## Back up, restore, upgrade

Run backups with the **current release's** image: administrative commands also
apply migrations. Mount a separate destination outside `/data`, and choose a
fresh backup name each time:

```sh
export NIGHTDESK_BACKUPS="$PWD/backups"
install -d -m 0700 "$NIGHTDESK_BACKUPS"
"$ENGINE" run --rm --user "$NIGHTDESK_UID:$NIGHTDESK_GID" \
  --mount "type=bind,src=$NIGHTDESK_DIR,dst=/data" \
  --mount "type=bind,src=$NIGHTDESK_BACKUPS,dst=/backups" \
  "$NIGHTDESK_IMAGE" backup /backups/pre-upgrade --config /data/config.yaml
```

This captures a consistent SQLite snapshot, retained immutable `images/` and,
when web push is set up, the push key `vapid.key` (mode 0600); it can run while
serving. Separately save the matching config, OIDC secret and
any subject files using your secret-management tooling. Raw agent keys are not
included; their hashes and revocation state are in the database. Copy backups to
another device: images may be hard-linked locally.

To restore, stop the server and all administrative writers. Copy the backup's
`nightdesk.db`, its `images/` directory and `vapid.key`, if present, into a
**fresh** persistent directory, keeping `vapid.key` at mode 0600 (without it,
every device's notifications silently stop until readers turn them on again); restore the matching config and secret files there, with the same
ownership and private permissions. Keep paths correct in the new container and
point its `/data` mount at that directory. Start the selected image and verify
sign-in, an archived edition, images and agent access. Do not copy over a running
database or reuse old `-wal`/`-shm` files. There is no `backup restore` subcommand.

Before changing the image digest, take the backup above and preserve the old
digest. Stop, select the new digest, validate and start; `serve` applies embedded
forward migrations. For rollback restore the pre-upgrade backup and old image
together. See [the upgrade promise](upgrading.md).

## Gathering and optional models

Gathering and model drafting run in a **second container** from the same image.
Issue the worker's own paper-scoped key on the server and save the printed secret
to a private worker-key file:

```sh
nightdesk key issue daily --worker --config /data/config.yaml
```

The `--worker` preset is `read,gather,draft`; it cannot edit, review or publish.
The key selects the paper, so there is no worker `--paper` flag. Start the worker
with the key and the server origin:

```sh
nightdesk gather --server https://paper.example \
  --key-file /run/secrets/nightdesk-worker-key \
  --worker-config /run/secrets/nightdesk-worker.yaml \
  --interval 30m
```

`--once` runs a single pass. `--server` must be an HTTPS origin; HTTP is accepted
only on loopback (`localhost`, `127.0.0.1`, `::1`) for a local development server.
Mount only the worker key and worker config read-only; never mount `/data`, the
database, OIDC secrets or the publishing `agent.key`. Schedule the worker with your
container scheduler or a host timer.

Article fetches retry only 429, 5xx and requests that got no response, at most
three tries each with short backoff (honoring `Retry-After` up to five seconds) and
at most 30 seconds of waiting per run; other 4xx responses and refused addresses
fail at once. A host with three failed articles in a row cools off for 30 minutes
and its articles are stored as unavailable; the cool-off lives in the running
worker, so prefer `--interval` over a fresh `--once` per run. When Readability
finds under 280 characters, the worker tries the page's JSON-LD `articleBody`, then
its AMP version. Per-host counts of outcomes, cool-offs and extraction methods
appear in `nightdesk quality` and on the operator page.

A worker config is optional and is never read by `serve`. Without `models`, the
worker only gathers; with both tiers it also drafts `reviewer` and `none` papers
that have a `deadline`, inside the paper's `draft_lead` (default 90m). If the
deadline passes with nothing drafted (the worker was down, say), the run is
*missed*; until local midnight the worker may still draft a late edition, and the
deadline policy then applies to it as at the deadline: a `none` paper publishes it,
a `reviewer` paper holds it for review or publishes it marked unreviewed. Earlier
missed days are never backfilled. A late edition counts as a deadline miss.

```yaml
models:
  fast:
    base_url: http://localhost:11434/v1   # include the API version; Ollama or any OpenAI-compatible endpoint
    model: llama3.2:3b
    key_file: /run/secrets/model-key       # optional for a local provider
  writer:
    base_url: http://localhost:11434/v1
    model: llama3.1:8b
    key_file: /run/secrets/model-key
tracing:
  otlp_endpoint: https://langfuse.example/api/public/otel/v1/traces
  record_content: false                    # prompts/outputs only with an explicit opt-in
prompts:
  directory: /etc/nightdesk/prompts        # optional <name>.md overrides
limits:
  max_calls: 12
  max_tokens: 100000
```

For Claude, set `provider: anthropic` on a tier and give a Claude API key in
`key_file` (required for this provider; `base_url` is optional). `effort` is
`low`, `medium`, `high`, `xhigh` or `max`, and defaults to the model's own default.
Output is constrained to each prompt's JSON shape, the system prompt is cached, and
a refused request fails the call rather than producing text. On Opus, Sonnet 5.5
and Fable models a refusal is first re-served by Anthropic on another model.

```yaml
models:
  fast:
    provider: anthropic
    model: claude-haiku-5-5
    key_file: /run/secrets/anthropic-key
  writer:
    provider: anthropic
    model: claude-opus-5-5
    key_file: /run/secrets/anthropic-key
    effort: medium
```

Tiers may mix providers, for example a local fast model with Claude writing.

A tier may also carry `price: {input_per_mtok: 4, output_per_mtok: 20}`, the
provider's list price in US dollars per million tokens. Prices only estimate cost:
`nightdesk check --config config.yaml --worker-config worker.yaml` validates the
worker config and, when both tiers are priced, prints the most a month of drafting
can cost (every reviewer and `none` paper spending its whole run budget daily, at
the dearest price) and a typical figure. The run budget in `limits` is what
actually bounds spending. With prices set, the worker also reports each run's
estimated cost (input at the base rate, so cached input is overestimated), and
`nightdesk quality` and the operator page show drafting runs, tokens and estimated
cost for the window.

A worker with models also finds feeds for sections marked `find_sources` (before
each gathering pass, at most daily per section, two fast-model calls within 12,000
tokens): every suggested feed is fetched through the guarded client and kept only
if it parses with at least one item. The `find-sources` prompt can be overridden
like the drafting prompts.

Relative `key_file` and `prompts.directory` paths resolve beside the worker config.
Omit `tracing` to send nothing. Traces carry metadata by default;
`record_content: true` includes prompts and outputs with credentials and reader
notes redacted. Prompt overrides are `section-fit.md`, `same-story.md` and
`draft-edition.md`; removing one restores the embedded default, as does
`nightdesk prompts reset --worker-config FILE <name>`, and `nightdesk prompts list`
shows which are overridden. `limits` bounds
the worker's model spending for each attempt. All of this is optional: an external
agent can write and publish without a worker config.

## Coverage lean (optional)

A section with `lean: true` shows a left/center/right coverage bar over its rated
outlets. Night Desk ships no ratings: point `lean.ratings_file` at a ratings file
built from a source you are licensed to use. `nightdesk ratings import` builds one
from an `outlet,domain,lean[,factuality]` CSV:

```sh
nightdesk ratings import --name 'Example Ratings' --url https://ratings.example \
  --license 'licence you hold' --attribution 'Ratings by Example Ratings' \
  --out ratings.json ratings.csv
```

The source's name, URL, licence and attribution are required, and the attribution
is shown wherever a rating appears. AllSides is one possible source, only under
[its own terms](https://www.allsides.com/media-bias/media-bias-ratings).

## Operator quality page

The read-only `/operator` page is off by default. To enable it, provision a
separate random operator key in your secret manager and put its SHA-256 hex digest
(64 hex characters, with an optional trailing newline) in a private file. Configure:

```yaml
server:
  operator:
    key_hash_file: operator-key.sha256
```

Relative paths resolve beside the configuration file. Restart `serve` after
changing the setting or digest; invalid or unreadable configured hash files stop
startup. Open `/operator` over HTTPS and use HTTP Basic username `operator` with
the key as the password. Automation can instead use `Authorization: Bearer` with
the same key. Keep credentials out of URLs, command arguments and logs. The key
confers no reader session or agent API capabilities; reader and agent credentials
do not open this page.

The page and `nightdesk quality --days N [--json]` share operational aggregates:
published editions, unreviewed and held editions, send-back actions, warnings by
rule, failed and unverified advisory source checks, empty required sections,
review turnaround and deadline misses. The page never displays reader identities,
edition text, notes, saves or votes (the CLI retains its existing aggregate
feedback table). Use `/operator?days=30` to change the default 14 local-calendar-day
window; the maximum is 3660. A deadline miss includes a late edition or an elapsed
deadline without an edition; today's future deadline is excluded. Published counts
are distinct edition dates; diagnostic counts include retained publish attempts,
including replacements and rejected validation attempts.

Publish diagnostics record the edition date, publication path (`put`, `draft`,
`deadline`), warning/error paths and rules, and advisory source-check outcome
counts. Successful publication records these in the edition transaction. Checks
use stored snapshots only: a source the worker never gathered for the paper counts
as unverified under `not gathered`, one whose fetch failed under `source
unavailable` (so a paper whose agent finds its own stories without a gathering
worker sees every source as not gathered), unsupported
claims as failed advisory checks, and field checks with no findings as passed.
One check is not advisory for drafts: publishing a draft, by hand or at the
deadline, refuses a title ending on a function word or a summary or why that stops
mid-sentence (no terminal punctuation), with the field's path. The deadline holds
such a draft; the model drafting worker discards one and drafts again. An agent's
own `PUT` of an edition keeps these as warnings.
Claim values are removed from operator rule aggregates. Each paper keeps at most
10,000 diagnostic records and prunes records older than its `retention_days`
(default 14) whenever it records another attempt. Older editions remain readable,
but diagnostic counts outside retained history are incomplete; no diagnostics
are reconstructed for editions published before this migration.
