# Night Desk design

This document describes how Night Desk is built and why. It covers the product's
architecture, trust boundaries, data model and contracts, and the design choices
behind them. Deployment choices of any one site (its host, identity provider,
address or backups) belong in the [installation guide](install.md), not here.

## What Night Desk is

A daily paper an AI agent writes for one reader, or that Night Desk writes for
one reader itself. Night Desk owns the paper end to end: its sources, schedule,
candidates, draft, checks, edition, reader app and feedback. An agent takes part
at whichever depth the paper's operator chooses:

| Agent role (per paper) | Night Desk does | The agent does |
|---|---|---|
| `author` (default) | Gathers and checks candidates; validates and publishes | Chooses, writes and submits the draft (through the draft API or the edition `PUT`) and publishes it |
| `reviewer` | Also drafts with the install's configured models | Reviews: approves, cuts, swaps, edits, adds a note or sends the draft back with a request; then publishes |
| `none` | Gathers, drafts, checks and publishes on schedule | Nothing; Night Desk runs alone |

The same protocol serves every role: HTTP with a published OpenAPI description,
MCP over stdio, and signed wake-up webhooks or a pollable event stream. No agent
framework gets code of its own; Hermes, any other agent or a shell script are all
clients of the same documented contract.

What makes an agent valuable is knowing its reader. That knowledge reaches Night
Desk as data before drafting, not only as a final review: paper and section
briefs, reader notes the agent maintains (interests, topics to avoid, what landed
well) and the reader feedback it reads back.

The reader signs in with OIDC and gets an installable web app with Pages, List and
Reader layouts in light, sepia and dark modes, archive search, offline reading, web
push when an edition lands, and 👍/👎/Save feedback returned to the agent.

## What Night Desk is not

- **Not an account system.** Readers are identities from the operator's OIDC
  provider, declared per paper. Night Desk stores no passwords and manages no
  user accounts.
- **Not a crawler or a feed reader.** It gathers only from the sources configured
  for each paper (feeds, OPML imports and site-limited news searches), keeps
  snapshots for a bounded retention period, and does not talk to bookmark or
  read-later apps.
- **Not a source of facts from a model.** Sun, moon, weather and markets are
  computed or sourced; a model never generates them (see fixed decision 8).
- **Not a public site.** An edition is visible only to its paper's signed-in
  readers. The sign-in page reveals nothing about any paper.
