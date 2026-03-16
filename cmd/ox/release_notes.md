# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.5.1] - 2026-03-16

### Added

- `ox agent session abort <session-name> --force` aborts orphaned, ghost, or stale sessions by name with partial name resolution

### Changed

- Faster code search and indexing via buffer reuse, optimized parsing, and in-memory blob caching
- Daemon notification deduplication is now O(1) instead of O(n)
- LFS upload/download reuses a shared HTTP client for connection pooling

### Fixed

- Session recording ParentPID now tracks the long-lived agent process instead of the transient hook process, preventing sessions from appearing as orphans immediately after startup
- Hook safety-net recording call no longer fails with "path cannot be empty" after prime subprocess completes
- `ox logout --force` now correctly skips confirmation prompt for scripted/non-interactive use
- `ox status` always shows ledger provisioning status, even when ledger isn't configured locally
- JWT exchange errors during authentication handled more securely with cleaner error messages
- Stale Personal Access Tokens automatically removed from git remote URLs on logout
- Race condition in `ox doctor` git connectivity check fixed (used `context.WithTimeout` instead of manual goroutine)

## [0.5.0] - 2026-03-15

### Added

**Session anti-entropy**
- Daemon automatically detects and recovers interrupted sessions with quality scoring
- Progressive disclosure hints guide coworkers toward session health actions

**Incremental session recording**
- Sessions record incrementally via hooks with unified artifacts
- Session lifecycle consolidated into a canonical state machine for reliability
- Timing metrics and async upload via daemon

**Session maintenance commands**
- `ox session remove` deletes sessions from the ledger
- `/ox-session-review` skill with auto-fix for stale commands

**GitHub PR/issue sync**
- Daemon automatically syncs GitHub PRs and issues into the local code search index
- GitHub token fallback for environments without explicit configuration

**Expert coworker agents**
- `ox coworker list` and `ox coworker load <name>` surface specialized agents (go-pro, code-reviewer, test-architect, etc.) directly in prime context

**Distillation**
- Local pipeline distills session observations into persistent team memory via `memory/GUIDE.md`
- Local pipeline distills team discussions into structured facts with file-based output
- Per-day bucketing, UUID7 filenames, content-based timestamps

**Team context change notifications**
- Daemon notifies when team context updates arrive from remote

**Code insights agent detection**
- `ox code insights` auto-detects agent context and returns JSON output with prime hints


### Changed

- `ox agent prime` and session commands switch to Claude recommended XML output format
- **One daemon per repo** — Daemon identity tied to `repo_id` for isolation across projects
- **Daemon self-restart** — Daemon automatically restarts on version mismatch
- **go-git v6** — CodeDB upgraded from go-git v5 to v6 with comprehensive regression tests
- **Hooks in shared settings** — ox hooks now install to `.claude/settings.json` instead of per-project
- **Agent parent PID tracking** — Instant liveness detection via parent process
- **Parallel team context sync** — Faster sync with parallel fetches and improved health display
- **External packages** — frictionax and agentx migrated to standalone packages
- **Deprecated events.jsonl removed** — Session artifacts simplified

### Fixed

- Auto-repair missing LFS pointers that block ledger push
- Session recovery writes atomically to prevent corrupted raw.jsonl
- Live PIDs never incorrectly considered stale
- Ghost session classification accuracy improved
- Non-blocking search indexing status checks prevent daemon stalls
- Team context search actually executes (was silently skipped due to stale source check)
- Wrong team context selection in multi-team repos prevented
- CodeDB moved to `.sageox/cache/` (out of ledger root)
- IPC timeouts increased for daemon status queries and heartbeat detection
- Agent list works correctly across worktrees
- Legacy cache paths scanned and updated for current layout
- UTC normalization for time comparisons fixes daemon status contradictions
- Bulk cleanup of stale empty recording stubs
- Daemon GC lock acquisition distinguishes lock-exists from other errors
- Hook command made reachable from dispatcher
- CodeDB bypasses go-git extension rejection for repos with unsupported extensions

[0.5.0]: https://github.com/sageox/ox/releases/tag/v0.5.0

## [0.4.1] - 2026-03-12

### Fixed

**`ox session list` no longer silently returns empty**
- Shows which repo was searched when no sessions are found (name + repo ID)
- Tells you when the ledger is unavailable and suggests `ox doctor --fix`
- Shows current directory when run outside a SageOx project
- Debug logging (`-v`) now surfaces why the ledger was skipped

### Added

**`ox session list --json`**
- Structured JSON output for AI coworkers, including `repo_name`, `repo_id`, and `ledger_available`

[0.4.1]: https://github.com/sageox/ox/releases/tag/v0.4.1

## [0.4.0] - 2026-03-09

### Added

