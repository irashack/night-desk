# Reader API v1

The reader API serves a reader's papers as JSON for native apps: the papers they
read, editions, stories, archive search, 👍/👎/Save, and images. It is the reader's
own view, the same as the web reader shows, and it is separate from the agent API
(`/api/v1`, [OpenAPI](openapi.json)), whose paper keys have editorial powers.

Everything here lives under `/api/reader/v1`. Version 1 only grows: new fields and
endpoints may appear, and clients should ignore fields they do not know. A change
that would break a client gets a new version beside this one.

## Signing in an app

An app signs in through the server's own reader sign-in, whatever
`server.auth.mode` is (`oidc`, `local`, `proxy` or `none`). It never sees a password
or an identity provider's tokens. The flow is OAuth 2.0's authorization code grant
with PKCE (RFC 7636, `S256` only), with a fixed redirect for Night Desk apps:

1. **Check the address.** `GET /api/reader/v1` needs no sign-in and answers
   `{"api": 1, "name": "Night Desk", "version": "v0.12.0", "auth": {"mode": "oidc",
   "authorize": "/app/authorize", "token": "/api/reader/v1/token",
   "redirect_uris": ["nightdesk://auth"]}, "web_push": true}`.
2. **Open the confirmation in the system browser** (on iOS,
   `ASWebAuthenticationSession` with the callback scheme `nightdesk`):

   ```
   GET /app/authorize?response_type=code
       &redirect_uri=nightdesk://auth
       &state=<16 to 128 URL-safe characters>
       &code_challenge=<BASE64URL(SHA-256(verifier))>
       &code_challenge_method=S256
       &client_name=<app and device, e.g. "Night Desk on Ira's iPhone">
   ```

   A reader who is not signed in signs in first (the provider, the password form,
   or the proxy) and comes back to this page. It names the app and asks
   **Allow** or **Cancel**.
3. **Receive the code.** Allow redirects to `nightdesk://auth?code=<code>&state=<state>`;
   Cancel to `nightdesk://auth?error=access_denied&state=<state>`. Check `state`.
   A code works once, for two minutes.
4. **Exchange it** with the verifier, as a form or JSON:

   ```
   POST /api/reader/v1/token
   grant_type=authorization_code&code=<code>&code_verifier=<verifier>&redirect_uri=nightdesk://auth
   ```

   ```json
   {"access_token": "…", "token_type": "Bearer", "expires_in": 2592000, "client": "Night Desk on Ira's iPhone"}
   ```

   A wrong verifier, a used or expired code, or another redirect gets
   `400 {"error": "invalid_grant"}`, and the code is spent either way.
5. **Call the API** with `Authorization: Bearer <access_token>`. Keep the token in
   the platform's keychain.

The token is a reader session of its own, named after the app. Each use renews it
for `server.session_lifetime` (default 720h, 30 days), so an app that is opened now and
then stays signed in. It ends when the app signs out (`POST /api/reader/v1/signout`),
and with everything else that ends a reader's sessions: a password change or reset,
an administrator removing the account or ending its sessions, or a change of
`server.auth.mode`.

The API reads only the `Authorization` header, never the browser's cookie, so it
needs no CSRF token.

**Group papers.** A token carries the group membership of the browser sign-in
that granted it, for as long as that sign-in's membership counts
(`server.group_session_lifetime`, default 24 hours, as for the browser). After that `/me` lists the
paper under `sign_in_again`, and reading it answers `403 sign_in_again`. Running
the sign-in again restores it if the reader is still a member.

