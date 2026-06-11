# Session Capture — Component Walkthroughs

> Audience: engineer onboarding onto a specific session-capture component. Each
> section is a ~5-minute read and maps concepts from the
> [architecture document](session-capture-architecture.md) to the files and types
> that implement them. This is not a line-by-line tour — it is the minimum you need
> to find your way and make non-breaking changes.

**Conventions in this doc:**
- Paths are relative to repo root.
- `Type` references in the form `pkg.Type` point at a Go type in that package.
- Non-obvious design choices are called out explicitly — most of the code is boring;
  only the interesting bits are annotated.

---

## A. `internal/session/recording` — the lifecycle core

**Purpose:** own the `.recording.json` state file and the StartRecording/StopRecording
verbs. Every other component defers to this one for "is there a recording?" and
"where does it live?"

**Key files:**
- [recording.go](../../internal/session/recording.go) — state struct, start/stop,
  path resolution.
- [recording_helpers_test.go](../../internal/session/recording_helpers_test.go) —
  fixture helpers used widely across tests.
- [recording_ghost_grace_test.go](../../internal/session/recording_ghost_grace_test.go) —
  spec of the 10-minute grace period.
- [recording_concurrency_test.go](../../internal/session/recording_concurrency_test.go) —
  spec of multi-agent isolation.

**Types & functions:**
- `session.RecordingState` — the on-disk schema. All fields have `json:` tags; new
  fields must be `omitempty` to stay backwards-compatible with older recordings.
- `StartRecording(projectRoot, opts) (*RecordingState, error)` — the *only* function
  that may write a recording. Resolves the canonical write path via
  `resolveSessionsWritePath` (never via the search path ladder). Writes
  `raw.jsonl` header and `.recording.json`.
- `StopRecording(projectRoot, agentID) error` — marks `StoppedAt`, triggers the final
  drain + artifact write via `artifacts.WriteSessionArtifacts`.
- `LoadRecordingStateForAgent(projectRoot, agentID)` — returns the specific agent's
  state. **Prefer over `LoadRecordingState`** when an agent ID is known; workspace
  fallback picks arbitrarily across worktrees.
- `LoadAllRecordingStates(projectRoot)` — iterates the search path, deduplicates by
  canonical symlink resolution, returns all active recordings.
- `CleanupGhostSessionsInDir(dir)` — called at hook time; respects
  `GhostGracePeriod = 10*time.Minute`.

**Non-obvious design choices:**
1. **Search path is read-only.** The constant bug source historically has been writes
   that iterate the search path and land in a wrong directory. Keep
   `sessionsSearchPaths` strictly for reads.
2. **Symlink resolution before comparison.** macOS `/tmp` is a symlink to
   `/private/tmp`; without resolution a session started under one path and found
   under the other is treated as two sessions.
3. **`LastHookStatus` is a stable enum string** (e.g. `ok`, `session-file-not-found`,
   `read-error`). It is the contract for `ox session status` and `ox doctor`; do not
   rename existing values.

**To change this component safely:**
- Adding a state field: add with `omitempty`; do not require it in
  `LoadRecordingState`'s validation.
- Adding a new terminal state: add to the transition enum and map it through
  `stopSessionForClear`/`StopRecording`; update `recording_ghost_grace_test.go`.
- Tests must use `t.TempDir()` + explicit `projectRoot`; never `cwd` fallbacks.

---

## B. `cmd/ox/agent_hook` — the edge handler

**Purpose:** the `ox agent hook <event>` command, called by Claude Code and other
agent hosts on lifecycle events. Dispatches to phase handlers that read/write
recording state.

**Key files:**
- [agent_hook.go](../../cmd/ox/agent_hook.go) — phase dispatch and handlers.
- [agent_hooks.go](../../cmd/ox/agent_hooks.go) — wiring and event → phase map.
- [agent_hook_banner_test.go](../../cmd/ox/agent_hook_banner_test.go),
  [agent_hook_stop_test.go](../../cmd/ox/agent_hook_stop_test.go) — behavior tests.

