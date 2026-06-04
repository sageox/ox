# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **`ox plan` — team-context-enriched implementation plans** — turns a plan an AI coworker drafts into one that knows where the team is going. `ox plan` annotates each part of a plan with deterministic, locally-computed signals (zero model tokens): **collisions** (a file you're about to touch is in a teammate's open PR — or one they murmured about minutes ago, before any commit exists), **prior art** (a teammate already planned or did this — surfaced from the ledger), and **expert routing** (who owns this area, with cited evidence, so you ask the right person). The plan-mode agent then authors the judgment calls — does this align with or conflict with a team decision — reasoning over a context bundle ox assembles. ox makes no model call itself; the inference cost lands in plan mode where you already expect it. Finalized plans are saved to the team ledger (`data/plans/`) as first-class, searchable artifacts, so today's plan becomes tomorrow's prior art. On Claude Code, a renderer skill turns the enriched plan into a beautiful self-contained HTML page for fast human review; other agents get the same enrichment via guidance and the `ox plan` CLI. Configure with `plan.save` and `plan.html` (`off`/`recommend`/`always`), or `SAGEOX_PLAN_HTML` per-run.

## [0.9.0] - 2026-05-28

### Added

- **Pause and resume sessions** — `ox session pause` and `ox session resume` let you suspend an in-progress recording and pick it back up later without losing context, with a proper session lifecycle (active → paused → resumed → stopped) underneath. Long-running work that spans breaks, meetings, or machine restarts is now captured as one coherent session instead of fragmenting.
- **Ephemeral mode for throwaway environments** — set `OX_EPHEMERAL=1` to run ox in a capability-based mode tuned for short-lived sandboxes (Codespaces, CI, dev containers): no daemon assumptions, session finalize syncs inline, and Codespaces is now detected reliably. The old per-command `--ephemeral` flag is deprecated (a flag on a single command silently drifted back to non-ephemeral on the next invocation in the same shell) — set the environment variable instead.
- **Personal access token auth via `SAGEOX_TOKEN`** — non-interactive environments can authenticate by exporting a SageOx PAT in `SAGEOX_TOKEN`, no browser login required. ox warns on stderr as the token nears expiry so automation doesn't fail silently.
- **Faster clones on large repos** — code-search and ledger clones now support shallow, partial (blobless), and shared-alternates fetching, cutting both wall-clock time and disk for big histories.
- **Performance metrics in `ox doctor` and the daemon** — `ox doctor` now reports timing for its checks and the daemon exposes per-subsystem performance counters, making slow setups diagnosable instead of mysterious.
- **`/security-review` pipeline** — a diff-scoped, two-tier (deterministic OSS scanners + AI hunter/validator) security review you can run on demand. Never blocks merge; surfaces input-handling bugs, redaction-bypass risks, daemon IPC authz holes, and supply-chain issues. See `security/README.md`.
- **Durable session commit + PR/issue linkage** — sessions commit atomically and maintain a reverse index linking each session to the PRs and issues it touched, with stale-URL repair. Past work is now discoverable from the PR/issue side, not just the session list.
- **Knowledge Bubble as a workspace primitive** (ADR-017) — the resolver, config, and file-locking foundation for treating personal/team/repo knowledge ("bubbles") as first-class workspaces.
- **Customer-facing env-var namespace convention** — sageox-mono ADR-047 ("Customer-Facing Env Var Namespace") is the canonical home for the rule that customer-facing SageOx env vars use `SAGEOX_*` (product/auth/network identity) and `OX_*` is reserved for CLI-local behavior flags. The legacy customer-facing `OX_TOKEN` / `OX_ENDPOINT` names are removed; `internal/auth/env_naming_test.go` guards against re-introduction. The matching sageox-mono ADR-046 ("Credential Classes and Principal Normalization") is now Accepted with companion sections D7-D10 covering the PAT validation contract, principal `AuthMethod`, customer-facing surface, and cryptographic-separation targets.
- **Local recall on every prompt** — the UserPromptSubmit hook prepends `ox query --local` recall, local-only by default (ADR-018).
- New `hooks.userpromptsubmit.cloud_query` config key (default `off`) opts the UserPromptSubmit hook into a parallel SageOx cloud query. When enabled, prompt content is redacted via the session secrets pipeline before any byte leaves the machine, and the cloud path silently degrades to local-only if `ox login` has not run. `ox doctor` reports the effective value and the privacy/recall tradeoff.

