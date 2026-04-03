# Open Questions & Unresolved Design Decisions

All questions resolved. Rationale preserved for context.

---

## Decisions Made

| Q | Decision | Where documented |
|---|----------|-----------------|
| Q1 | Two-way: adapter pushes watch events (enables instant indexing) | ADR-003, protocol/spec.md |
| Q2 | `ox integrate install` auto-detects and installs missing adapters; keep both `integrate` and `adapter` commands (different scopes) | ADR-006, design/installation.md |
| Q3 | Same `ox-adapter-*` prefix; `type` field in `info` routes adapters; don't build VCS/indexer subsystems yet | design/adapter-types.md |
| Q4 | Align subcommand names with sibling tools where equivalent operations exist | — |
| Q5 | `claude-code` and `codex` bundled in ox release; all others via catalog and `ox adapter install` | ADR-006 |
| Q6 | Best-effort degradation (B): ox uses what it understands; does not reject adapters with newer protocol | protocol/spec.md |
| Q7 | One `--serve` process per adapter *type*, shared across sessions; each request carries `agent_id` | ADR-005, protocol/spec.md |
| Q8 | `ox adapter link` for interactive dev (symlink, hot-reload); `$OX_ADAPTER_PATH` for CI | design/installation.md |
| Q9 | Only scan `$OX_ADAPTER_PATH` and `~/.local/share/ox/adapters/`. No `$PATH` scan. | ADR-006 |
| Q10 | Design for Windows, implement later. No architectural dead-ends. | — |
| Q11 | GitHub release artifacts; HTTPS + content-addressing sufficient for Phase 1 | ADR-006 |
| Q12 | No governance policy (caveat emptor). Add if ecosystem demands it. | — |
| Q13 | Not now; GitHub template repo (`sageox/ox-adapter-template`) is the right approach when needed | — |

---

## Q1: Watch mode direction

**Decision: Two-way (Q1B)** — adapter pushes entries via watch events.

Enables instant indexing: other team members' ox instances receive session entries mid-session.
Hook-driven pull is bounded by tool-call frequency. Push delivers entries the moment the agent
writes them.

Watch mode is optional: adapters declare `"watcher"` capability. Adapters without it fall back
to hook-driven `read-from-offset` polling.

---

## Q2: `ox integrate install` and adapter auto-install

**Decision: B + C** — `ox integrate install` detects and prompts for missing adapters; claude-code
and codex are bundled so no prompt is ever needed for them.

Keep both `ox integrate` and `ox adapter` commands — they operate at different scopes:
- `ox integrate install` — **project-level** hook installation (writes to `.claude/settings.json`)
- `ox adapter install/link/list` — **user-level** binary management (`~/.local/share/ox/adapters/`)

They coordinate: `ox integrate install` triggers adapter install inline if needed.

---

## Q3: Non-session adapter types

**Decision: type-field routing, build nothing new yet.**

The `type` field in `info` (`session`, `vcs`, `indexer`, `test`) already exists. Discovery is
type-agnostic — it scans, calls `info`, reads `type`. Unknown types are ignored gracefully.

When VCS or indexer subsystems are needed, add routing logic without changing discovery. Same
`ox-adapter-*` prefix throughout. No new namespaces (`ox-indexer-*`, etc.) until there's a
concrete use case.

---

## Q4: Relation to `entire` external-agent protocol

**Decision: C — separate products, but align naming.**

ox adapter subcommand names intentionally align with sibling tools where equivalent operations
exist. This makes future interoperability cheaper without requiring mechanical compatibility now.

---

## Q5: Built-in adapters lifecycle

**Decision: claude-code and codex bundled; others via catalog.**

`claude-code` and `codex` ship in every ox release tarball and Homebrew formula. All other adapters
(gemini, kiro, amp, cursor, windsurf, etc.) live in `sageox/ox-adapters` and install via
`ox adapter install`. No decision yet on when/if bundled adapters move to external.

---

## Q6: Protocol version enforcement

**Decision: B — best-effort degradation.**

- ox refuses adapters with a lower major version than its minimum supported (hard reject).
- ox does NOT refuse adapters with a higher major version — uses what it understands, ignores
  unknown capabilities.
- Unknown serve-mode methods return `{"error": {"code": "method_not_found"}}` — ox treats
  this as capability absent (not a session error).

---

## Q7: Adapter process granularity

**Decision: B — one process per adapter type.**

One `ox-adapter-claude-code --serve` handles all active Claude Code sessions. Each serve-mode
request carries `agent_id`. The adapter maintains per-session state internally.

Reasons: fewer processes in multi-agent environments; SQLite adapters use one DB connection; watch
events are naturally multiplexed by `agent_id`. Adapters bear the responsibility of isolating
per-session state.

---

## Q8: Local adapter development ergonomics

**Decision: `ox adapter link` + `$OX_ADAPTER_PATH`.**

`ox adapter link /path/to/binary` — creates a symlink in `~/.local/share/ox/adapters/`. Rebuild
takes effect after `ox adapter reload`. `ox adapter unlink <name>` removes it.

`$OX_ADAPTER_PATH` — for CI and automation where symlinks are inconvenient.

Both are documented in design/installation.md.

---

## Q9: Binary discovery trust boundary

**Decision: A — no `$PATH` scan.**

Daemon only scans:
1. `$OX_ADAPTER_PATH`
2. `~/.local/share/ox/adapters/`

No `$PATH` scan. A malicious `ox-adapter-*` binary in `node_modules/.bin/` or similar would never
be executed. Homebrew-installed adapters are handled by symlinking into the adapters directory at
formula install time.

---

## Q10: Windows support

**Decision: A — design for Windows, implement later.**

Avoid architectural dead-ends. No Unix-only assumptions should be baked into protocol design.
Platform-specific implementation (named pipes, `%APPDATA%`, `.exe` extension) is deferred.

---

## Q11: Registry integrity

**Decision: A — GitHub release artifacts for Phase 1.**

Registry served from GitHub releases. HTTPS and content-addressed assets provide sufficient
integrity for early adopters. Sigstore cosign (Phase 2) when the ecosystem is public and
high-value enough to warrant it.

---

## Q12: Community adapter governance

**Decision: A — no policy (caveat emptor).**

Keep it simple. Add a badge system and compliance requirements when the community adapter
ecosystem actually exists. Don't govern a community that doesn't exist yet.

---

## Q13: Adapter scaffold / template

**Decision: D (not now) → B (GitHub template repo) when ready.**

`sageox/ox-adapter-template` is the right approach: clone, rename, pass compliance tests.
Not needed until there are external adapter authors. In the meantime, inline code comments
in the protocol spec are the documentation.
