# Kobi · Sigil (runtime assets)

The Sigil is Kobi's mark — a deconstructed K with an intelligence dot. Production assets for use in the web app.

## Variants

| File | Purpose |
|---|---|
| `kobi-sigil.svg` | Default static mark, semantic color (uses `currentColor`) |
| `kobi-sigil-watching.svg` | State: idle / monitoring (emerald `#34d399`) |
| `kobi-sigil-investigating.svg` | State: active work / streaming (amber `#fbbf24`) |
| `kobi-sigil-awaiting.svg` | State: paused / proposal pending Execute or Dismiss (sky `#38bdf8`) |
| `kobi-sigil-mono-dark.svg` | State-agnostic for dark surfaces (stone-200) |
| `kobi-sigil-mono-light.svg` | State-agnostic for light surfaces (stone-900) |
| `kobi-sigil-micro.svg` | 12–16 px variant (no dot) for favicons and very small UI |
| `kobi-animations.css` | Keyframes for watching / investigating / awaiting |

## Mapping to Copilot UX

- **Watching** → chat panel idle, no active stream
- **Investigating** → tool calls in flight, response streaming
- **Awaiting** → an `ActionProposalCard` is rendered and the user has not yet clicked Execute or Dismiss

## Non-negotiable invariants (don't break the mark)

1. The 2.5px gap between diagonals and spine — never close it.
2. Square caps — never round.
3. The intelligence dot is a circle, not a square or custom shape.
4. Symmetry — upper and lower diagonals mirror exactly.

See `internal/kobi/brand/brand-guidelines.md` (gitignored design reference) for full rationale.

## Source of truth

These files are **canonical**. The original design copies in `internal/kobi/assets/sigil/` were moved here on 2026-04-30 — the design directory at the repo root no longer holds visual assets.
