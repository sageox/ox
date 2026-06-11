### ADR-018: UserPromptSubmit JIT Discovery via `ox query --local`

**Status:** Proposed
**Date:** 2026-05-27
**Epic:** [ox-r9mq](../../bd/ox-r9mq) — Discoverability via UserPromptSubmit local-ledger query injection
**Related tasks:** [ox-m01h](../../bd/ox-m01h) (local query mode), [ox-tshf](../../bd/ox-tshf) (hook wiring), [ox-8wmo](../../bd/ox-8wmo) (cloud opt-in)

## Context

ox accumulates a substantial body of repo-scoped knowledge: cached sessions, murmurs, prior discussions, and a per-repo ledger. Yet coding agents rarely reach for these surfaces mid-task. The features exist; the discoverability does not. When a user types "how did we handle pagination on the activity feed?", the agent is far more likely to grep the codebase than to consult prior decisions captured in the ledger — even when the ledger has the answer verbatim.

This is the same anti-pattern that motivated `ox agent prime`: powerful context exists, but the agent must be *told* to reach for it. Prime solves the cold-start case. It does not solve the mid-session case, where the prompt itself is the only signal that a knowledge lookup would help.

Two adjacent observations sharpen the problem:

1. **Agents do not reflexively use optional tools.** Even with MCP and skill catalogs in front of them, agents default to first-party operations (Read, Grep, Bash) over ecosystem tools. A useful query surface that isn't pre-injected into context tends to stay unused.
2. **The discoverability gap is most visible exactly when it matters least to fix it manually.** Mid-flow, the user is typing — they don't want to context-switch into "should I have asked ox first?" The injection has to happen invisibly or it won't happen at all.

## Constraints

Three constraints shape the decision space. Each is concrete; each rules out the obvious naive approach.

### Privacy

Every user prompt is potentially sensitive. Prompts routinely contain pasted secrets (tokens accidentally copied with a stack trace), proprietary code, customer data, and unreleased product details. Any design that sends prompts over the network for the *common* case violates a baseline privacy expectation that most users hold implicitly: "what I type to my local agent stays local unless I opt in."

### Latency

Network-mediated lookups against remote services add 200-2000ms per turn for the WebFetch-class of operations we have measured. At the UserPromptSubmit boundary that latency is paid synchronously before the agent begins working — it is felt by the user as "the agent feels slow now." A 2000ms tax on every prompt is unacceptable; even a 200ms tax is poor stewardship of attention given that most prompts will not benefit from the injection.

### Relevance

Most prompts will not benefit from a recall lookup. Short prompts ("yes", "go ahead", "try again") and pure-action prompts ("write a test for foo", "rename X to Y") have no recall surface to hit. Firing the lookup on every prompt — and ingesting results — pollutes context with irrelevant matches. The signal-to-noise ratio collapses fast unless the trigger is gated.

The cautionary precedent is rtk-ai/rtk#582: an unconditional PreToolUse compression hook caused an observed 18% cost increase in production over two weeks, because the hook fired on every tool call regardless of whether compression would actually help. The lesson generalizes: an always-on hook with no relevance gate is a regression even when the underlying operation is cheap, because context churn has its own downstream cost.

## Decision

Ship a UserPromptSubmit hook that runs `ox query --local` against the cached ledger and JIT-prepends findings to the agent's prompt context. Local-only by default. Cloud-mode is opt-in via a single config key.

The decision has three load-bearing pieces:

1. **`ox query --local`** is a new, network-free query mode (epic sub-task ox-m01h). It searches the locally-cached ledger only, targets <50ms p95 on a warm cache, and degrades to "empty result" rather than to an error when the cache is cold.
2. **The hook fires on UserPromptSubmit** (not PreToolUse, not SessionStart) and is gated by a length check (<40 tokens skipped), a regex intent classifier (find/recall/explain patterns fire; action verbs skip), and a hard 100ms async timeout. Any error path leaves the prompt unchanged (fail-open). See `docs/specs/userpromptsubmit-jit-discovery.md` for the full gate matrix.
3. **Cloud mode is opt-in via `hooks.userpromptsubmit.cloud_query` (default `false`).** Even when enabled, prompts pass through the existing redactor (`internal/session/secrets.go`) before any byte leaves the machine, and a missing auth token degrades silently to local-only rather than erroring the turn.

