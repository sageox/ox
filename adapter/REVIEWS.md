# Expert Reviews: ox External Adapter System

Three parallel reviews conducted against this design. Each reviewer focused on their domain.
Findings are unfiltered — including disagreements between reviewers.

---

## Review 1: Senior Principal Engineer

**Scope**: Architecture soundness, security, operational concerns, correctness.

### Critical Issues

#### RCE via PATH-based binary discovery (CRITICAL)
The daemon scanning `$PATH` for `ox-adapter-*` binaries and executing them is a remote code execution
vector. Any `ox-adapter-*` binary on PATH will be spawned by the daemon with daemon-level permissions,
regardless of origin.

Attack scenarios:
- User installs a malicious npm package that drops an `ox-adapter-evil` binary into `node_modules/.bin/`, which is on PATH in many dev environments.
- Temporary files or race conditions in world-writable directories on PATH.
- `$OX_ADAPTER_PATH` set by a CI secret accidentally in the wrong environment.

**Required mitigations** (pick at least two):
1. Only execute binaries that are owned by the current user (stat + uid check before exec).
2. Maintain a cryptographic allowlist: adapter binary must have a valid SHA-256 match from a local manifest before first execution.
3. Restrict scan to only `$OX_ADAPTER_PATH` and `~/.local/share/ox/adapters/` (no `$PATH` scan without explicit opt-in via config flag).
4. Prompt user on first use of any previously-unknown adapter: "Found ox-adapter-foo at /path. Allow? [Y/n]".

The current spec treats this as a discovery convenience. It needs to be treated as a trust boundary.

#### IPC timeout is too high
The spec doesn't define adapter IPC timeouts. The daemon-integration design implies 3 seconds. For a
hook called on every tool use (PostToolUse), 3 seconds is unacceptable — it will stall Claude Code's
tool execution pipeline visibly.

**Required**: `read-from-offset` IPC round-trip budget ≤ 300ms. Adapter must respond or daemon
cancels the request and logs a warning. After N consecutive timeouts, adapter session is marked
degraded (surfaces in `ox doctor`).

#### Concurrent hook race condition
When Claude Code fires hooks rapidly (multiple tool calls overlapping), multiple hook processes
may issue `adapter.read-from-offset` IPC to the daemon simultaneously for the same session.

The daemon-integration doc says "daemon sends requests sequentially to a given adapter process"
but doesn't say how concurrent *IPC requests from hooks* are serialized before reaching the adapter.

**Required**: Daemon must queue IPC requests per `agent_id`. Second hook call for the same agent
must block on the IPC socket until the first `read-from-offset` completes. This prevents
duplicate reads and offset corruption.

#### Adapter process inherits daemon environment
When the daemon spawns `ox-adapter-*`, it will inherit the daemon's environment variables including
any secrets (ANTHROPIC_API_KEY, etc.). Adapters should not have access to daemon-internal env vars.

**Required**: Strip environment before spawning adapter. Only pass explicitly allowlisted vars:
`HOME`, `PATH`, `XDG_*`, and any vars in the adapter's declared `required_env` list (from `info`).

### Significant Concerns

#### No observability for adapter lifecycle
When an adapter crashes, the daemon restarts it. There's no way for the user (or ops) to know this
happened outside of `ox doctor`.

**Recommended**: Daemon writes adapter lifecycle events (spawn, crash, restart, shutdown) to a
per-adapter-type log at `.sageox/adapters/<name>.log`. One log per adapter process, not per agent
session — the adapter process is shared across all sessions of that type. `ox adapter logs <name>`
streams it live. `ox doctor --verbose` surfaces the tail on failure. Individual session health
(degraded, missed reads) remains accessible via `ox agent <id> doctor`, which queries daemon
session state rather than adapter logs.

#### Adapter stderr disappears
The spec says adapter stderr is "captured by daemon for debugging." Where does it go? If an adapter
crashes with a panic, that panic log must be surfaced somewhere.

**Recommended**: Daemon captures adapter process stderr (one stream per adapter type, not per
agent). `ox adapter logs <name>` streams it. Filter to a specific session with `--agent <id>`.
On adapter crash, the last 50 lines of stderr are included in `ox doctor` output.

#### Protocol extension is under-specified
The spec says "new subcommands are optional — adapter returns an error, ox degrades gracefully."
But what error? A shell exit code? A JSON `{"error": ...}` response? An unknown subcommand should
return a defined `{"error": {"code": "unknown_method"}}` shape that ox recognizes as capability
absence (not a real error).

