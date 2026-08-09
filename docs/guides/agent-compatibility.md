# AI Coworker Compatibility

ox works with multiple AI coding agents. Support depth varies by agent — here's what works where.

## Support Tiers

| Tier | What It Means |
|------|---------------|
| **Gold** | Full feature parity. Real-time session recording, hooks on every lifecycle phase, whisper push, anti-entropy recovery. Tested in CI. |
| **Silver** | Core features work. Hooks fire, context primes correctly, sessions record. Tested weekly. |
| **Bronze** | Context injection works and sessions record. Limited or no lifecycle hooks. |
| **Marker only** | ox writes an `ox agent prime` marker into the agent's instruction file. No adapter, so no session recording. |

## Feature Availability

| Feature | Claude Code | Codex | Gemini | Droid | OpenCode | Amp | Pi | Aider | Goose |
|---------|:-:|:-:|:-:|:-:|:-:|:-:|:-:|:-:|:-:|
| Tier | Gold | Silver | Silver | Silver | Bronze | Bronze | Bronze | Bronze | Silver |
| Context prime | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Session recording | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅* | ✅ | ✅ |
| Auto-prime at session start | ✅ | ✅ | — | — | ✅ | — | — | — | ✅ |
| Lifecycle hooks | ✅ | ✅ | ✅ | ✅ | plugin | plugin | — | — | ✅ |
| Whisper push | ✅ | ✅ | fallback | fallback | — | — | — | — | ✅ |
| Team rules install | ✅ | — | — | ✅ | — | — | — | — | — |
| Skills / commands install | ✅ | — | — | — | — | — | — | — | — |
| Anti-entropy recovery | ✅ | — | — | — | — | — | — | — | — |

**Auto-prime** means a hook or plugin runs `ox agent prime` for you. Where it's absent,
priming relies on the agent obeying the blocking marker in its instruction file.

**Whisper push** is delivered on the `UserPromptSubmit` hook. Agents marked *fallback*
only install tool and stop hooks, so whispers arrive on the next tool call rather than
the next prompt. Every agent can pull explicitly with `ox agent <id> whisper`.

\* Pi session recording reads transcript format version 3 only — see
[Known Limitations](#known-limitations) below for the v4 caveat.

## Where each agent's sessions come from

| Agent | Session store |
|---|---|
| Claude Code | `~/.claude/projects/<hash>/<uuid>.jsonl` |
| Codex | `~/.codex/sessions/YYYY/MM/DD/*.jsonl` |
| Gemini | `~/.gemini/tmp/<hash>/session-*.json` |
| Droid | `~/.factory/projects/<slug>/<uuid>.jsonl` |
| OpenCode | `~/.local/share/opencode/opencode.db` (SQLite) |
| Amp | `~/.cache/amp/ox-sessions/<thread>.jsonl` (written by the ox plugin) |
| Pi | `~/.pi/agent/sessions/--<mangled-cwd>--/<timestamp>_<uuid>.jsonl` |
| Aider | `.aider.chat.history.md` |
| Goose | `~/.local/share/goose/sessions/sessions.db` (SQLite) |

## Quick Start by Agent

### Claude Code (Gold)
```bash
ox init        # sets up everything automatically
ox doctor      # verifies hooks + context injection
```
No extra setup needed — Claude Code is the reference implementation.

### Codex
```bash
ox init
ox integrate install --codex
codex features enable codex_hooks  # enable hook support in Codex
```

### Gemini CLI
```bash
ox init
ox integrate install --gemini
```

### Droid (Factory)
```bash
ox init
ox adapter install droid
```
Installs hooks into `.factory/settings.json` and team rules into `.factory/rules/`.

### OpenCode
```bash
ox init
ox integrate install --opencode
```
Installs a TypeScript plugin that runs `ox agent prime` on `session.created`.
OpenCode exposes 27+ plugin events; ox uses only that one today.

### Amp
```bash
ox init
ox integrate install --amp
```
Installs a user-scope plugin at `~/.config/amp/plugins/ox-bridge.ts` that records
the session, and an `AGENTS.md` marker that primes it. Amp does not persist
transcripts itself, so the plugin is what makes recording possible.

### Pi
```bash
ox init
ox integrate install --pi
```
Pi reads `AGENTS.md`, `CLAUDE.md`, and `SYSTEM.md` natively. Install or upgrade with
`npm install -g @earendil-works/pi-coding-agent` — the old `@mariozechner/pi-coding-agent`
package is deprecated (frozen at 0.73.1). The binary is still `pi` and the config
directory is still `~/.pi`. See [Known Limitations](#known-limitations) for the
transcript-format caveat.

### Aider
```bash
ox init
ox adapter install aider
```
Aider loads `CONVENTIONS.md` via the `--read` flag in `.aider.conf.yml`, so the
marker goes there rather than in `AGENTS.md`.

### Goose
```bash
ox init
ox adapter install goose
```
Installs an [Open Plugins](https://open-plugins.com/agent-builders/components/hooks)
plugin directory at `.agents/plugins/sageox/` — a `plugin.json` manifest plus
`hooks/hooks.json`. Goose ignores a plugin directory with no manifest, so both
files are required.

ox installs seven of Goose's eleven hook events. The four it skips
(`BeforeReadFile`, `AfterFileEdit`, `BeforeShellExecution`, `AfterShellExecution`)
are each a strict subset of `PreToolUse`/`PostToolUse` — reading a file and
running a shell command are both tool calls — so installing them would spawn
`ox agent hook` twice per tool call for no signal ox does not already have.
`PostToolUseFailure` **is** installed: Goose fires `PostToolUse` only on success,
so without it a failed turn stays invisible until the next success or `Stop`.

Goose also loads `AGENTS.md` before `.goosehints`, hierarchically from the working
directory up to the repo root, so context primes even before hooks are installed.

## Known Limitations

- **Goose**: has **no compaction event**, so team context primed at session start
  is lost when Goose compacts. ox cannot work around this. Goose also sends
  `working_dir` only on *tool* events, so the project-scope hook command pins
  `OX_PROJECT_ROOT` to the absolute repo root rather than relying on the cwd walk.
- **Gemini, Droid**: no `SessionStart` hook, so priming depends on the instruction
  file marker rather than firing automatically.
- **Codex**: no `SessionEnd` hook, so sessions do not auto-finalize; they close on
  the next `ox agent <id> session stop` or daemon sweep.
- **Pi**: instruction-file marker only, no lifecycle hooks. Session recording also only
  understands transcript format version 3. Pi 0.84+ introduced a v4 lane-based session
  model that ox does not yet parse — `ox doctor` reports `pi:format-unsupported` when it
  sees a newer format, and those sessions record as empty until the reader is updated.
- **Aider**: instruction-file marker only. No lifecycle hooks.
- **Cursor, Windsurf, Cline, Copilot, Kiro**: marker only. ox writes the prime
  marker into `.cursorrules`, `.windsurfrules`, `.clinerules`,
  `.github/copilot-instructions.md`, and `.kiro/steering/ox.md` respectively.

## Version Requirements

All agents should be at their latest version for best compatibility. Run `ox doctor`
to check for version issues.

## Detailed Matrix

Per-capability data, including which surfaces resolve on which agent, lives in
[docs/specs/agent-support-matrix.md](../specs/agent-support-matrix.md). Test-level
compatibility runs live in the private `sageox/ox-test-harness` repo.
