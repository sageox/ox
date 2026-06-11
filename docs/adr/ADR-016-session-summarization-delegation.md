# ADR-016: Session Summarization Delegation

**Status**: Accepted
**Date**: 2026-04-26

## Context

When `ox session stop` finalizes a recording, a rich JSON summary
(`summary.json`) must be produced by an LLM. Two viable execution sites
exist for that LLM call:

1. **Calling agent (inline).** `ox session stop` returns a
   `summary_prompt` string in its JSON output. The agent that just
   finished the session reads the prompt, runs the model on its existing
   conversation context, and pipes the result back to
   `ox session push-summary`, which validates, commits, and pushes
   `summary.json` to the ledger.
2. **Daemon (delegated).** The CLI signals the local ox daemon (via
   the existing IPC socket) and returns immediately. The daemon's
   `agentwork` machinery (`internal/daemon/agentwork/`) spawns a
   user-configured LLM CLI (`claude`, `codex`, `gemini`, …) in the
   background, runs the same prompt, and pushes the same artifacts via
   the same code path. Today this is reachable only via the
   `SAGEOX_ASYNC_SESSION_UPLOAD=1` environment variable —
   not a user-facing config.

The inline path was the original design and remains the only path most
users hit. It has real strengths: the calling agent already has the
conversation in context (so input tokens are effectively free), it
works without any extra binary on the user's `PATH`, and it works
identically for every supported agent (Claude Code, Cursor, Aider,
Amp, Codex CLI, …) without ox needing to know which LLM CLIs the user
has installed.

It also has a real cost. Inline summarization runs synchronously in
the foreground. The user is not free to `/clear`, close the agent
window, switch tasks, or even start a new prompt until the LLM finishes
generating ~1–4KB of structured JSON and `push-summary` returns. For a
typical session that is 30–120 seconds of dead waiting, applied at the
exact moment the user has signaled they want to be done. This is the
single most user-visible source of friction in the session lifecycle.

The daemon path eliminates that friction entirely — `session stop`
returns within milliseconds — but introduces dependencies (a configured
or auto-detectable LLM CLI on `PATH`) and a different cost-attribution
model (user's `claude`/`codex` login pays, not the calling agent's).

## Decision

**Delegate summarization to the daemon by default whenever a user has
a usable LLM CLI configured (or auto-detectable on `PATH`). Fall back
to inline (return `summary_prompt` to the calling agent) only when no
delegated runner is available.**

The decision is captured precisely by this rule, which is the canonical
phrasing referenced from the inline anchors:

> Delegating summarization back to the calling agent is the right
> default *technically* — the agent already has context loaded, no
> extra binary is required, and it works for any agent type. But it
> blocks the user at exactly the wrong moment: they have just said
> "I'm done" and want to close the agent or `/clear` and move on. We
> should only force the inline path when the user has not specified
> (and ox cannot auto-detect) an LLM agent for daemon callouts.

### Configuration surface

A new user-config key, `agent.summarizer`, controls dispatch:

| Value | Behavior |
|-------|----------|
| `auto` *(default)* | Daemon detects `claude`/`codex`/`gemini` on `PATH`; uses the first available. Inline fallback only if none found. |
| `claude` / `codex` / `gemini` | Force daemon to use the named runner. Hard error (with inline fallback) if the binary is absent. |
| `off` | Force inline. The calling agent always receives `summary_prompt`. |
| (legacy) `SAGEOX_ASYNC_SESSION_UPLOAD=1` env | Equivalent to `auto`. Kept for compatibility; users should migrate to `agent.summarizer=auto`. |

A per-invocation override exists for power users:
`OX_SESSION_INLINE_SUMMARY=1` forces the inline path regardless of
config. Useful for debugging and for users who want to keep cost
attribution on the calling agent's billing.

### Behavior matrix

| User config | LLM CLI on PATH | Result |
|---|---|---|
| `auto` (default) | yes | **Daemon path.** `session stop` returns immediately with `summary_prompt: ""` and a guidance string explaining the daemon owns it. |
| `auto` (default) | no | **Inline path.** `session stop` returns `summary_prompt` populated; calling agent runs the LLM and calls `push-summary`. |
| explicit runner (`claude`/`codex`/`gemini`) | yes | **Daemon path** with that specific runner. |
| explicit runner | no | **Inline path** + warning logged; doctor surfaces the misconfiguration. |
| `off` | n/a | **Inline path** unconditionally. |
| `OX_SESSION_INLINE_SUMMARY=1` | n/a | **Inline path** unconditionally for this invocation. |

