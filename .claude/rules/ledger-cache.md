---
paths:
  - "internal/codedb/**"
  - "internal/config/local_config.go"
---

# Ledger Cache for Local-Only Repo State

When a feature needs computed/derived data that should be shared across worktrees, persistent across re-clones, and local-only (never synced), store it in the ledger's `.sageox/cache/` directory:

```
~/.local/share/sageox/<endpoint>/ledgers/<repo_id>/.sageox/cache/<feature>/
```

This directory is gitignored. Resolve via `ProjectContext.DefaultLedgerPath()` or `config.DefaultLedgerPath(repoID, endpointURL)`.

| Storage Location | Use When |
|-----------------|----------|
| Project `.sageox/cache/` | Never — per-worktree, lost on re-clone |
| Ledger `.sageox/cache/` | Computed indexes, derived data, local caches |
| Ledger git-tracked | Sessions, data that syncs to cloud |
| XDG cache (`~/.cache/sageox/`) | User-level caches not tied to a specific repo |

Current consumer: `codedb` (SQLite + Bleve indexes).
