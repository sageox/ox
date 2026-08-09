---
paths:
  - "cmd/ox/session_*.go"
  - "cmd/ox/agent_session*.go"
  - "internal/lfs/**"
  - "internal/session/**"
  - "internal/daemon/agentwork/session_finalize.go"
  - "internal/daemon/agentwork/session_watcher.go"
---

# CACHE-ONLY DESIGN — Hydrated Session Content (MANDATORY)

The git-tracked file at `<ledger>/sessions/<name>/<filename>` MUST stay as
an LFS pointer for any session synced from the ledger. Hydrated full
content lives at `<ledger>/.sageox/cache/sessions/<name>/<filename>`,
which is gitignored.

**Never write hydrated bytes to the in-place git-tracked path.** Two
failure modes if this invariant is broken — both observed and
post-mortem'd in the 2026-04-25 Phase 2 incident:

1. **LFS-linkage break.** `commitAndPushLedger` globs `*.jsonl`,
   `*.html`, `*.md` in the session dir and `git add`s whatever is
   there. A hydrated in-place raw.jsonl gets committed as a regular
   git blob, replacing the LFS pointer reference. The ledger then
   rejects future pushes (`pre-receive hook declined: LFS objects are
   missing`) for any session whose meta.json references the now-
   orphaned OID.

2. **Daemon anti-entropy clobber.** The daemon's session-finalize
   skips sessions whose raw.jsonl IS still a pointer
   (`internal/daemon/agentwork/session_finalize.go:306`). When in-
   place is full content, the skip doesn't apply, the daemon can
   re-finalize an already-finalized session, race with concurrent CLI
   work, and overwrite a freshly-produced good summary with a
   failure-marker stub.

## The rules

| Rule | Status |
|---|---|
| Reading session content from the ledger | **MUST** route through `cmd/ox/session_content.go:openSessionContent` |
| Writing hydrated content during a read | **NEVER** to `<ledger>/sessions/<name>/`; cache only |
| `git add` after reading session content | **MUST** verify in-place is still an LFS pointer (size ~140B, `lfs.IsPointerFile == true`) |
| `commitAndPushLedger` style globbing | OK only when raw.jsonl in-place is unchanged from HEAD |
| Recording a NEW session (locally-authored) | Writes to `state.SessionPath` (xdg / project cache), uploads bytes to LFS, commits POINTER to ledger |
| `--redact` mode (intentional overwrite-then-reupload) | Allowed because it's a controlled write-then-upload-new-OID cycle that updates meta.json |
| Draft placeholder (ADR-029) | `meta.json` ONLY. Never raw.jsonl, never any content file. `lfs.WriteDraftSessionMeta` refuses a directory that already holds raw.jsonl; `validateDraftShape` refuses a draft meta with a populated `files` manifest; `sessionArtifactsToStage` returns nothing for a draft so its empty manifest cannot fall through to the glob |

## Canonical helpers

| Helper | Use |
|---|---|
| `cmd/ox/session_content.go:openSessionContent(projectRoot, ledgerPath, sessionName, filename)` | Cache-aware reader. Returns a path holding REAL bytes, hydrating on demand to the ledger cache. |
| `internal/lfs/meta.go:ResolveContentPath(sessionDir, cacheDir, filename)` | Lower-level resolver. Cache → in-place real content → "" (caller hydrates). |
| `cmd/ox/session_hydrate.go:hydrateFromLedger(...)` | Cache-only Batch-API download. Does NOT write to in-place sessionDir. |

## Banned patterns

```go
// BANNED — direct in-place read, errors on stubs
rawPath := filepath.Join(ledgerPath, "sessions", sessionName, "raw.jsonl")
session.ReadSessionFromPath(rawPath)

// BANNED — hydrate in-place
downloadFileFromLFS(projectRoot, sessionDir, meta, "raw.jsonl") // writes to sessionDir!

// BANNED — assuming raw.jsonl is real content
data, _ := os.ReadFile(filepath.Join(sessionDir, "raw.jsonl"))
```

## Correct patterns

```go
// Read session content via the canonical resolver
rawPath, err := openSessionContent(projectRoot, ledgerPath, sessionName, "raw.jsonl")
if err != nil {
    return err
}
stored, err := session.ReadSessionFromPath(rawPath)

// Or, lower-level if you have sessionDir + cacheDir already
cacheDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName)
contentPath := lfs.ResolveContentPath(sessionDir, cacheDir, "raw.jsonl")
if contentPath == "" {
    // hydrate first
}
```

## Recording-time invariant (separate)

Initial session recording writes to `state.SessionPath`, which is the
local recording cache (xdg/project), NEVER the ledger. The recording
lifecycle:

1. `session start` → mkdir `state.SessionPath`; write header into
   `state.SessionPath/raw.jsonl` (full content, local)
2. agent activity → append entries to that local file
3. `session stop` → upload bytes to LFS via Batch API; commit an LFS
   POINTER (not the bytes) to `<ledger>/sessions/<name>/raw.jsonl`

If a future change starts writing recording content directly to
`<ledger>/sessions/<name>/raw.jsonl`, every push including the
resulting commit will break LFS linkage and the daemon's anti-entropy
will start clobbering. See `cmd/ox/agent_session.go` invariant
comment for the full lifecycle annotation.

## Detection

`make check-cache-only` (TODO — to be added) should grep for the
banned patterns above and fail the build if any reappear, similar to
`make check-no-git-lfs-shell`. For now, code review and the
`TestResolveContentPath_CacheOnlyDesign` / `TestOpenSessionContent_*`
tests in `cmd/ox/` are the enforcement.

## Ledger index mutation must be guarded, not just pushes

Every ledger index writer in the CLI used to be followed by `pushLedger` →
`gitutil.PushWithRetry` → `IsSafeForGitOps`, so the mid-rebase guard rode
along for free. Draft placeholders were the first writer that deliberately
does NOT push, and they fell straight through it.

The failure is silent and team-wide. During a conflicted `git pull --rebase`
on `sessions/<name>/meta.json`, an unguarded `git add` **marks the conflict
resolved** and `git commit -- <pathspec>` **succeeds on the detached rebase
HEAD, consuming the replay step**. The next `rebase --continue` reports
success and the commit being replayed is gone — a teammate's finalize, a
murmur, a plan, imported team data.

**Any new code path that mutates the ledger index MUST check
`gitutil.IsRebaseInProgress` / `IsSafeForGitOps` first, whether or not it
pushes.** In `cmd/ox` the chokepoint is `prepareDraftLedgerWrite`.

Two related traps in the same area:

- `git commit -- <pathspec>` commits the **working tree**, not the index.
  Anything that rewrote those paths between `git add` and `git commit` gets
  committed under your message. Re-verify before committing.
- `deriveLedgerPath` returns `filepath.Dir` for ANY path whose parent is
  named `sessions` — including the XDG cache and the legacy in-repo fallback,
  where the "ledger" is the user's own project root. Validate the derived
  path against the project's configured ledger before running git on it.

## Related

- `.claude/rules/ledger-cache.md` — when to use cache vs. other dirs
- `.claude/rules/lfs-no-git-lfs-binary.md` — pure-Go LFS, never shell-out
- `.claude/rules/daemon-git.md` — daemon-CLI ownership split
- 2026-04-25 incident PRs: #559, #561, #562, #563, #564
- bd: ox-4ncz, ox-5cc9, ox-o45g, ox-91sl, ox-b917, ox-9o29