**Phases (canonical names):**
| Phase | Trigger | What it does |
|---|---|---|
| `phaseStart` | `SessionStart` | Prime, StartRecording if none, heartbeat |
| `phasePrompt` | `UserPromptSubmit` | Deliver whispers via stdout (ONLY reliable stdout channel) |
| `phaseAfterTool` | `PostToolUse` | **Incremental drain** — tail source JSONL, append redacted entries |
| `phaseCompact` | `PreCompact` | `stopSessionForClear` → IPC to daemon |
| `phaseStop` | `Stop` | Every agent turn end; used for heartbeat, NOT session end |

**Critical gotcha:** Claude Code *discards* PostToolUse stdout. Anything printed from
a hook handler other than `phasePrompt` is invisible to the model. This is why
whispers are delivered in `handlePrompt` and capture is not.

**Non-obvious design choices:**
1. `handleAfterTool` is the innermost loop — every microsecond here multiplies by
   tool count. Tail read + classify + redact + append runs in the same process as
   the hook; no IPC.
2. `deriveLedgerPath(sessionPath)` is used to construct the IPC payload at compact
   time. The session path format is stable, so we can derive `<ledger-root>` by
   trimming.
3. `handleStart` is idempotent — re-running `ox agent prime` on the same host does
   not start a second session. The dedup key is `AgentID`.

**To extend:**
- New host event → canonical phase: update `agentx.BuildEventPhaseMap` (in the
  `github.com/sageox/agentx` module).
- New phase semantics: add handler + dispatch entry. Do not reuse existing phase
  names.

---

## C. `internal/session/adapters` + `cmd/ox-adapter-*` — agent host abstraction

**Purpose:** isolate per-host knowledge (session file location, JSONL dialect,
entry field names) behind a small interface. External-binary model so bugs and
releases are isolated from ox.

**Key files:**
- [internal/session/adapters/adapter.go](../../internal/session/adapters/adapter.go) —
  interface definitions and `SessionLookup`.
- [internal/session/adapters/external.go](../../internal/session/adapters/external.go) —
  subprocess wire protocol.
- [internal/adapterprotocol/](../../internal/adapterprotocol/) — protocol types
  shared by ox and adapters.
- `cmd/ox-adapter-claude-code`, `cmd/ox-adapter-codex`, … — adapter binaries. Each is
  its own `main` package.

**Interface:**
```go
type Adapter interface {
    Name() string
    Capabilities() []Capability
    Detect(context.Context) (bool, *Metadata, error)
    FindSessionFile(SessionLookup) string
    ReadMetadata(path string) *SessionMetadata
}
type IncrementalReader interface {
    ReadFromOffset(path string, offset int64) ([]RawEntry, int64, error)
}
type CapturePriorCapable interface {
    CapturePrior(SessionLookup) (*CapturePriorResult, error)
}
```

**Non-obvious design choices:**
1. **Capability flags, not type assertions on caller.** `adapter.HasCapability(...)`
   keeps ox's caller code uniform and lets adapters opt in to advanced features.
2. **`SessionLookup.Since`** lets ox ask "find session files newer than X" so that
   stale or unrelated JSONL files in `~/.claude/` don't get picked.
3. **Adapters register at runtime** via `adapters.RegisterExternalAdapters()`, which
   scans `PATH` for `ox-adapter-*` binaries. Users without an adapter installed get
   a clean `LastHookStatus = adapter-not-found`, not a panic.

**To add an agent host:**
1. Implement a binary under `cmd/ox-adapter-<type>/`.
2. Honor `ox-adapter <type> detect | find-session-file | read-from-offset | …`
   subcommands per `internal/adapterprotocol/`.
3. Add a row to [agent-support-matrix.md](../specs/agent-support-matrix.md).
4. Tests live in `internal/session/adapters/adapters_<type>_test.go` using a fake
   session file.