### Changed

- **AI adapters stop fast on terminal errors** — when a host agent hits an unrecoverable condition (e.g. a rate-limit/quota wall), the adapter detects it and stops promptly instead of retrying into the same wall.
- **Daemon team discovery relaxed from every 5 minutes to hourly** — less background chatter and CPU for a signal that changes rarely.

**`ox session audit` and `ox session redact` now require an explicit scope**

Bare invocation used to silently hydrate and scan the entire ledger — a multi-minute LFS Batch fetch that could process hundreds of sessions without the operator's consent. The command now refuses to run without one of:

- `--session <name>` (repeatable) — limit to specific sessions
- `--since <date>` / `--until <date>` — half-open lexicographic window against the ISO-prefixed session name (e.g. `--since 2026-04-01`)
- `--all` — explicit opt-in to the full-ledger sweep

`--all` is mutually exclusive with the narrowing flags. Mistyped `--session` names error before any hydration begins, so a typo no longer triggers minutes of unnecessary LFS fetch followed by an error. The full-ledger sweep that used to fire on bare `ox session redact` is preserved verbatim under `--all`.

### Fixed

**`ox doctor` redaction-debt guidance now points at a command that exists and works**

The 0.8.1 `ledger-redaction-debt` doctor check told the user to run `ox session redact <session>` for interactive cleanup of a quarantined session. No such positional surface existed — cobra silently dropped the positional and scanned the entire ledger. The fix is two-part:

1. The doctor message now emits a copy-pasteable `ox session redact --session <name>` command for each quarantined session (up to five, with a "+N more" hint above that).
2. `ox session redact --session <name>` now also walks `.sageox/cache/quarantine/<name>/` for the targeted sessions. For JSONL quarantine, it redacts at the quarantine path via the canonical chokepoint, moves the file back to `sessions/<name>/`, appends a `RedactionPass` to `meta.json`, and removes the debt marker on success. Non-JSONL quarantine is listed as "manual scrub required" — the chokepoint applies the raw-writer redaction stack and expects JSONL.

Before this PR, the doctor warning pointed at a command that couldn't help: the forward path (`prepush_autoredact.quarantineUnredactableFindings`) had moved the bytes OUT of `sessions/<name>/`, and the backward path only walked `sessions/`. Recovery required manually inspecting, scrubbing, and moving files back. Now `ox doctor` → copy-paste → done.

**Daemon and sync reliability**

- The daemon no longer deletes its IPC socket file when a superseded instance shuts down, so a freshly-started daemon stays reachable instead of leaving the CLI unable to connect.
- Code-search self-heals a corrupt bleve `_mapping` automatically and falls back to SQL-only insights, instead of failing the query outright.
- Observability exports now attach a fresh JWT Bearer token per OTLP request, fixing dropped telemetry once the initial token expired on long-running daemons.
- Ledger pushes that wedged in a "U" (unmerged) state are now surfaced and audited rather than silently stalling, and the summary push no longer writes to a `/tmp` scratch path.

### Security

**Redaction-debt markers are now validated against path-traversal**

The quarantine integration above reads `.sageox/cache/redaction-debt/<session>.json` markers to locate quarantined bytes. An attacker with write access to that directory could previously craft a marker whose `quarantine_paths[].to` pointed outside the ledger (e.g. `../../../tmp/owned.jsonl`); when the operator next ran `ox session redact`, the marker-driven `os.Rename(quarantineAbs, inPlaceAbs)` would overwrite arbitrary files reachable by the operator's UID. Threat model is narrow — the attacker already needs ledger write — but it escalates "ledger-writer" to "arbitrary-file-writer at operator UID."

The fix rejects any marker whose `session_name` contains a path separator or `..`, whose marker filename doesn't match the embedded `session_name`, or whose `quarantine_paths` entries aren't direct children of `sessions/<sess>/` (source) and `.sageox/cache/quarantine/<sess>/` (destination). Defense-in-depth `safeRelativeUnder` checks at the `os.Rename` / `os.Open` call sites block any path that slips past the marker-shape guard.

Closes #608.

**`.claude/settings.json` no longer rewritten on every session**

