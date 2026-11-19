# Night Desk brand

| File | Use |
|---|---|
| `wordmark-light.svg`, `wordmark-dark.svg` | "Night Desk" with the red rule, for light and dark backgrounds (README header). |
| `nd-mark-cream.svg`, `nd-mark-ink.svg` | The ND mark for dark and light backgrounds, transparent. |
| `nd-mark-mono.svg`, `mask-icon.svg` | Single-colour mark (print, monochrome icon slots). |
| `icon.svg` | The app icon: the mark on the ink tile, square. |
| `night-desk-icon-512.png` | The app icon as PNG, e.g. for an identity provider's application icon. |
| `hero.png` | The desk-lamp illustration with the wordmark. |
| `social-preview.png` | 1280×640 repository social preview. |
| `spot-*.svg` | Lamp, mug, newspaper and moon spot illustrations, cream over transparency, for empty states and docs. |

Colours: ink `#171719`, cream `#EFE4D5`, rule red `#C54E30`; the lamp's glow is
the only gradient.

The reader's own icons are generated: `web/brand/nd-mark.svg` and
`nd-mark-small.svg` (a heavier cut for 32 pixels and below) are the sources;
`web/icon-gen/main.go` explains how they are rendered into `web/static`.

The SVGs were traced from raster renders with `tools/trace.mjs` (mark and
wordmark) and `tools/spots.mjs` (spots, cut from the hero). Both need Node with
`npm install sharp potrace`; run them from outside the repository with the
render directory and an output directory as arguments.
