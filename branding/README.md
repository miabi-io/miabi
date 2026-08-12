<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="miabi-wordmark-white.svg">
    <img src="miabi-wordmark.svg" alt="Miabi" width="300">
  </picture>
</p>

Official [Miabi](https://github.com/miabi-io/miabi) logo assets, provided for
public use — articles, slide decks, "works with Miabi" badges, integration
docs, and similar. Please read [`LICENSE`](LICENSE) before using them.

## Assets

| File | Use it for |
|------|------------|
| [`miabi-wordmark.svg`](miabi-wordmark.svg) | Full logo (mark + wordmark) on **light** backgrounds. |
| [`miabi-wordmark-white.svg`](miabi-wordmark-white.svg) | Full logo on **dark** backgrounds. |
| [`miabi-mark.svg`](miabi-mark.svg) · [`miabi-mark.png`](miabi-mark.png) | The symbol only (gradient) — favicons, avatars, tight spaces. |
| [`miabi-mark-white.svg`](miabi-mark-white.svg) | The symbol only, solid white — for dark/colored backgrounds. |
| [`miabi-logo.svg`](miabi-logo.svg) · [`miabi-logo.png`](miabi-logo.png) | App icon (rounded square, **purple** — primary). |
| [`miabi-logo-navy.svg`](miabi-logo-navy.svg) · [`miabi-logo-navy.png`](miabi-logo-navy.png) | App icon, **navy** variant. |

SVG is preferred (scales cleanly). PNGs are provided for tools that don't accept
SVG (mark ≈ 512 px, navy icon ≈ 1024 px).

## Badges

Ready-made pills in [`badges/`](badges), also served from
`https://miabi.io/badges/<file>` — preview them at
[miabi.io/badges/preview.html](https://miabi.io/badges/preview.html).

| Badge | Use it for |
|-------|------------|
| **Running on Miabi** | The footer of a site or app you host on Miabi. |
| **Deploy on Miabi** | The top of a README, pointing readers at how to run your project. |

Both come in the same four variants. Pick `-dark` on light backgrounds, `-light`
on dark ones, and `-white` (transparent, single-colour) on a coloured or brand
background. `-purple` is a filled gradient that carries itself on any
background — the one to reach for when the badge is a call to action.

```markdown
[![Running on Miabi](https://miabi.io/badges/running-on-miabi-dark.svg)](https://miabi.io?ref=badge)

[![Deploy on Miabi](https://miabi.io/badges/deploy-on-miabi-purple.svg)](https://docs.miabi.io/docs/getting-started/quickstart?ref=badge)
```

They are vectors: set any `height` (24–40 px reads well) and the width follows.
Please keep the link back to Miabi, and don't recolour or redraw them.

## Which one?

- **Referring to Miabi in text/docs** → a wordmark (`miabi-wordmark*.svg`).
- **App/tile/avatar** → an icon (`miabi-logo*`).
- **Favicon / very small / single-colour** → a mark (`miabi-mark*`).
- Pick the **white** variant on dark backgrounds, the default on light ones.

## Brand colors

| | Hex |
|-|-----|
| Navy (primary) | `#101A37` |
| Purple (accent) | `#9333EA` — gradient `#A855F7` → `#7E22CE` |

## Usage guidelines

**Do**
- Keep clear space around the logo (at least the height of the mark's stroke).
- Use the provided files as-is; scale proportionally.
- Use a variant with enough contrast against its background.

**Don't**
- Recolor, rotate, stretch, add effects, or redraw the mark.
- Use the logo to imply endorsement, partnership, or that your product **is**
  Miabi.
- Use it as your own app/product/organization icon.

See [`LICENSE`](LICENSE) for the full terms.
