# Agent Integration Tests

These tests verify that coding agents properly understand and use ox CLI features
after running `ox agent prime`. The framework is designed to support multiple
agents, though currently only Claude Code is implemented.

## Hard Rule: Real Agent Instances Only

**E2E / integration tests MUST use real agent CLI instances (e.g., real `claude`
binary). Simulated entries, mock agents, or fake JSONL files are NOT permitted
in this directory.** The entire purpose of these tests is to catch regressions
in the real agent-to-ox integration: JSONL format changes, hook stdin format
changes, session file path changes, etc. Simulated data cannot catch these.

If you need component-level tests that exercise ox internals with synthetic data,
put them in `cmd/ox/` under the `slow` build tag — not here.

## Supported Agents

| Agent | Status | CLI | Notes |
|-------|--------|-----|-------|
| Claude Code | Implemented | `claude` | Primary supported agent |
| OpenCode | Planned | `opencode` | Open source coding agent |
| Codex | Planned | `codex` | OpenAI Codex CLI |

Note: We focus on legitimate CLI-based coding agents that can run in terminal environments.

## Prerequisites

1. **Agent CLI**: The agent's CLI must be installed and authenticated
2. **Go**: For running the tests
3. **ox CLI**: Built from this repository (tests build it automatically)

## Running Tests

```bash
# Run all agent integration tests (slow, requires agent CLI)
go test -v -tags=integration ./tests/integration/agents/...

# Run Claude-specific tests only
go test -v -tags=integration -timeout 5m ./tests/integration/agents/claude/...

# Skip if agent CLI not available (graceful degradation)
go test -v -tags=integration -short ./tests/integration/agents/...

# Debug mode - see full agent output
AGENT_TEST_DEBUG=1 go test -v -tags=integration ./tests/integration/agents/...
```

## Test Architecture

### Environment Isolation

Tests use isolated environments to avoid affecting user configuration:
- Custom `XDG_*` environment variables point to test fixtures
- `OX_OFFLINE=1` prevents cloud API calls
- Pre-cached guidance responses in fixtures

### Test Fixtures

```
fixtures/
  mock-project/           # Simulated project directory
    .sageox/
      local.json          # Config pointing to mock team context
      cache/guidance/     # Pre-cached guidance
    .git/                 # Git repo (required for ox detection)
    AGENTS.md             # Project-level agents file
  mock-ledger/            # Simulated ledger git repo
  mock-team-context/      # Simulated team context
    coworkers/ai/claude/
      CLAUDE.md           # Team Claude config
      AGENTS.md           # Team agents index
      agents/             # Team subagent definitions
      commands/           # Team slash commands
```

### Test Harness

The harness (`harness.go`):
1. Builds ox CLI to a temp directory
2. Sets up isolated environment
3. Runs Claude with structured prompts
4. Parses JSON responses from Claude
5. Verifies understanding of ox features

### What We Test

1. **Prime Output Understanding**
   - Claude knows about key ox commands
   - Claude understands attribution requirements
   - Claude sees team context and subagents

2. **Guidance System Usage**
   - Claude fetches guidance for relevant paths
   - Claude uses progressive disclosure (fetches deeper guidance when needed)

## Adding a New Coding Agent

To add support for a new coding agent, you need both an adapter (Go code) and
E2E test coverage. A coding agent is only considered **supported** when all
required E2E tests pass.

### 1. Implement the Adapter

Create a new adapter in `internal/session/adapters/` implementing the `Adapter`
interface (6 methods). Register it in `init()` and add alias mappings. See
`claude_code.go` or `codex.go` for reference implementations.

### 2. Create Integration Test Directory

```text
tests/integration/agents/<agent-name>/
  prime_test.go               # Prime output understanding
  incremental_recording_test.go  # Session recording pipeline
```

### 3. Required E2E Tests (Minimum for "Supported" Status)

Every supported agent MUST have passing E2E tests for each category below.
Tests use the real agent CLI binary — no mocks.

#### Session Recording Pipeline

| Test | What It Verifies |
|------|-----------------|
| `TestIncrementalRecording_PostToolUse` | PostToolUse hooks write entries to raw.jsonl during the session (not just at stop time). Verifies incremental recording works. |
| `TestIncrementalRecording_ContinueSession` | Recording survives a session resume (`--continue`). Entries accumulate across invocations. |
| `TestIncrementalRecording_CompactHook` | PreCompact hook re-primes without losing recording state. Entries survive compaction. |
| `TestIncrementalRecording_NoToolUse` | Sessions without tool use still produce valid raw.jsonl via the stop-time batch drain. |

#### Content Fidelity

| Test | What It Verifies |
|------|-----------------|
| `user_prompt_captured` (subtest) | The actual user prompt text appears in `user`-type entries in raw.jsonl. |
| `tool_calls_tagged` (subtest) | Tool entries have `tool_name` populated (tool call metadata is preserved). |

#### Slash Commands (ox CLI Commands)

| Test | What It Verifies | Underlying Command |
|------|------------------|--------------------|
| `TestSlashCommand_SessionStartStop` | `/ox-session-start` creates recording state; `/ox-session-stop` finalizes raw.jsonl with entries. | `ox agent session start` / `ox agent <id> session stop` |
| `TestSlashCommand_SessionList` | `/ox-session-list` runs without crashing, both via direct CLI and through the agent. | `ox session list --limit 5` |
| `TestSlashCommand_SessionAbort` | `/ox-session-abort` discards session data and clears recording state. | `ox agent <id> session abort --force` |

#### Prime & Context Discovery

| Test | What It Verifies |
|------|-----------------|
| `TestHookExecution` | `ox agent prime` runs via the agent's session-start hook mechanism. |
| `TestPrimeDiscovery` | Agent discovers ox commands from prime output, AGENTS.md, or team context. |

### 4. Update Support Matrix

Update the table at the top of this README when the agent reaches "Implemented" status.

### 5. Key Constraints

- **Hooks must exist before agent launch**: `ox init` installs hooks into
  `.claude/settings.local.json`. Claude Code caches hook config at startup,
  so hooks must be on disk before the agent starts. The test fixture lacks
  this file, so tests run `runOxPrime()` first (which auto-installs hooks
  if missing via `tryAutoInstallClaudeHooks()`).
- **Multiple agent IDs**: Prime, SessionStart hook, and AGENTS.md prime may each
  create separate agent instances. Tests should use `findActiveAgentID()` to
  discover the right one.
- **Isolated environments**: Tests must use `SetupTestEnvironment()` for XDG
  isolation. Never touch real user config.

## Adding New Tests

1. Add test fixtures if needed
2. Create test function with `//go:build integration` tag
3. Use `env.RunAgentPrompt()` from harness
4. Parse response and assert expectations

## Debugging

Set `AGENT_TEST_DEBUG=1` to see the agent's full output:

```bash
AGENT_TEST_DEBUG=1 go test -v -tags=integration ./tests/integration/agents/...
```
