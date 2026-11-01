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

Step 6 · Dock
  ↓ Box (Repo → Team, who can read the sessions)
  ↓ Try this first (the plan prompt)
  ↓ Next Steps
  ↓ Optional add-ons (once per machine, at a terminal)
```

## Key decisions

- **Inline, not full-screen.** A wizard that scrolls past lets users re-read their answers; a full-screen TUI overwrites them. Inline composes naturally with shell history and screenshots.
- **Smart skips.** If the user has only one team, skip the team picker entirely and print a "Team: <name>" line. Don't make people press enter on a one-option menu.
- **Catalog primitives only.** Every step is a catalog component — never a bespoke prompt. New steps must justify NOT using an existing primitive.
- **Streaming progress.** The apply step renders [Timeline](../components/timeline.md) with each `TimelineNode` streamed as it completes, so the user sees their wizard answers being applied.
- **Recoverable.** Cancel at any step rolls back partial config. The [doctor](doctor-output.md) pattern is the cleanup path after.
- **End on a disclosure, not a banner.** The last screen is the [init dock](../components/init-dock.md): repo, team, and who can read the sessions.

<details>
<summary>More: the dock, the prompt line, and the offers</summary>

- **The payoff comes before the chores.** "Try this first" (the repo-initialized email's plan prompt) sits above Next Steps. Committing `.sageox/` stays step 1.
- **Add-ons are offered once.** At a terminal, the first `ox init` on a machine lists what is not installed. Pipes, CI and AI coworkers get nothing and spend nothing.
- **The dock fixes a silent gap.** Nothing in the terminal said sessions are recorded; that lived only in `.sageox/README.md`. The sentence follows the repo's own recording and publishing settings and never claims more than they give.
- **The prompt is one unwrapped line.** The terminal soft-wraps it, so a click-and-copy takes the sentence, not a border or a hard break.
- **Offers: `ox pet enable`, `ox session report pane enable`.** Init installs the pane itself when Claude Code is a selected integration, so the pane is only offered when it was not.
- **Framed only at a terminal.** A rounded Box on a TTY, plain labeled lines everywhere else. `--quiet` prints neither.

</details>

## Source

- [`cmd/ox/init.go`](../../../cmd/ox/init.go)
  - `selectInitEndpoint()` — step 1
  - `selectTeam()` — step 2
  - `selectAgentsForInit()` — step 3
  - `ensureSageoxConfig()` — apply
- [`cmd/ox/init_dock.go`](../../../cmd/ox/init_dock.go) — builds the dock from the repo's settings, decides who is offered the add-ons
- [`internal/ui/init_dock.go`](../../../internal/ui/init_dock.go) — the dock, the plan prompt, the offers

## Components used

[Select](../components/select.md) (steps 1, 2) · [Multi-select](../components/multi-select.md) (step 3) · [Confirm](../components/confirm.md) (step 4) · [Spinner](../components/spinner.md) (step 5) · [Timeline](../components/timeline.md) (apply progress) · [Box](../components/box.md) via the [init dock](../components/init-dock.md) (success summary)
