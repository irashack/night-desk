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
nightdesk demo                                   # or: docker run --rm -p 127.0.0.1:8080:8080 ghcr.io/irashack/night-desk demo
go run ./docs/screenshots/demo                   # from a checkout, on 127.0.0.1:8080
go run ./docs/screenshots/demo -addr 127.0.0.1:0 # any free port
```

The demo lives in `internal/demo`. It starts the real reader and agent API with a
temporary data directory, no sign-in (`server.auth.mode: none`) and a banner
saying it is a demo, and an image fetcher that paints the pictures instead of
downloading them. It then publishes the editions in `internal/demo/editions/`
through `PUT /api/v1/editions/{date}` and prints the reader's address. Ctrl-C
stops it and removes the data directory (`--keep` leaves it behind). Weather is
off because it needs Open-Meteo; sun and solunar times are computed locally.

## Regenerate the screenshots

```sh
node docs/screenshots/capture.mjs
```

This builds and starts the demo without its banner (`-screenshots`), opens it in
Playwright's Chromium, writes the PNGs here and stops the demo. To shoot a reader you already started, pass its
URL: `node docs/screenshots/capture.mjs http://127.0.0.1:8080`. It needs Go and
Node with Playwright (`require('playwright')` must resolve and Chromium must be
installed, for example with `npx playwright install chromium`).

To change the paper, edit the JSON editions and check them with the validator:

```sh
go run ./cmd/nightdesk validate internal/demo/editions/2026-11-19.json \
  --sections world:required,economy,science,local:required,opinion
```