**Local code search (CodeDB)**
- Agents can search your codebase locally via a built-in code search engine
- Integrated with the daemon for background indexing and worktree support
- Compact inline results surfaced in `ox status`
- [See how CodeDB came together in just a few days](https://www.youtube.com/watch?v=ODMZyEU3Bz8)

**`ox query` command**
- New top-level command for querying team knowledge directly from the CLI

### Changed
- Daemon preserves uncommitted changes during blue-green GC reclone
- Daemon logs colorized with semantic colors and compact timestamps

### Fixed
- LFS stub files correctly detected during session recording
- Agent-specific recording state prevents cross-agent interference in multi-agent scenarios

[0.4.0]: https://github.com/sageox/ox/releases/tag/v0.4.0

## [0.3.0] - 2026-03-06

### Added

**Semantic search**
- Agents can search over team knowledge via the CLI

**Document import (`ox import`)**
- Import documents into team context
- `--team` flag for explicit team targeting

**Session improvements**
- `ox session regenerate` to re-generate session summaries on demand
- Multi-session status with inflight recording detection
- Workspace path and branch shown in session status
- Redesigned HTML viewer with narrative timeline and semantic phases

**Improvements**
- Various prime improvements to enable better discovery of context
- Sync reliability improvements
- Sync staleness detection and warnings
- All team contexts surfaced to agents with slug-based lookup
- Doctor warnings made actionable for non-technical users
- Agent support tiers and scorecard specs
- Daemon status redesigned with actionable CTAs
- Consolidated environment variables for config overrides
- User-defined REDACT.md rules for filtering sensitive content from sessions
- Metadata improvements and sandbox safety fixes
- Initial work towards supporting Codex

### Fixed
- Codex integration silently absorbing errors and creating empty session files
- Squash merge stomping that lost changes
- Doctor false warnings after fresh `ox init`
- Sparse checkout: `--sparse` on all git add calls, `--autostash` on pulls
- Stale cache paths not rewritten to ledger after prune
- Session start after clear + abort lifecycle edge cases
- RecordFlush cooldown reset on empty buffers
- Duplicate repo detection during `ox init`
- Doctor/status output improved when run outside a git repo
- Daemon startup visibility and performance
- File I/O hardening, clone recovery, and credential safety

[0.3.0]: https://github.com/sageox/ox/releases/tag/v0.3.0

## [0.2.0] - 2026-02-24

### Added

**Redesigned `ox doctor` with timeline TUI**
- Visual timeline showing check progress and results
- Auto-sync ledger health checks detect drift before it causes problems
- Doctor recovery options for common failure modes

**Version update notifications**
- `ox status` and `ox agent prime` notify when a newer release is available
- Update check runs via daemon cache — no extra network calls in the CLI hot path

**Smarter AI coworker context**
- `ox agent prime` now includes user and agent tips for better session guidance
- Intent-to-command guidance field helps coworkers discover the right `ox` command
- Team docs progressive disclosure — coworkers get relevant team context without flooding their context window
- Team instruction files emitted directly into agent context

**Session abort command**
- `ox session abort` discards a session without uploading, useful for throwaway explorations

**Orchestrator detection**
- Detects orchestration layers (e.g., multi-agent setups) via `X-Orchestrator` header
- Improved Amp agent detection accuracy

**Cleaner status output**
- `.sageox/` symlink paths shown as short relative paths instead of full XDG paths
- Repo-specific team context highlighted across `ox` commands

### Changed
- Ledger checkout moved to user data directory (XDG-compliant, keeps repo clean)
- Session HTML compacted — tool calls are collapsed, duration/tool-count noise removed
- Git safety primitives extracted into `internal/gitutil` for reuse
- Daemon sync uses ls-remote pre-check and exponential backoff for resilience
- Better agent ID error messages with diagnostic guidance
- `ox init` now shows `ox sync` as step 2 in next-steps output

### Fixed
- Ghost sessions no longer appear after onboarding
- Session summaries now generated from push-summary for accuracy
- Tool noise filtered from session summarization
- Project-level hook settings checked correctly during install detection
- Team context discoverable without waiting for daemon sync
- Stale PAT in git remote URLs fixed on login/logout
- Daemon config cache no longer clobbers ledger path
- System-injected content classified correctly in raw session data
- Fresh checkout failures in `ox doctor` resolved
- Credential token refresh separated from team discovery in daemon
- Cloud Code project hash uses dashes instead of underscores

[0.2.0]: https://github.com/sageox/ox/releases/tag/v0.2.0

## [0.1.1] - 2026-02-19

### Added
- Pre-built binaries for 6 platforms (curl one-liner install)
- Ed25519 artifact signing

### Changed
- Daemon liveness uses socket-ping instead of flock
- All API calls are endpoint-aware

### Fixed
- `ox sync` now surfaces daemon errors instead of silent success (#9)
- `ox status` crash on empty ledger repos
- `ox doctor --fix` discovers uncloned team contexts
- Git credentials masked in error output

## [0.1.0] - 2026-02-18

Initial public release of the SageOx CLI (`ox`).

### Highlights

- **Session recording**: Capture, view, and export human-AI coding sessions with HTML and Markdown output
- **Team discussion**: Record and transcribe team conversations so arch decisions and product context flows automatically to agents
- **Background daemon**: Automatic git sync for ledgers and team contexts with self-healing clone recovery

[0.1.1]: https://github.com/sageox/ox/releases/tag/v0.1.1
[0.1.0]: https://github.com/sageox/ox/releases/tag/v0.1.0
