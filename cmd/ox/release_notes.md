# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.6.1] - 2026-04-02

### Fixed
- Session push failures no longer cascade-block LFS uploads or destroy cached session data
- Daemon anti-entropy now correctly recovers fully-finalized and raw-only cache sessions
- Auth no longer crashes when distilling memory with a nil token

[0.6.1]: https://github.com/sageox/ox/releases/tag/v0.6.1

## [0.6.0] - 2026-03-30

### Added

**Murmur & whisper — team communication for AI coworkers**
- AI coworkers can now publish work-in-progress updates to teammates via `ox murmur`
- Whisper delivery via `UserPromptSubmit` hook and active pull keeps coworkers in sync
- User-level config for pause/resume control, nudge tracking, and whisper budgets
- Daemon handles file writes and commits via IPC, keeping the CLI stateless
- `ox murmur list` shows recent murmurs; `ox murmur status` shows delivery state

**Pure-Go tree-sitter symbol extraction**
- Code search now extracts symbols (functions, classes, types) using a pure-Go tree-sitter implementation
- No CGo dependency — works everywhere ox builds

**New commands**
- `ox upgrade` — self-update with daemon whisper broadcast to notify active coworkers
- `ox teams` — discover and list your teams from the CLI
- `ox glance` — session-based team activity feed with file contention detection

**Import improvements**
- Audio and video MIME type detection for `ox import`
- URL-based video import with progress tracking and `ox import list`

**Distillation pipeline**
- Per-stage guidance files with progressive disclosure
- Unified JSONL fact schema across all fact sources
- GitHub activity assembled into event clusters for alignment feed
- Session summary facts extracted into the distill pipeline

**Infrastructure**
- sqlc typed SQL for whisper and codedb stores
- Self-healing rebase pipeline with manifest-driven conflict resolution rules
- Self-healing for codedb infrastructure failures (daemon auto-recovers corrupted indexes)
- PAT liveness validation in `ox doctor` and `ox status`
- DB maintenance scheduler and whisper resilience in daemon
- Session `--summary` flag for `ox session regenerate`

### Changed

- 5.5x faster code search indexing; symbol index build time reduced by 90%
- Agent selector replaces boolean config: choose `auto`, `none`, `claude`, or `codex`
- Default sync intervals adjusted: 60s ledger, 15s team context
- Resummary uses local daemon instead of server-side API
- Notifications consolidated into whisper pipeline with stdout XML delivery
- Shared `PushWithRetry` primitive and `pkg/sessionsummary` for cross-repo use
- Structural cleanup: god files split, IPC service interface extracted, legacy code removed
- Visual progressive disclosure for video discussions
- Keyframe content types aligned with server vision pipeline
- Codecov Test Analytics added to scheduled coverage workflow

### Fixed

- **Session recording reliability**: pre-start leak, cross-env cache path split, decoupled from auth, token refresh, `files_changed` populated in summary.json, concurrent agent URL disambiguation, `StartOffset` capture on session start, stop marker no longer leaks into user repository, process tree walk captures correct agent PID instead of transient bash PID
- **Auth resilience**: capture `refresh_token` from JWT exchange, handle missing refresh tokens, auto-repair revoked PATs, login no longer blocks on token refresh failure
- **CodeDB stability**: prevent CLI hang when daemon is indexing, detect and report empty index, fast fail when worktree disappears, prevent projectRoot oscillation across worktrees, break perpetual indexing loop from freshness race and bleve lock timeout, skip indexing when ledger not yet cloned
- **Ledger sparse-checkout**: sparse-checkout init no longer wipes codedb cache on sync, `.sageox` added to sparse-checkout cone, staged files protected from `sparse-checkout set`
- **Data safety**: LFS data loss prevention on push failure, dead force-push code path removed
- Doctor handles push 403 errors, local remote credential injection, and uses `version.Full()` for daemon version comparison
- Daemon uses registry-aware IPC client everywhere; CWD inheritance bug fixed
- Daemon log entries now include PID and project path in sync warnings
- Endpoint normalizer prepends `https://` to bare hostnames
- GitHub sync rebuilds state from disk to prevent cold-start hang; PR commits preserved on replay
- System credential helpers suppressed during PAT liveness probe
- Stale daemons killed before starting new ones to prevent orphan accumulation
- Session abort search and stale agent ID resolution
- Default to auto-record for ox-initialized repos
- Friction telemetry re-queues events on flush failure (frictionax v0.1.2)

[0.6.0]: https://github.com/sageox/ox/releases/tag/v0.6.0

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
