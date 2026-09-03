# ADR-030: Per-clone serialization of git operations

**Status:** Accepted — implemented in [PR #868](https://github.com/sageox/ox/pull/868), merged 2026-09-03
**Date:** 2026-09-02
**Supersedes:** nothing
**Related:** ADR-007 (direct LFS, amended alongside this), `adr-ledger-architecture.md`
("Git operations are split between daemon and CLI" — the daemon owns read-side clone/fetch/pull),
`adr-ephemeral-mode.md`, `.claude/rules/daemon-git.md`,
`docs/coes/2026-09-02-daemon-git-sync-race-and-lfs-divergence.md`

---

## Context

ox drives git against managed clones — the ledger and every team context — from more than
one actor at a time:

- the daemon's sync scheduler (`internal/daemon/sync_managed.go`),
- the daemon's GC / wedge probe on its own schedule (`internal/daemon/sync_gc.go`),
- the daemon's doctor pass (`autofix RunNow`),
- the CLI, in a separate process: `ox status` (`internal/ledger/ledger.go`), `ox doctor
  --fix` (`cmd/ox/doctor_ledger_git.go`), `ox sync`, push-with-rebase paths.

`.claude/rules/daemon-git.md` divides *intent* — the daemon reads (pull), the CLI writes
(add/commit/push) — but a `git fetch` is a write to `.git/FETCH_HEAD`, and git itself does
not lock that file. Two overlapping fetches interleave their appends and leave two
merge-eligible heads, after which `git pull --rebase` refuses with *Cannot rebase onto
multiple branches*. This is reproducible on demand (COE 2026-09-02, 5/5 trials) and has been
possible since v0.1.0.

The existing guards do not cover it:

- `gitutil.MinFetchHeadAge` (30 s) skips a fetch if one happened *recently*. It cannot stop a
  fetch that starts *during* another one.
- `gitutil.HasLockFiles` / `RemoveStaleLockFiles` detect git's own lock files. `FETCH_HEAD`
  has none.
- The daemon's pull-failure recovery (`rebase --skip` / `AuditAndAbort`) assumes it is the
  only actor and will act on a rebase directory created by a different process.

The same shape caused the April 2026 COE (silent autostash-pop conflict committed to the
ledger). Both incidents are a `--quiet` git pipeline whose error path assumes a single actor.

## Decision

### D1 — One advisory lock per clone, held across every mutating git operation

Every fetch, pull, rebase, stash, checkout, commit, and push that ox runs against a managed
clone runs inside `gitutil.WithRepoLock(clonePath, fn)`. The lock is an exclusive `flock(2)`
keyed on `<clone>/.git/ox-sync`, reusing `internal/fileutil`'s existing advisory-lock
primitive (the same one session `meta.json` and `config.local.toml` writers use). The
*physical* lock file is **not** written inside the clone: `fileutil.LockPath` puts it in the
OS tmpdir, hashed from the key path — matching that package's existing rationale (a lock file
inside a git-tracked tree risks getting committed, confuses `ls -a`, and is a state-divergence
risk under blue-green GC reclone). It is process-shared by construction — the daemon and the
CLI are different processes and both must take it.

`flock` is chosen over a lockfile-with-age scheme because the kernel releases it when the
holder dies. A crashed holder cannot wedge the clone, so no stale-lock reaper is needed and
none is written.

Read-only plumbing (`rev-parse`, `status`, `log`, `ls-files`, `show`) stays lock-free.

### D2 — A pull that fails on a transient race retries before it recovers

When `git pull --rebase` fails with `Cannot rebase onto multiple branches`, the daemon
re-fetches once (under the lock) and retries the pull before entering the conflict-recovery
ladder. The error is provably transient — it describes `FETCH_HEAD`, not the branch.

### D3 — Recovery acts only on state this operation created

The pull-failure ladder (`rebase --skip`, `rebase --abort`, `AuditAndAbort`) runs only when
the rebase directory did not exist before the pull and exists after it. A rebase directory
that was already present belongs to someone else and is left alone. `gitutil.RebaseAge()`
already encodes "a fresh rebase is not ours to abort" for the pre-existing-rebase path; this
extends the same rule to the failure path.

### D4 — Every issue the daemon raises has a clear path

A `DaemonIssue` type may not be added without the code path that clears it on recovery.
`IssueTypeRebaseStuck` and the sync-suspended warning are cleared on the next successful
pull, matching `IssueTypeDirtyWorkspace` / `IssueTypeGCFailed`.

## Alternatives considered

- **In-process mutex only.** Serializes the daemon's own goroutines but not the CLI. The
  observed race had the CLI on one side. Rejected.
- **Widen `MinFetchHeadAge`.** Reduces the window; does not close it. A fetch is not
  idempotent while another is running. Rejected.
- **Route every CLI git operation through daemon IPC.** Correct in principle; contradicts
  `docs/specs/ipc-architecture.md` (IPC is never required, clone has a fallback) and
  ephemeral mode (no daemon). The lock works in every mode. Rejected for now; the lock does
  not preclude it later.
- **Lockfile with mtime-based staleness.** Needs a reaper, a threshold, and a story for
  clock skew; `flock` needs none. Rejected.

## Consequences

**Benefits**

- The FETCH_HEAD race and every derivative of it (latched error, foreign-rebase abort) become
  impossible rather than rare.
- CLI and daemon can keep their current division of labour; only the primitive changes.
- A single, named place to look when "two ox things touched one clone" comes up again.
- Nothing new is written inside the clone — the physical lock file lives in the OS tmpdir
  (`fileutil.LockPath`), so there is no new file to `.gitignore`, no risk of it being
  committed, and no interaction with sparse-checkout or blue-green GC reclone.

**Tradeoffs**

- A long daemon operation (large fetch, GC) now blocks a concurrent `ox status` for its
  duration instead of racing it. Acceptable: `ox status` already skips the fetch when
  `FETCH_HEAD` is fresh, and a blocked-but-correct status beats a fast-but-wrong one.
- `flock` is advisory. A non-ox process (a user running raw git in the ledger clone) does not
  take it. That is the existing state of the world, not a regression, and
  `.claude/rules/daemon-git.md` already says ledger content is accessed through ox.
- `internal/fileutil`'s cross-process `flock` is a no-op on Windows today (`flock_windows.go`
  documents this: "we currently don't ship to Windows, but keep the build green"); only the
  in-process gate serializes there. Not a regression given that existing scope, but worth
  knowing before this primitive backs anything that does ship on Windows.

## References

- COE: `docs/coes/2026-09-02-daemon-git-sync-race-and-lfs-divergence.md`
- Issues: bd `ox-baz5.1` (D1, D2), `ox-baz5.2` (D3), `ox-baz5.3` (D4)
- Implementation: [PR #868](https://github.com/sageox/ox/pull/868) — merged (`a7466f08`) —
  `internal/gitutil/repolock.go` (`WithRepoLock`), `internal/fileutil/flock_inprocess.go`
  (made context/deadline-aware to support D1: two of the four lock sites are goroutines in
  the same daemon process, not separate processes, so the in-process gate needed the same
  ctx/timeout the cross-process flock already honored). D3 was also applied to
  `cmd/ox/doctor_ledger_git.go`'s `fixLedgerBranchBehind` — found missing there in review,
  see the COE's "Post-merge" section.
- Precedent for flocked read-modify-write in this codebase: `internal/daemon/terminal_handler.go`
