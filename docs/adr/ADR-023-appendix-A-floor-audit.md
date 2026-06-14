# ADR-023 Appendix A — Floor-Behavior Audit of `ox-*` Claude Commands

> Companion to **[ADR-023 — Skill/Command Injection: The Two-Layer Model](./ADR-023-skill-injection-two-layer-model.md)**. This appendix is the verified evidence base for the epic's central claim — *a thin-relay violation IS a cross-agent gap*. It is referenced by the floor-remediation (`ox-fvjh.6`) and fat-playbooks-to-skills (`ox-fvjh.7`) tasks. (beads: `ox-fvjh.3`)

> **Status (post-implementation — `ox-fvjh` shipped):** this audit is the *point-in-time evidence base* that drove the epic; the verdict table below intentionally records the **pre-remediation** state. Since the audit:
> - **`ox-cart-start` — RESOLVED in `ox-fvjh.6`.** `runCartsStart` now emits a portable `guidance` field in its `--json` output (`cmd/ox/carts.go`, `cartStartGuidance`); regression test `cmd/ox/carts_test.go` `TestCartStartGuidancePortable` fails if it leaks a host-specific token like `/rename`. Codex/Droid now receive the naming intent.
> - **`ox-plan.md` and `ox-session-review.md` — migrated to skills in `ox-fvjh.7`.** They now live at `extensions/claude/skills/<id>/SKILL.md`, so **2 of the 16 files below are no longer commands**. Read the table as the audit's findings, not the current on-disk layout.

## What this audits

Every command file installed by the Claude adapter (`cmd/ox-adapter-claude-code`,
`CapCommandsInstaller` at `cmd/ox-adapter-claude-code/main.go:80`) under
`extensions/claude/commands/`. Each is classified **thin** (pure relay to an
`ox` CLI command) vs **thick** (inline behavioral guidance in the body), and
each is checked for **floor-trapped** behavior: guidance a Codex or Droid user
would also need, present ONLY in the Claude body with NO Layer-1 (CLI JSON
`guidance`) representation.

This matters because the other two adapters install no commands:

| Adapter | Capabilities declared | Installs `ox-*` commands? | Evidence |
|---------|----------------------|---------------------------|----------|
| claude-code | hooks + rules + **commands** | **yes** | `cmd/ox-adapter-claude-code/main.go:78-80` |
| codex | hooks only | no | `cmd/ox-adapter-codex/main.go:49` |
| droid | hooks + rules | no | `cmd/ox-adapter-droid/main.go:57-58` |

So any FLOOR behavior trapped in a Claude command body is, by construction,
invisible to Codex and Droid.

## Classification key

- **thin (pure relay):** body is a description line plus a `$ox …` invocation; no behavioral directives.
- **thin + legit post-command:** thin relay plus a Post-Command block whose *only* job is to interpret a JSON field the CLI already emits (`guidance`, `notice`, `summary_prompt`, …). Body restates Layer 1 → staleness risk, not a coverage gap.
- **thick (activation metadata + troubleshooting):** carries use-when / keywords / common-issues prose. Acceptable per the ADR verdict: activation cues are agent-tuned and not portable; troubleshooting overlaps `ox doctor`'s own Layer-1 remediation.
- **thick (Layer-2-by-design):** fat playbook — substantive rendering/operational content with no single backing subcommand. Layer-2 blob on purpose; feeds `fat-playbooks-to-skills` (`ox-fvjh.7`).

## Verdict table (16 files)

