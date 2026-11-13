# ShiftBox brand kit

Approved direction: **02 — Command Space**. The open corner brackets and terminal prompt represent a ready-to-code workspace. Use the exact product spelling **ShiftBox**.

## Choose a file

| Use | File |
| --- | --- |
| WebUI, light background | `svg/shiftbox-logo-light.svg` |
| WebUI, dark background | `svg/shiftbox-logo-dark.svg` |
| Compact navigation or icon | `svg/shiftbox-mark-light.svg` / `shiftbox-mark-dark.svg` |
| Single-color reproduction | `svg/shiftbox-logo-black.svg` / `shiftbox-logo-white.svg` |
| Vertical layout | `svg/shiftbox-stacked-light.svg` / `shiftbox-stacked-dark.svg` |
| Wordmark alone | `svg/shiftbox-wordmark-light.svg` / `shiftbox-wordmark-dark.svg` |
| Repository / organization / container registry avatar | `repository/shiftbox-avatar-violet-1024.png` |
| Alternate repository avatars | `repository/shiftbox-avatar-light-1024.png` / `shiftbox-avatar-dark-1024.png` |
| Repository social preview | `repository/shiftbox-social-light-1280x640.png` / `shiftbox-social-dark-1280x640.png` |
| Modern browser favicon | `icons/favicon.svg` |
| Legacy browser favicon | `icons/favicon.ico` (16, 32, 48 px) |
| Apple home screen icon | `icons/apple-touch-icon.png` (180 px) |
| App icons | `icons/icon-192.png`, `icons/icon-512.png` |
| Maskable app icon | `icons/icon-maskable-512.png` |
| Review sheet | `shiftbox-brand-preview.png` |

All logo SVGs contain real paths, including outlined letters: no fonts, linked images, scripts, or external dependencies. SVG and PNG logo/mark files have transparent backgrounds. Avatar and social-preview files have opaque backgrounds. The favicon has transparent outer corners; home-screen icons are full bleed. PNG horizontal logos are 1600 px wide, stacked logos are 1200 px wide, marks are 512 px square, and repository avatars are available at 512 and 1024 px.

## Color and spacing

| Token | Value | Use |
| --- | --- | --- |
| Violet | `#4834F5` | Primary symbol and app-icon tile |
| Light violet | `#A599FF` | Symbol on dark backgrounds |
| Ink | `#191821` | Wordmark on light backgrounds / dark backgrounds |
| White | `#FFFFFF` | Reversed wordmark and symbol |

Use the light version on white or near-white and the dark version on Ink or a similarly dark surface. Use black or white when only one color is available. Do not distort, rotate, add gradients, add shadows, change letter spacing, or replace the wordmark with live text. The colors describe the logo assets; they do not change the application's semantic theme tokens.

Maintain clear space around the visible artwork of at least one arrow-stroke thickness (roughly 1/6 of the symbol height). Exported SVGs include a small safety margin; add layout padding where needed to meet this clear space. Suggested minimums: 140 px wide for the horizontal logo, 24 px wide for the standalone mark. Use the dedicated favicon at 16–20 px. Keep the complete symbol inside avatar crops; the supplied square avatars include padding for circular crops. Use the maskable icon only for masks; it has extra padding.

## WebUI use

Runtime copies are already available in `web/public/brand/`, served by Vite at `/brand/`. This kit supplies assets for the rebrand; existing application screens and deployment identities are not changed by generating it.

```tsx
<img src="/brand/shiftbox-logo-light.svg" alt="ShiftBox" className="h-8 w-auto dark:hidden" />
<img src="/brand/shiftbox-logo-dark.svg" alt="ShiftBox" className="hidden h-8 w-auto dark:block" />
```

The existing WebUI's `dark` variant follows `.dark-mode`, so the visible asset follows the application's selected theme. If a nearby label or link already says ShiftBox, use `alt=""` for its decorative logo. Do not make the logo a focusable image; wrap it in a labeled link when it navigates home.

Add these tags when adopting the new branding in `web/index.html`:

```html
<link rel="icon" href="/brand/favicon.ico" sizes="16x16 32x32 48x48" />
<link rel="icon" href="/brand/favicon.svg" type="image/svg+xml" sizes="any" />
<link rel="apple-touch-icon" href="/brand/apple-touch-icon.png" sizes="180x180" />
<meta name="theme-color" content="#4834F5" />
```

An optional `icons/site.webmanifest` is also copied to `/brand/site.webmanifest`. Its icon URLs are relative to that directory, and its start URL and scope assume an application hosted at `/`. Adjust those values for deployments under a path prefix before linking the manifest. The manifest supplies identity and launch icons; it does not add offline behavior.

```html
<link rel="manifest" href="/brand/site.webmanifest" />
```

## Sources and regeneration

`source/command-space.json` is the canonical vector outline and palette source. The outlines were traced from the large primary logo in the approved option #2 concept, preserving its letterforms and symbol rather than substituting a font. Flat, explicit fills replace the concept image's slight texture. The dark-background color and padded icon layouts are production adaptations.

The original concept was created using the built-in image generator. Its original prompt is recorded in `source/provenance.json`. A later transparent-image preparation was evaluated; the final vectors use the approved original to preserve fidelity.

To regenerate all exports and their WebUI copies:

```sh
cd branding
npm ci
npm run build
```

Only the brand tooling depends on Sharp; the WebUI gains no package dependency. To use an existing Sharp installation, set `SHARP_MODULE` to its absolute package directory and run `node branding/build-brand-assets.mjs` from the repository root. Do not hand-edit the runtime copies: change the canonical source or exporter and regenerate.

The generated files are editable project assets. No external font is required or bundled. This kit does not introduce a new trademark or redistribution license; those remain project policy decisions.
