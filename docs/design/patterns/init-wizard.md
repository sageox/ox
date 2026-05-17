<!-- doc-audience: preserve-voice -->

# Pattern · `ox init` wizard (inline multi-step)

How the project-initialization wizard composes catalog primitives.

## Composition

`ox init` is **not** a full-screen TUI — it's an **inline** sequence of catalog primitives, one per logical step. Output stays on the same scroll so the user can review what they answered.

```
Step 1 · Pick endpoint
  ↓ Select (radio)

Step 2 · Pick team   (skip-able if user has only one)
  ↓ Select (radio) — falls through if list size 1

Step 3 · Pick agent hooks to install
  ↓ Multi-select (checkbox)

Step 4 · Confirm destructive operation (if upgrading)
  ↓ Confirm (yes/no with default)

Step 5 · Apply
  ↓ Spinner (auto-hides if fast)
  ↓ Timeline (per-stage result)
```

## Key decisions

- **Inline, not full-screen.** A wizard that scrolls past lets users re-read their answers; a full-screen TUI overwrites them. Inline composes naturally with shell history and screenshots.
- **Smart skips.** If the user has only one team, skip the team picker entirely and print a "Team: <name>" line. Don't make people press enter on a one-option menu.
- **Catalog primitives only.** Every step is a catalog component — never a bespoke prompt. New steps must justify NOT using an existing primitive.
- **Streaming progress.** The apply step renders [Timeline](../components/timeline.md) with each `TimelineNode` streamed as it completes, so the user sees their wizard answers being applied.
- **Recoverable.** Cancel at any step rolls back partial config. The [doctor](doctor-output.md) pattern is the cleanup path after.

## Source

- [`cmd/ox/init.go`](../../../cmd/ox/init.go)
  - `selectInitEndpoint()` — step 1
  - `selectTeam()` — step 2
  - `selectAgentsForInit()` — step 3
  - `ensureSageoxConfig()` — apply

## Components used

[Select](../components/select.md) (steps 1, 2) · [Multi-select](../components/multi-select.md) (step 3) · [Confirm](../components/confirm.md) (step 4) · [Spinner](../components/spinner.md) (step 5) · [Timeline](../components/timeline.md) (apply progress) · [Box](../components/box.md) (success summary)