| file | thin/thick | floor-trapped? | where guidance lives now / should move | evidence (file:line) |
|------|-----------|----------------|----------------------------------------|----------------------|
| `ox-cart.md` | thin (pure relay) | no | Relays `ox carts ready --json`. Body is description + invocation only. | runner `cmd/ox/carts.go:140` `runCartsReady` (emits raw JSON, line 64-area pattern) |
| `ox-cart-start.md` | thick (inline directive) | **YES — TRUE GAP** | Body carries a REQUIRED post-command directive (parse JSON → `/rename` session after cart title → confirm). Backing runner emits NO `guidance`. **Should move:** add a `guidance` field to the `--json` output of `runCartsStart`. | runner `cmd/ox/carts.go:283` `runCartsStart`; `--json` branch emits only `carts.FormatIssueJSON(issue)` at **`cmd/ox/carts.go:300`** (`fmt.Println` at :301). `grep -i guidance cmd/ox/carts.go` → zero matches. |
| `ox-cart-done.md` | thin (pure relay) | no | Relays `ox carts done $ARGUMENTS`. | runner `cmd/ox/carts.go:317` `runCartsDone` |
| `ox-cart-drop.md` | thin (pure relay) | no | Relays `ox carts drop $ARGUMENTS`. | runner `cmd/ox/carts.go:342` `runCartsDrop` |
| `ox-doctor.md` | thick (activation + troubleshooting) | no | Use-when/keywords/Common-Issues. Activation metadata is agent-tuned; troubleshooting overlaps `ox doctor`'s own Layer-1 remediation. | relays `ox doctor` (`cmd/ox/doctor.go`) |
| `ox-init.md` | thick (activation + troubleshooting) | no | Use-when/keywords/Common-Issues; same rationale as `ox-doctor`. | relays `ox init` (`cmd/ox/init.go`) |
| `ox-plan.md` (258 lines) | thick (**Layer-2-by-design**) | no | Rendering spec for the enriched HTML plan. *Whether to render* is decided by `ox plan` JSON (`signals.material`, `guidance`), not the body — stated in the file's own frontmatter. Feeds `fat-playbooks-to-skills`. | `ox plan` signals/Material JSON at `cmd/ox/plan.go:424-438`; frontmatter `extensions/claude/commands/ox-plan.md:1-15` |
| `ox-prime.md` | thick (activation + troubleshooting) | no | Use-when/keywords/Common-Issues; activation metadata + troubleshooting. | relays `ox agent prime` |
| `ox-session-abort.md` | thin + legit post-command | no | Post-action note has a Layer-1 home (`guidance` field). | `cmd/ox/agent_session_abort.go:23` `Guidance` field; value at `:249` |
| `ox-session-list.md` | thin + legit post-command | no | Stub/download "Steps" (dehydrated → `ox session download <name>`) already in Layer 1. | `cmd/ox/session_list.go:104` `sessionListAgentGuidance` const, wired at `:136`, `:226`, `:306`; JSON field at `:81` |
| `ox-session-review.md` (290 lines) | thick (**Layer-2-by-design**) | no | Operational/incident knowledge (failure-mode watch-list) not backed by a single subcommand. Feeds `fat-playbooks-to-skills`. | no single backing runner; frontmatter `extensions/claude/commands/ox-session-review.md:1-12` |
| `ox-session-start.md` | thin + legit post-command | no | Plan-capture / `notice` / `guidance` handling maps to real JSON fields. | `cmd/ox/agent_session.go:55-57` `sessionStartGuidance` const, wired `Guidance: sessionStartGuidance` at `:290` |
| `ox-session-status.md` | thin + legit post-command | no | Field-interpretation (`recording`, `guidance`, `entry_count`, `count`, `agent_id`) maps to real JSON. | `cmd/ox/session_status.go:107` `Guidance` field; values at `:261/:287/:317/:382` |
| `ox-session-stop.md` | thin + legit post-command | no — **CLEARED SUSPECT** | Sync/async branching the body describes is FULLY in Layer 1. Body RESTATES it → staleness risk only, not a coverage gap. See "Cleared suspect" below. | `cmd/ox/agent_session.go:64` `sessionStopGuidance` const, wired `Guidance: sessionStopGuidance` at `:791`; `summary_prompt`-driven branch at `:806`, `:818-819`, `:1240`, `:1705` |
| `ox-status.md` | thick (activation + troubleshooting) | no | Use-when/keywords/Common-Issues; activation metadata + troubleshooting. | relays `ox status` |
| `ox.md` | thin (reference card) | no | Static command-reference card (no behavioral directives). | n/a (reference only) |