**Recommended**: Define `{"id": N, "error": {"code": "method_not_found", "message": "..."}}` as
the canonical response to unknown serve-mode methods. Ox treats `method_not_found` as capability
absent (logs at debug level), not as a session error.

### Minor Issues

- The `RawEntry.timestamp` field uses string ISO-8601. Should be documented to use RFC3339 with
  UTC timezone (`Z` suffix) to avoid timezone parsing ambiguity.
- Adapter `info` response includes `capabilities: []` but there's no formal capability negotiation
  step. Consider: ox should assert all required capabilities are present before starting a session,
  not discover absences at runtime.
- `find-session` timeout: what happens if an adapter takes 10 seconds to find a session file?
  The hook process will block. Needs a timeout shorter than the hook process total budget.

---

## Review 2: CLI Engineer

**Scope**: User experience, CLI ergonomics, developer workflow, discoverability.

### Missing CLI Commands

The current design is daemon/protocol-centric. The following CLI commands are needed for a
good developer and operator experience:

```
ox adapter verify [name]           # run compliance checks against installed adapter
ox adapter reload                  # signal daemon to re-scan adapters without full restart
ox adapter logs [name] [--follow]  # stream adapter process stderr/lifecycle events
ox adapter dev /path/to/binary     # run a specific binary as an adapter (skips discovery)
ox adapter info [name]             # show adapter info response, capabilities, version
ox adapter pin [name] [version]    # pin adapter to specific version (prevents auto-upgrade)
```

Without `ox adapter verify`, adapter authors have no convenient way to know if their adapter
passes compliance. The compliance test suite exists (design/testing.md) but there's no `ox`
command that wraps it.

Without `ox adapter dev`, building an adapter requires copying the binary to
`~/.local/share/ox/adapters/` and restarting the daemon on every rebuild iteration. `ox adapter dev`
should symlink or directly exec the binary without copy, and tell the daemon to prefer it.

### First-Run and Onboarding UX

The install flow doc covers `ox adapter install` but doesn't cover the "I just installed ox and
have no adapters" case.

**Required**: On first daemon start (no adapters discovered), ox should output a clear message:
```
No adapters installed. Run 'ox adapter install' to get started.
See https://docs.sageox.com/adapters for a list of available adapters.
```

This is much better than silent failure where `ox agent start` produces cryptic errors.

### Error Message Quality

When a hook fires and the adapter fails, the user sees nothing (the hook exits silently on error
to not interrupt the coding agent). The failure is invisible until `ox doctor`.

**Recommended**: Two improvements:
1. `ox agent status` shows real-time adapter health for the current session (not just historical).
2. When a session ends with one or more adapter errors, the stop hook should print a visible
   summary: "⚠ ox: 3 entries may have been missed due to adapter errors. Run 'ox doctor' for details."

### Adapter Development Workflow

The spec recommends `$OX_ADAPTER_PATH` for development. This works but has friction:
- Developer has to set the env var before starting the daemon.
- Rebuilding the adapter doesn't hot-reload — daemon must be restarted.
- No way to test adapter in isolation without a full daemon running.

**Recommended**: Add `ox adapter dev /path/to/ox-adapter-myagent --session /path/to/session.jsonl`
as a standalone test mode. Simulates the daemon sending `find-session` and a series of
`read-from-offset` calls, prints what the adapter returns, without needing a real daemon or a
real coding agent session.

This is critical for adapter authors — they need to iterate without a full ox daemon setup.

### Progress UX for `ox adapter install`

The install flow shows a `--fix` command name (e.g., `install-hooks`) but the fix description
is the only user-visible string. If the adapter binary download takes 10 seconds, the user sees
nothing. `ox adapter install` needs a progress indicator (spinner + file size/speed).

### Windows Support

The entire adapter design assumes Unix (`~/.local/share/ox/adapters/`, Unix sockets, `chmod +x`,
`SIGTERM`). Windows is not mentioned.

Issues on Windows:
- `~/.local/share/` doesn't exist; use `%APPDATA%\ox\adapters\`
- Unix sockets replaced by named pipes
- `SIGTERM` → `TerminateProcess` API
- Executables need `.exe` extension; discovery must check both `ox-adapter-*` and `ox-adapter-*.exe`
- Path separator in `$PATH` is `;`, not `:`

**Required**: A Windows compatibility section in the protocol spec. Even if Windows is not
priority-1, the design shouldn't make Windows impossible.

### Shell Completion

