# ox · Design

> Calm, precise terminal output for humans and AI coworkers.

ox's visual language inherits from the SageOx design system: sage-green primary, copper-gold secondary, forest accent, low-noise Tufte minimums. Components are reusable, theme-aware, and adapt to the terminal in front of them (light/dark, `NO_COLOR`, TTY vs piped, headless SSH).

This file is the entry point. It is intentionally short.

## Five rules

1. **`sageox-design` owns the palette.** Colors, themes, and light/dark behavior live in [github.com/sageox/sageox-design](https://github.com/sageox/sageox-design/). ox consumes them via `npm run sync`. Never hand-edit `internal/theme/generated.go`.
2. **Use semantic styles.** `StyleSuccess`, `StyleError`, `StyleAccent` — not raw hex. Defined in `internal/cli/styles.go` and `internal/ui/styles.go`.
3. **Components live in the catalog.** Boxes, timelines, sparklines, spinners, selects, prompts, confirms, columns, log-formatting, markdown. Run `ox dev catalog` (hidden command) to see them all. Don't invent a new widget without checking first.
4. **Terminal context is respected.** Every component has a non-TTY fallback. `NO_COLOR=1` strips color. 80 columns must render cleanly.
5. **Text-only catalog assets.** asciinema `.cast` for recordings, SVG for static snapshots. No PNGs, GIFs, or other binaries in `docs/design/` — ever.

The expanded version with worked rationale lives in [`.claude/rules/design.md`](.claude/rules/design.md).

## Where things are

| Concern | Location |
|---------|----------|
| Live component catalog | `ox dev catalog` (hidden command) |
| Published catalog (browser) | [sageox-design.netlify.app/2026-05-17-ox-cli-component-catalog/](https://sageox-design.netlify.app/2026-05-17-ox-cli-component-catalog/) |
| Per-component specs | [`docs/design/components/`](docs/design/components/) |
| Composite patterns (doctor, status, session timeline) | [`docs/design/patterns/`](docs/design/patterns/) |
| Theming + upstream sync | [`docs/design/theming.md`](docs/design/theming.md) |
| Semantic tokens | [`docs/design/tokens.md`](docs/design/tokens.md) |
| Deep reference (CLI design system) | [`docs/specs/cli-design-system.md`](docs/specs/cli-design-system.md) |
| Agent UX (orthogonal — how output serves AI coworkers) | [`docs/specs/agent-ux-principles.md`](docs/specs/agent-ux-principles.md) |
| Generated theme code (do not edit) | `internal/theme/generated.go` |
| Component implementations | `internal/ui/`, `internal/tui/`, `internal/cli/` |
| Catalog registry | `internal/uicatalog/` |
| Enforcement rules | [`.claude/rules/design.md`](.claude/rules/design.md) |

## Contributing

To add a new component:

1. Implement it in `internal/ui/` (most cases), `internal/tui/` (bubbletea-based), or `internal/cli/` (input/IO helpers).
2. Register an `Entry` in `internal/uicatalog/entries/<name>.go`.
3. Write the spec at `docs/design/components/<name>.md` using the existing template.
4. Run `make catalog-export` — this regenerates `.cast` recordings and updates the bundle.
5. Run `make publish-catalog` — this stages the updated proposal directory into `sageox-design/bogota-v2/`. A human deploys via the existing Netlify pipeline.

CI fails if any of (1)–(3) are skipped. See [`.claude/rules/design.md`](.claude/rules/design.md) for the full enforcement story.