Before this fix, ox could silently rewrite a user's `.claude/settings.json` on every Claude Code lifecycle event (via the daemon's 30-minute autofix tick, and on session start when the hook set drifted). The rewrite came from running the file through `encoding/json`'s defaults: literal `<`, `>`, `&` inside a permission rule got escaped to `<`, `>`, `&`; hand-written `\uXXXX` source escapes were decoded to literal runes; trailing newlines were stripped; and indentation inside opaque blocks like `permissions` was normalized to two-space. Each rewrite produced bytes that drifted from on-disk on the *next* pass too, so the file churned in a loop even when no content had actually changed.

The fix replaces the encoder with one that has `SetEscapeHTML(false)` and preserves a trailing newline, and switches the "already canonical?" guard from byte equality (which the previous tests proved was satisfiable in lockstep with the encoder's own output but never against real user content) to a combination of strict-shape detection plus semantic content comparison. Result: doctor and the daemon autofix now leave user-authored files alone if Claude Code can read them, and only rewrite when the on-disk hooks shape is one Claude Code actually rejects.

Regression tests seed adversarial inputs that would have failed the byte-equal guard on every pass — literal HTML characters in permission rules, tab indentation, trailing newlines — and assert the file is byte-identical across two consecutive checks.

## [0.8.1] - 2026-05-12

### Fixed

**Pre-push credential gate no longer blocks routine pushes**

0.8.0 introduced a pre-push secret scanner that scanned every file in the push range and refused the push on any finding. In practice this had two failure modes:

1. The scanner ran against `data/github/**` PR/Issue caches. PR titles, bodies, and comments often contain text that matches credential heuristics (sample `Authorization: Bearer` snippets, phrases like "STS session key", and other public bytes already on GitHub). `ox doctor` reconcile failed with *"Push refused: 3 credential pattern(s) detected in 2 file(s)"* pointing at JSON the user did not author. The recovery message named `ox session audit` / `ox session redact` — commands that only operate on `sessions/`, leaving the user unable to follow the instructions for paths outside `sessions/`.

2. Even after scoping, a single session with a finding the chokepoint had missed would refuse the entire push, holding up every other session and unrelated commit.

The gate is now scoped + recoverable + never-blocking:

- **Scoped:** the scanner only inspects paths under `sessions/`. `data/github/**` (verbatim cache of bytes already public on GitHub), `kb/**` (user-curated), and `team-context/**` (user-authored markdown) are intentionally out of scope. The companion writer-side redactor that 0.8.0 wired into `WriteGitHubPR` / `WriteGitHubIssue` is unwired for the same reason.
- **Auto-recovers:** on a finding in a session's JSONL, the gate rewrites the file in place through the canonical chokepoint, appends a `RedactionPass` audit-trail entry to the session's `meta.json`, amends the holding commit, and re-scans. The push then proceeds with scrubbed bytes.
- **Quarantines what can't be auto-redacted:** findings in non-JSONL session paths (notes, summaries, transcripts) are moved to `.sageox/cache/quarantine/<session>/` — bytes preserved verbatim on disk — and dropped from the holding commit. The rest of the push proceeds normally; other sessions and unrelated commits sync as before.
- **Surfaces persistent state in doctor:** a new `ledger-redaction-debt` check reports every quarantined session with the affected detectors and next-step recovery commands. The check is read-only; recovery is a deliberate user gesture (`ox session redact`, or manually inspect + restore from `.sageox/cache/quarantine/`).
- **Never blocks:** the gate always returns nil. Recovery errors are logged, not propagated.

`OX_ALLOW_SECRETS=1` still short-circuits the recovery pipeline for explicit "publish as-is" overrides.

New tests pin the scope contract, the auto-redact happy path, the quarantine path with data preservation, and the doctor surface.

## [0.8.0] - 2026-05-12

### Added

**Modular team rules with first-class context-budget accounting**
- Team rules now live as one-file-per-concern under `<team-context>/agents/rules/<topic>.md` (subdirectories supported, walked recursively). Mirrors the muscle memory of Claude Code's `.claude/rules/` and Cursor's `.cursor/rules/`, scaled up to team scope. Frontmatter spec covers `name`, `description`, `repos`, `audience`, `visibility`, `status`, `from-discussion`. `visibility: always` rules are inlined in `ox agent prime`; `visibility: indexed` rules emit a catalog entry only and the agent reads them on demand. Backward-compat fallback to `coworkers/rules/` for any teams that adopted that location early.
- `ox agent prime` XML now reports a `<context-budget>` block split by content source (sageox / team / project). The split lets SageOx be measured on its own tool overhead instead of conflating it with team-authored content. It flows through every layer: per-prime budget, per-heartbeat per-source aggregation, daemon-side cumulative tracking, and `ox agent list`'s per-source footer. The schema is open — adding a new content source takes one new constant in `internal/prime/types.go` plus tagging emit sites.
- New `<rule-promotion-guidance>` block in prime XML proactively coaches AI coworkers to ask before publishing a project-local rule team-wide ("this looks like it could apply to your whole team — want me to also add it under `<team-context>/agents/rules/`?"). Default to asking; never silently publish.
- New `<team-rules-budget>` block reports the running token cost of `always`-tier rules so teams self-regulate rule-library size.
- Regression-test guard on minimal-prime SageOx overhead (currently ~600 tokens, ceiling 1500). A future change that quietly adds 5K of `<instructions>` blocks itself on review.

**Bundled topical guides via `ox guide`**
- New `ox guide [topic]` reads from `//go:embed`'d markdown — no internet required, no docs-site dependency. Five starter guides ship: `team-rules`, `agents-md`, `team-context`, `murmur-vs-rule`, `getting-started`. `--raw` flag emits unrendered markdown for AI agents that prefer plain text.
- `ox init`, `ox import`, `ox murmur`, and `ox agent team-ctx` --help now cross-reference the relevant guide so users discover them in context.
- Prime XML's commands table includes a new "learn how to do something in ox" row pointing at `ox guide [topic]`.

**Adapter rule installation under `.claude/rules/sageox/` namespace**
- `cmd/ox-adapter-claude-code` now installs a second rule alongside the canonical `.claude/rules/ox.md`: `.claude/rules/sageox/use-team-context.md` — a "MORE RULES → here" pointer that teaches the agent to discover team rules in their canonical home rather than syncing every rule into every cloned repo. No mirror semantics, no conflict resolution, no per-adapter sync coverage gap. The `sageox/` namespace reserves room for future SageOx-installed rules without polluting `.claude/rules/` with `ox-feature1.md`, `ox-feature2.md`, ... siblings.
- `cmd/ox-adapter-droid` mirrors the same pattern under `.factory/rules/sageox/`.
- `handleUninstallRules` walks `sageox/` and removes only ox-stamped files (preserves user-authored content), then cleans up the empty namespace dir. Works around an agentx-v0.1.10 limitation where `ExtractCommandHash` only inspects the first line and misses files with frontmatter.

**Rules-support scaffolding for the remaining adapters**
- New `rules.go` files for `ox-adapter-codex`, `ox-adapter-amp`, `ox-adapter-aider`, `ox-adapter-gemini`, `ox-adapter-opencode`, and `ox-adapter-pi` — each documenting the May 2026 state of that agent's rules surface. None of these agents has a Claude-Code-style modular *behavioral* rules directory today (Codex's `.codex/rules/` is for Starlark execution policies, not behavioral content). The handlers are stub no-ops, NOT wired into `main.go`, and the adapters do NOT advertise `CapRulesInstaller`. When upstream adds modular rules, flipping the wiring on is a 3-line change per adapter.