### Daemon-path responsibilities

When the daemon owns summarization:

1. CLI persists `raw.jsonl` + `meta.json` to the ledger and uploads via
   LFS (synchronous — these are small operations, and they MUST happen
   before `session stop` returns so the user's content is durable).
2. CLI signals the daemon via IPC (`signalDaemonSessionFinalize`,
   already implemented) and returns to the user.
3. Daemon's `agentwork` queue (`internal/daemon/agentwork/queue.go`)
   serializes the work, applies rate limiting, spawns the configured
   runner, captures output, validates richness/content, calls the
   shared `pushSummaryToLedger` flow.
4. On failure, the session is left in a recoverable state. `ox doctor`
   detects "session has meta.json but no summary.json" and re-enqueues
   into the same queue.

### Inline-path responsibilities (unchanged when chosen)

The existing flow stays intact: `session stop` returns
`summary_prompt`, the calling agent runs the model, the result is
piped to `ox session push-summary`. This path remains the source of
truth for prompt construction and validation logic — the daemon
calls into the *same* `sessionsummary.BuildSummaryPrompt` and
`pushSummaryToLedger` functions. There is no second implementation
to drift.

## Consequences

### Positive

- **Foreground latency on `session stop` drops from 30–120s to <1s** in
  the common case. The user is unblocked at the moment they signaled
  intent to move on.
- **Failure isolation.** Today, if the calling agent crashes, runs out
  of context, or is force-quit before `push-summary`, the summary is
  silently lost. With daemon ownership, the queue persists across CLI
  invocations and doctor naturally retries.
- **Cross-agent users (Cursor, Aider) get summaries for free.** Today
  these agents typically don't run the inline `summary_prompt` flow at
  all; their sessions land on the ledger with no summary.json.
- **One repair path.** `doctor` enqueues missing summaries into the
  same queue; no separate "stale-summary repair" subsystem.

### Negative

- **Cost-attribution shift.** Users now pay for summarization through
  whatever credentials their `claude`/`codex` CLI is logged into,
  rather than through the calling agent's session. Documented in
  `docs/guides/`; surfaced once on first auto-detection.
- **Background-failure visibility.** Inline failures are loud (the
  agent sees them); daemon failures need explicit surfacing. Mitigated
  by: doctor checks, telemetry event
  `session_summarize_delegated{success}`, and a one-liner in the next
  `session start` if the previous session failed to summarize.
- **Stronger dependency on the daemon.** A dead daemon means inline
  fallback (acceptable) — but a *flaky* daemon (queue stuck) is harder
  to diagnose than an inline failure. Doctor must surface stuck queue
  state.

### Neutral

- The inline path is not removed. It remains the fallback, the
  power-user override, and the contract-of-record for prompt shape.
  Removing it would break older agents and the
  `OX_SESSION_INLINE_SUMMARY=1` debug path.

## Implementation notes

- Today: env-var-only switch (`SAGEOX_ASYNC_SESSION_UPLOAD=1`) at
  `cmd/ox/agent_session.go`. The daemon path is built but
  not exposed as user config.
- This ADR's primary near-term action: lift the env var to a
  first-class config key (`agent.summarizer`), default to `auto`,
  document it. The actual dispatch infrastructure does not need to
  change.
- Inline anchors that reference this ADR:
  - `cmd/ox/agent_session.go` — at the SummaryPrompt dispatch site
  - `cmd/ox/session_push_summary.go` — top-of-file rationale
  - `internal/daemon/agentwork/session_finalize.go` — daemon-side
    delegation handler

## References

- ADR-002: Unix Socket IPC (the channel CLI uses to signal the daemon)
- ADR-004: Session Lifecycle State Machine (where `stop` fits)
- ADR-007: Direct LFS (why `raw.jsonl` upload stays in the CLI)
- `.claude/rules/cache-only-design.md` (load-bearing invariant the
  daemon path must respect)
- bd: ox-m0i9
