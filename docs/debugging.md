<!-- doc-audience: human -->
# Debugging Guide

Quick reference for troubleshooting ox. Scan the section you need.

---

## Essential Files

### Project Level (`.sageox/`)

| File | What It Is |
|------|-----------|
| `config.json` | Project identity: endpoint, repo_id, team_id, workspace_id. **The** initialization marker. |
| `config.local.toml` | Machine-specific paths: ledger path, team context paths, last sync times. NOT committed. |
| `.repo_*` | Endpoint discovery markers (legacy fallback when config.json endpoint is empty). |
| `cache/health.json` | When `ox doctor` last ran. Staleness triggers hints. |
| `sessions/<name>/` | Session recordings: `raw.jsonl`, `meta.json`, `summary.md`, `session.html`. |
| `agent_instances/<user>/agent_instances.jsonl` | Agent identity registry (agent IDs, models, prime counts). |

### User Level (`~/.config/sageox/`)

| File | What It Is |
|------|-----------|
| `config.yaml` | User preferences: view_format, sessions mode, telemetry, attribution. |
| `auth.json` | Auth tokens keyed by normalized endpoint. **Permissions must be 0600.** |
| `git-credentials.json` | Git tokens for ledger/team-context repos. Fallback when keychain unavailable. **0600.** |

### Sibling Directory (`<repo>_sageox/`)

| Path | What It Is |
|------|-----------|
| `<endpoint>/ledger/` | The ledger git repo. Sessions live at `sessions/<name>/meta.json`. |
| `<endpoint>/teams/<team-id>/` | Symlinks to `~/.local/share/sageox/<endpoint>/teams/<team-id>/`. |

### Daemon Runtime (`$XDG_RUNTIME_DIR/sageox/` or `~/.local/state/sageox/`)

| File | What It Is |
|------|-----------|
| `daemon-<workspace>.sock` | IPC socket (CLI talks to daemon here). |
| `daemon-<workspace>.pid` | Daemon process ID. |
| `daemon-<workspace>.lock` | Prevents concurrent daemon instances. |
| `daemon/registry.json` | All active daemons across workspaces. |

### Daemon Logs (`$XDG_CACHE_HOME/sageox/daemon/`)

| File | What It Is |
|------|-----------|
| `daemon-<workspace>.log` | Structured daemon logs. View with `ox daemon logs --follow`. |

---

## Debugging State & Sync

### Quick Health Check

```bash
ox doctor              # priority-first summary (issues only)
ox doctor -v           # all checks with timing
ox doctor --fix        # auto-repair what it can
ox doctor --json       # structured output for scripting
```

### System State

```bash
ox status              # auth, project, ledger, team contexts, daemon
ox status --json       # machine-readable
```

### Daemon

```bash
ox daemon status -v    # health, sync history, sparkline
ox daemon logs --follow  # real-time log tail
ox daemon list         # all running daemons
ox daemon restart      # nuclear option
```

### Common Sync Issues

| Symptom | Check | Fix |
|---------|-------|-----|
| "ledger not found" | `ox doctor -v` (Ledger Git Health) | `ox doctor --fix` or wait for daemon to clone |
| Session not in web viewer | `ls <ledger>/sessions/<name>/meta.json` | `ox session stop` to push, or `ox doctor --fix` |
| Team context stale | `ox status` (team context last sync) | `ox daemon restart` |
| Daemon not syncing | `ox daemon logs --follow` | Check auth: `ox doctor --fix-slug auth` |
| "another checkout in progress" | `ox daemon status -v` | Wait or `ox daemon restart` |

### Manual Inspection

```bash
# Ledger location
cat .sageox/config.local.toml | grep path

# Auth tokens
cat ~/.config/sageox/auth.json | jq 'keys'

# Daemon socket alive?
ls -la $XDG_RUNTIME_DIR/sageox/daemon-*.sock 2>/dev/null || \
  ls -la ~/.local/state/sageox/daemon-*.sock 2>/dev/null

# Active daemons
cat $XDG_RUNTIME_DIR/sageox/daemon/registry.json 2>/dev/null || \
  cat ~/.local/state/sageox/daemon/registry.json 2>/dev/null
```

### Environment Variables

| Variable | Purpose |
|----------|---------|
| `SAGEOX_ENDPOINT` | Override endpoint URL |
| `OX_XDG_DISABLE=1` | Legacy path mode (`~/.sageox/` instead of XDG) |
| `OX_PROJECT_ROOT` | Override project root discovery (for CI/devroot) |
| `OX_SESSION_RECORDING` | Override session recording mode (`auto`/`manual`/`disabled`) |
| `OX_USER_CONFIG` | Load user config from explicit file path |

---

## Prompt Injection (Critical for Product)

ox operates by injecting English instructions into files that AI agents read. If these are missing or broken, the product doesn't work.

### 1. Prime Markers (Self-Bootstrap)

Two HTML comment markers ensure agents discover and run `ox agent prime`:

**Header** (line 1 of AGENTS.md or CLAUDE.md):
```
<!-- ox:prime-check -->
**SageOx**: Before your first response, verify `agent_id: Ox____` appears
in this conversation. If not, run `ox agent prime` now.
```

**Footer** (end of AGENTS.md or CLAUDE.md):
```
<!-- ox:prime --> Run SageOx `ox agent prime` on session start, after compaction, or clear. (DO NOT REMOVE)
```

Also injected into `~/.claude/CLAUDE.md` for user-level coverage.

**Verify:** `grep "ox:prime" CLAUDE.md .sageox/AGENTS.md ~/.claude/CLAUDE.md`

### 2. Claude Code Hooks (Auto-Trigger)

Installed in `~/.claude/settings.json` under `hooks.SessionStart` and `hooks.PreCompact`:

```bash
# Runs automatically on: startup, resume, /clear, compact
if command -v ox >/dev/null 2>&1; then
  AGENT_ENV=claude-code ox agent prime [--idempotent] 2>&1 || true
else
  echo 'This repo uses SageOx: install https://github.com/sageox/ox ...'
fi
```

- `startup`/`resume` use `--idempotent` (skip if already primed)
- `clear`/`compact`/`PreCompact` use force mode (always re-prime)

**Verify:** `cat ~/.claude/settings.json | jq '.hooks'`

### 3. Session Recording Guidance

When `ox agent <id> session start` runs, structured JSON guidance is injected into agent context telling the agent what to include/exclude from recordings:

- **Include:** user inputs, key progress, commands, decisions, errors that changed approach
- **Exclude:** verbose logs, failed retries, raw tool JSON, unchanged file contents
- **Reminder interval:** every 50 entries, agent is reminded to run `ox agent <id> session remind`

### 4. Team Context (AGENTS.md)

When team context is configured, `ox agent prime` tells the agent:

```
## MANDATORY: Read Team AGENTS.md
Your team has shared guidance that MUST be loaded before any work:
  READ NOW: <path-to-team-AGENTS.md>
```

This loads team norms, conventions, and architectural decisions.

### Debugging Prompt Injection

| Problem | Check |
|---------|-------|
| Agent never runs `ox agent prime` | `grep "ox:prime" CLAUDE.md` -- markers missing? |
| Agent primes but ignores team context | `ox doctor -v` -- team context check |
| Hooks not firing | `cat ~/.claude/settings.json \| jq '.hooks'` |
| Session not recording | Check if `ox agent <id> session start` was called |
| Agent doesn't know its agent ID | Check `/tmp/sageox/sessions/*.json` for session marker |
