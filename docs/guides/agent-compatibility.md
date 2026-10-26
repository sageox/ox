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
| Skills / commands install | ✅ | ✅ | ✅ | — | — | — | — | — | — |
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

**Live activity line.** Alongside the hooks, `ox init` and `ox doctor` set the
`statusLine` in the project's `.claude/settings.json` to `ox statusline`. Claude Code
strips color from hook output, so the status bar is where ox shows, in green, what it
just did for the AI coworker and the newest teammate murmur:
`● ox  posted murmur · whisper from ryan [wip] Refactoring auth… 3m`. It reads local
files only (no network, no daemon). A `statusLine` already in the project file is never
replaced; one in your own `~/.claude/settings.json` keeps working, with ox's line
appended after it. Turn it off with `ox config set statusline off`.

**Usage pane.** `/ox-usage` opens a pane beside the chat with this session's usage
summary (calls, context injected, items surfaced and used) and a scrollable list of every
ox call, refreshed as they happen. It is a Claude Code plugin, `ox-cli-usage-pane`, that
`ox init` and `ox doctor` install at user scope when the `claude` CLI is on PATH (Claude
Code 2.1.309 or newer); the same mechanism ships Oxy (`ox pet`). The pane is a thin view
over `ox usage --json`, so it shows exactly what `/ox-cli-usage` reports. `/ox-usage off`
closes it, `ox config set usage_pane off` removes it, and `ox usage pane status` shows
whether it is installed.

## Canonical Agent Skills

ox authors every portable playbook once in `extensions/skills/`, in the open
Agent Skills `SKILL.md` format. Claude Code receives a managed copy under
`.claude/skills/`; Codex, ChatGPT desktop's Codex surface, and Gemini CLI
receive the same managed skill under `.agents/skills/`. `ox doctor --fix`
reconciles the project-selected native targets, including updates, missing
skills, and retired ox-owned files, while preserving user-authored additions
and modified retired files.

Run `/ox-cli-usage` (Claude Code slash command, or pick it in Codex's skill
picker) to see what ox did in the current session: the ox calls your AI
coworker made, the team context ox injected and from where, the decisions,
rules, murmurs and sessions it surfaced, and which of those the coworker
reports using. `/ox-cli-usage me` rolls up your recent sessions in this repo;
`/ox-cli-usage team` shows team-wide totals only, never per-person reads. The
skill is a thin relay over `ox usage --json`, so other AI coworkers get the same
report by running that command.

### Codex
```bash
ox init
ox integrate install --codex
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
Installs a user-scope plugin, `ox-bridge.ts`, in Amp's system plugin directory
(`$XDG_CONFIG_HOME/amp/plugins/` when `XDG_CONFIG_HOME` is set to an absolute path,
otherwise `~/.config/amp/plugins/`) that records the session, and an `AGENTS.md`
marker that primes it. Amp does not persist transcripts itself, so the plugin is
what makes recording possible. In an Amp orb, the plugin ends a thread's session
before the orb pauses, and when you reply in that thread later it starts a new
session, linked as continued from the first.

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
- **Codex**: sessions auto-finalize through `SessionEnd`, which Codex fires when a
  conversation is archived or deleted, when Codex closes normally, or after 30
  minutes idle with no connected client. A hard kill of the Codex process skips
  the hook; the daemon's dead-process sweep finalizes those recordings instead.
  Project-scope hooks (`.codex/hooks.json`) load only once the project `.codex/`
  layer is trusted via `/hooks`; user-scope `~/.codex/hooks.json` needs no per-repo
  trust. Installs made before `SessionEnd` was added are completed by `ox doctor`.
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
