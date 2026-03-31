<!-- doc-audience: ai -->
# Project Watcher (Watchman-Inspired File Change Detection)

Daemon-only subsystem that watches project files for changes and publishes
file-change murmurs so other coworkers know what's actively being modified.

## Architecture

```
 ┌─────────────────────────────────────────────────────────────────┐
 │                        OS Kernel                                │
 │                  (inotify / kqueue / FSEvents)                  │
 └──────────────────────────┬──────────────────────────────────────┘
                            │ raw events
                            ▼
 ┌──────────────────────────────────────────────────────────────┐
 │  ProjectWatcher                    project_watcher.go        │
 │                                                              │
 │  • At startup, queries git ls-files to build tracked set     │
 │  • Only watches dirs containing git-tracked files            │
 │  • Refreshes tracked set every 30s                           │
 │  • Caps at 10,000 watched directories                        │
 │  • Auto-watches newly created dirs, removes deleted ones     │
 │  • Filters events: only tracked files + new creates pass     │
 └──────────────────────────┬───────────────────────────────────┘
                            │ classified events (relPath, op, isDir)
                            ▼
 ┌──────────────────────────────────────────────────────────────┐
 │  ChangeAccumulator                 project_watcher.go        │
 │                                                              │
 │  • Batches events with 3-second settle period                │
 │  • Collapses duplicates (Watchman-inspired rules):           │
 │      create + delete  → suppressed (temp file)               │
 │      create + modify  → created                              │
 │      delete + create  → modified (atomic save)               │
 │  • Pending → settled when 3s of quiet elapses                │
 └──────────────────────────┬───────────────────────────────────┘
                            │ settled []FileChange
                            ▼
 ┌──────────────────────────────────────────────────────────────┐
 │  FileChangeMurmurPublisher         file_change_source.go     │
 │                                                              │
 │  • Drains accumulator every 5s into pending buffer            │
 │  • Publishes batched murmur every 10 minutes                 │
 │  • Collapses duplicate paths (create+delete = suppressed)    │
 │  • Startup cap: drops changes older than 30 minutes          │
 │  • Adds [branch@worktree] context tag                        │
 │  • Formats changes as compact text:                          │
 │      1-5 files   → inline listing                            │
 │      6-20 files  → dir groups with M/A/D counts              │
 │      21-100      → top 5 dirs by count                       │
 │      100+        → ultra-terse "N files across M dirs"       │
 │  • Publishes as murmur to ledger (topic: file-changes)       │
 │  • No changes = no murmur (silent when idle)                 │
 └──────────────────────────┬───────────────────────────────────┘
                            │ PublishMurmur → writes JSON + git add/commit
                            ▼
 ┌──────────────────────────────────────────────────────────────┐
 │  Ledger (data/murmurs/YYYY-MM-DD/HH/<uuid>.json)            │
 │                                                              │
 │  → git push syncs to remote                                  │
 │  → other daemons pull + MurmurRelay delivers as whispers     │
 └──────────────────────────────────────────────────────────────┘
```

## Key Files

| File | Purpose |
|------|---------|
| `internal/daemon/project_watcher.go` | `ProjectWatcher`, `ChangeAccumulator`, `IgnoreMatcher` |
| `internal/daemon/file_change_source.go` | `FileChangeMurmurPublisher`, change formatting |
| `internal/daemon/daemon.go` | Wiring in `startWorkers()` |

## Daemon Integration

Wired in `daemon.go:startWorkers()`, only when both `ProjectRoot` and `LedgerPath` are set:

1. Creates `ChangeAccumulator` (3s settle)
2. Creates `GitTrackedMatcher` for project root (queries `git ls-files`)
3. Creates `ProjectWatcher` → starts in its own goroutine
4. Creates `FileChangeMurmurPublisher` → publishes murmurs to ledger via `PublishMurmur`

## Path Filtering

**Git-tracked only:** Uses `git ls-files --cached` to build the set of tracked
files and their parent directories. Only these directories are watched. The set
is refreshed every 30 seconds to pick up newly committed files.

**Always skipped:** `.git` directory and its children (hardcoded).

**Event filtering:** Only tracked files and `Create` events (new files that may
become tracked) pass through to the accumulator. Untracked file modifications
are silently dropped.

## Murmur Output

Each murmur includes branch and worktree context:

```
branch: ajit/feature-branch worktree: myproject
Files changed:
- modified: internal/daemon/project_watcher.go
- created: internal/daemon/file_change_source.go
```

Metadata (for programmatic consumers):
```json
{"branch": "ajit/feature-branch", "worktree": "/full/path", "files": "a.go,b.go", "file_count": "2"}
```

Topic: `file-changes`. Importance: `ambient` (≤10 files), `normal` (>10 files).

## Logs

| Level | Message | When |
|-------|---------|------|
| Info | `project watcher started root=... dirs_watched=N` | Startup |
| Debug | `project watcher: watching dir dir=...` | Each watched dir at startup (sorted) |
| Debug | `git-tracked matcher refreshed tracked_dirs=N tracked_files=N` | On refresh |
| Warn | `project watcher: reached directory watch limit max=10000` | Hit cap |
| Debug | `project watcher: failed to watch dir path=... error=...` | Permission/OS error |
| Debug | `file change murmur published file_count=N branch=...` | Murmur published |
| Error | `project watcher error error=...` | fsnotify error |
| Info | `project watcher stopped` | Context canceled |