### Changed

**Reference docs regenerated**
- `docs/reference/` is now in sync with current cobra command definitions. Adds `guide.mdx`, `session/repair-meta-summary.mdx`, and `session/token-optimize.mdx`. Drops a stale `distill.mdx` that was never registered as a root command.

**Adapter ergonomics**
- The Amp adapter now records sessions via a user-global `ox-bridge` plugin. No per-repo configuration needed — install once and every Amp session in every cloned repo is captured automatically.
- `adapter-pi` now detects its host agent's identity from the `PI_CODING_AGENT` environment variable instead of fragile process-name heuristics.
- `--format=json` is now accepted as a hidden alias for `--json` across the CLI, so scripts written against either flag work everywhere.

### Fixed

**Daemon CPU & resource hygiene**
- Eliminated four recurring hot-loop CPU patterns that could pin a core under steady-state idle. Affected paths: failed session-upload retry, project-watcher tear-down, IPC reconnect, and friction-event drain.
- Closed a file-descriptor leak that occurred when the daemon ended up watching a directory that turned out to be gitignored. Long-running daemons no longer accumulate FDs proportional to gitignored-subdir churn.

**Doctor accuracy**
- Credential checks now run after the post-EEQI bootstrap so doctor no longer flags freshly-rotated credentials as missing on the very next run; user-facing guidance was also corrected to point at the right remediation command.
- Doctor scan gained correct session scoping, automatic hydration of LFS-stub recordings, catalog-identity verification, and an append-only redaction trail so previously-redacted content stays redacted across re-scans.
- `ox doctor --force-session-uploads` now actually re-uploads past failed sessions instead of being a silent no-op.