- **Not a ratings provider.** Night Desk ships a ratings file format and an
  importer for coverage lean, but no ratings (see [Coverage lean](#coverage-lean)).

## Components

Everything is one Go binary, `nightdesk`, run in different roles.

```
             OIDC provider
                  │
 reader ──HTTPS── nightdesk serve ──────────── SQLite + image store
 (browser)        │  agent API, reader,          (one data directory)
                  │  scheduler, outbox
                  │
      ┌───────────┼─────────────────┬──────────────────┐
      │ HTTPS     │ HTTPS           │ webhooks / push   │
 nightdesk gather │            nightdesk mcp       agents, push
 (worker: fetch,  │            (stdio, for an      services
  model calls)    │             agent's MCP host)
                  │
            agents over HTTP
```

- **Server (`nightdesk serve`).** Holds the database, reader sessions and agent
  keys. It serves the agent API, the reader, the optional read-only `/operator`
  page, the scheduler, and the delivery loops for webhooks and web push. It never
  calls a language model and never fetches articles.
- **Reader.** Server-rendered pages: an edition is one HTML document, which is
  also the unit stored for offline reading. A small script adds page turns,
  feedback and the service worker; without it the reader still works as plain
  links and forms. The reader also offers archive search, a Sections editor (add,
  rename, reorder and remove sections and edit their briefs, saved as editorial
  revisions like an agent's edits) and "Since published" follow-ups on archived
  stories, matched at render time from later candidates.
- **Gathering worker (`nightdesk gather`).** A separate, unprivileged process that
  fetches feeds and articles, groups wire copies and same-story coverage, applies
  ingestion filters, and, when given models, drafts `reviewer` and `none` papers
  and finds feeds for sections that ask for them. It talks to the server only
  through paper-scoped worker endpoints (see [Trust boundaries](#trust-boundaries)).
  Its own `--worker-config` file holds model, tracing, prompt and spending settings
  and is never read by the server.
- **Scheduler.** Runs inside the server. Each paper has a durable run per local
  date with restart recovery and DST-aware deadlines. At the deadline it applies
  the paper's deadline policy (`hold` or `publish-unreviewed`) to that day's draft;
  a deadline that passes with nothing to publish marks the run missed, and the
  worker may still draft a late edition until local midnight. Earlier missed days
  are not backfilled: a stale paper is not worth drafting.
- **Event outbox, webhooks and web push.** Draft ready, review requested, edition
  published and new feedback are written to a durable event table in the same
  transaction as the change that caused them. Agents either poll `GET /api/v1/events` or receive
  the same events as webhooks, signed with HMAC-SHA256 over the timestamp and body
  (in the style of GitHub's), with event IDs, replay protection, durable retries
  and secret rotation. A second outbox sends one empty, VAPID-signed web push to
  each subscribed device the first time a date is published as the paper's newest
  edition.
- **MCP client (`nightdesk mcp`).** A stdio MCP server for agent hosts that speak
  MCP. It is a pure HTTP client of the agent API: it holds a key, has no database
  access, and the server still enforces capabilities and validation. HTTP routes,
  the OpenAPI description and MCP tool schemas come from one Go route table.
- **Supporting commands.** `init` (a first paper, migrated database, agent key and
  push key), `check` (strict validation of the whole configuration, and of a worker
  config), `key`, `migrate`, `backup`, `quality` (the operator report),
  `ratings import`, `prompts list|reset`, `conformance` (scripted scenarios an
  integration runs against itself) and `validate` (the server's edition rules,
  offline).

## Trust boundaries

**Agent keys are paper-scoped and carry capabilities.** A key names one paper and
a set of actions: `read`, `publish`, `edit`, `review`, `gather` and `draft`. The
default key is `read,publish,edit,review`; `nightdesk key issue <paper> --worker`
issues `read,gather,draft`, which cannot edit a paper, review or publish. The
server checks the route's capability on every request and MCP reuses the same
checks. Keys are 256-bit random values shown once; only their SHA-256 is stored.

**The worker never opens the database.** It holds only its worker key and reaches
the server over HTTPS, or plain HTTP on loopback for local development. It holds
no session, OIDC or publishing credential, cannot read sessions or reach another
paper, and writes only candidates, drafts, discovered section feeds and counts
(dropped items by filter rule, fetch health by host). Model calls and article
fetching happen only there, so a compromised worker, a malicious page or a
misbehaving model cannot reach reader sessions or publish. Approval is bound to a
draft revision and publishing checks it, so a late worker or a retry cannot
overwrite an approved edition. Tests prove these limits against a hostile worker.

**Fetched content is data, never instructions.** Web pages, feeds and candidate
text are passed to models as data, and the API documents candidate content to
agents the same way.

**One guarded fetch client.** Every outbound fetch (feeds, redirects, news-search
resolution, articles, images, webhooks and push) checks the resolved address in
the dialer, after DNS, so DNS rebinding fails. Loopback, private, link-local,
unique-local and 100.64.0.0/10 addresses are refused, and every redirect is
checked again. Only gathering may use exact `host:port` exceptions
(`gather.allow_private`) for local feed services; images, webhooks and push get
none. Image fetches are HTTPS-only with timeouts and a 5 MB cap; push sends follow
no redirects. The server's own outbound traffic is short and listed: image
retention at publish, OIDC discovery, weather, webhook delivery and web push.

**Readers are (issuer, subject), never email.** A reader is the verified OIDC
issuer and subject pair declared in the paper's configuration, so a changed or
reused email address cannot move anyone into another paper. Sign-in uses the
authorization code flow with PKCE, state and nonce through `coreos/go-oidc`.
Sessions are random IDs in an `HttpOnly`, `Secure`, `SameSite=Lax` cookie, stored
server-side and revocable; signing out ends the session, clears the offline copy
and removes that session's push subscriptions. A paper may also admit a provider
group: the ID token's `groups` claim is kept with the session and counts for
`group_session_lifetime` (default 24 hours) from sign-in, so a session reads its own
paper plus each group paper, each under its own URL; only subject readers edit
sections.

**Tenant isolation is in the data layer.** Reader queries take the paper from the
session, never from the request body; a `/p/{paper}` URL only chooses among the
papers the session already reads. The administrator role has no reader
access, and the operator page uses its own hashed key and shows counts, never
reader identities or edition text.

**Pages and process.** A strict Content-Security-Policy (`default-src 'self'`, no
inline script), CSRF tokens on every form and feedback call in addition to
`SameSite`, and no third-party requests (fonts are embedded). Images are
downscaled and re-encoded at ingest, which strips EXIF and location data and
neutralizes polyglot files. The binary runs as a non-root user on a read-only root
filesystem and writes only its data directory. Webhook secrets, the OIDC client
secret and reader subject files are read from files and never returned or logged.

## Data model

Papers and readers are declared in a YAML file, reloaded on change; the database
holds everything that changes at run time. At a high level:

- **Papers and editorial revisions.** The YAML declaration is the paper's identity:
  timezone, coordinates, look, readers, sources, webhooks and its default editorial
  fields. Editorial edits from agents or the reader's Sections editor (masthead,
  place, briefs, deadline, motto guidance, sections and boxes) are numbered
  revisions overlaid on the declaration; the YAML file is never rewritten. The last
  50 revisions are kept and any can be restored, revision 0 being the declaration.
  Reader notes are revisions of their own.
- **Candidates.** Immutable article snapshots per paper: canonical URL and aliases,
  extracted text and byline, or an explicit unavailable state, grouped into wire
  copies and same-story groups, kept for the paper's retention period. A stored
  snapshot never changes, except that an unavailable one is replaced the first
  time a later pass fetches the article.
- **Runs and drafts.** One run per paper and local date (lease, attempts, budget
  spent, state); one draft per date moving through `gathering`, `drafted`,
  `in_review`, `published` or `held`, with its revision and an action log.
- **Editions.** Each published edition with its items, the section layout it was
  published with, and, for lean-enabled sections, the ratings and coverage snapshot
  used, so later configuration changes never rewrite the archive. A full-text index
  (SQLite FTS5) over editions and saved items is updated in the same transaction.
- **Feedback and sessions.** Reader votes and saves, sessions, and per-device push
  subscriptions.
- **Outboxes and diagnostics.** Events and webhook deliveries, push deliveries,
  publish diagnostics (warnings by rule, source-check outcomes, deadline misses),
  fetch health by host and dropped-item counts: counts and rule names, not reader
  content.
- **Images.** A content-addressed directory beside the database, served immutable.

Migrations are embedded and forward-only, and stored editions and editorial
revisions are decoded by version. A stored revision that no longer validates after
an upgrade is kept and reported as `revision_problems`, never silently dropped.

## Paper configuration

Per paper the YAML file declares the masthead, look, timezone, coordinates,
`agent_role` and `deadline_policy`, the ordered sections (1–16) and whether each
must appear daily, the enabled boxes, sources with a retention period (1–90 days,
default 14), filters, webhooks, and the readers' (issuer, subject) pairs. Agent keys
are not configuration; `nightdesk key issue` and `revoke` manage them.

Sources are RSS/Atom or JSON feeds, OPML imports and site-limited Google News
searches, per paper or per section; an omitted section list inherits the paper's
sources and an explicit empty list disables gathering there. A section with
`find_sources` and no declared sources gathers a news search on its title while the
worker looks for publications' feeds; three verified feeds retire the search, and
declared sources always win. Ingestion filters drop betting picks, fantasy advice,
roundups and editorial cartoons, plus each paper's blocked outlets and headline
keywords, using only the feed title and URL, so a dropped item costs no fetch and
never reaches a model.

Optional editorial fields let one pipeline serve any paper: a paper `brief` (at most
2000 characters), a `brief` per section (at most 600) and motto guidance. Guidance is
trimmed plain text without HTML or Markdown links. `deadline` is an `HH:MM` local
time and `draft_lead` (default 90 minutes) is the window before it in which the
worker may draft. Each reader supplies a `subject`, an absolute `subject_file` or a
provider `group`,
so secret stores need not template YAML. An invalid reload keeps the previous
snapshot.

## Edition schemas v1 and v2

```json
{
  "schema": 2,
  "lead": "economy-1",
  "motto": { "kind": "scripture", "text": "…", "source": "Proverbs 16:3 (KJV)" },
  "sections": [
    { "key": "economy",
      "items": [
        { "id": "economy-1", "kind": "news", "title": "…", "summary": "…",
          "why": "…", "source": "…", "url": "https://…", "minutes": 6,
          "byline": "…",
          "sources": [ { "outlet": "…", "url": "https://…" } ] } ] } ],
  "boxes": { "markets": { "source": "…", "as_of": "…", "quotes": [] } }
}
```

In schema v1 an item's `kind` is `news`, `seasonal`, `evergreen` or `brief`, and it
carries fields such as a title, summary, why, source, HTTPS URL, reading time and
an optional image. Section keys must
be the paper's, and with "every section daily" on, a missing section is an error.
Item IDs are the agent's; feedback and the item API refer to them, and an ID keeps
naming the same story across replacements.

Schema v2 adds an optional per-item `byline`, up to 12 further-coverage `sources`
(outlet, URL, optional byline) and the `opinion` kind. Opinion items follow the news
in their section, never count as coverage and cannot be the lead; one may name the
story it argues about (`about`) with a one-line `stance`, and the reader then shows
it under that story. Both schemas are published as JSON Schema in
[`schema/`](../schema/), generated from the Go types and tested against them.

**The compatibility promise.** The server accepts v1 permanently alongside v2;
an agent written against v1 never has to upgrade, and v1's published contract does
not change. More generally, a minor release never breaks stored configuration,
editorial revisions or reader notes; see [upgrading](upgrading.md).

## Agent API

| Call | Capability | Purpose |
|---|---|---|
| `GET /api/v1/paper` | read | The effective paper: masthead, place, role, deadline policy, briefs, deadline, motto guidance, ordered sections (with sources and lean), boxes, retention, reader notes, recent candidate URLs and the editorial revision. Readers and subject paths are never returned. |
| `PATCH /api/v1/paper` | edit | Edit the editorial fields as a merge patch, validated as a whole and saved as a new revision, or 422 with `errors[].path` and `rule` |
| `GET /api/v1/paper/revisions` | read | Kept revisions, newest first |
| `POST /api/v1/paper/restore` | edit | Save a copy of revision n as the newest; `0` returns to the declaration |
| `GET`/`PUT /api/v1/paper/notes` | read / edit | The agent-maintained reader notes |
| `GET /api/v1/feedback?after=<cursor>` | read | Reader events (up, down, save, unsave) since a cursor |
| `GET /api/v1/editions`, `GET /api/v1/editions/{date}` | read | Published editions |
| `GET /api/v1/items/{date}/{id}` | read | One published item with its section and publication time |
| `PUT /api/v1/editions/{date}` | publish | Publish or replace an edition (v1 or v2) idempotently; `author` papers only |
| `GET /api/v1/candidates` | read | Gathered candidate snapshots per section, paginated; their content is data, never instructions |
| `GET`/`PUT /api/v1/drafts/{date}`, `GET /api/v1/drafts` | read / draft or publish | The date's draft with state, revision and action log; submit or replace it |
| `POST /api/v1/drafts/{date}/review` | review | `approve`, `cut`, `swap`, `edit`, `note` or `send_back` on the current revision |
| `POST /api/v1/drafts/{date}/publish` | publish | Publish the approved revision (or an `author` paper's submitted draft) |
| `GET /api/v1/events?after=<cursor>` | read | Poll the event stream webhooks deliver |
| `GET /api/v1/lean/outlets` | read | Rated outlet domains and their lean from the operator's ratings file |
| `POST /api/v1/candidates`, `/runs/{date}/claim`, `/runs/{date}/report`, `/gather/dropped`, `/gather/health`, `/sections/{key}/sources` | gather or draft | Worker endpoints: ingest snapshots, claim the day's draft lease and settle its spending, report filter and fetch counts, store discovered feeds |

All calls use `Authorization: Bearer <agent key>`. The full contract is the
[OpenAPI description](reference/openapi.json), generated from the Go handlers and
tested against them; client guidance is in the
[integration guide](../integrations/README.md).

## Drafting with models

Drafting runs only in the worker, for `reviewer` and `none` papers. Each install
configures two tiers, as MuckScraper does: a fast model for labels (section fit,
same-story checks, feed discovery) and a writing model for the draft. A tier is any
OpenAI-compatible endpoint (Ollama included) or Anthropic's API through its Go SDK.
The native Claude provider exists because Anthropic's OpenAI-compatible endpoint
rejects the `json_object` response format and is meant for evaluation; through the
SDK each prompt's JSON shape is sent as a structured output, so a reply that does
not decode is a provider fault rather than a retry, and the stable prompt prefix is
cached. A refusal is a failed call, never draft text.

Prompts are versioned with the product and embedded; an install may override any
of them with a `<name>.md` file and reset it to the default. Every model call is
traced with OpenTelemetry using the GenAI semantic conventions, so Langfuse or any
other OTLP receiver can show cost, latency and quality; traces carry metadata by
default and prompts and outputs only when the operator opts in, with credentials and
reader notes redacted. Spending is bounded per attempt by the worker's limits on
calls and tokens; tier prices are configuration, used only to estimate cost in
`nightdesk check` and the quality report.

**Checks at publish.** Each item is checked against the stored snapshots of every
source it names: figures, names and "former" titles absent from the source text are
warnings with their path, and an unavailable source is "unverified", never a
failure. Cut-off text in a draft (a title ending on a function word, a summary or
why without terminal punctuation) refuses publication; the worker discards such a
draft and tries again. An agent's own `PUT` keeps these as warnings, so the edition
contract does not change under it.

## Coverage lean

A per-section switch, off by default, for sections where lean matters (national
politics and policy, say). On each story the reader shows a left/center/right bar
over its distinct outlets, with wire copies counted once and opinion and unrated
outlets left out and counted, labeled as coverage found among monitored sources
within its time window. A blindspot mark appears only above a minimum number of
outlets, and failed fetches are reported rather than counted as a side's silence.
When a section is switched on, gathering seeks coverage from each side, and the bar
says so, because seeking changes the sample.

**Night Desk ships no ratings.** It ships the ratings file format (outlet, domains,
lean on a five-point scale, optional factuality, and a required source with name,
URL, licence and attribution) and an importer, `nightdesk ratings import`, that
turns a CSV into that file. The operator supplies ratings from a source they are
licensed to use; the attribution is shown wherever a rating appears, and each
edition keeps the ratings it used. [AllSides](https://www.allsides.com/media-bias/media-bias-ratings)
is one example of such a source, usable only under its own terms.

## Design choices and their reasons

| Part | Choice | Why |
|---|---|---|
| Language | Go | One static binary; cross-compiles to arm64 and amd64 without cgo; the standard library covers HTTP, templating with contextual escaping, `embed`, crypto and image decoding. |
| HTTP | `net/http` and its pattern router | Nothing a framework adds is needed. |
| Storage | SQLite (WAL) through `modernc.org/sqlite`, pure Go | One writer and small data: one-file backups (`VACUUM INTO`), no second service or credential, and full-text search (FTS5) in the same database. |
| Pages | Server-rendered `html/template` and about 15 KB of plain JavaScript | Escaping by default, one rendering path and a fast first paint on a phone. |
| Front-end tooling | None: no framework, no bundler, no build step | The interactions are few; a framework adds a build, bundle weight and update churn. |
| Images | A content-addressed directory; downscaled to at most 1200 px wide and re-encoded as JPEG in pure Go | Strips metadata, neutralizes polyglot files, served immutable. |
| Sign-in | `coreos/go-oidc` and `golang.org/x/oauth2` | Hand-written token verification is where mistakes happen. |
| Fonts | OFL faces embedded and subset to Latin | No third-party font requests; works offline. |
| Container image | Distroless static, non-root, multi-arch | About 45 MB, almost all of it the stripped static binary. |

**A single binary with SQLite.** Night Desk serves a handful of papers and readers;
one process, one file database and one data directory make installing, backing up
and restoring simple, and leave no database server or credential to run. The same
reason ruled out Meilisearch for search: SQLite's FTS5 is enough at this scale and
keeps one binary and one database.

**A reader that works without JavaScript.** Navigation is plain links and editing
is plain forms; the script only enhances page turns, feedback and offline reading.
The reader keeps working on old phones, in reader modes and with script blocked,
the strict Content-Security-Policy stays simple, and an edition stays one cacheable
HTML document, which is also the offline unit.

**Server-rendered templates instead of a single-page app.** A SPA (React, Svelte or
SvelteKit) would add a Node build, a bundle and a second rendering path, and would
break the no-script reader, the single binary and the first-load budget. If a part
of the reader ever needs real client state, the preferred route is small compiled
islands for just the interactive parts (contents, Menu, section editor), embedded
in the binary as static files, with the server still rendering the paper. A
whole-reader rewrite is ruled out unless the no-script reader and the single binary
are given up deliberately.

**No embeddings.** Same-story checks at a paper's scale (dozens of candidates) are a
fast-model question or a word-overlap rule. Wire copies are found by text overlap;
"Since published" follow-ups use candidate groups and a conservative Jaccard rule
over headline words. If similarity search is ever needed, vectors in SQLite
searched directly would be enough; no vector database or embedding model is
required.

**Ideas ported, not vendored.** MuckScraper's useful parts (copy detection, source
checks, Google News resolution, bylines, headline truncation rules, ingestion
filters) are small pure functions, ported to Go with their tests and its MIT notice.
Running its Python would add a second runtime and its own dependency upkeep.

**Extraction behind one interface.** Article extraction uses a maintained Go port
of Mozilla Readability, falling back to JSON-LD `articleBody` and the page's AMP
version for short results. Python's trafilatura often extracts better, but its Go
port is unmaintained; if Readability proves too weak, a trafilatura sidecar can
replace the extractor without touching the rest.

**Fetching that degrades gently.** Retries depend on the status (429, 5xx and no
response only; refused addresses never), with a per-run retry budget and a cool-off
for hosts that keep failing. The cool-off lives in the worker process, because the
worker has no database and forgetting it on restart costs at most a few more tries.

**Web push without a payload.** The push says only that today's paper is ready, so
nothing about the paper leaves the server and no message encryption is needed; the
VAPID key pair comes from the standard library.

**Modules, each with a reason.** `coreos/go-oidc` and `golang.org/x/oauth2`
(sign-in), `modernc.org/sqlite` (pure-Go SQLite), `golang.org/x/image` (WebP
decoding and scaling), `golang.org/x/net` (HTML tokenization), one YAML parser,
`mmcdole/gofeed` (RSS, Atom and JSON feeds), `codeberg.org/readeck/go-readability/v2`
(article extraction), the official MCP Go SDK (the stdio transport and protocol),
OpenTelemetry Go (model-call traces) and Anthropic's Go SDK (the Claude provider,
used only by the worker). A new module needs a reason of the same kind; licences
are in [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md).

**Alternatives considered.** Python (FastAPI, pydantic, authlib, Pillow): a heavier
image and memory, and Pillow's native libraries bring steady security churn. Node or
TypeScript: npm supply chain and a build step. Rust: the smallest and safest, but
slower to change for an I/O-bound web service.

## Fixed decisions

These are contracts; changing one is a breaking change.

1. **Versioned edition schemas.** Every edition carries `"schema": 1` or
   `"schema": 2`, and v1 is accepted permanently.
2. **Strict validation with exact errors.** Unknown fields are rejected. Each error
   names its JSON path and rule, for example
   `sections[3].items[0].url: must be https`, so an agent (often a small model) can
   correct and retry within the same run and the reader never sees a malformed
   edition. A publish with warnings succeeds and returns them.
3. **Text is plain text.** No Markdown or HTML in any field; nothing to sanitize.
4. **Publishing is idempotent.** `PUT /api/v1/editions/{date}` replaces that date's
   edition, so a retry is safe.
5. **Readers are (issuer, subject),** never email.
6. **Tenant isolation is in the data layer.**
7. **Migrations are embedded and forward-only.**
8. **Facts come from code, not the model.** Sun, moon and solunar times are computed
   from the paper's coordinates; weather comes from Open-Meteo (keyless, cached
   hourly); a markets box must carry `source` and `as_of` or it is not shown; a
   motto must carry its citation. Without an agent, mottos and markets come only
   from verified sources or are left out.

## Targets

First load under 160 KB before images (a reader test enforces it); server time
under 50 ms; container image about 45 MB; memory under 50 MB.

## Designed for, not built

A reader with several papers (the data model is many-to-many) and more looks (each
a stylesheet over shared tokens). Planned work is in the [roadmap](roadmap.md).
