# ADR-007: Hooks Integration Pattern

**Status:** Accepted
**Date:** 2025-12-22
**Deciders:** SageOx Engineering

## Context

Coding agents (Claude Code, OpenCode, Gemini CLI, Codex) need SageOx context injected at session start. Each agent has different extension mechanisms:

| Agent | Mechanism | Location |
|-------|-----------|----------|
| Claude Code | Hooks (shell commands) | `.claude/settings.json` |
| OpenCode | Hooks | `.opencode/config.json` |
| Gemini CLI | Hooks | `.gemini/settings.json` |
| Codex | Instructions file | `codex.md` |
| GitHub Copilot | No hook system | N/A |

**Challenge:** Consistent behavior across agents with different capabilities.

## Decision

Use **native hook systems** where available, with graceful fallback to static files.

### Core Principle: Multi-Layered Fallback

**Never rely on a single integration point.** AI agent ecosystems are evolving rapidly, and any single hook mechanism may have bugs, limitations, or behavior changes. SageOx uses defense-in-depth:

```
┌─────────────────────────────────────────────────────────────┐
│                  Guidance Delivery Layers                    │
├─────────────────────────────────────────────────────────────┤
│  Layer 1: SessionStart Hook    → Runs on /clear, --resume   │
│  Layer 2: PreCompact Hook      → Runs on /compact           │
│  Layer 3: CLAUDE.md/AGENTS.md  → Always read by agent       │
│  Layer 4: .sageox/README.md    → Discovered via exploration │
└─────────────────────────────────────────────────────────────┘
```

**Why this matters:**
- **Hooks can have bugs** (see Claude Code #10373 where SessionStart doesn't work for new sessions)
- **Agents evolve** — hook behavior may change between versions
- **User configurations vary** — some users may not have hooks installed
- **Multiple entry points increase reliability** — if one fails, others catch it

**Implementation:**
1. `ox init` installs BOTH hooks AND CLAUDE.md/AGENTS.md instructions
2. Instructions are marked "(REQUIRED)" to ensure agents execute them
3. `ox doctor` warns when fallback layers are missing
4. Each layer is designed to trigger `ox agent prime` independently

This redundancy is intentional. The marginal token cost (~100 tokens for CLAUDE.md instructions) is far outweighed by the reliability gain.

### Hook Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    Agent Session Start                       │
└─────────────────────────────────┬───────────────────────────┘
                                  │
                                  ▼
┌─────────────────────────────────────────────────────────────┐
│              Agent's Native Hook System                      │
│  (user_prompt_submit, session_start, etc.)                  │
└─────────────────────────────────┬───────────────────────────┘
                                  │
                                  ▼
┌─────────────────────────────────────────────────────────────┐
│                    ox agent prime                            │
│  - Loads team guidance from cloud/cache                     │
│  - Formats for agent consumption                            │
│  - Outputs to stdout (captured by hook)                     │
└─────────────────────────────────────────────────────────────┘
```

### Hook Installation

```bash
# Install hooks for detected agents
ox hooks install

# Install for specific agent
ox hooks install --claude
ox hooks install --opencode
ox hooks install --gemini

# Install at user level (all projects)
ox hooks install --user

# Verify installation
ox doctor  # Shows hook status per agent
```

### Hook Configuration Examples

**Claude Code (`.claude/settings.json`):**
```json
{
  "hooks": {
    "user_prompt_submit": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "ox agent prime --format claude"
          }
        ]
      }
    ]
  }
}
```

**OpenCode (`.opencode/config.json`):**
```json
{
  "hooks": {
    "session_start": "ox agent prime --format opencode"
  }
}
```

### Output Format by Agent

```bash
# Claude Code format
ox agent prime --format claude
# Returns: System prompt additions as plain text

# OpenCode format
ox agent prime --format opencode
# Returns: JSON structure with context

