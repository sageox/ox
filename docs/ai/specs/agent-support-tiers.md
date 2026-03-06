<!-- doc-audience: ai -->
# Agent Support Tiers

What it means to be a "supported AI coworker" in ox, and the checklist for each tier.

Keywords "MUST", "MUST NOT", "SHOULD", "SHOULD NOT", and "MAY" follow [RFC 2119](https://datatracker.ietf.org/doc/html/rfc2119).

## Why Tiers?

Not all coding agents expose the same extensibility primitives. Some have full lifecycle hooks, MCP, and custom commands. Others only read a context file. Tiers set expectations for what ox can deliver with each agent and guide engineering investment.

## Prioritization

**CLI-based agents are prioritized.** ox is a CLI tool and integrates most naturally with agents that run in the terminal. IDE-based agents (VS Code extensions, JetBrains plugins) are deprioritized — they may still be supported but receive less engineering investment and are lower priority for tier advancement.

## Tier Definitions

### Unsupported: Detection Only

The agent is recognized by ox (auto-detection, config paths, version check) but **cannot record sessions** — either because the agent has no mechanism for session capture (no hooks, no transcript access, no event stream) or because ox hasn't built the integration. An agent that cannot record sessions is not a supported AI coworker; it is merely detected.

**Minimum bar to exit Unsupported:** The agent MUST support session recording via at least one path — hooks, skills, `notify`, `--json` event stream, or manual CLI invocation with a working adapter.

---

### Bronze: Detection + Context Delivery + Session Recording

The agent is recognized by ox, receives team knowledge, and supports session recording (at minimum, manual).

| RFC | Requirement | Description |
|-----|-------------|-------------|
| MUST | **Agent type registered** | `AgentType` constant in `pkg/agentx/agent.go`, agent struct in `pkg/agentx/agents/` |
| MUST | **Auto-detection** | `Detect()` identifies the agent via env vars, config dirs, or CLI heuristics |
| MUST | **AGENT_ENV support** | Agent responds to `AGENT_ENV=<slug>` for explicit identification in agentx |
| MUST | **Config paths** | `UserConfigPath()` and `ProjectConfigPath()` return correct locations |
| MUST | **Context file support** | `ContextFiles()` returns the files the agent reads (e.g., `AGENTS.md`) |
| MUST | **`ox agent prime` works** | Agent receives team context and project guidance on prime |
| MUST | **Prime marker injection** | ox injects `<!-- ox:prime -->` (or equivalent) into the agent's context file so priming is automatic |
| MUST | **Team context delivery** | Agent receives SOUL.md, TEAMS.md, and docs/ content from team context |
| MUST | **MEMORY.md support** | Agent can read/write MEMORY.md for persistent knowledge across sessions |
| MUST | **Session recording works** | `ox agent <id> session start/stop` successfully captures and uploads sessions via at least the generic JSONL adapter |
| MUST | **Registered in setup** | Agent is auto-registered in `pkg/agentx/setup/setup.go` |
| SHOULD | **Installation check** | `IsInstalled()` finds the agent binary or config directory |
| SHOULD | **Version detection** | `DetectVersion()` returns installed version (best-effort) |
| SHOULD | **Doctor check** | At least one doctor check validates the integration is healthy |
| SHOULD | **Session adapter mapped** | Adapter alias in `internal/session/adapters/adapter.go` |

**What users get:** `ox agent prime` delivers team context automatically (via marker). Session recording via `ox agent <id> session start/stop`. MEMORY.md for cross-session persistence. No lifecycle automation.

**What's missing:** No hooks, no auto-session capture, no lifecycle integration, no custom commands/skills.

---

### Silver: Hooks + Lifecycle Integration

The agent supports hooks that ox can install to automate the lifecycle.

| RFC | Requirement | All of Bronze, plus: |
|-----|-------------|----------------------|
| MUST | **Hook support** | `Capabilities().Hooks == true` |
| MUST | **Hook manager** | `HookManager()` returns a working manager that can install/uninstall/validate hooks |
| MUST | **Lifecycle event mapper** | Implements `LifecycleEventMapper` with `EventPhases()` mapping native events to canonical phases |
| MUST | **Session start/end hooks** | At least `SessionStart` and `SessionEnd` (or equivalent) events mapped |
| MUST | **Tool hooks** | At least `PreToolUse` and `PostToolUse` (or equivalent) events mapped |
| MUST | **`ox init` installs hooks** | Running `ox init` sets up hooks for this agent automatically |
| MUST | **`ox doctor` validates hooks** | Doctor checks that hooks are installed and not stale |
| SHOULD | **Native session ID** | `SupportsSession()` + `SessionID()` return the agent's native session/thread identifier (env var or hook stdin) |
| SHOULD | **AGENT_ENV aliases** | `AgentENVAliases()` returns identifiers for hook commands |
| SHOULD | **Prompt submit hook** | `UserPromptSubmit` (or equivalent) event triggers context refresh |
| MAY | **Compact/context-reset hook** | `PreCompact` (or equivalent) event triggers re-priming |

**What users get:** Automatic priming on session start. Hooks fire on lifecycle events. Session recording can be triggered by hooks. `ox doctor` repairs broken hook installations.

**What's missing:** No custom commands/skills, no auto-session capture, no deep session adapter.

---

### Gold: Full Integration

The agent has hooks, auto-session capture, custom commands/skills, and deep session support.

| RFC | Requirement | All of Silver, plus: |
|-----|-------------|----------------------|
| MUST | **Auto-session capture** | Sessions are captured automatically without manual CLI calls (via hooks or equivalent) |
| MUST | **Extension files shipped** | `extensions/<agent>/commands/` (or equivalent) directory with ox command/skill files |
| MUST | **Session commands installed** | Agent has `/ox-session-start`, `/ox-session-stop`, `/ox-session-abort`, `/ox-session-status` (or skill equivalents) |
| MUST | **Full lifecycle coverage** | Events mapped for: Start, End, BeforeTool, AfterTool, Prompt, Stop, Compact |
| SHOULD | **Custom commands or skills** | `Capabilities().CustomCommands == true` with working `CommandManager()`, OR equivalent skill system |
| SHOULD | **Deep session adapter** | Dedicated adapter in `internal/session/adapters/` that reads the agent's native transcript format |
| SHOULD | **MCP support** | `Capabilities().MCPServers == true` (if the agent supports MCP) |
| MAY | **Compact/context-reset hook** | `PreCompact` (or equivalent) event triggers re-priming |

**What users get:** Zero-configuration experience. Sessions auto-start and auto-stop. Custom commands/skills provide agent-native UX for ox features. Full lifecycle telemetry. Context refreshes on compaction.

---

## Tier Advancement Checklist

Moving an agent from one tier to the next:

```
Unsupported -> Bronze:
  MUST:
  [ ] Agent type registered in pkg/agentx/agents/ and pkg/agentx/setup/setup.go
  [ ] Auto-detection works (env vars, config dirs, CLI heuristics)
  [ ] AGENT_ENV=<slug> support in Detect()
  [ ] ox agent prime delivers team context and project guidance
  [ ] Prime marker injected into agent's context file (CLAUDE.md, AGENTS.md, etc.)
  [ ] Team context delivery works (SOUL.md, TEAMS.md, docs/)
  [ ] MEMORY.md read/write works for cross-session persistence
  [ ] Session recording works end-to-end (start -> capture -> stop -> upload)
  SHOULD:
  [ ] At least one doctor check validates the integration
  [ ] Session adapter mapped in internal/session/adapters/adapter.go
  [ ] IsInstalled() and DetectVersion() implemented

Bronze -> Silver:
  MUST:
  [ ] Agent supports hooks (check upstream docs)
  [ ] Implement HookManager for the agent
  [ ] Implement LifecycleEventMapper with EventPhases()
  [ ] Session start/end events mapped
  [ ] Tool use events (pre/post) mapped
  [ ] Hook installation via ox init
  [ ] Hook validation via ox doctor
  [ ] Test hook install/uninstall/validate cycle
  SHOULD:
  [ ] Native session ID available (env var or hook stdin JSON)
  [ ] AGENT_ENV aliases (AgentENVAliases())
  [ ] Prompt submit hook mapped

Silver -> Gold:
  MUST:
  [ ] Auto-session capture wired (sessions start/stop without manual CLI calls)
  [ ] Extension files created in extensions/<agent>/
  [ ] Session commands installed (start, stop, abort, status)
  [ ] All 7 canonical lifecycle phases mapped
  [ ] Test end-to-end: init -> prime -> session start -> work -> session stop -> upload
  SHOULD:
  [ ] Agent supports custom commands or skills (check upstream docs)
  [ ] CommandManager implemented (or equivalent)
  [ ] Deep session adapter (or verify generic adapter is sufficient)
  [ ] MCP server integration (if agent supports MCP)
```

## Tier Blockers (Upstream Limitations)

Some agents cannot advance tiers because they lack upstream support:

| Blocker | Affected Agents | Status |
|---------|----------------|--------|
| No hook system at all | Codex | [Hooks PR rejected](https://github.com/openai/codex/pull/9796) by OpenAI |
| No custom commands | Windsurf, Cline | May change as agents evolve |
| No MCP support | Copilot (cloud-based) | Copilot has MCP in IDE, not in coding agent mode |
| Read-only context file | Agents that only read but don't support injection | Varies |

When an agent gains upstream support, re-evaluate its tier ceiling.

## Implementation References

| Component | Location |
|-----------|----------|
| Agent interface | `pkg/agentx/agent.go` |
| Agent implementations | `pkg/agentx/agents/*.go` |
| Agent registration | `pkg/agentx/setup/setup.go` |
| Hook events (all agents) | `pkg/agentx/agent.go:274-419` |
| Session adapters | `internal/session/adapters/` |
| Extension commands | `extensions/<agent>/commands/` |
| Doctor checks | `cmd/ox/doctor_agent.go`, `cmd/ox/doctor_integration.go` |
| Prime logic | `cmd/ox/agent_prime.go` |
| Session commands | `cmd/ox/agent_session.go` |