`ox adapter install <tab>` should complete with adapter names from the registry. `ox adapter list`
output is machine-readable? The format shown in docs is human-readable with Unicode characters.
For shell completion, there needs to be a `ox adapter list --format json` flag.

---

## Review 3: Open Source Release Manager

**Scope**: Distribution, governance, community, release pipeline, ecosystem health.

### Registry Integrity: No Signing

The `registry.yaml` (or `registry.json`) format defines URLs and SHA-256 checksums for adapter
binaries. But nothing signs the registry file itself.

Attack scenario: MITM on the CDN serving `registry.yaml` substitutes a different registry with
a malicious adapter URL and matching SHA-256. The checksum validates fine. The user gets malware.

**Required**: Registry file must be signed. Options:
- Sigstore cosign (modern, keyless, integrates with GitHub Actions OIDC)
- PGP with published sageox signing key
- At minimum: registry served from a GitHub release artifact (not a CDN) so GitHub's HTTPS
  and content-addressed releases provide integrity

**Recommended approach**: Cosign. It's what the Go and container ecosystems are moving toward.
ox verifies the signature before trusting any URL or checksum from the registry.

### No Governance Policy

The doc says "community adapters live in their own repos." But:
- What is the quality bar for a community adapter?
- Can any adapter call itself an "ox adapter"?
- If a popular community adapter goes abandoned, who maintains it?
- What happens if a community adapter is found to be malicious?

Without a governance policy, the ecosystem looks chaotic to enterprise buyers. With too much
governance, community contributors won't bother.

**Recommended**: A lightweight policy in the `sageox/ox-adapters` repo:
1. "Official" badge: adapters in `sageox/ox-adapters` or verified by sageox
2. "Community" badge: any adapter that passes compliance tests (public test run via CI)
3. "Unverified" badge: anything else
4. Malware removal: sageox can blacklist adapter binaries by SHA-256 hash in a `revoked.yaml`
   file; ox refuses to use revoked binaries

### Release Pipeline is Underspecified

The distribution doc describes per-adapter releases from `sageox/ox-adapters`, but the pipeline
for what triggers a release is not documented.

**Required clarity**:
- Does ox core set a minimum adapter protocol version? If yes, who bumps it and when?
- When ox bumps minimum protocol version, what signals to adapter maintainers to release?
- Is there a breaking-change freeze window before ox major releases?
- Who publishes updates to `registry.yaml`? Automated CI on every adapter release, or manual?

### No Adapter Scaffold / Template

Community adapter authors start from scratch. There's a compliance test suite but no scaffold.
This is a significant barrier to adoption.

**Required** (before public ecosystem launch):
- `ox adapter new my-agent-name` command or a GitHub template repo at `sageox/ox-adapter-template`
- Template includes: `main.go` with all required subcommands, `Makefile` with compliance test target,
  `README.md` with protocol reference, CI workflow for goreleaser
- The template should compile and pass compliance tests out of the box (just with stub implementations
  that return empty results)

### Third-Party Registry Discoverability

Right now there's no way to find community adapters. GitHub search for "ox-adapter" works but is
chaotic. The docs say "GitHub is the registry" for community adapters, but this doesn't scale.

**Recommended (Phase 2)**: A community adapter index — not a registry (no binary hosting), just
an index file in `sageox/ox-adapters` repository that lists community adapters by name with their
repo URL and last-verified-compliance date. Community adapter authors submit a PR to get listed.
Low ops overhead, no infrastructure required.

### License Clarity

The docs don't mention licensing. When a community adapter author publishes an adapter:
- What license should it use?
- Does sageox's trademark policy allow naming binaries `ox-adapter-*`?
- If ox relicenses, do community adapters need to change anything?

**Recommended**: Add a `CONTRIBUTING.md` to the future `sageox/ox-adapters` repo that clarifies
expected licenses (MIT, Apache 2.0, or commercial allowed) and trademark usage rules.

---

## Summary: Items Requiring Action