*Reference idiom (not a command file):* the canonical Layer-1 guidance pattern
is `subagentDispatchGuidance` at `cmd/ox/agent_tasks.go:31`, emitted via
`writeTasksJSON` (`cmd/ox/agent_tasks.go:298`, e.g. `:211`). New floor guidance
should follow this shape.

## The cleared suspect — `ox-session-stop`

The ADR design note flagged `ox-session-stop`'s sync/async branching as a
"known suspect" for floor-trapping. **It is NOT a floor gap.** The branching is
already fully represented in Layer 1:

- `sessionStopGuidance` const at `cmd/ox/agent_session.go:64`, wired
  `Guidance: sessionStopGuidance` at `:791`.
- The sync-vs-async distinction is driven by `SummaryPrompt` presence/absence in
  the CLI itself: populated for sync mode (`:806`, `:1705`), explicitly cleared
  to `""` for async/daemon-owned mode (`:818-819`, `:1240`).

The Claude body merely RESTATES this. It is a **staleness risk** (the installed
file, stamped `ox-hash: 9b5ef157c16c`, can drift from the live binary), not a
cross-agent coverage gap. Remediation (`ox-fvjh.6`) should NOT waste effort
"moving" this guidance — it is already in Layer 1. Trim the restatement under
freshness-hardening, do not migrate it.

## The one true gap — `ox-cart-start`

`ox-cart-start.md` carries a REQUIRED post-command directive in its body:

1. Parse the JSON output to get the cart title.
2. Rename the session via `/rename` with a kebab-case name derived from the title.
3. Display a confirmation (cart ID, title, in_progress assignment).

The backing runner emits **no** `guidance`:

```go
// cmd/ox/carts.go
283  func runCartsStart(cmd *cobra.Command, args []string) error {
...
299      if isJSON(cmd) {
300          data, _ := carts.FormatIssueJSON(issue)   // <- only raw issue JSON, NO guidance
301          fmt.Println(string(data))
302      } else {
...
```

`grep -n -i guidance cmd/ox/carts.go` returns **zero matches** across the entire
file. The sibling cart runners are the same pattern (`FormatIssueJSON` only at
`cmd/ox/carts.go:64`, `:192`, `:266`, `:300`), but only `ox-cart-start` has a
floor-level directive in its body, so only it is floor-trapped.

A Codex or Droid user running `ox carts start` gets the raw issue JSON and none
of the rename/confirm flow. **Remediation (`ox-fvjh.6`):** add a `guidance`
field to `runCartsStart`'s `--json` output (following the
`subagentDispatchGuidance`/`writeTasksJSON` idiom) and reduce
`ox-cart-start.md` to a thin relay.

## Summary

- **Floor-trapped count: 1** — `ox-cart-start.md` (evidence: `cmd/ox/carts.go:300`, `runCartsStart` emits only `carts.FormatIssueJSON`, zero `guidance` in the file).
- **Cleared suspect: `ox-session-stop`** — sync/async branching already in Layer 1 (`sessionStopGuidance` const `cmd/ox/agent_session.go:64`, wired `:791`; `SummaryPrompt` branch `:818-819`). Staleness risk only.
- **Layer-2-by-design (feed `fat-playbooks-to-skills`):** `ox-plan.md` (258 lines), `ox-session-review.md` (290 lines).
- **Thick-but-acceptable (activation + troubleshooting):** `ox-doctor`, `ox-init`, `ox-prime`, `ox-status`.
- **Thin (pure relay / reference):** `ox-cart`, `ox-cart-done`, `ox-cart-drop`, `ox.md`.
- **Thin + legit post-command (restates Layer 1):** `ox-session-abort`, `ox-session-list`, `ox-session-start`, `ox-session-status`, `ox-session-stop`.