**Session reliability**
- `ox session stop` now writes the prompt + pointer commit inline so finalize is atomic. Previously the two writes could interleave with daemon work and leave a session half-committed for up to a minute.

**Security & redaction**
- Additional credential-redaction patterns close gaps in friction-event sanitization and team-context git-URL handling. Strengthened path-traversal, auth, and LFS size-bound checks per the latest internal review.

**Code search resilience**
- `codedb` self-heals a corrupt bleve sub-index without forcing a full reindex. On large repos this drops recovery time from "several hours" to "2–5 minutes."

[0.9.0]: https://github.com/sageox/ox/releases/tag/v0.9.0
[0.8.0]: https://github.com/sageox/ox/releases/tag/v0.8.0

## [0.7.2] - 2026-05-04

### Added

**Session summarization is now configurable and observable**
- New `agent.summarizer` setting picks who runs the LLM that summarizes a session at stop. `inline` (default) runs it in the calling agent's already-warm prompt cache — cheap, but blocks the user for ~30–120s. `delegated` runs it in the daemon as a background subprocess — non-blocking, but pays the full input-token cost on every stop. `off` skips LLM summarization. `cloud` is reserved for future SageOx cloud-side summarization.
- The legacy `SAGEOX_ASYNC_SESSION_UPLOAD` and `OX_SESSION_INLINE_SUMMARY` env vars still work for one release as deprecation aliases, with a warning pointing at `ox config set agent.summarizer`.
- `ox session stop` now finalizes automatically when you exit Claude Code. Previously the SessionEnd hook fired but had no handler, leaving recordings stranded in the cache for up to 24 hours until the daemon's anti-entropy sweep noticed.
- New `summarization` telemetry event captures input/output tokens, model, duration, and quality score for every delegated summarization call. When the LLM-as-judge runs, its tokens piggyback on the same event.

### Changed

**Cheaper session summarization on the delegated path**
- Delegated summarization now defaults to Claude Haiku 4.5 instead of inheriting the user's local default (typically Sonnet). The summarization task is structured JSON extraction over a fixed schema — well within Haiku's capabilities and 5–15× cheaper. `OX_SUMMARY_MODEL` overrides the default.
- The summary-input optimizer slims tool entries to `{type:"tool_mark", description:"...", count?:N}`. Agent-authored `description` strings (Bash, Agent, Task, WebFetch, ...) are kept because they're already a one-line statement of intent and ideal as `key_action` candidates; tool name, raw inputs, and outputs are dropped. Tool calls without a description (Edit, Read, Write, Glob, Grep, ...) drop entirely — assistant prose names those actions reliably. Adjacent calls with the same description collapse via `count` (typical: a polling loop). On a realistic 300-entry session this is roughly an 80% byte/token reduction over the previous shape.

[0.7.2]: https://github.com/sageox/ox/releases/tag/v0.7.2

## [0.7.1] - 2026-05-03

### Fixed