**Proxy mode.** The app has no proxy cookie, so the proxy must let
`/api/reader/` through without its own sign-in. The bearer token is what
authenticates those requests, and Night Desk ignores the proxy's user header on
them. `/app/authorize` stays behind the proxy like every other page. See
[configuration](../configuration.md#reader-sign-in).

**None mode.** Tokens are issued and accepted under the same rule as the pages: only
from private networks unless `acknowledge_public_access` is set.

## Endpoints

All answers are JSON. Errors are `{"error": "<code>", "message": "<sentence>"}` with
codes `unauthorized` (401: no token, or it ended), `no_paper` (403: no paper is set up
for this reader), `sign_in_again` (403), `private_only` (403), `not_found`,
`no_edition` (404: the paper has published nothing yet), `invalid_request` (400) and
`unavailable` (500).

`GET` answers carry an `ETag` and `Cache-Control: private, no-cache`. Send
`If-None-Match` to revalidate a copy kept offline; an unchanged answer is
`304 Not Modified`.

| Method and path | Answer |
|---|---|
| `GET /api/reader/v1` | Server check (no sign-in), above. |
| `POST /api/reader/v1/token` | Code exchange (no sign-in), above. |
| `POST /api/reader/v1/signout` | `204`; ends this token and the push subscriptions made with it. |
| `GET /api/reader/v1/me` | The reader and their papers. |
| `GET /api/reader/v1/papers/{paper}/editions?page=N` | Edition dates, newest first, 60 a page. |
| `GET /api/reader/v1/papers/{paper}/editions/latest` | The newest edition. |
| `GET /api/reader/v1/papers/{paper}/editions/{date}` | One edition. |
| `GET /api/reader/v1/papers/{paper}/editions/{date}/items/{item}` | One story with its stored text. |
| `GET /api/reader/v1/papers/{paper}/search?q=&in=&saved=1` | Archive search, as `/search`. |
| `POST /api/reader/v1/papers/{paper}/feedback` | `204`; body `{"date", "item", "kind"}`, kind `up`, `down`, `save` or `unsave`. |
| `GET /api/reader/v1/images/{sha256}.jpg` | The paper's copy of an item image. |

### `GET /me`

```json
{
  "name": "ira",
  "client": "Night Desk on Ira's iPhone",
  "papers": [
    {"id": "daily", "masthead": "The Daily", "default": true, "timezone": "America/Chicago",
     "latest": {"date": "2026-11-28", "number": 212, "lead": "Bridge reopens downtown"}}
  ],
  "sign_in_again": []
}
```

- `name` is the local username or the proxy's user. It is empty for OIDC, where
  the subject is opaque, and in `none` mode.
- `papers` lists the reader's own paper first (`default`, the paper at `/` in the
  browser), then group papers. `latest` is `null` until the first edition.
  Widgets can use `latest.lead` without fetching the edition.

### `GET /papers/{paper}/editions`

`{"editions": [{"date": "2026-11-28", "lead": "Bridge reopens downtown"}, …], "next_page": 2}`.
`next_page` is `0` on the last page.

### `GET /papers/{paper}/editions/{date}` and `/latest`

```json
{
  "paper": {"id": "daily", "masthead": "The Daily", "default": true, "timezone": "America/Chicago", "latest": null},
  "date": "2026-11-28",
  "dateline": "Saturday, November 28, 2026",
  "number": 212,
  "motto": {"kind": "quotation", "text": "…", "source": "…"},
  "lead": { "…": "an item, below" },
  "sections": [
    {"key": "news", "title": "News", "items": [ ], "markets": null}
  ],
  "weather": {"date": "2026-11-28", "high": 41, "low": 30, "wind": 9, "code": 3, "units": "imperial"},
  "almanac": {"sunrise": "2026-11-28T06:58:00-06:00", "sunset": "…", "phase": "Waxing gibbous", "illumination": 0.82},
  "checked": "6:12 am",
  "unreviewed": false,
  "previous": {"date": "2026-11-27"}
}
```

- Sections follow the paper's layout. Opinion columns about a story sit in that
  story's `opinions`; the others come last in their section.
- `weather` and `almanac` appear when the paper turns those boxes on.
- `markets` follows the edition schema's `boxes.markets`.
- `unreviewed` says the edition was published without the reviewer's pass.
- `previous` is `null` for the first edition.

**Items** are the [edition schema v2](../../schema/edition-v2.json) item (`id`,
`kind`, `title`, `summary`, `why`, `source`, `byline`, `sources`, `url`, `minutes`,
`image_url`, `about`, `stance`) plus what the reader shows beside it:

| Field | Meaning |
|---|---|
| `image` | API path of the paper's own copy of the image, or absent. Prefer it to `image_url`. |
| `readable` | The paper holds the story's text: the story endpoint returns paragraphs. |
| `feedback` | `{"vote": "up" \| "down" (absent for none), "saved": bool}`, this reader's. |
| `opinions` | Opinion columns about this story: `title`, `url`, `source`, `stance`, `lean`. |
| `later` | Later coverage gathered after an archived edition: `title`, `url`, `outlet`, `gathered`. |
| `coverage` | The coverage-by-lean bar: `left`, `center`, `right`, `unrated`, `opinion`, `blindspot`, `window`, `attribution`. |
| `provenance` | The source checks: `status` (`checked` or `notes`), `summary`, `notes`, `checked`, `compared`, `cited`. |

### `GET …/items/{item}`

`{"item": {…}, "date": "2026-11-28", "paragraphs": ["…"], "partial": false, "gathered": "2026-11-28T03:12:00Z"}`.
`paragraphs` is `null` when the paper keeps only the link; `partial` says the stored
text looks shorter than the piece (a paywall, usually).

### `GET …/search`

`{"results": [{"date", "item", "kind", "title", "summary", "why", "source", "byline", "stance", "outlets"}]}`.
`q` is at most 500 characters; `in` is `writer`, `stance` or `outlets`; `saved=1`
searches the reader's saved stories.