# Gemini format
ox agent prime --format gemini
# Returns: Gemini-compatible instructions
```

## Tradeoffs

### Hooks vs Alternatives

| Approach | Pros | Cons |
|----------|------|------|
| **Native hooks** (chosen) | Dynamic, always fresh, agent-native | Requires shell execution, agent-specific |
| Static AGENTS.md | Simple, no execution | Stale, manual updates, no personalization |
| IDE plugins | Deep integration | Per-IDE development, maintenance burden |
| Proxy/middleware | Universal | Complex setup, latency, security concerns |

### Why Native Hooks

1. **Dynamic content**: Fresh guidance on every session
2. **Personalization**: Team-specific content from cloud
3. **No manual sync**: Updates automatic
4. **Agent-native**: Uses each agent's intended extension point

### Tradeoff: Shell Execution

**Risk:** Hooks execute shell commands, potential security concern.

**Mitigations:**
- Hooks only run `ox` binary (user already trusts it)
- No arbitrary command execution
- Hooks visible in config files (auditable)
- `ox doctor` verifies hook configuration

## Gotchas

### 1. Hook Execution Timing

**Problem:** Some hooks run before project context is available.

**Solution:** `ox agent prime` detects context:
```go
func getPrimeContext() string {
    if gitRoot := findGitRoot(); gitRoot != "" {
        return loadProjectGuidance(gitRoot)
    }
    return loadUserGuidance() // fallback to user-level
}
```

### 2. Hook Output Size Limits

**Problem:** Some agents truncate hook output.

**Solution:**
- Keep output concise (< 4KB recommended)
- Use `--compact` flag for minimal output
- Link to full docs rather than embedding

### 3. Concurrent Hook Execution

**Problem:** Multiple hooks may run simultaneously.

**Solution:**
- `ox agent prime` is stateless, safe for concurrent execution
- File locks on cache writes
- Idempotent operations only

### 4. Hook Installation Conflicts

**Problem:** User may have existing hooks.

**Solution:**
- `ox hooks install` preserves existing hooks
- Appends to hook arrays, doesn't replace
- `ox hooks uninstall` cleanly removes only ox hooks

### 5. Agent Detection Accuracy

**Problem:** How to know which agents are installed?

**Solution:**
```go
func detectAgents() []Agent {
    var agents []Agent

    // Check for config directories
    if exists(".claude") { agents = append(agents, Claude) }
    if exists(".opencode") { agents = append(agents, OpenCode) }
    if exists(".gemini") { agents = append(agents, Gemini) }

    // Check for binaries in PATH
    if _, err := exec.LookPath("claude"); err == nil {
        agents = append(agents, Claude)
    }

    return agents
}
```

### 6. User vs Project Hooks

**Problem:** Some hooks should apply globally, others per-project.

**Solution:**
- Project hooks: `./.claude/settings.json`
- User hooks: `~/.claude/settings.json`
- Project hooks take precedence
- `ox hooks install --user` for global installation

### 7. Hook Failure Handling

**Problem:** What if `ox agent prime` fails?

**Solution:**
- Exit code 0 even on soft failures (don't break agent)
- Stderr for errors (agents typically ignore)
- Fallback to bundled minimal guidance
- `ox doctor` reports hook health

## Known Issues

### SessionStart Hook Bug (Claude Code #10373)

**Discovered:** 2025-01-18
**Status:** Active (upstream bug in Claude Code)
**Reference:** https://github.com/anthropics/claude-code/issues/10373

**Problem:** SessionStart hooks execute but their output is **not processed** for new conversation starts. The `qz()` function responsible for processing SessionStart hooks is only called for:
- `/clear` command ✅
- `/compact` command ✅
- `--resume` flag ✅
- **New conversations ❌** (output is discarded)

**Impact:** `ox agent prime` runs via SessionStart hook but Claude doesn't receive the guidance for new sessions. This is the most common use case.

**Workaround:** ox CLI uses a multi-layered approach:

| Layer | Mechanism | When It Works |
|-------|-----------|---------------|
| 1. SessionStart hook | Shell command | /clear, --resume |
| 2. PreCompact hook | Shell command | /compact |
| 3. CLAUDE.md instructions | Static file | Always (if Claude reads it) |
| 4. AGENTS.md instructions | Static file | Always (if Claude reads it) |

**Detection:** `ox doctor` includes a check (`session-start-hook-bug`) that warns when:
- SessionStart hook is configured
- No CLAUDE.md/AGENTS.md fallback exists

**Fix Applied:**
1. Made CLAUDE.md/AGENTS.md instructions more prominent with explicit "REQUIRED" language
2. Added code block formatting for the ox agent prime command
3. ox doctor warns about the bug and suggests `ox integrate install --user` for fallback

## Implementation Checklist

- [x] Hook installation for Claude Code
- [x] Hook installation for OpenCode
- [x] Hook installation for Gemini CLI
- [x] Static file fallback for Codex
- [x] User-level hook support
- [x] Hook conflict detection
- [x] Hook uninstallation
- [x] Doctor checks for hook health
- [x] SessionStart hook bug workaround (ADR-007 update 2025-01-18)