**Daemon reliability**
- File watchers no longer leak a file descriptor per project file under long uptimes — the per-file handles in `ProjectWatcher` (fsnotify userspace mirror) are now released on directory teardown (#580).
- `ox murmur` file-change notifications now respect `.gitignore`, so build artifacts and editor temp files no longer spam teammates (#581).

**Session UX**
- Sessions whose meta entry was missing a title (rendered as "Summary unavailable" in the UI) are now repaired automatically by the daemon (#578).

[0.7.1]: https://github.com/sageox/ox/releases/tag/v0.7.1

## [0.7.0] - 2026-05-01

### Added

**Globally unique session recording IDs**
- Every session recording now carries a stable `ses_<UUIDv7>` identifier in `meta.json`, independent of path or name. Renames, moves, and re-imports no longer change identity.
- Legacy recordings without the field get a deterministic `ses_<UUIDv5>` derived from `(repo_id, session_name)` via the `EffectiveSessionID()` accessor — client and server compute the same value byte-for-byte, so no backfill is required.
- `ox doctor --fix-slug=session-ids` opt-in backfill persists the deterministic value into `meta.json` for cleaner ledgers.
- Adapter coverage: ses_ IDs are stamped by ox core for sessions captured by every adapter (Claude Code, Aider, Amp, Codex, Droid, Gemini, OpenCode, Pi).

**Session summary quality**
- New evaluation harness scores summary richness against a curated 18-session golden corpus, catching distiller regressions before release.
- LLM judge wired into the daemon for live summary validation; richness checks block stub or empty summaries from reaching the ledger.
- Tokenstrip is now on by default, reducing recording sizes without losing detail.
- Streaming compressor and `ox session token-optimize` shrink recordings for long-running agents.

**Ledger resilience epic**
- Multi-writer safety: structural protections against concurrent CLI/daemon writes corrupting `meta.json` or losing summary fields.
- Daemon LLM tier and autofix scheduler proactively repair corrupted or missing artifacts.
- `meta.json` manifest now carries an explicit `Storage` tag (`lfs` vs `git`) per file (ADR-016), preventing silent demotion of git-stored summaries to LFS pointers.

**Session UX**
- `ox session list` shows session titles by default; agent-context invocations default to JSON output.

### Fixed

**Session recording**
- Regenerate now hydrates LFS-stub raw.jsonl files instead of producing stub summaries.
- Regenerate writes to the canonical ledger path for team sessions instead of the local cache.
- Validator errors no longer leak into user-visible `meta.title` or `meta.summary`.
- `meta.json.title` is populated alongside `summary` so list views render correctly (previously 91/155 sessions on the ox team's ledger shipped with empty titles).
- Session content readers unified behind `openSessionContent` to enforce the cache-only invariant — hydrated bytes never overwrite the in-place LFS pointer.
- Closed an autostash race where the LFS pointer rewrite could be lost during commit.

**Daemon**
- Whisper SQLite handles are properly closed and child watches recursively unwatched, eliminating a file-descriptor leak under long uptimes.

**Init and doctor**
- `ox doctor --fix` now restores missing `ox-*` slash commands (the `claude-commands` check was previously registered but never run).
- `ox init` no longer offers Claude Code twice when an external adapter is already installed.

[0.7.0]: https://github.com/sageox/ox/releases/tag/v0.7.0

## [0.6.4] - 2026-04-22

### Fixed

**Session recording**
- Claude Code session recording was producing header-only `raw.jsonl` files with no turn entries — the one-shot adapter path didn't wire its `ReadFromOffset` handler, so every `PostToolUse` hook no-op'd. Turns are captured again.
- Silent recording failures now surface as errors instead of producing an empty session file.
- Sessions the LLM scores 0 are discarded rather than uploaded as empty entries.

**AI coworker isolation**
- `AGENTS.md` no longer leaks one coworker's active-recording context into another's view — each coworker sees only its own stamp, preventing accidental cross-agent contamination
- Recording-reminder whispers now reach only their intended recipient instead of fanning out to every coworker on the team
- `ox agent prime` no longer falls back to attributing a session to the sole active-recording agent when the real ID can't be resolved, which previously credited the wrong coworker

**Daemon correctness**
- Fixed a data race during scheduler shutdown where an in-flight clone trigger could panic `sync: WaitGroup is reused before previous Wait has returned`
- Fixed a race between two concurrent GC reclones on the same workspace that could destroy each other's in-flight artifacts
- Fixed GC reclone of a repo with an empty working tree — previously the captured diff deleted all restored files; now the empty tree is treated as corruption and the remote content is restored cleanly
- Added a 30-second lock-mtime heartbeat during GC so long reclones don't have their lock reclaimed by the stale-lock watchdog

### Changed
- Claude Code stamp prefix and rewrite rules updated with team-first framing in team-aware repos
- `ox init` and related installers now require the `claude` binary (the `primaryEnv` fallback has been removed from SageOx ClawHub skills)
- ClawHub ox install is now pinned to a specific tarball with an embedded sha256 instead of a floating reference

### Added
- Session-capture architecture documentation

[0.6.4]: https://github.com/sageox/ox/releases/tag/v0.6.4

## [0.6.3] - 2026-04-13

### Added

**Multi-agent init and teammate discovery**
- `ox init` now presents a multi-select agent prompt so teams can onboard multiple AI coworkers at once
- `ox agent prime` surfaces teammate names and credits SageOx throughout sessions

**Session history and distillation**
- `ox distill history list`, `show`, and `since` commands for browsing distilled session knowledge
- Unique entry IDs (`eid`) added to raw.jsonl session entries for reliable deduplication
- `ox distill --quiet` suppresses stdout for non-interactive use

**Observability**
- OpenTelemetry tracing with per-command trace context and W3C `traceparent` headers on CLI HTTP requests
- Per-task OTel trace contexts in the daemon
- Enriched CLI spans for better production debugging

**Daemon event hooks**
- Extensible hook system for daemon events, enabling automation on session lifecycle changes

**PR review workflow**
- New `/monitor-pr` skill drives open pull requests to green by triaging CI failures and review threads

**Feature flags**
- Layered feature flag resolver with disk-cached remote settings
- Daemon polling, IPC handler, and CLI startup wired for flags

**Attribution**
- Conditional commit attribution based on SageOx contribution score
- Unified attribution model removes OAuth gate from session start
- Current user identity (`you=`, `you_aliases=`) passed to agents so they distinguish their own prior work from teammate contributions
- Periodic recording reminder whispers from the daemon

**Other**
- OpenClaw SageOx skills and `clawhub-skill-lint` for community skill quality
- Server-side token validation and twinapi digital twin for auth
- Ledger migration system for legacy GitHub data filenames
- `ox config` surfaces `attribution.commit` and `attribution.pr` settings
- TUI dashboard redesigned with section-based layout
- Built-in adapters extracted to external binaries with adapter registry and CLI management
- Agentx bumped to v0.1.5 for Gemini support and flexible version detection
- Release testing playbook documentation

### Changed
- Team timezone feature removed — UTC hardcoded everywhere for consistency
- `make` targets quiet by default; `V=1` for verbose output
- Distilled facts now use UUID7 filenames for time-sortable ordering
- Pure-Go LFS architecture documented; `git-lfs` binary dependency fully removed

### Fixed
- Attribution prompts no longer credit the current user's own prior work as a teammate contribution
- Vulnerable dependencies bumped (4 Dependabot alerts)
- `DirtyOverlayDebouncer` stale-timer race in daemon resolved
- Session recording: prevent empty sessions from being committed, resolve symlinks before file lookup, prevent agent ID orphaning
- Session upload: resolve all three causes of upload failure; skip LFS stubs in detect loop
- Distill: carry source links through summary citations, apply lookback window to extraction phase, validate summary content against agent meta-output contamination
- Auth: flock-based locking prevents `auth.json` TOCTOU race; credential wipe on null tokens prevented
- Daemon: close leaked file descriptors in workspace scanning
- Doctor: commit migration changes to ledger, restore `FixLevelAuto`, skip adapter warnings for absent CLIs, restore github-data-migration check
- Ledger: handle rename/rename conflicts in rebase auto-resolve, prevent multi-node GitHub data conflicts and comment loss
- Agent: accept session subcommand flags and pick adapter deterministically
- Murmur: surface diagnostics when list is empty, reduce token noise in file-change output
- Distill: pick latest snapshot per PR/issue in GitHub indexer, drop mtime filter on session facts
- OpenClaw skills: enforce 24h window and dedupe state, trim prose, clarify install choice
- Legacy string-format hooks handled; `ox agent prime` made idempotent
- Flaky `TestGetExpired` tempdir cleanup race fixed

[0.6.3]: https://github.com/sageox/ox/releases/tag/v0.6.3

## [0.6.2] - 2026-04-05

### Added

**Agent rules via adapter protocol**
- External adapters can now install, check, and uninstall modular rule files for their agent (e.g., `.claude/rules/ox.md`, `.factory/rules/ox.md`)
- New `rules_installer` capability and `install-rules` / `check-rules` / `uninstall-rules` adapter subcommands
- Claude Code and Droid adapters ship ox behavioral guidance (command reference, session recording, murmuring, attribution) as agent-native rule files
- `ox init` installs rules via adapters; `ox doctor` detects missing/stale rules via adapter diagnostics; `ox uninstall` removes them
- Rules content is version-stamped with downgrade guards via agentx `RulesManager`

### Changed
- Rules management moved from direct agentx calls in the ox CLI to the adapter protocol — each adapter owns how rules are written for its agent
- `DiagnoseParams` now includes `Version` field so adapters can detect stale rules

[0.6.2]: https://github.com/sageox/ox/releases/tag/v0.6.2

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
