---
component: init-dock
package: internal/ui/init_dock.go
since: 0.26.0
family: screen
renderer: freeze
exports: [InitDock, RenderTryFirst, AddonOffer, RenderAddonOffers]
---

# Init dock

> The box `ox init` ends on: what is now connected to what, and who can read what flows between.

Live: `ox init`. Catalog snapshot: `ox dev catalog --component=init-dock`. Composition narrative: [patterns/init-wizard.md](../patterns/init-wizard.md).

## When this is the right reference

A command that joins a person's work to a team's. The disclosure belongs at the moment of connecting, not in a README.

It is the terminal twin of the repo-initialized email's facts card: same facts, same order.

## When NOT to copy

Commands that change nothing a teammate can see: a box that always appears gets skimmed.

Framed output is for a terminal only. Pipes, CI and AI coworkers get the same facts as labeled lines.

## What it says

| Repo setting | The dock's sentence |
|---|---|
| Recording on, uploads automatic | `Sessions go to <team>'s Ledger; teammates can read them.` |
| Recording on, uploads manual | `Sessions stay on this machine until you upload them to <team>'s Ledger.` |
| Recording off | `Session recording is off here; nothing goes to the Ledger.` |
| Not on a team | no sentence: there is no Ledger to name |

The dock never claims more than the repo's own `session_recording` and `session_publishing` settings give.

## Composition

- [Box](box.md) (`BoxSuccess`), framed at a terminal.
- "Try this first": the plan prompt, one unwrapped line so it copies cleanly.
- [Next Steps](../patterns/init-wizard.md), unchanged underneath.
- "Optional for Claude Code": add-ons, offered once per machine at a terminal.

## Source

[`internal/ui/init_dock.go`](../../../internal/ui/init_dock.go) · wiring in [`cmd/ox/init_dock.go`](../../../cmd/ox/init_dock.go) and [`cmd/ox/init.go`](../../../cmd/ox/init.go)
