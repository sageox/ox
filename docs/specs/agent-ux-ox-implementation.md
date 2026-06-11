# Agent UX: ox Implementation

Implementation details for Agent UX principles in the SageOx ox.

> **Prerequisites:** Read [Agent UX Principles](agent-ux-principles.md) first for the conceptual framework.

---

## Overview

This document covers ox-specific implementation of Agent UX patterns:
- Agent instance management with Agent IDs
- Integration hooks for Claude Code and other agents
- Caching strategy for guidance
- Security considerations (signed prompts)
- Testing patterns

---

## Agent Instance Management

### Agent IDs

Short, memorable IDs save tokens:

```go
// 6 chars total saves ~40 tokens vs full UUID (32 chars) per command invocation
// Format: "Ox" + 4 alphanumeric chars
agent_id := "Oxa7b3"
```

### Agent Instance Lifecycle

```
ox agent prime              → Creates agent instance, returns agent_id
ox agent <id> guidance <p>  → Uses agent instance context
ox agent <id> session       → Records session for later analysis
```

### Prime Output Structure

```json
{
  "agent_id": "Oxa7b3",
  "agent_detected": "claude-code",
  "agent_version": "1.0.25",
  "dialect": {
    "format": "json",
    "verbosity": "minimal",
    "max_tokens": 600
  },
  "capabilities_used": ["tool_use", "mcp"],
  "content": "...",
  "kb": [
    {"kb_id": "kb_...", "type": "personal", "slug": "personal-abc", "path": "/...", "viewer_role": "owner", "tokens": 1240},
    {"kb_id": "kb_...", "type": "team",     "slug": "platform",    "path": "/...", "viewer_role": "member", "tokens": 8200}
  ],
  "team_context": "...",
  "ledger": "...",
  "mcp_servers": [],
  "hooks": {
    "session_start": "ox agent prime",
    "pre_commit": "ox agent review"
  }
}
```

The `kb` array is the canonical envelope for the user's knowledge sources —
it merges personal bubbles, profiles, team contexts, and per-repo ledgers
into a single typed list. Each entry carries `kb_id`, `type`
(`personal`/`profile`/`team`/`repo`/`custom`), `slug`, `path`,
`viewer_role`, and a per-source `tokens` count. The legacy
`team_context` and `ledger` fields are still populated for one release as
deprecated mirrors of the same data; new consumers should read `kb`.
See `ox kb list` and `cmd/ox/kb_list.go` for the source-of-truth shape.

---

## Integration Hooks

### Claude Code Hooks

```yaml
# .claude/settings.local.yaml
hooks:
  session_start:
    - command: "ox agent prime"
      # Automatically injects team context on session start
```

### Other Agents

Commands work for any agent, but UX is optimized for Claude Code patterns:
- Hook-based automatic priming
- Agent instance persistence across commands
- Session recording for learning

---

## Flag Scoping

Only show flags where they apply:

| Flag | Scope | Why |
|------|-------|-----|
| `--json` | Global | Many commands support JSON |
| `--review` | Agent only | Security audit for agent output |
| `--text` | Agent only | Human debugging of agent output |
| `--fix` | Doctor only | Auto-fix specific to diagnostics |

**Principle:** Avoid polluting global help with command-specific options.

---

## Contextual Highlighting

The same progressive disclosure pattern applies to both humans and agents:

### Help Output (Human)

```go
func getContextualHighlight(cmdName string) *commandHighlight {
    hasSageox := dirExists(".sageox")
    isLoggedIn, _ := auth.IsAuthenticated()

    switch cmdName {
    case "init":
        if !hasSageox {
            return &commandHighlight{hasStar: true, step: 1}
        }
    case "login":
        if hasSageox && !isLoggedIn {
            return &commandHighlight{hasStar: true, step: 2}
        }
    case "doctor":
        if hasSageox {
            return &commandHighlight{hasStar: true}
        }
    }
    return nil
}
```

### Prime Output (Agent)

```go
// Suggested paths ordered by relevance to detected project context
output.SuggestedPaths = rankPathsByRelevance(detectedInfra, availablePaths)
```

---

## Token Budget Awareness

Agents should know how much context they're consuming:

```json
{
  "content": "...",
  "metadata": {
    "tokens": 523,
    "estimated_tokens_remaining": [
      {"model": "claude-3.5-sonnet", "remaining": 195477},
      {"model": "gpt-4", "remaining": 127477}
    ]
  }
}
```

---

## Caching Strategy

Agent guidance is cached to avoid redundant API calls:

```go
type CacheEntry struct {
    Content    string    `json:"content"`
    Tokens     int       `json:"tokens"`
    CachedAt   time.Time `json:"cached_at"`
    TTL        Duration  `json:"ttl"`
    Signature  string    `json:"signature"` // for security verification
}
```

**Cache locations:**
- Project-level: `.sageox/cache/`
- User-level: `~/.config/sageox/cache/`

---

## Security Considerations

### Signed Prompts

Agent guidance is signed to prevent prompt injection:

```go
// Guidance content includes cryptographic signature
// Agents should verify before acting on guidance
type SignedGuidance struct {
    Content   string `json:"content"`
    Signature string `json:"signature"`
    PublicKey string `json:"public_key"`
}
```

### Audit Mode

The `--review` flag enables security engineers to inspect:
- What content agents receive
- Source of guidance (cached vs live)
- Signature verification status

---

## Daemon Responsibilities

The ox daemon handles long-running operations:

- Background sync of `.sageox/` changes
- Ledger updates and git operations
- Health monitoring and self-healing
- Session processing and summarization

```go
// Command returns immediately
func runSync(cmd *cobra.Command, args []string) error {
    // Tell daemon to sync (non-blocking)
    if err := daemon.RequestSync(); err != nil {
        return err
    }

    // Return status immediately
    fmt.Println("Sync queued. Run 'ox status' to check progress.")
    return nil
}
```

---

## Testing Agent UX

### Fresh Install Invariant

```go
// TestDoctorFreshInstall_NoWarnings
// After fresh `ox init`, doctor should report NO warnings.
// Users should never start in a "dirty" state.
```

### Token Counting

```go
// Verify output stays within expected token budgets
func TestPrimeOutput_TokenBudget(t *testing.T) {
    output := runPrime(ctx)
    assert.Less(t, output.Metadata.Tokens, 600, "prime output exceeds token budget")
}
```

---

## Background Execution

Non-blocking operations for responsive agent experience:

```go
// Session processing runs in background
// Main agent context stays responsive
go func() {
    processSession(ctx, session)
}()
```

---

## ox Implementation Checklist

When adding new agent-facing features to ox:

- [ ] Non-blocking (delegate to daemon where appropriate)
- [ ] Uses short Agent IDs (Ox + 4 chars)
- [ ] Default output is JSON with `agent_id` field
- [ ] Includes token count in `metadata`
- [ ] Supports `--text` for human debugging
- [ ] Supports `--review` for security audit
- [ ] Errors include actionable `action` field
- [ ] Cached in `.sageox/cache/` or `~/.config/sageox/cache/`
- [ ] Signed guidance where applicable
- [ ] Tested for fresh install invariant

---

## Related Documents

- [Agent UX Principles](agent-ux-principles.md) - Core principles (portable to any project)
- [CLI Design System](cli-design-system.md) - Colors, styles, contextual highlighting
- [Development Philosophy](../guides/development-philosophy.md) - Simplicity principles
