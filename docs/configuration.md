# Configuration and protocol reference

The [install guide](install.md) walks through a first paper. This page is the
reference for the server configuration, the reader, editions, webhooks and the
event stream.

## Server and papers

The backend implements the agent API, drafts, candidates, events, retained
images, feedback retrieval, the deadline scheduler and administrative commands,
plus the OIDC-signed-in reader. Source and article fetching and model calls do not
run here; they run in `nightdesk gather`.

Start with a private YAML configuration (relative server paths resolve to this file).
This example is for running the binary directly; in a container, use
`listen: "0.0.0.0:8080"` and `data_dir: .` with the configuration in `/data` (see
[Container image and rollback](#container-image-and-rollback)):

```yaml
server:
  listen: "127.0.0.1:8080"   # bare metal; containers need 0.0.0.0:8080
  public_base_url: https://paper.example
  data_dir: data
  session_lifetime: 720h
  group_session_lifetime: 24h   # how long sign-in's group membership counts
  oidc:
    issuer: https://identity.example
    client_id: nightdesk
    client_secret_file: oidc-client-secret
    scopes: [openid]
papers:
  - id: daily
    masthead: The Daily
    place: Chicago
    agent_role: author            # author, reviewer or none
    deadline_policy: hold         # hold or publish-unreviewed
    draft_lead: 90m               # worker drafting window before deadline
    brief: Prefer primary sources and explain what changes for the reader.
    deadline: "06:30"
    motto:
      guidance: A verified line from a philosopher or a Greek maxim.
    look: broadsheet
    timezone: America/Chicago
    latitude: 41.88
    longitude: -87.63
    sections:
      - key: economy
        title: Economy
        required: true
        brief: Focus on household costs and local effects.
    boxes: {weather: true, sun: true, solunar: true, markets: false}
    readers:
      - issuer: https://identity.example
        subject_file: /run/secrets/reader-subject
        # Or instead of subject_file: subject: stable-reader-subject
      - issuer: https://identity.example
        group: household          # everyone in this provider group reads it
```

```sh
nightdesk serve --config nightdesk.yaml
nightdesk migrate --config nightdesk.yaml
nightdesk key issue daily --label Writer --config nightdesk.yaml
nightdesk key issue daily --label Worker --worker --config nightdesk.yaml
nightdesk key list daily --config nightdesk.yaml
nightdesk key revoke KEY_ID --config nightdesk.yaml
nightdesk backup /backups/nightdesk-2026-10-27 --config nightdesk.yaml
```

Administrative commands default to `nightdesk.yaml`. Key issuance prints the
secret only once to stdout; save it directly to your agent's secret store. Key
metadata (including its revocation id and capabilities) is available through
`key list`. Keys default to `read,publish,edit,review`. Use `--capabilities read,publish` to select an explicit
set, or `--worker` for `read,gather,draft`. The worker preset cannot publish or
edit a paper. Capabilities are `read` (paper, feedback, editions and candidates),
`publish` (edition publication), `edit` (paper edits and restore), `review` (draft
review), `gather` (candidate writes), and `draft` (draft writes).

Put the service behind an HTTPS reverse proxy. Authenticate agent requests with
`Authorization: Bearer <key>`. Author papers may publish with
`PUT /api/v1/editions/YYYY-MM-DD`; reviewer and none papers publish their current
draft through `POST /api/v1/drafts/{date}/publish` after review, or through the
deadline policy. Inspect `GET /api/v1/paper` for current rules and
`GET /api/v1/feedback?after=0&limit=100` for feedback. The next cursor is
`next_cursor`; `events` is always an array. Failed image retention yields warnings,
not a failed publication. Reader rendering must use the local `edition_images`
associations, never remote URLs. Image downloads also have an eight-million-pixel
decoded limit to bound decompression costs.

Paper changes reload on SIGHUP or within ten seconds of an mtime change. Invalid
reloads keep the previous snapshot. Server settings require a restart; changing
them rejects the entire reload rather than silently moving the live database or
listener. The OIDC secret file is read at startup, when provider discovery also
runs. Register `<public_base_url>/auth/callback` as the provider's redirect URI.

`brief` and section `brief` are optional editorial guidance (at most 2000 and 600
characters respectively). Optional `motto.guidance` is the motto policy (at most
2000 characters); omit `motto` to tell the agent to omit it. All guidance is trimmed,
non-empty plain text, without control characters, HTML tags or Markdown links.
`deadline` is a quoted `HH:MM` local target in the paper's timezone. `agent_role`
is `author` (default), `reviewer` or `none`; `deadline_policy` is `hold` (default)
or `publish-unreviewed`; `draft_lead` is the worker's drafting window before the
deadline (default `90m`, at most 24h). At the deadline the server scheduler applies
the policy to that day's draft: `publish-unreviewed` marks a reviewer paper's
edition unreviewed, while a `none` paper publishes normally. The worker drafts
reviewer/none papers only inside `draft_lead`; author papers are written by their
agent. `GET /api/v1/paper` serves these fields, masthead and optional place,
sections and their briefs, sources, retention, reader notes and enabled boxes;
absent optional fields are omitted. See the generic writing pipeline in
[integrations/hermes](../integrations/hermes/README.md).

Sources, retention and coverage lean are operator configuration:
`papers[].sources` or `papers[].sections[].sources` accept `feed`, operator-only
`opml` and site-limited `google_news` sources; an omitted section list inherits the
paper's sources and an explicit empty list disables gathering there. A section
with `find_sources: true` and no sources of its own finds them: on the next run it
gathers from a Google News search on its title as well as the paper's feeds, and a
worker with models asks its fast model (prompt `find-sources`) at most daily for
publications' feeds, keeping up to five that it fetched and parsed itself; three
verified feeds retire the search. Sections added from the reader set it.
`retention_days` defaults to 14 (1–90); the server removes older snapshots hourly, whether or not a worker is running. `gather.allow_private` lists exact
`host:port` exceptions for local feed services (and webhook receivers). Only that
service's own feed, pages and redirects lead the worker to it; links and redirects
from public feeds and pages are refused. `papers[].filters` drops junk
before its page is fetched: built-in rules for betting picks, fantasy advice,
roundups and editorial cartoons (each may be listed in `disabled`), plus
`blocked_outlets` (feed name, or URL host and its subdomains) and
`blocked_keywords` (whole-word headline phrases). The quality report and operator
page count drops by rule. `lean.ratings_file` points at the
ratings file and a section's `lean: true` enables its coverage bar. Night Desk ships
the ratings format and `nightdesk ratings import` but no ratings: use a ratings
source you are licensed to use and keep its attribution (AllSides, for example,
only under [its own terms](https://www.allsides.com/media-bias/media-bias-ratings)).

Each reader must provide exactly one of `subject`, `subject_file` and `group`. A subject file
must use an absolute path and contain a non-empty subject after trimming whitespace.
It is read at load/reload; missing, unreadable or invalid files reject the whole
snapshot, and duplicate detection uses the resolved issuer/subject pair. File
contents and reader configuration are never served by the agent API. After rotating
only a subject file, send SIGHUP to reload it (the periodic watch tracks YAML mtime).

### Group readers

A `group` reader admits every identity from that issuer whose ID token lists the
group in its `groups` claim, an array of group names (Pocket ID, Authentik and
Keycloak, with a groups mapper, send one). When any paper has a group reader,
sign-in also asks for the `groups` scope, or you can list `groups` in
`server.oidc.scopes` yourself. The claim is stored with the session; duplicate
issuer/group pairs on one paper are rejected.

- A session reads its subject paper first (the first paper naming its subject,
  served at `/`), then every paper one of its groups reads, in configuration
  order. A reader with only group papers gets the first one at `/`. The others
  are at `/p/{paper}/` (with `/p/{paper}/e/{date}`, `/archive`, `/search`), and the
  menu bar shows a switcher between them.
- Group readers read, search, give feedback and turn on notifications. Editing
  sections (`/sections`) stays with the subject reader of their own paper.
- Membership is as of sign-in. It counts for `server.group_session_lifetime`
  (default `24h`) from sign-in, whatever `session_lifetime` is. After that, a
  subject reader keeps their own paper, and the switcher's group papers ask them to
  sign in again; a reader with no paper of their own is asked to sign in again at
  once. Signing in reads the groups afresh, so someone removed from the group in
  the provider loses the paper within that time. Removing the `group` reader from
  the configuration takes effect at once. A groups claim that is not a list of
  names (at most 512, each at most 256 bytes) grants no group and does not block
  sign-in.
- A session that signed in before any paper had a group reader (or before
  v0.2.0) has no recorded groups. Like a session whose membership has run out, it
  gets the sign-in page, not a 404, for any paper a group of its issuer reads, and
  signing in returns it to that page. A 404 stays for papers the session's current
  groups don't include.
- Notifications are per paper. Each sign-in refreshes the groups that reader's
  push subscriptions carry, and each push rechecks that the reader still reads the
  paper, by subject or by those groups. A removed member who never signs in again
  keeps getting the empty "paper is ready" push until the group reader is removed
  from the configuration or the device unsubscribes; opening it asks for sign-in.
- The agent API is unchanged: a shared paper has its own agent key like any other.
- Only the default paper (at `/`) is saved for offline reading, and a
  notification opens `/`; the other papers need a connection and the switcher.

Health checks use `GET /healthz`. SIGTERM allows ten seconds for requests to
finish. Backups require a fresh destination outside the data directory and include
a consistent SQLite database plus immutable images (hard-linked where possible).
Retain and restore both together; hard-linked backups are not protection against
disk failure. Restore while stopped by copying the database and images into a
fresh data directory, never over a running WAL database.

## Webhooks

Each paper may declare operator-managed wake-up webhooks:

```yaml
papers:
- id: daily
  # ...the rest of the paper configuration...
  webhooks:
  - url: https://agent.example/nightdesk
    secret_file: /run/secrets/nightdesk-webhook
    events: [draft.ready, review.requested, edition.published, feedback.new]
```

Omit `events` to receive every event. An explicit list must be non-empty and
contain only the four supported types, without duplicates. URLs must be HTTPS
without credentials or fragments and unique within a paper. `secret_file` must
be absolute; its contents are loaded with the configuration and never returned
by the agent API. The file contains one UTF-8 secret per line (current first,
previous secrets afterwards during rotation), without surrounding whitespace or
blank lines, at most 64 KiB total. Reload the configuration after rotating it
(SIGHUP also reloads unchanged YAML). Invalid reloads retain the previous snapshot.

Requests are JSON POSTs with `id`, `paper`, `type`, `at` (UTC RFC3339), and `data`.
Draft events carry `{ "date": "YYYY-MM-DD", "revision": 1 }`, publication events
carry `{ "date": "YYYY-MM-DD" }`, and feedback wake-ups carry `{}`. Read new reader
feedback through the feedback API; the webhook does not expose reader identity.
A submitted draft emits `draft.ready`; reviewer papers also emit
`review.requested`. Entering `in_review` emits `review.requested`. Every publication
path emits `edition.published`. Feedback wake-ups coalesce to one event per paper
per UTC minute, while every feedback entry remains available in the feedback API.

Headers are `Night-Desk-Event`, `Night-Desk-Delivery` (the event ID),
`Night-Desk-Timestamp` (Unix seconds), and `Night-Desk-Signature-256`. Verify each
`sha256=<hex>` signature against HMAC-SHA256 of the exact raw bytes
`<timestamp>.<body>` using your shared secret and a constant-time comparison.
During rotation the signature header contains a comma-separated signature for
each active secret. Reject timestamps outside a short tolerance (for example,
five minutes) and deduplicate event IDs. Retries retain the event ID and body;
each attempt receives a fresh timestamp and signature.

Events and deliveries are stored atomically with the triggering change. Delivery
resumes after restart. Network failures, HTTP 429, and HTTP 5xx retry after one
minute, doubling up to 24 hours, with at most 12 attempts including interrupted
ones. Other non-2xx responses fail permanently; redirects are not followed.
An interrupted attempt is eligible again after its one-minute lease. Delivery
uses the guarded fetch client: private destinations are refused unless their
exact `host:port` is listed under operator-controlled `gather.allow_private`.
Removing a webhook retires its pending deliveries. Secrets are resolved from
current configuration at each attempt, never stored in the outbox.

Agents without an HTTP receiver poll the same durable stream using a key with
`read` capability:

```text
GET /api/v1/events?after=<last-event-id>&limit=100
```

Omit `after` for the beginning of the paper's stream; `limit` defaults to 100 and
must be 1–500. The response is `{ "events": [...], "next_cursor": "..." }`.
Persist `next_cursor` after processing a page and pass it as the next `after`.
Empty pages retain the supplied cursor. Ordering follows database insertion
order, not the random 128-bit hexadecimal IDs. Cursors are scoped to the key's
paper; unknown or foreign cursors return an exact `after` validation error.
Polling works even when the paper has no webhook configured.

## Editions

Validate an edition offline with `go run ./cmd/nightdesk validate edition.json`.
Pass `--sections economy,health:required,...` after the filename to check a
paper's ordered sections and required sections; without it, only intrinsic
edition rules are checked. Warnings are printed but do not fail validation.

Regenerate both published JSON Schemas with `go generate ./internal/edition`.

Editions accept `"schema": 1` permanently, alongside `"schema": 2` (see
[v1](../schema/edition-v1.json) and [v2](../schema/edition-v2.json)). In v2 an item may
add a `byline` and `sources`: up to 12 objects with required `outlet` and `url`,
and optional `byline`. Outlets are at most 80 characters; bylines are at most
120. All text is trimmed, non-empty plain text. Source URLs follow the item's
HTTPS URL rules, must be unique within that item, and cannot repeat its own URL.
The v2 `opinion` kind appears after other items in its section with an Opinion
tag and cannot be the front-page lead. An opinion item may set `about` to the ID
of a story in the same edition and `stance` to its position in one line (at most
140 characters); the reader then shows it under that story as "Opinion on this
story", with the outlet's lean where the edition's ratings cover it, instead of in
the section's list. Opinion never counts as coverage. The new fields and kind are
rejected in v1 editions.

## Reader

Visit `/` and follow the sign-in link. OIDC identities are the verified issuer and
subject (or a provider group, see [Group readers](#group-readers)), never email;
configure them in the paper's `readers` list. Unmapped
identities see only “No paper for you yet”. A removed mapping takes effect on the
next online request after configuration reload.

The embedded reader has Pages, List and Reader layouts (Reader is a centered
single column with larger type), keyboard/swipe page turns, front-page shortcuts,
Auto/Light/Sepia/Dark modes, an archive with full-text search over past editions
and saved items, and per-item feedback. While reading, the only fixed control is a
one-row page bar (previous, the page's place with a link to the front page, next
and Menu); Menu opens Today, Archive, Sections, the modes, the layouts and Sign out. On a
desktop-width window the bar runs across the top beside Today and Archive, and the
paper widens: the front page sets the lead beside the section index and sections
set their stories in columns. A past edition's news lists up to three later articles
on the same story under "Since published", from candidates the gathering worker
stored after that date; without a worker there are none. A story whose page the
gathering worker stored text for has "Read here" beside its outlet link: it opens
the reading view (`/read/<date>/<item>`), the stored text set in the paper's type
with a sticky "Back to the paper" bar that returns to the story's page and an
"Open at <outlet>" link. Text that looks shorter than the piece (a paywall or a
page that needs script) carries a note saying so. A− and A+ set the text size,
and the place in each story is kept, per device, in the browser's local storage,
which signing out clears. Stories without stored text keep only the outlet link,
and the view says it has no copy; the server never fetches a page for it. Stored
text lasts as long as the paper's `retention_days`. Without JavaScript the complete edition remains
readable as a list. Sections (`/sections`) lets a paper's readers add, rename,
reorder and remove its sections and edit their one-line briefs, each change saved as
an editorial revision like an agent's `PATCH /api/v1/paper`; new sections find their
own sources on the next run. Install through your browser's normal PWA controls. The latest
edition and its images are saved for offline reading. A front page left open
or resumed from the background checks for a newer edition when it returns to
view and reloads to it; when the server cannot be reached it says it is showing
the saved copy and names its date. "Notify me when the paper lands" in the Menu
sends that device a notification whenever a new edition is published (on iOS, only
in the Home Screen app) until it is turned off or the device signs out. Signing out removes the
session and clears Night Desk's offline caches. Offline devices cannot learn about
a server-side revocation until they reconnect. Avoid installing personal papers in
a shared browser profile.

The optional `place` label appears in the dateline. Sun and solunar events use the
paper's coordinates and timezone. Weather is optional, cached hourly and omitted
when Open-Meteo is unavailable (a request waits at most five seconds).

The app icons are generated from the SVG sources in `web/brand` with
`go run ./web/icon-gen`; see [the brand guide](brand/README.md).

## Container image and rollback

Releases are tagged `vX.Y.Z`. Each release's container image is published to
`ghcr.io/irashack/night-desk` for linux/amd64
and linux/arm64. Deploy by the image's `@sha256:...` digest, not the mutable tag.

Mount writable storage at `/data`, owned by the container user (UID/GID 65532 by
default, or the user you pass with `--user`), and keep the configuration in it as
`/data/config.yaml` with `server.data_dir: .` and `server.listen: "0.0.0.0:8080"`;
`nightdesk init --dir /data` writes exactly this layout. The image's default
command serves `/data/config.yaml`. The remaining root filesystem can be
read-only.

Run a released image pinned by digest. Before upgrading, run `nightdesk backup`
with the image you are running now and retain its configuration and secrets separately.
Migrations run automatically and move forward only. Rollback means restoring that
pre-upgrade backup into a fresh data directory and running the previous image;
see [upgrading](upgrading.md).
