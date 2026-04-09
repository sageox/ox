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
| `git add --sparse` | CLI | Session upload, import, doctor |
| `git commit` | CLI | Session upload pipeline |
| `git push` | CLI | Session upload pipeline |

The daemon only performs git pull (read) operations. The CLI performs add/commit/push (write) operations directly on the ledger.

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
