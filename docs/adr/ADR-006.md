# ADR-006: Multi-Layer Agent Context Fallback

**Status**: Accepted
**Date**: 2026-04-02

## Context

ox's core value proposition is that every AI coding session starts with the full picture — team knowledge, architectural decisions, session history. This requires injecting context into the agent's context window at session start.

The problem: no single delivery mechanism is reliable across all agents and all conditions.

- **Hooks break.** Claude Code had a bug (#10373) where `SessionStart` didn't fire for new sessions. Gemini CLI changed its hook format between versions. Hook installation can fail silently.
- **Agents evolve independently.** Each agent has its own hook system, its own config format, its own rules for what hook stdout reaches the model.
- **Users skip setup.** Not everyone runs `ox integrate install`. Some clone a repo and start coding immediately — they should still get team context, just through a less optimal path.
- **Network may be down.** The daemon may not have synced team context yet. The cloud API may be unreachable.

A single-mechanism design creates a single point of failure. When it fails, the agent starts cold — no team context, no session history, no conventions. This defeats the entire purpose of ox.

## Decision

### Four Layers of Context Delivery

Context reaches agents through four independent mechanisms, ordered from richest (most context, requires most infrastructure) to most degraded (least context, always available):

#### Layer 1: Native Hooks (richest)

`ox agent hook <EventName>` fires on agent lifecycle events. On `SessionStart`, it runs `ox agent prime` which returns full team context as structured JSON injected into the agent's context window.

- **Delivers**: Team context, session history, whispers, suggested next actions, agent guidance
- **Requires**: Hook installation (`ox integrate install`), daemon running, agent supports hooks
- **Failure modes**: Hooks not installed, hook format changed, daemon unavailable, agent doesn't inject hook stdout

#### Layer 2: CLAUDE.md / AGENTS.md Marker (static fallback)

`ox integrate install` writes a marker into the project's `CLAUDE.md` (or `AGENTS.md`) instructing the agent to run `ox agent prime` on session start. Even if hooks fail, agents that read project instructions will self-prime.

- **Delivers**: Same as Layer 1 (agent runs prime itself), but delayed until agent reads the file
- **Requires**: Marker installed in project, agent reads CLAUDE.md/AGENTS.md
- **Failure modes**: Marker not installed, agent doesn't read project instructions, ox not in PATH

#### Layer 3: .sageox/README.md (discovery)

When ox initializes a repo (`ox init`), it creates `.sageox/README.md` with human-readable context about what ox is and how to use it. Agents exploring the repo naturally discover this file.

- **Delivers**: Minimal context — what ox is, how to prime, team name
- **Requires**: `ox init` completed, agent explores directory structure
- **Failure modes**: Agent doesn't explore .sageox/, file deleted

#### Layer 4: Whispers (real-time, ongoing)

Even after initial priming, agents receive ongoing context via whispers — delivered through `UserPromptSubmit` hooks as `<system-reminder>` tags. This covers context that arrives after session start: teammate murmurs, build results, conflict warnings.

- **Delivers**: Real-time team coordination, incremental context updates
- **Requires**: Hooks installed, daemon running, whisper store populated
- **Failure modes**: Hooks not installed, daemon unavailable, no whispers pending

### Degradation Matrix

| Layer 1 (Hooks) | Layer 2 (Marker) | Layer 3 (README) | Layer 4 (Whispers) | User Experience |
|:---:|:---:|:---:|:---:|---|
| OK | OK | OK | OK | Full context, real-time updates |
| FAIL | OK | OK | FAIL | Full context on first prompt (agent self-primes), no real-time |
| FAIL | FAIL | OK | FAIL | Minimal context if agent explores .sageox/ |
| FAIL | FAIL | FAIL | FAIL | Cold start — no team context (worst case) |

The design goal: even with Layer 1 failed, the agent should still get team context through Layer 2 before writing any code.

## Consequences

**Benefits**:
- No single point of failure for context delivery
- Graceful degradation — partial context is better than no context
- Each layer is independently testable and independently deployable
- New agents get Layer 2 + 3 support for free (they just read files)
- Whispers provide ongoing context even if initial priming was degraded

**Tradeoffs**:
- Four mechanisms to maintain and test. Each has its own installation path, failure modes, and doctor checks.
- Layers can deliver redundant context (agent primes via hook AND reads CLAUDE.md marker). Acceptable — duplicate context is better than missing context.
- Layer 2 (CLAUDE.md marker) modifies the user's project file. Some users object to this. `ox integrate install` is explicit, not automatic.
- Layer 3 (.sageox/README.md) only works if the agent happens to explore that directory. It's a best-effort discovery mechanism, not a guarantee.
