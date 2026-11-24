# Screenshots

The images here show a fictional demo paper, *The Evening Ledger* of Riverside.
Every headline, outlet, byline and link is invented; the links use the reserved
`.example` domain and the pictures are drawn by the demo program itself.

| File | What it shows |
| --- | --- |
| `desktop-pages-light.png` | Front page, Pages layout, light (1440×900) |
| `desktop-pages-sepia.png` | The same in Sepia |
| `desktop-pages-dark.png` | The same in Dark |
| `desktop-list.png` | List layout, from the front page into World |
| `desktop-reader.png` | Reader layout, the World section |
| `desktop-search.png` | Archive search for "bridge" across four editions |
| `phone-front-dark.png` | Front page on an iPhone 15, dark |
| `phone-article.png` | A story with byline, other coverage and an opinion column, iPhone 15 |
| `phone-story.png` | The reading view of that story, from the demo's stored copy, Sepia, iPhone 15 |

## Run the demo reader

```sh
go run ./docs/screenshots/demo            # then open http://127.0.0.1:8080/login
go run ./docs/screenshots/demo -addr 127.0.0.1:0   # any free port
```

The demo is a development tool, not part of the `nightdesk` binary. It starts
the real reader and agent API with a temporary data directory, a fake OpenID
Connect provider on loopback that signs everyone in as the demo reader, and an
image fetcher that paints the pictures instead of downloading them. It then
publishes the editions in `demo/editions/` through `PUT /api/v1/editions/{date}`
and prints the sign-in URL. Ctrl-C stops it and removes the data directory
(`-keep` leaves it behind). Weather is off because it needs Open-Meteo; sun and
solunar times are computed locally.

## Regenerate the screenshots

```sh
node docs/screenshots/capture.mjs
```

This builds and starts the demo, signs in with Playwright's Chromium, writes the
PNGs here and stops the demo. To shoot a reader you already started, pass its
URL: `node docs/screenshots/capture.mjs http://127.0.0.1:8080`. It needs Go and
Node with Playwright (`require('playwright')` must resolve and Chromium must be
installed, for example with `npx playwright install chromium`).

To change the paper, edit the JSON editions and check them with the validator:

```sh
go run ./cmd/nightdesk validate docs/screenshots/demo/editions/2026-11-19.json \
  --sections world:required,economy,science,local:required,opinion
```
