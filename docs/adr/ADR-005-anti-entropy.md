# ADR-005: Anti-Entropy Over Transactions

**Status**: Accepted
**Date**: 2026-04-02

## Context

ox records AI coding sessions and uploads them to a team ledger (a git repo with LFS-backed content). The upload pipeline involves multiple steps: reading the agent's session file, computing SHA256 OIDs, uploading blobs to LFS, writing a `meta.json` manifest, committing to the ledger repo, and pushing to the remote.

Any step can fail: network drops mid-upload, the agent process is killed (Ctrl+C), the daemon crashes, LFS returns a transient 503, or git push fails due to a concurrent push from another machine.

A transactional approach would require two-phase commits, rollback logic, and distributed locking. This adds complexity disproportionate to the value — session recordings are valuable but not mission-critical. A lost session is recoverable (the agent's native session file still exists locally). A corrupted ledger state is not.

## Decision

### Best-Effort Pipeline, Repair on Failure

Session recording and upload are **best-effort**. The pipeline proceeds optimistically and fails open. When something goes wrong, `ox doctor` detects and repairs the inconsistency later.

### Pipeline Design

Each stage writes durable state before proceeding to the next:

```
1. Record entries    -> raw.jsonl (local, append-only)
2. Generate summary  -> summary.md, summary.json (local)
3. Upload blobs      -> LFS server (content-addressed, idempotent)
4. Write manifest    -> meta.json (local, in ledger checkout)
5. Commit            -> ledger git repo (local)
6. Push              -> remote (network)
```

If the pipeline fails at step N, steps 1 through N-1 are already persisted. No rollback needed. The next attempt picks up from the last durable state.

### Local Cache as Authoritative Copy

Session content files (`raw.jsonl`, `summary.md`) are cached locally in the ledger's `.sageox/cache/sessions/` directory. This cache is the authoritative copy until upload is confirmed. It survives daemon crashes, network outages, failed pushes, and re-clones of the ledger repo.

### Retry Strategy

- **CLI push**: 3 retries with exponential backoff. If all fail, session is cached locally for later.
- **Daemon anti-entropy**: Periodically scans for orphaned sessions (recorded locally but not in remote). Re-uploads with fresh OIDs.
- **Doctor repair**: `ox doctor` detects missing remote sessions by comparing local cache against ledger state, triggers re-upload.

### Content-Addressed Idempotency

LFS uploads are idempotent by design — the OID is the SHA256 of the content. Uploading the same content twice is a no-op on the server. Retries are always safe, and there's no "partial upload" state to clean up.

### Session Finalization

When a session ends (Ctrl+C, `/ox-session-stop`, or agent exit):

1. CLI sets `StoppedAt` in recording state (durable marker)
2. CLI sends fire-and-forget IPC to daemon: `session_finalize`
3. Daemon picks up finalization asynchronously
4. If daemon is unavailable, anti-entropy catches it on next scan

The fire-and-forget pattern means the user's terminal returns immediately. Finalization happens in the background. If it fails, it retries.

## Consequences

**Benefits**:
- No distributed transactions, no two-phase commits, no rollback logic
- Pipeline failures are localized — each stage is independently recoverable
- Content-addressed storage makes retries inherently safe
- User never waits for network I/O on session stop (fire-and-forget)
- Doctor provides a single repair command for all failure modes

**Tradeoffs**:
- Sessions may be delayed in reaching the remote ledger. Teammates don't see them until upload succeeds. Acceptable — sessions are historical records, not real-time collaboration (murmurs handle real-time).
- Local cache can grow if uploads consistently fail (offline for days). Managed by cache size limits and periodic cleanup.
- Doctor must know about every failure mode. Each new pipeline stage needs a corresponding doctor check.
- Race condition window: two machines uploading the same session simultaneously could create duplicate meta.json entries. Content-addressing makes this harmless (same OID = same content). Doctor deduplicates.
