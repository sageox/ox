<!-- doc-audience: ai -->
# Agent Support Scorecard

Current ox integration status for coding agents. See [agent-support-tiers.md](agent-support-tiers.md) for tier definitions and advancement checklists.

Last updated: 2026-03-06

## Summary

**Current tier** = what ox has implemented today. **Ceiling** = maximum tier achievable given the agent's upstream capabilities.

**CLI-based agents are prioritized** for tier advancement. IDE-based agents are deprioritized (see [agent-support-tiers.md](agent-support-tiers.md#prioritization)).

### Commercial Agents

| Agent | Type | Current Tier | Ceiling | Next Step |
|-------|------|-------------|---------|-----------|
| **Claude Code** | CLI | **Gold** | Gold | Reference implementation |
| **Codex** | CLI | Unsupported | Bronze | Verify session recording, inject AGENTS.md prime marker |
| **Amp** | CLI | Unsupported | Silver | Implement session recording, prime marker, then hooks |
| **Cursor** | IDE | Unsupported | Gold | Implement session recording, prime marker, then hooks |
| **GitHub Copilot** | IDE | Unsupported | Gold | Implement session recording, prime marker, then hooks |
| **Windsurf** | IDE | Unsupported | Silver | Implement session recording, prime marker, then hooks (no custom commands upstream) |
| **Charlie** | Cloud | Unsupported | TBD | Not in agentx yet — register agent type first |

### Open Source Agents (5K+ stars only)

| Agent | Type | Stars | Current Tier | Ceiling | Next Step |
|-------|------|-------|-------------|---------|-----------|
| **Aider** | CLI | 42K | Unsupported | Bronze | No hooks, no sessions upstream. Verify manual session recording |
| **Goose** | CLI | 33K | Unsupported | Bronze | No hooks upstream. Verify manual session recording |
| **OpenCode** | CLI | 11K | Unsupported | Silver | Implement session recording, then plugin-based hooks |
| **Cline** | IDE | 59K | Unsupported | Gold | Implement session recording, prime marker, then hooks |
| **Continue** | IDE | 32K | Unsupported | Bronze | No hooks, no sessions upstream. Verify manual session recording |

### Progression Key

```
Unsupported ──► Bronze ──► Silver ──► Gold
(detected)    (sessions)  (hooks)    (full)
```

---

## Claude Code  —  Current: Gold / Ceiling: Gold

The reference implementation. All tiers fully satisfied.

### What ox Has Implemented
- **Detection:** `CLAUDECODE=1`, `CLAUDE_CODE_ENTRYPOINT`, `CLAUDE_CODE_SESSION_ID`, `AGENT_ENV`
- **Hooks:** Full lifecycle — SessionStart, SessionEnd, PreToolUse, PostToolUse, UserPromptSubmit, Stop, PreCompact
- **Custom commands:** 11 slash commands in `extensions/claude/commands/` (`/ox-session-start`, `/ox-session-stop`, `/ox-session-abort`, `/ox-session-status`, `/ox-session-list`, `/ox-prime`, `/ox-doctor`, `/ox-status`, `/ox-init`, `/ox`)
- **Session adapter:** Deep adapter reads Claude Code's native JSONL from `~/.claude/projects/`
- **Auto sessions:** Hooks trigger session start/stop automatically
- **MCP:** Supported via `.claude/settings.json`
- **Prime marker:** `<!-- ox:prime -->` injected into CLAUDE.md
- **Doctor:** Full hook validation, stale command detection, session state checks
- **Context files:** CLAUDE.md, AGENTS.md

### Upstream Capabilities

| Capability | Supported | Details |
|------------|-----------|---------|
| Lifecycle hooks | Yes | SessionStart, SessionEnd, PreToolUse, PostToolUse, UserPromptSubmit, Stop, SubagentStop, PreCompact |
| MCP servers | Yes | Via settings.json |
| Custom slash commands | Yes | `.claude/commands/*.md` |
| Context files | Yes | CLAUDE.md, AGENTS.md |
| Session ID env var | Yes | `CLAUDE_CODE_SESSION_ID` |

---

## Cursor  —  Current: Unsupported / Ceiling: Gold

Detection and agentx registration exist. Hooks, session recording, commands, and deep adapter are **not implemented in ox**.

### What ox Has Implemented
- **Detection:** `CURSOR_AGENT=1`, `AGENT_ENV=cursor`, CLI heuristics
- **Agent registration:** `AgentTypeCursor` in agentx, `Detect()`, `IsInstalled()`, `DetectVersion()`
- **Config paths:** `~/.cursor` (user), `.cursor/` (project)
- **Context files:** `.cursorrules`
- **Lifecycle events defined:** 18 event types mapped in agentx (`EventPhases()`)
- **AGENT_ENV aliases:** `["cursor"]`

### What ox Has NOT Implemented
- **Session recording:** Not verified end-to-end with generic JSONL adapter
- **Prime marker injection:** Not injecting `<!-- ox:prime -->` into `.cursorrules`
- **Hook manager:** Not implemented — hooks are defined in agentx but `ox init` does not install them
- **Custom commands:** Cursor supports `.cursor/commands/*.md` but no `extensions/cursor/` directory exists
- **Deep session adapter:** No dedicated adapter
- **Auto sessions:** No hook-triggered session start/stop
- **Doctor validation:** No Cursor-specific hook checks

### Upstream Capabilities

| Capability | Supported | Details |
|------------|-----------|---------|
| Lifecycle hooks | Yes | 18 event types including shell, MCP, file, and agent-level events |
| MCP servers | Yes | Via settings or plugin system |
| Custom slash commands | Yes | `.cursor/commands/*.md` (added 2026) |
| Context files | Yes | `.cursorrules` |
| Session ID env var | No | Via hook stdin JSON only |
| Plugins | Yes | Bundle MCP, skills, rules, hooks (2026) |

### Path Forward
1. **Unsupported -> Bronze:** Verify session recording, inject prime marker into `.cursorrules`, verify team context delivery
2. **Bronze -> Silver:** Implement HookManager, `ox init` hook installation, doctor checks
3. **Silver -> Gold:** Create `extensions/cursor/commands/`, implement CommandManager, wire auto-session capture

---

## GitHub Copilot  —  Current: Unsupported / Ceiling: Gold

Detection and agentx registration exist. Hooks, session recording, and commands are **not implemented in ox**.

### What ox Has Implemented
- **Detection:** `COPILOT_AGENT=1`, `AGENT_ENV=copilot`
- **Agent registration:** `AgentTypeCopilot` in agentx
- **Config paths:** `~/.config/github-copilot` (XDG), `.github/` (project)
- **Context files:** `.github/copilot-instructions.md`
- **Lifecycle events defined:** 6 event types mapped in agentx
- **AGENT_ENV aliases:** `["copilot", "github-copilot"]`

### What ox Has NOT Implemented
- **Session recording:** Not verified end-to-end
- **Prime marker injection:** Not injecting into `.github/copilot-instructions.md`
- **Hook manager:** Not implemented
- **Custom commands:** Copilot supports custom agents/prompts/skills (`.github/copilot/`) but ox hasn't integrated
- **Deep session adapter:** No dedicated adapter
- **MCP integration:** MCP works in IDE agent mode; coding agent (cloud) has separate MCP config via repo settings

### Upstream Capabilities

| Capability | Supported | Details |
|------------|-----------|---------|
| Lifecycle hooks | Yes | 6 event types in `.github/hooks/*.json` |
| MCP servers | Partial | In IDE: yes. Coding agent: via repo settings |
| Custom commands | Yes | Custom agents, prompts, skills (`.github/copilot/`) |
| Context files | Yes | `.github/copilot-instructions.md` |
| Session ID env var | No | Via hook stdin JSON only |
| Composable system | Yes | Instructions + prompts + skills + agents + hooks |

### Path Forward
1. **Unsupported -> Bronze:** Verify session recording, inject prime marker, verify team context delivery
2. **Bronze -> Silver:** Implement HookManager, `ox init` hook installation, doctor checks
3. **Silver -> Gold:** Create `extensions/copilot/` with skill/prompt files, implement CommandManager, wire auto-session capture

---

## Windsurf  —  Current: Unsupported / Ceiling: Silver

Detection and agentx registration exist. Hooks, session recording, and adapter are **not implemented in ox**. Ceiling is Silver because Windsurf does not support custom commands upstream.

### What ox Has Implemented
- **Detection:** `WINDSURF_AGENT=1`, `CODEIUM_AGENT=1`, `AGENT_ENV=windsurf`
- **Agent registration:** `AgentTypeWindsurf` in agentx
- **Config paths:** `~/.codeium` (user), `.windsurf/` (project)
- **Context files:** `.windsurfrules`
- **Lifecycle events defined:** 12 event types mapped in agentx
- **AGENT_ENV aliases:** `["windsurf", "codeium"]`

### What ox Has NOT Implemented
- **Session recording:** Not verified end-to-end
- **Prime marker injection:** Not injecting into `.windsurfrules`
- **Hook manager:** Not implemented
- **Deep session adapter:** No dedicated adapter
- **Auto sessions:** No hook-triggered session start/stop
- **No SessionStart/SessionEnd events:** Windsurf hooks are action-oriented (pre/post tool use), not session-oriented. Session boundaries must be approximated via first/last cascade response hooks.

### Upstream Capabilities

| Capability | Supported | Details |
|------------|-----------|---------|
| Lifecycle hooks | Yes | 12 event types, action-oriented (no session start/end) |
| MCP servers | Yes | Via settings, GitLab remote MCP, OAuth for GitHub remote MCP |
| Custom commands | **No** | Not supported — blocks Gold tier |
| Context files | Yes | `.windsurfrules` |
| Session ID env var | No | Via hook stdin JSON only |

### Path Forward
1. **Unsupported -> Bronze:** Verify session recording, inject prime marker, verify team context delivery
2. **Bronze -> Silver:** Implement HookManager, approximate session boundaries via cascade hooks
3. **Silver -> Gold:** **Blocked** — no custom command support upstream. Re-evaluate if Windsurf adds commands/skills.

---

## Codex  —  Current: Unsupported / Ceiling: Bronze

Detection and agentx registration exist. Session recording has not been verified end-to-end. Ceiling is Bronze because Codex has no hook system (upstream [rejected hooks PR](https://github.com/openai/codex/pull/9796)).

### What ox Has Implemented
- **Detection:** `CODEX_CI`, `CODEX_SANDBOX`, `CODEX_THREAD_ID`, `.codex/` directory, `AGENT_ENV=codex`
- **Agent registration:** `AgentTypeCodex` in agentx, version detection via `codex --version`
- **`ox agent prime`:** Works, delivers AGENTS.md content and team context
- **Generic JSONL adapter:** `"codex" -> "generic"` alias mapped
- **Doctor check:** Integration status reported

### What ox Has NOT Implemented
- **Session recording verification:** Manual `ox agent <id> session start/stop` exists but not verified end-to-end for Codex
- **AGENTS.md prime marker:** No `<!-- ox:prime -->` equivalent injected
- **Skills:** Codex supports [Skills](https://developers.openai.com/codex/skills/) (SKILL.md format, `$skill-name`) but ox hasn't created any
- **MCP in ox:** Codex [supports MCP](https://developers.openai.com/codex/mcp) natively, but ox marks `MCPServers: false`
- **Notify integration:** Codex `notify` config (`agent-turn-complete`) could trigger session stop but isn't wired
- **Extension files:** No `extensions/codex/` directory

### Upstream Capabilities

| Capability | Supported | Details |
|------------|-----------|---------|
| Lifecycle hooks | **No** | PR rejected; only `notify` on `agent-turn-complete` |
| MCP servers | Yes | Via `config.toml` and `codex mcp` commands |
| Custom slash commands | No | Built-in only (24 commands) |
| Skills | Yes | SKILL.md format, `$skill-name` invocation |
| Context files | Yes | `AGENTS.md` |
| Session ID env var | Yes | `CODEX_THREAD_ID` |
| `--json` output | Yes | Newline-delimited JSON events for automation |

### Path Forward
1. **Unsupported -> Bronze:** Verify session recording end-to-end, inject AGENTS.md prime marker, create ox skills
2. **Bronze -> Silver:** **Blocked** — no hook system upstream. Cannot advance past Bronze.
3. **Bronze enhancements:** MCP server integration, `notify` for session lifecycle, `--json` transcript piping

---

## Amp  —  Current: Unsupported / Ceiling: Silver

Detection and agentx registration exist. Hooks defined in agentx but **not implemented in ox**. Ceiling is Silver — Amp has hooks but only 2 event types (tool pre/post execute), no session start/end events.

### What ox Has Implemented
- **Detection:** `AMP=1`, `AMP_AGENT=1`, `AMP_THREAD_URL`, `AGENT_ENV=amp`
- **Agent registration:** `AgentTypeAmp` in agentx
- **Config paths:** `~/.config/amp` (XDG)
- **Context files:** `AGENTS.md`
- **Lifecycle events defined:** 2 event types mapped (`tool:pre-execute`, `tool:post-execute`)
- **AGENT_ENV aliases:** `["amp"]`
- **Session ID source:** `AMP_THREAD_URL` env var

### What ox Has NOT Implemented
- **Session recording:** Not verified end-to-end
- **Prime marker injection:** Not injecting into `AGENTS.md`
- **Hook manager:** Not implemented
- **Custom commands/skills:** Amp supports [toolboxes and skills](https://ampcode.com/manual) but ox hasn't created any
- **Deep session adapter:** No dedicated adapter
- **Auto sessions:** No hook-triggered session start/stop

### Upstream Capabilities

| Capability | Supported | Details |
|------------|-----------|---------|
| Lifecycle hooks | Yes | 2 events: `tool:pre-execute`, `tool:post-execute` (no session start/end) |
| MCP servers | Yes | Via `.vscode/settings.json` or skill `mcp.json` |
| Custom commands | Yes | Toolboxes (`AMP_TOOLBOX`) and skills |
| Context files | Yes | `AGENTS.md` |
| Session ID env var | Yes | `AMP_THREAD_URL` |

### Path Forward
1. **Unsupported -> Bronze:** Verify session recording, inject AGENTS.md prime marker, verify team context delivery
2. **Bronze -> Silver:** Implement HookManager for `tool:pre-execute`/`tool:post-execute`, doctor checks
3. **Silver -> Gold:** **Blocked** — only 2 hook events (no session start/end, no prompt submit, no compact). Re-evaluate as Amp adds events.

---

## Aider  —  Current: Unsupported / Ceiling: Bronze

Detection and agentx registration exist. Aider is a terminal-based Git-integrated coding tool. **No hooks, no session concept upstream.** Ceiling is Bronze — can only support manual session recording.

### What ox Has Implemented
- **Detection:** `AIDER=1`, `AIDER_AGENT=1`, `AGENT_ENV=aider`, CLI heuristic
- **Agent registration:** `AgentTypeAider` in agentx
- **Config paths:** `~/.aider` (user), `.aider` (project)
- **Context files:** `.aider.conf.yml`, `CONVENTIONS.md`

### What ox Has NOT Implemented
- **Session recording:** Not verified end-to-end
- **Prime marker injection:** Not injecting into `.aider.conf.yml` or `CONVENTIONS.md`
- **No hooks upstream:** Aider is a CLI tool with no hook/lifecycle system
- **No session concept upstream:** `SupportsSession()` returns false, no session ID
- **No AGENT_ENV aliases:** Not implemented
- **No lifecycle event mapper:** Not applicable (no hooks)

### Upstream Capabilities

| Capability | Supported | Details |
|------------|-----------|---------|
| Lifecycle hooks | **No** | CLI-based, no hook system |
| MCP servers | **No** | No native MCP support (community MCP server wrappers exist) |
| Custom commands | Yes | `/commands` in-chat commands |
| Context files | Yes | `.aider.conf.yml`, `CONVENTIONS.md` |
| Session ID env var | **No** | No session concept |
| Git integration | Yes | Direct Git integration, tracks diffs |

### Path Forward
1. **Unsupported -> Bronze:** Verify manual session recording end-to-end, inject prime marker into `CONVENTIONS.md`
2. **Bronze -> Silver:** **Blocked** — no hook system upstream. Cannot advance past Bronze.

---

## Cline  —  Current: Unsupported / Ceiling: Gold

Detection and agentx registration exist. Cline is an open-source VS Code extension with full hook support (v3.36+). **Not implemented in ox** beyond detection.

### What ox Has Implemented
- **Detection:** `CLINE=1`, `CLINE_AGENT=1`, `AGENT_ENV=cline`
- **Agent registration:** `AgentTypeCline` in agentx
- **Config paths:** VS Code extension storage (platform-specific), `.cline/` (project)
- **Context files:** `.clinerules`, `.cline/instructions.md`
- **Lifecycle events defined:** 8 event types mapped (`TaskStart`, `TaskResume`, `TaskCancel`, `TaskComplete`, `PreToolUse`, `PostToolUse`, `UserPromptSubmit`, `PreCompact`)
- **AGENT_ENV aliases:** `["cline", "claude-dev"]`

### What ox Has NOT Implemented
- **Session recording:** Not verified end-to-end
- **Prime marker injection:** Not injecting into `.clinerules`
- **Hook manager:** Not implemented
- **Deep session adapter:** No dedicated adapter
- **Auto sessions:** No hook-triggered session start/stop
- **Doctor validation:** No Cline-specific checks

### Upstream Capabilities

| Capability | Supported | Details |
|------------|-----------|---------|
| Lifecycle hooks | Yes | 8 events: TaskStart, TaskResume, TaskCancel, TaskComplete, PreToolUse, PostToolUse, UserPromptSubmit, PreCompact |
| MCP servers | Yes | Native MCP support, MCP prompts appear as `/mcp:server:prompt` |
| Custom commands | **No** | Not supported (MCP prompts serve as partial substitute) |
| Context files | Yes | `.clinerules`, `.cline/instructions.md` |
| Session ID env var | No | Via hook stdin JSON only |

### Path Forward
1. **Unsupported -> Bronze:** Verify session recording, inject prime marker into `.clinerules`, verify team context delivery
2. **Bronze -> Silver:** Implement HookManager for Cline's 8 events, `ox init` hook installation, doctor checks
3. **Silver -> Gold:** Wire auto-session capture via TaskStart/TaskComplete hooks, create extension files, build deep adapter

---

## Goose  —  Current: Unsupported / Ceiling: Bronze

Detection and agentx registration exist. Goose is Block's open-source, MCP-native agent. **No hooks upstream.** Ceiling is Bronze.

### What ox Has Implemented
- **Detection:** `GOOSE=1`, `GOOSE_AGENT=1`, `AGENT_ENV=goose`, CLI heuristic
- **Agent registration:** `AgentTypeGoose` in agentx
- **Config paths:** `~/.config/goose` (XDG)
- **Context files:** `.goose/config.yaml`, `.goosehints`

### What ox Has NOT Implemented
- **Session recording:** Not verified end-to-end
- **Prime marker injection:** Not injecting into `.goosehints`
- **No hooks upstream:** Goose is CLI-based with no hook system
- **No session concept upstream:** `SupportsSession()` returns false
- **No AGENT_ENV aliases:** Not implemented
- **No lifecycle event mapper:** Not applicable (no hooks)

### Upstream Capabilities

| Capability | Supported | Details |
|------------|-----------|---------|
| Lifecycle hooks | **No** | CLI-based, no hook system |
| MCP servers | Yes | Core extensibility model — 3,000+ MCP servers, co-developed MCP standard with Anthropic |
| Custom commands | **No** | Not supported |
| Context files | Yes | `.goose/config.yaml`, `.goosehints`, `AGENTS.md` |
| Session ID env var | **No** | No session concept |
| Extensions | Yes | Built-in, remote (MCP URL), and CLI-wrapped extensions |

### Path Forward
1. **Unsupported -> Bronze:** Verify manual session recording, inject prime marker into `.goosehints`
2. **Bronze -> Silver:** **Blocked** — no hook system upstream. Cannot advance past Bronze.
3. **Bronze enhancements:** MCP server integration (Goose is MCP-native, strong fit)

---

## OpenCode  —  Current: Unsupported / Ceiling: Silver

Detection and agentx registration exist. OpenCode is an open-source terminal agent with a JS/TS plugin system that provides hook-like lifecycle events. **Not implemented in ox** beyond detection.

### What ox Has Implemented
- **Detection:** `OPENCODE=1`, `OPENCODE_AGENT=1`, `AGENT_ENV=opencode`
- **Agent registration:** `AgentTypeOpenCode` in agentx
- **Config paths:** `~/.opencode` (user), `.opencode` (project)
- **Context files:** `AGENTS.md`
- **Lifecycle events defined:** 14 event types mapped via plugin system (`session.created`, `session.compacted`, `tool.execute.before/after`, `file.edited`, etc.)
- **AGENT_ENV aliases:** `["opencode"]`

### What ox Has NOT Implemented
- **Session recording:** Not verified end-to-end
- **Prime marker injection:** Not injecting into `AGENTS.md`
- **Hook/plugin manager:** Not implemented
- **Deep session adapter:** No dedicated adapter
- **Auto sessions:** No plugin-triggered session start/stop

### Upstream Capabilities

| Capability | Supported | Details |
|------------|-----------|---------|
| Lifecycle hooks | Yes | 14 events via JS/TS plugin system (session, tool, file, permission events) |
| MCP servers | Yes | Local and remote MCP servers |
| Custom commands | **No** | Not supported |
| Context files | Yes | `AGENTS.md` |
| Session ID env var | No | Via plugin events only |
| Plugin system | Yes | JS/TS modules, auto-loaded from plugin directory |

### Path Forward
1. **Unsupported -> Bronze:** Verify session recording, inject AGENTS.md prime marker, verify team context delivery
2. **Bronze -> Silver:** Implement plugin-based hook integration for OpenCode's 14 lifecycle events
3. **Silver -> Gold:** **Blocked** — no custom command support upstream. Re-evaluate if OpenCode adds commands.

---

## Continue  —  Current: Unsupported / Ceiling: Bronze

Detection and agentx registration exist. Continue is a popular open-source IDE extension (20K+ GitHub stars). **No hooks, no session concept upstream.** Ceiling is Bronze.

### What ox Has Implemented
- **Detection:** `CONTINUE_AGENT=1`, `AGENT_ENV=continue`
- **Agent registration:** `AgentTypeContinue` in agentx
- **Config paths:** `~/.continue` (user), `.continue` (project)
- **Context files:** `.continuerc.json`

### What ox Has NOT Implemented
- **Session recording:** Not verified end-to-end
- **Prime marker injection:** Not injecting into `.continuerc.json`
- **No hooks upstream:** VS Code/JetBrains extension with no hook system
- **No session concept upstream:** `SupportsSession()` returns false
- **No AGENT_ENV aliases:** Not implemented
- **No lifecycle event mapper:** Not applicable (no hooks)

### Upstream Capabilities

| Capability | Supported | Details |
|------------|-----------|---------|
| Lifecycle hooks | **No** | VS Code/JetBrains extension, no hook system |
| MCP servers | **No** | No MCP support |
| Custom commands | Yes | Slash commands via config |
| Context files | Yes | `.continuerc.json` |
| Session ID env var | **No** | No session concept |
| Model agnostic | Yes | Any LLM (local or cloud) |

### Path Forward
1. **Unsupported -> Bronze:** Verify manual session recording, inject prime marker into `.continuerc.json`
2. **Bronze -> Silver:** **Blocked** — no hook system upstream. Cannot advance past Bronze.

---

## Charlie  —  Current: Unsupported / Ceiling: TBD

**Not in agentx yet.** [Charlie](https://charlielabs.ai/) is a cloud-based coding agent from Charlie Labs. Unlike terminal/IDE agents, Charlie is event-driven — triggered by GitHub, Linear, Slack, and Sentry events, running in ephemeral devbox VMs. TypeScript-focused.

### What ox Has Implemented
- Nothing — no `AgentTypeCharlie` in agentx, no detection, no registration.

### What ox Needs First
- **Register in agentx:** Create `pkg/agentx/agents/charlie.go` with agent type, detection, config paths
- **Determine context file:** Charlie may use `AGENTS.md` or a custom format
- **Determine session model:** Charlie's cloud execution model (ephemeral VMs) may require a different session capture approach than terminal/IDE agents
- **Determine hook support:** Charlie has an event-driven runtime but it's unclear if it exposes lifecycle hooks that ox can integrate with

### Upstream Capabilities (from public docs)

| Capability | Supported | Details |
|------------|-----------|---------|
| Lifecycle hooks | TBD | Event-driven runtime (GitHub, Linear, Slack, Sentry triggers) — unclear if ox-compatible hooks |
| MCP servers | TBD | Not documented |
| Custom commands | TBD | Not documented |
| Context files | TBD | May use `AGENTS.md` |
| Session concept | TBD | Ephemeral VM-based — each task runs in isolated compute |
| Execution model | Cloud | Runs from workflow events, not terminal/IDE |

### Path Forward
1. **Research:** Determine Charlie's extensibility primitives, context file format, and session model
2. **Register:** Create agentx agent type and detection logic
3. **Assess ceiling:** Once capabilities are known, assign a tier ceiling

---

## Quick Reference: Upstream Capability Matrix

Which agents support which extensibility primitives (regardless of ox integration status):

| Primitive | Claude Code | Cursor | Copilot | Windsurf | Codex | Amp | Aider | Cline | Goose | OpenCode | Continue |
|-----------|-------------|--------|---------|----------|-------|-----|-------|-------|-------|----------|----------|
| **Lifecycle hooks** | Yes (8) | Yes (18) | Yes (6) | Yes (12) | No | Yes (2) | No | Yes (8) | No | Yes (14) | No |
| **Session start/end** | Yes | Yes | Yes | No | No | No | No | Yes | No | Yes | No |
| **Tool use events** | Yes | Yes | Yes | Yes | No | Yes | No | Yes | No | Yes | No |
| **Prompt submit** | Yes | Yes | Yes | Yes | No | No | No | Yes | No | No | No |
| **Compact/reset** | Yes | Yes | No | No | No | No | No | Yes | No | Yes | No |
| **MCP servers** | Yes | Yes | Partial | Yes | Yes | Yes | No | Yes | Yes | Yes | No |
| **Custom commands** | Yes | Yes | Yes | No | No | Yes | Yes | No | No | No | Yes |
| **Skills/plugins** | No | Yes | Yes | No | Yes | Yes | No | No | No | Yes | No |
| **Context files** | CLAUDE.md | .cursorrules | copilot-inst.md | .windsurfrules | AGENTS.md | AGENTS.md | CONVENTIONS.md | .clinerules | .goosehints | AGENTS.md | .continuerc.json |
| **Session ID** | Env var | Hook JSON | Hook JSON | Hook JSON | Env var | Env var | No | Hook JSON | No | Plugin | No |

---

## Tracking

- GitHub issue for Codex gaps: [#145](https://github.com/sageox/ox/issues/145)
