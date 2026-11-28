# Night Desk brand

Night Desk's brand is **Lamplight**: the logo's ink and cream, the red rule as
its one accent, and the desk lamp's glow. The reader also offers two other
themes, Broadsheet and Late Edition (see [Themes](#themes)).

## The logo

Night Desk's logo is the **wordmark** (“Night Desk” over a short red rule) and
the **ND mark** (the same letters and rule, for small spaces). Both are fixed
artwork: use the files in `docs/brand/`, never retype or redraw them.

| Use | File |
|---|---|
| README, docs, anywhere with room | `wordmark-light.svg` / `wordmark-dark.svg` |
| Icons, avatars, colophons | `nd-mark-*.svg`; `nd-mark-small-*.svg` at 32 px and below |
| One colour (print, emboss, photos, pinned tab) | `*-mono.svg` (ink) / `*-mono-white.svg` |
| Launch art, about pages | `lockup-stacked-*.svg`, `lockup-horizontal-*.svg` (desk illustration + wordmark) |

**Clear space.** Keep empty space around the logo equal to the height of the
red rule × 3 for the wordmark, and the rule's height × 2 for the mark. Nothing
(text, edges, other logos) enters it.

**Minimum size.** Wordmark 96 px (20 mm) wide; mark 16 px, using the small cut
below 32 px.

**Do**
- Use the light version on light grounds and the dark version on dark grounds.
- Keep the rule red (`#C54E30`) in colour versions; use mono when only one colour is available.
- Put the logo on plain ink, cream or paper colours.

**Don't**
- Recolour the letters or the rule, or swap the red for the accent.
- Stretch, condense, rotate, outline or add shadows or glows to the logo.
- Set “Night Desk” in another font and call it the logo.
- Put the logo on busy photos or low-contrast grounds.
- Use the ND mark and the wordmark side by side; they repeat each other.

## Colour

The brand colours, which are also the reader's default (Lamplight) theme:

| Role | Light | Dark | Use |
|---|---|---|---|
| Paper | `#F8EFE1` | `#1B1918` | The edition page |
| App ground | `#EAE0D0` | `#121214` | Behind the paper, app chrome |
| Ink | `#171719` | `#EFE4D5` | Headlines and text |
| Muted | `#655249` | `#B9A796` / `#C5B2A1` | Datelines, metadata |
| Rule | `#171719` | `#EFE4D5` | Column and section rules |
| Accent | `#A63E22` | `#E8805F` | Links, labels, focus |
| Logo rule | `#C54E30` | `#C54E30` | The logo only |
| Lamp glow | `#F0B76D` | `#F0B76D` | Illustration only, never text |

Every text colour reaches 4.5:1 against its background, and rules 3:1 (WCAG 2.2
AA). Check new pairs with `python3 docs/brand/tools/contrast.py FG BG`; `web/themes_test.go`
checks every theme on each build.

## Type

All free, all SIL Open Font License 1.1, embedded and subset in the app:

- **UnifrakturCook Bold**: a paper's own masthead, and nothing else.
- **Source Serif 4** and **IBM Plex Sans**: in place of Old Standard TT and Libre
  Franklin in the Late Edition theme only.
- **Old Standard TT**: headlines, stories, taglines.
- **Libre Franklin**: labels, datelines, buttons, app chrome.

UnifrakturCook covers Latin only; mastheads in other scripts fall back to Old
Standard TT and then Georgia.

## Themes

Readers pick a theme per paper in the reader's menu (Theme), next to the light,
sepia and dark modes, which work in every theme. Lamplight is the default.

| Theme | Idea | Type | Masthead |
|---|---|---|---|
| Lamplight | Ink and cream under a desk lamp | Old Standard TT, Libre Franklin | Centred, with the logo's short red rule under the name |
| Broadsheet | Newsprint grey, near-black ink, proof red | Old Standard TT, Libre Franklin | Centred between double rules |
| Late Edition | Blue-black and moonlight | Source Serif 4, IBM Plex Sans | Flush left on an accent rail |

[`themes.json`](themes.json) is the source of truth for every theme's colours,
roles and fonts. The web reader's `app.css` is tested against it, and other
clients (the iOS app) read it rather than copying hex values. Sepia is shared
by all themes. Late Edition's fonts are only downloaded when a reader picks it.

## Mastheads and colophon

A paper's name belongs to its reader: Night Desk never replaces it. Night Desk
signs the edition with the colophon: the small ND mark and “Set by Night Desk”
under the masthead, in the muted colour.

## Voice and tone

Night Desk talks like a good night editor: plain, calm and exact. Short
sentences; say what happened and what to do. Warm without being cute. No hype,
no exclamation marks, no “AI-powered”. It is a paper for one reader, so it says
“your paper”, never “users”.