| Priority | Item | Reviewer |
|----------|------|----------|
| CRITICAL | RCE via unrestricted PATH binary execution | Principal Eng |
| CRITICAL | Registry signing missing | OSS Release |
| HIGH | IPC timeout budget for PostToolUse path | Principal Eng |
| HIGH | Concurrent hook race condition (per-agent IPC queue) | Principal Eng |
| HIGH | Missing `ox adapter verify`, `ox adapter dev`, `ox adapter logs` | CLI Eng |
| HIGH | No governance / badge policy for community adapters | OSS Release |
| MEDIUM | Adapter process env stripping before spawn | Principal Eng |
| MEDIUM | Adapter stderr capture and surfacing | Principal Eng |
| MEDIUM | First-run UX: no adapters installed message | CLI Eng |
| MEDIUM | Windows compatibility design | CLI Eng |
| MEDIUM | No adapter scaffold / template repo | OSS Release |
| MEDIUM | Release pipeline specification | OSS Release |
| LOW | `method_not_found` canonical error shape | Principal Eng |
| LOW | RFC3339/UTC requirement for timestamp fields | Principal Eng |
| LOW | `ox adapter list --format json` for shell completion | CLI Eng |
| LOW | Community adapter index (Phase 2) | OSS Release |
| LOW | License / trademark clarity doc | OSS Release |

---

## Review 4: Learnings From Production Adapter Implementations

**Scope**: What real kiro and Pi adapter implementations teach us that the design didn't anticipate.

### Protocol gaps to fix

#### Stdin timeout guard — production bug pattern (HIGH)
Some IDE hook runners hold stdin open without sending data. Without a timeout, one-shot subcommands
that read stdin block indefinitely.

```go
data, err := ndjson.ReadStdinWithTimeout(os.Stdin, 100*time.Millisecond)
if data == nil { /* no input — treat as empty */ }
```

**Action**: `pkg/ndjson.ReadStdinWithTimeout` is now part of the shared package. Protocol spec
requires all one-shot subcommands that read stdin to apply this guard. Documented in ADR-003.

#### `check-hooks` returns an array of hook files, not one (MEDIUM)
Some agents install hooks in 2-3 locations simultaneously (CLI hook file, IDE hook file, editor
settings). The single `hook_file` field is insufficient.

**Action**: Changed to `hook_files []string`. Now in spec.

#### `offset` semantics are adapter-specific (MEDIUM)
For JSONL-based agents (Claude Code), `offset` is a byte position. For agents with JSON blob
formats (history arrays), offset may be a semantic entry count. The protocol correctly makes
offset opaque to the daemon — adapter authors need explicit guidance that offset type is
their choice.

**Action**: Add a note to the `read-from-offset` spec section and adapter authoring guide.

#### `bufio.Scanner` silent truncation at 64KB default (HIGH)
The default `bufio.Scanner` buffer is 64KB. A tool call that writes a large file (>64KB) in
a single JSONL entry silently truncates. This is not checked with `scanner.Err()` in naive
implementations, causing silent data loss.

**Action**: `pkg/ndjson.Scanner` enforces 1MB buffer and documents mandatory `Err()` checking.
All adapters use this, not raw `bufio.Scanner`.

#### Local dev mode for hook commands (LOW)
`install-hooks --local-dev` should switch installed hook commands from production binary paths to
`go run` invocations. Adapter authors need this to test hook handlers without installing a
production binary.

**Action**: Add `--local-dev` flag to the `install-hooks` subcommand spec.

### Adapter implementation gotchas (kiro-specific)

These are things the ox kiro adapter will need to handle:

- **Three-tier transcript discovery**: kiro exposes transcripts in 2-3 incompatible locations
  depending on whether the user ran it as a CLI or IDE. Three-tier fallback: (1) IDE workspace
  sessions, (2) CLI SQLite DB, (3) empty placeholder.
- **Execution log enrichment**: The IDE only writes "On it." as assistant content. Real action
  detail lives in a separate hashed directory. This enrichment is required for useful recordings
  from IDE sessions.
- **Cumulative transcript offset**: kiro's transcript is a growing JSON blob, not an append-only
  log. The offset must be a semantic history-entry count, not a byte position.
- **VS Code settings surgery**: hook uninstall must remove only the entries it added, not delete
  the entire settings key when it becomes empty — other tools may rely on the key's presence.
- **Session ID on crash**: if the stop hook doesn't fire, sidecar session ID files may be stale.
  The serve-mode model (daemon owns session lifecycle) avoids this entirely.

### Anti-patterns to avoid in ox adapter implementations

| Anti-pattern | Impact |
|---|---|
| Not checking `scanner.Err()` after scan loop ends | Silent data loss on large tool outputs |
| Using `time.Now().UnixNano()` as fallback ID | ID collision risk on fast VM clones |
| Deleting a shared settings key when empty (not just the entries added) | Corrupts third-party tool configuration |
| Assuming all sessions share the same `repo_root` | Breaks multi-repo daemon model |
| Global state indexed by path string | Breaks with worktrees (same content, different path) |
