---
component: login-screen
package: cmd/ox/login.go
since: 0.3.0
family: screen
renderer: freeze
exports: [loginSpinnerModel, cli.DeviceCodeLines, cli.LoginArrival]
---

# Login screen

> Smallest possible full-screen bubbletea program (one spinner, one message, one Ctrl+C), plus the two moments around it: the code to match, and a three-line "you're in" block.

Live: `ox login`. Catalog snapshot: `ox dev catalog --component=login-screen`. Composition narrative: [patterns/login-flow.md](../patterns/login-flow.md).

## When this is the right reference

Any "wait for external thing" surface — OAuth device code, daemon handshake, slow network resource. The full-screen treatment is reserved for genuinely open-ended waits where Ctrl+C is the user's only out.

## When NOT to copy

Foreground operations with predictable durations — use inline [Spinner](spinner.md). The full-screen treatment costs a screen wipe; pay it only when the wait is unpredictable.

## Composition

| State | What prints | Source |
|---|---|---|
| Waiting for approval | `Visit:` link, `Code:` line (always), then the [Spinner](spinner.md) | `cli.DeviceCodeLines` |
| Approved, at a color terminal | Three lines (who, where, next) with a half-block Oxy beside them | `cli.LoginArrival`, `pet.TerminalFrame` |
| Approved, piped / CI / `--quiet` / `--json` | The same three lines, no art | `cli.LoginArrival` |
| Denied or expired | The error, then one retry line | `loginFailure` in `cmd/ox/login_arrival.go` |

The catalog snapshot draws every state with the functions `ox login` calls, so it cannot drift from the command. Real `ox login` only shows the active state at any given moment.

## Source

[`cmd/ox/login.go`](../../../cmd/ox/login.go) — `loginSpinnerModel` · [`cmd/ox/login_arrival.go`](../../../cmd/ox/login_arrival.go) — failure lines, "Welcome back" signal, when Oxy may appear · [`internal/cli/login_arrival.go`](../../../internal/cli/login_arrival.go) — the text
