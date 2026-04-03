# ADR-004: Session Lifecycle State Machine

**Status**: Accepted
**Date**: 2026-04-02

## Context

ox integrates with multiple AI coding agents (Claude Code, Gemini CLI, Codex CLI, OpenCode, Amp, Pi, and others). Each agent has its own hook event vocabulary:

- Claude Code: `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `PreCompact`, `Stop`
- Gemini CLI: `SessionStart`, `BeforeAgent`, `AfterTool`, `SessionEnd`
- Codex CLI: `session_start`, `session_end`
- OpenCode: `session.created`, `message.created`, `tool.started`, `tool.finished`

Without a canonical model, every handler would need per-agent switch statements. Adding a new agent would require modifying every handler. The mapping between agent-native events and ox behavior would be scattered across the codebase.

## Decision

### Canonical Phases

All agent-native events map to exactly one of six canonical lifecycle phases:

| Phase | Meaning | Primary Behavior |
|-------|---------|-----------------|
| `start` | Session initialized | Prime agent with team context, begin recording |
| `prompt` | User submitted a prompt | Deliver pending whispers (primary channel) |
| `beforetool` | Agent is about to use a tool | Reserved (currently noop) |
| `aftertool` | Agent finished using a tool | Incremental session drain, fallback whisper delivery |
| `compact` | Context window cleared/compacted | Re-prime agent, reset recording offset |
| `stop` | Session ending | Final drain, trigger finalization |

### Phase Resolution

A single function `resolvePhase(agentType, eventName)` maps any agent's native event name to a canonical phase. This is the only place agent-specific event vocabulary exists.

### Active Phase Behavior

Not all phases trigger behavior. A fast-path check short-circuits phases that are noops:

```go
var activePhaseBehavior = map[string]bool{
    phaseStart:     true,
    phasePrompt:    true,
    phaseAfterTool: true,
    phaseCompact:   true,
    phaseStop:      true,
}
```

Hooks resolving to inactive phases (e.g., `beforetool`) return immediately with zero work — critical for keeping hook latency low on the tool-call hot path.

### Dispatch

After resolution, `dispatchPhase(ctx)` routes to the appropriate handler. Handlers are phase-specific, not agent-specific. Agent differences are absorbed entirely by `resolvePhase`.

```
Agent hook fires -> resolvePhase(agent, event) -> canonical phase
    -> activePhaseBehavior check (fast-path noop if inactive)
    -> dispatchPhase -> handleStart/handlePrompt/handleAfterTool/handleStop
```

### Whisper Delivery Constraint

An empirical discovery drove the phase design: not all hook events can inject content into the agent's context window. Testing revealed:

| Hook Event | stdout reaches agent model? |
|------------|---------------------------|
| `UserPromptSubmit` | Yes — primary delivery channel |
| `SessionStart` | Yes — but fires only once |
| `PreCompact` | Yes — but fires rarely |
| `PostToolUse` | No — stdout completely discarded by Claude Code |
| `PreToolUse` | No — stdout discarded |
| `Stop` | No — fires after session ends |

`prompt` phase is the only reliable channel for whisper delivery. `aftertool` handles incremental recording but cannot inject context. This constraint is architectural to how agents process hook output, not a bug.

## Consequences

**Benefits**:
- Adding a new agent requires only one new entry in `resolvePhase` — zero handler changes
- Phase behavior is testable independently of any agent
- Fast-path noop keeps hook latency minimal for inactive phases
- Clear contract: if you're building an ox integration, map your events to these 6 phases

**Tradeoffs**:
- Some agent events don't map cleanly. Gemini's `BeforeAgent` is closest to `prompt` but fires at a different point in the turn. The mapping is a lossy compression.
- The 6 phases are a frozen contract. Adding a new phase requires updating every agent's mapping table and potentially every handler.
- Whisper delivery being limited to `prompt` phase means agents that don't fire `UserPromptSubmit` (or equivalent) cannot receive real-time whispers during a session.