The non-negotiable invariant: with default config, no user prompt ever transits to a remote service via this hook. This invariant is enforced at the call site by an inline comment explaining why local-default exists; a future contributor reading the code is told, in line, that "improving" this to remote is the wrong direction and given the config knob to find if the user really wants it.

## Alternatives Considered

### Always-cloud query injection

Skip the local path entirely; always hit the SageOx remote query API.

**Rejected because:** Violates all three constraints. Sends every prompt over the wire (privacy). Adds 200-2000ms per turn (latency). Fires on prompts where recall is useless (relevance). The RTK precedent quantifies the relevance failure at 18% cost regression in a comparable hook design.

### Manual invocation only

Document `ox query` and trust users to invoke it themselves.

**Rejected because:** This is the status quo, and the status quo is what motivated the epic. Optional surfaces that require the user to remember them are surfaces that stay unused. The whole point of UserPromptSubmit is to make the lookup happen without the user having to ask for it.

### PreToolUse-based injection

Fire the query before each tool call rather than at prompt submit time.

**Rejected because:** Wrong abstraction layer. The prompt is the signal of intent; the tool call is the action. Firing per-tool means the same query runs N times for a single user turn that decomposes into N tool calls, multiplying both cost and context churn. RTK#582 specifically lived at the PreToolUse boundary, and the 18% cost regression there is direct evidence that this layering choice is the expensive one.

### Skip the human-facing doc

The team-context-facing changelog entry and the AI spec already cover what the feature does and how to opt out. The human doc would duplicate them without adding crafted narrative value beyond "we have a hook now."

**Decision:** A human-facing doc *is* warranted — see `docs/guides/jit-discovery.md`. The user-visible surface (an "ox found" preamble appearing above prompts) needs a place to explain "what is this, why is it appearing, how do I make it stop." Without that page, the support burden lands in issues or office hours.

## Consequences

### Enables

- Prior sessions, murmurs, and discussions become reachable from natural-language prompts without the user having to know they exist.
- The local-default posture lets us ship the feature broadly without per-team privacy review.
- The cloud opt-in gives power users a single flag to expand recall scope when they have already accepted SageOx as a remote endpoint.
- Future work (ox-r9mq follow-ons) can extend the query source — KB lookups, codedb hotspots, team-context decisions — through the same gated hook surface without revisiting the privacy/latency contract.

### Risks remaining

- **Gate tuning drift.** The 40-token length gate and the intent regex are heuristics. They will need adjustment as we observe real prompts. Mitigation: surface the gate decision in `ox doctor --check=hooks` so a user who feels they are missing recall can see why and disable the gates.
- **Preamble noise budget.** Even a 5-line preamble is non-zero context. If the local query becomes too aggressive, agents will start treating the preamble as noise to ignore. Mitigation: track preamble injection rate and false-positive rate via friction telemetry; tighten gates if hit rate stays low.
- **Cloud-mode redactor coverage.** The redactor catches known secret shapes; novel shapes will leak. Mitigation: cloud_query is opt-in, the existing redactor is well-tested via `internal/session/secrets.go`, and any new prompt-mode redaction work lands there (not in `cmd/ox`).
- **Hook reliability on Claude Code.** UserPromptSubmit hook delivery has bug history (see ADR-006-context-fallback-layers and Claude Code #10373 referenced in `cmd/ox/doctor_hooks.go`). Mitigation: doctor check (ox-8wmo) reports effective hook state so the user is not left guessing.

## Cross-references

- Spec: [docs/specs/userpromptsubmit-jit-discovery.md](../specs/userpromptsubmit-jit-discovery.md)
- Human-facing guide: [docs/guides/jit-discovery.md](../guides/jit-discovery.md)
- Hook plumbing: `cmd/ox/agent_hook.go` (phasePrompt handler)
- Doctor check: `cmd/ox/doctor_hooks.go` (`--check=hooks`)
- Local query: `cmd/ox/query.go` (`--local` flag, sub-task ox-m01h)
- Redaction: `internal/session/secrets.go` (reused for cloud-mode prompt redaction)
- Prior art: ADR-006 (context fallback layers), rtk-ai/rtk#582 (PreToolUse cost regression precedent)
