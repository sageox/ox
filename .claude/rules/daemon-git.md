---
paths:
  - "internal/daemon/**"
  - "internal/session/**"
  - "cmd/ox/session*.go"
  - "cmd/ox/status*.go"
---

# Daemon-CLI Git Operations Split

## Ownership

| Operation | Owner | Notes |
|-----------|-------|-------|
| `git clone` | daemon | Initial setup / anti-entropy |
| `git fetch` | daemon | Background sync timer |
| `git pull --rebase --autostash` | daemon | Background sync timer |
| `git add --sparse` | CLI | Session upload, import, doctor, session drafts |
| `git commit` | CLI | Session upload pipeline, session drafts |
| `git push` | CLI **and** daemon | CLI: session upload pipeline. Daemon: batched push of its OWN narrow, pathspec-scoped commits (murmurs, session finalize, session drafts) |

The CLI owns the session-upload write path end to end. The daemon writes only
on narrow, pathspec-scoped, daemon-owned paths and batches their pushes onto
its sync cycle.

**The daemon push is deliberately narrower than the CLI's.** `pushLedger` runs
a pre-push secret gate and an LFS reconcile; the daemon's batched pushes run
neither, and `git push` moves the whole branch. So
`pushSessionDraftCommits` refuses to push when ANY unpushed commit is not a
draft commit — otherwise it could ship a commit the CLI deliberately refused
(a secret-gate rejection), or a finalize commit before its LFS blobs exist.
Any future daemon-side push must apply the same restriction or run the gate.

**Mid-rebase safety belongs at index-mutation time, not push time.** See
`.claude/rules/cache-only-design.md` — an unguarded `git add` during a
conflicted rebase marks the conflict resolved and the following commit
consumes the replay step, silently destroying whatever was being replayed.

**Why this split:** Minimal IPC surface, CLI writes don't depend on daemon, daemon stays simple. Conflicts are extremely unlikely (unique path per session with random suffix). Push failure after 3 CLI retries is acceptable — best-effort, not transactional.

```go
// CLI writes directly to ledger (add/commit/push)
commitAndPushLedger(ledgerPath, sessionName)

// Daemon handles reads (pull) via sync scheduler
// CLI triggers pull via IPC when needed:
client := daemon.NewClient()
client.SyncWithProgress(...)
```

## Daemon as Source of Truth for Pull Status

The daemon is THE source of truth for what ledgers and team contexts are being pulled.

- **ALWAYS** query the daemon for workspaces being synced (pull direction)
- **NEVER** call cloud APIs directly to show "available" repos

```go
// WRONG: ox status calls cloud API directly
cloudRepos, _ := client.GetRepos()

// RIGHT: ox status asks daemon what it's syncing
daemonStatus, _ := client.Status()
for _, ws := range daemonStatus.Workspaces { ... }
```

Flow: CLI fetches credentials → saved to disk → daemon loads credentials → discovers team contexts → starts syncing → `ox status` queries daemon.

## Team Context and Ledger Repos Are NOT Read-Only Mirrors

Both remote and local writes happen:
- **Remote:** team knowledge (SOUL.md, docs/, memory/) and session data
- **Local:** `ox import` (data/), daemon (`EnsureCheckoutGitignore`), `ox remember` (memory/), direct user edits

**NEVER discard uncommitted changes.** Use `--autostash` on pulls. During blue-green GC reclone, carry dirty files from old clone to new.

## Git Operations in Sparse-Checkout Repos

- All `git add` MUST use `--sparse` (git 2.37+ refuses staging outside sparse definition otherwise)
- All `git pull --rebase` MUST use `--autostash` (uncommitted changes block pull otherwise)
- Use `git add -f` for files inside `.gitignore`-excluded paths

## Ephemeral-mode exception

When `ephemeral.IsEphemeral()` is true (`OX_EPHEMERAL=1`, user-config opt-in,
or auto-detected via `CLAUDE_CODE_REMOTE`, `DEVIN_TASK_ID`, `CODESPACES`, CI
signals), the daemon does not run and the daemon-CLI split has no daemon
side. The CLI performs **reads via HTTP API** (team context via
`GET /api/v1/teams/:id/context`, ledger metadata via
`GET /api/v1/repos/:id/ledger-status`) and **writes via HTTP** (Phase 2:
session upload + LFS Batch API). Pull-direction git operations are skipped
entirely; the local ledger clone never exists.

See `docs/adr/adr-ephemeral-mode.md` for the full rationale and rollout phases.
