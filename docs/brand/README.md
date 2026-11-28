# Night Desk brand

How to use all of this: [BRAND.md](BRAND.md). The reader's themes: [themes.json](themes.json).

## Logo

| File | Use |
|---|---|
| `wordmark-light.svg`, `wordmark-dark.svg` | "Night Desk" with the red rule, for light and dark backgrounds (README header). |
| `wordmark-mono.svg`, `wordmark-mono-white.svg` | The wordmark in one colour: ink, or white. |
| `nd-mark-ink.svg`, `nd-mark-cream.svg` | The ND mark for light and dark backgrounds, transparent. |
| `nd-mark-mono.svg`, `nd-mark-mono-white.svg`, `mask-icon.svg` | Single-colour mark (print, monochrome icon slots). |
| `nd-mark-small-{light,dark,mono,mono-white}.svg` | The heavier cut of the mark for 32 pixels and below. |
| `lockup-stacked-*.svg`, `lockup-horizontal-*.svg` | The desk illustration with the wordmark, in light, dark, mono and white. |

## Icons and images

| File | Use |
|---|---|
| `icon.svg` | The app icon: the mark centred on the ink tile, square. |
| `night-desk-icon-512.png` | The app icon as PNG, e.g. for an identity provider's application icon. |
| `social-preview.png` | 1280×640 repository social preview (GitHub: Settings → Social preview). |
| `readme-banner-light.png`, `readme-banner-dark.png` | The README's header, 2560×640. |
| `hero.png` | The desk-lamp illustration with the wordmark. |
| `spot-*.svg` | Lamp, mug, newspaper and moon spot illustrations, cream over transparency, for empty states and docs. |

## Sources

`src/` holds the HTML sources of the social preview, README banner, edition
masthead and the edition OG image template (`og-template.html`, 1200×630,
placeholders `{{masthead}}`, `{{date}}`, `{{headline}}`, `{{paper}}`). They load
the reader's own fonts from `web/static/fonts`; render them with Chromium at
their stated size, for example with Playwright:

```js
await page.setViewportSize({ width: 1280, height: 640 });
await page.goto('file://' + path.resolve('docs/brand/src/social-preview-dark.html'));
await page.screenshot({ path: 'docs/brand/social-preview.png' });
```

The banners are captured at device scale 2.

The reader's own icons are generated: `web/brand/nd-mark.svg` and
`nd-mark-small.svg` are the sources; `web/icon-gen/main.go` explains how they
are rendered into `web/static`. `web/static/nd-mark-mono.svg` is the small cut
in one colour, used as a mask for the masthead colophon.

The mark and wordmark were traced from raster renders with `tools/trace.mjs`,
and the spots with `tools/spots.mjs` (cut from the hero). Both need Node with
`npm install sharp potrace`; run them from outside the repository with the
render directory and an output directory as arguments. `tools/contrast.py`
checks colour pairs and every theme against WCAG 2.2 AA.
