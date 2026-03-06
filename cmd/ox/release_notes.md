# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
