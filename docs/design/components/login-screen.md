---
component: login-screen
package: cmd/ox/login.go
since: 0.3.0
family: screen
renderer: freeze
exports: [loginSpinnerModel]
---

# Login screen

> Smallest possible full-screen bubbletea program: one spinner, one message, one Ctrl+C.

Live: `ox login`. Catalog snapshot: `ox dev catalog --component=login-screen`. Composition narrative: [patterns/login-flow.md](../patterns/login-flow.md).

## When this is the right reference

Any "wait for external thing" surface — OAuth callback, daemon handshake, slow network resource. The full-screen treatment is reserved for genuinely open-ended waits where Ctrl+C is the user's only out.

## When NOT to copy

Foreground operations with predictable durations — use inline [Spinner](spinner.md). The full-screen treatment costs a screen wipe; pay it only when the wait is unpredictable.

## Composition

[Box](box.md) (opening message: "Opening browser…") · [Spinner](spinner.md) (the wait) · [Box](box.md) (success summary).

The catalog snapshot stitches the **before** and **after** states together so you can see both within one figure. Real `ox login` only shows the active state at any given moment.

## Source

[`cmd/ox/login.go`](../../../cmd/ox/login.go) — `loginSpinnerModel`