---

## D. `internal/session/pipeline` + supporting utilities — the drain loop

**Purpose:** transform raw bytes from the adapter into redacted `raw.jsonl` entries,
and maintain the offset/counter state.

**Key files:**
- [internal/session/pipeline/](../../internal/session/pipeline/) — the
  composable drain pipeline.
- [classify.go](../../internal/session/classify.go) — entry type classification
  (user/assistant/system/tool).
- [filter.go](../../internal/session/filter.go) — pre-session timestamp filter
  and tool-spam filters for summarization (NOT for raw).
- [entry.go](../../internal/session/entry.go) — `SessionEntry`, `seq`/`eid`
  assignment.
- [redact_rules.go](../../internal/session/redact_rules.go) — literal/regex
  redaction rules; sources: builtin, team, repo, user.
- [secrets.go](../../internal/session/secrets.go) — the regex set for cloud keys,
  JWTs, DB strings, etc.
- [manifest.go](../../internal/session/manifest.go) — canonical JSON serialization
  + `Hash()`.
- [signing.go](../../internal/session/signing.go) — Ed25519 signing integration.

**Pipeline stages (in order):**
1. `adapter.ReadFromOffset` — bytes → `[]RawEntry`.
2. Classify — fill `type` if missing; promote `<system-reminder>` content to
   `type=system`.
3. Timestamp filter — drop entries with `ts < StartedAt`.
4. Redact — apply builtin manifest + custom rules; replace with
   `[REDACTED:pattern_name]`.
5. Assign `seq` (sequential) and `eid` (5-char random).
6. Append line to `raw.jsonl`.
7. Advance `SourceOffset` + `EntryCount` in `.recording.json`.

**Non-obvious design choices:**
1. **Raw gets everything (except secrets).** The summarization-side filter
   (`FilterForSummarization`) lives in the *derivation* path, not here. A noisy
   session is still a complete session.
2. **Classification before redaction.** The redactor wants to know which fields to
   scan — a `tool` entry has structured `tool_input`/`tool_output`; a `user` entry
   is free-form.
3. **`eid` is assigned at append, not at emit.** An adapter that re-emits an entry
   (e.g. because the agent rewrote its own JSONL) gets a new `eid`. Downstream
   dedupers can drop by content hash if needed.
4. **Redaction manifest is signed.** `signing.RegisterArtifact("redaction",
   generateRedactionManifest)` hooks the manifest into the Ed25519 signing path.
   Session headers record the manifest hash so readers can verify.

**To change:**
- Adding a redaction rule: extend builtin rules in `secrets.go`, then
  `make generate-manifest`. Sessions recorded before the change will still pass
  verification because they reference the old hash.
- Adding an entry type: update `classify.go`; readers of raw already ignore unknown
  types with a warn.

---

## E. `internal/session/artifacts` + markdown/summarize — derivation

**Purpose:** turn `raw.jsonl` into human-readable artifacts at finalize.

**Key files:**
- [artifacts.go](../../internal/session/artifacts.go) —
  `WriteSessionArtifacts(sessionPath)` is the orchestrator.
- [enrich.go](../../internal/session/enrich.go) — add computed fields
  (`files_changed`, `chapters`) to `summary.json`.
- [markdown.go](../../internal/session/markdown.go) —
  `MarkdownGenerator` renders full session.md.
- [summary_md.go](../../internal/session/summary_md.go) —
  `SummaryMarkdownGenerator` renders summary.md.
- [summarize.go](../../internal/session/summarize.go) — shared prompt (via
  `pkg/sessionsummary`), three execution paths.
- [plan_extract.go](../../internal/session/plan_extract.go) — plan priority
  ladder.
- [mermaid.go](../../internal/session/mermaid.go) — extract Mermaid diagrams
  from entries.

**Summarization paths** (see [session-summarization.md](../specs/session-summarization.md)):
1. **Server-side API** — network call to SageOx server, full LLM.
2. **Client-side prompt embed** — prompt is baked into `raw.jsonl` for the agent to
   summarize before stop (used when offline).
3. **Resummary CLI** — `ox session resummary` prints prompt + raw to stdout so the
   user can pipe into any LLM. No auto-exec — keeps the user in control.

**Plan extraction priority ladder:**
1. Entries with `is_plan: true` metadata.
2. Entries with `## Final Plan` header.
3. Entries with `## Implementation Plan` header.
4. Entries with `## Plan` header.
5. Last assistant message.

**Non-obvious design choices:**
1. **Artifacts are fully re-derivable.** `ox session regenerate` blows them away and
   rebuilds from raw. Hence no migration logic on artifact format changes — just
   re-regen.
2. **`summary.json` has a validation step** (`validate.go`) — otherwise agents
   occasionally echo meta-commentary into the summary, which breaks distillation.
3. **Mermaid extraction is opportunistic.** Any ```mermaid fence is lifted into
   `summary.md`'s diagrams section, regardless of whether the agent intended it to
   be shown.

**To add an artifact:**
- Write a generator function that takes `sessionPath` and returns either bytes or
  writes to a file.
- Call from `WriteSessionArtifacts`.
- Add the filename to `ContentFiles` in `cmd/ox/session_upload.go` so it uploads to
  LFS.

---

## F. `internal/session/contexttrace` — provenance audit log

**Purpose:** record which team-context snippets were made available to the agent and
which the agent attributed a decision to. Distinct from `raw.jsonl` — it's a *peer*
file, not nested.

**Key files:**
- [contexttrace/contexttrace.go](../../internal/session/contexttrace/contexttrace.go) —
  `Writer`, `EventType`, `SourceType`.
- [cmd/ox/agent_prime_context_trace.go](../../cmd/ox/agent_prime_context_trace.go) —
  emits `provided` events during prime.
- [cmd/ox/agent_session_context_trace.go](../../cmd/ox/agent_session_context_trace.go) —
  agent-invoked endpoint for `influenced` events.

**Event types:**
- `EventProvided` — ox injected this context (at prime time, mainly).
- `EventInfluenced` — the agent says a decision was driven by this context; carries
  the `eid` of the raw entry expressing the decision.

**Source types:** `team-context`, `team-memory`, `team-docs`, `project-config`,
`team-whisper`, `project-whisper`, `on-demand`.

**Non-obvious design choices:**
1. **`eid` is the stable join key** between raw entries and influenced events. Do
   not use `seq` — `seq` changes if raw is ever re-normalized.
2. **Provided events are written once at prime.** Later injections (on-demand tool
   `ox agent team-ctx <slug>`) emit their own Provided events on invocation.
3. **Trace is optional.** A session without `context-trace.jsonl` is valid; readers
   must tolerate absence.

---

## G. `internal/agentinstance` — identity and lifetimes

**Purpose:** allocate and persist Agent IDs (`OxSk2e`) and track the metadata for
each agent instance across hook invocations.

**Key files:**
- [internal/agentinstance/agentid.go](../../internal/agentinstance/agentid.go) —
  ID generation (62⁴ space).
- [internal/agentinstance/store.go](../../internal/agentinstance/store.go) —
  per-user JSONL append-only store.

**Schema:**
```go
type Instance struct {
    AgentID         string    // "Oxabcd"
    ServerSessionID string    // full oxsid_… from server
    CreatedAt       time.Time
    ExpiresAt       time.Time
    AgentType       string
    AgentVer        string
    Model           string
    ParentPID       int
    ParentAgentID   string
    PrimeCallCount  int
}
```

**Storage:** `~/.sageox/agent_instances/<user-slug>/agent_instances.jsonl`.
Append-only, newest-first scan. 500-instance hard cap.

**Non-obvious design choices:**
1. **No registry of "current" instance.** Callers pass the AgentID in via env var
   (`SAGEOX_AGENT_ID`) or CLI arg. There is no global "who am I" state.
2. **Per-user slug in path** lets multi-user machines coexist without locking.
3. **Hard cap with eviction** prevents runaway growth if sessions aren't reaped.

---

## H. `internal/lfs` — pure-Go LFS client

**Purpose:** upload/download session blobs via the Git LFS Batch API over plain
HTTPS. No `git-lfs` binary ever.

**Key files:**
- [internal/lfs/client.go](../../internal/lfs/client.go) — Batch API client.
- [internal/lfs/transfer.go](../../internal/lfs/transfer.go) — upload/download
  flows with concurrency.
- [internal/lfs/session_upload.go](../../internal/lfs/session_upload.go) —
  session-specific orchestration.
- [internal/lfs/pointer.go](../../internal/lfs/pointer.go) — pointer I/O (used for
  parsing historical pointers, not writing new ones).
- [internal/lfs/meta.go](../../internal/lfs/meta.go) — `SessionMeta`, `FileRef`,
  `HydrationStatus`.

**Key types:**
- `SessionMeta` — built via builder: `NewSessionMeta(...).Title(...).Build()`. The
  `files: {name -> FileRef}` map is the git-tracked manifest that replaces LFS
  pointer files.
- `FileRef{OID, Size}` — OID is `sha256:<hex>`.
- `Client` — `NewClient(repoURL, user, token)`; `Batch(ctx, op, objects)`.

**Non-obvious design choices:**
1. **No pointer files, ever.** `WritePointerFile` is public but unused by the
   session path; historical callers are being removed.
2. **Up to 4 concurrent uploads.** Empirically the sweet spot for both GitHub and
   GitLab; higher triggers throttling on smaller projects.
3. **OID is the canonical filename in LFS.** Re-uploading a file produces the same
   OID; idempotent by construction.
4. **Hard rule: no `.gitattributes` with `filter=lfs`.** Enforced by
   `make check-no-git-lfs-shell`. See
   [.claude/rules/lfs-no-git-lfs-binary.md](../../.claude/rules/lfs-no-git-lfs-binary.md).

**To debug upload failures:** the GitLab error `LFS objects are missing` is a *false
trail* — the fix is never `git lfs push --all`. See the rule file; the real fix is
"find the pointer file that references an un-uploaded OID and upload it via
`internal/lfs/client.go`."

---

## I. `internal/ledger` — sidecar repo

**Purpose:** own the lifecycle of the per-project ledger git repository: clone it,
push to it, push GitHub data. Session capture is one of several consumers.

**Key files:**
- [internal/ledger/ledger.go](../../internal/ledger/ledger.go) — `Ledger` type,
  `Open`, `Init`, `Exists`.
- [internal/ledger/github_push.go](../../internal/ledger/github_push.go) — the
  `git add --sparse / commit / push` used by session upload.

**Layout (relative to ledger repo root):**
```
sessions/<name>/meta.json          # committed
sessions/<name>/[other files]      # .gitignore'd
.sageox/cache/sessions/<name>/     # local-only, never committed
data/, memory/, docs/              # team context (separate subsystem)
.gitignore, .gitattributes         # .gitattributes never has filter=lfs
```

**Non-obvious design choices:**
1. **Cloud-provisioned, never locally initialized.** `ledger.Init` clones from a
   remote URL returned by the SageOx API; no `git init`.
2. **Sparse checkout is default.** `git add --sparse` is mandatory (git ≥ 2.37).
3. **Retry 3x on push.** Push failures beyond that are reported but not fatal to
   the session — the raw file stays local and `ox session push <name>` can retry.

---

## J. `cmd/ox/session_*` — user-facing CLI surface

**Purpose:** the commands users and agents invoke to manage sessions.

**Command groups:**

| Group | Commands | Notes |
|---|---|---|
| Lifecycle | `start`, `stop`, `force-stop`, `abort`, `recover` | `start`/`stop` are rarely typed by hand — hooks drive them |
| Inspection | `list`, `show`, `view`, `status`, `url` | `view --text` renders locally if hydrated |
| Derivation | `regenerate`, `resummary`, `resummary-batch` | Idempotent re-runs |
| Distribution | `upload`, `push-summary`, `hydrate`, `migrate-lfs` | Upload is auto after stop; hydrate is on-demand |
| Subagent hooks | `agent-session subagent`, `plan-history`, `capture-prior`, `context-trace`, `incremental` | Used by the agent itself, not humans |
| Ops | `lint`, `validate`, `commit`, `remove`, `export`, `score` | Doctor integration + housekeeping |

**Key files to start from:**
- [session.go](../../cmd/ox/session.go) — cobra tree root.
- [session_start.go](../../cmd/ox/session_start.go) — creates recording (usually
  called from `handleStart` in the hook, not by hand).
- [session_stop.go](../../cmd/ox/session_stop.go) — the full finalize pipeline
  including `WriteSessionArtifacts`, LFS upload, ledger push.
- [session_upload.go](../../cmd/ox/session_upload.go) — upload pipeline that
  `stop` uses; can be re-invoked independently.
- [agent_session.go](../../cmd/ox/agent_session.go) — the `ox agent <id> session
  …` namespace used by agents (capture-prior, subagent, context-trace).

**Non-obvious design choices:**
1. **Most "session" commands are safe to run on an already-published session.** They
   re-derive artifacts or re-upload. The one that isn't is `abort` — it deletes.
2. **`doctor session`** has a dedicated family (see `cmd/ox/doctor_session*.go`) for
   finding incomplete, uncommitted, or failed-upload sessions. This is the primary
   recovery surface.

---

## K. `cmd/ox/distill*` — downstream consumer

**Purpose:** scan finalized sessions and extract structured facts for team memory.
Not part of capture proper, but reads the same `summary.json` the capture pipeline
produces.

**Key files:**
- [cmd/ox/distill.go](../../cmd/ox/distill.go), [distill_sessions.go](../../cmd/ox/distill_sessions.go) —
  CLI entry.
- [internal/distill/](../../internal/distill/) — pipeline.

**Contract with capture:** distillation reads `summary.json` via the Store interface.
Anything that changes `summary.json`'s schema must update the distill consumer
(`pkg/facts/`) in the same PR.

**Non-obvious:** minimum-quality gate (`minSessionQuality = 0.2`) drops noisy
sessions from distillation. A session can be published but not distilled.

---

## L. Test patterns worth knowing

- **`t.TempDir()` + explicit `projectRoot`.** Never rely on cwd. Session tests that
  don't pass an explicit repo root expose the `projectRoot/sessions/` leak bug.
- **`cmd.Dir = tmpDir` for git ops.** Tests must never touch the real repo's git
  config; see [.claude/rules/daemon-git.md](../../.claude/rules/daemon-git.md).
- **Table-driven for classify/redact/filter.** They take string input and produce
  string output; table-driven is idiomatic and catches edge cases cheaply.
- **Ghost grace & concurrency have dedicated tests.** When touching recording state,
  extend `recording_ghost_grace_test.go` and `recording_concurrency_test.go`.
- **Integration test** lives at
  [internal/session/integration_test.go](../../internal/session/integration_test.go) —
  end-to-end StartRecording → drain → StopRecording → artifact shape. The slowest
  but most load-bearing test; keep it green.

---

## M. Where to ask for help

- **Recording state, lifecycle questions:** load the `tooling-engineer` coworker —
  `ox coworker load tooling-engineer`.
- **Adapter behavior, agent host quirks:** load `agent-ux`.
- **LFS and git ops safety:** load `code-reviewer`.
- **Go idioms and daemon concurrency:** load `go-pro`.
- **Test design for new behavior:** load `test-architect`.

Prior sessions often cover regressions in this area. Use `ox session list` with
filters and `ox query "<question>"` to find teammate context before touching
capture paths.
