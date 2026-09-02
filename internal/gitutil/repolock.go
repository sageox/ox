package gitutil

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/fileutil"
)

// RepoLockTimeout bounds how long WithRepoLock waits for a peer to release a
// managed clone. A fetch or pull on a large ledger can legitimately run for
// tens of seconds; two minutes covers that with margin while still failing a
// genuinely wedged peer fast enough that the daemon's next cycle retries.
// Callers with a tighter budget (a CLI status check) bound the context
// instead — the wait honors ctx.Done().
const RepoLockTimeout = 2 * time.Minute

// WithRepoLock serializes git operations that mutate a managed clone —
// fetch, pull, rebase, checkout, commit, push — across every ox process on
// the machine: the daemon's sync scheduler, its GC / wedge probe, its doctor
// pass, and any concurrently running CLI (ox status, ox doctor --fix, ox
// sync, session upload). ADR-030 D1.
//
// Why a lock and not the existing dedup heuristics: git writes
// .git/FETCH_HEAD with no lock of its own. Two overlapping fetches interleave
// their appends and leave two merge-eligible heads, after which
// `git pull --rebase` refuses with "Cannot rebase onto multiple branches".
// MinFetchHeadAge skips a fetch that happened *recently*; it cannot stop one
// that starts *during* another. HasLockFiles detects git's own lock files;
// FETCH_HEAD has none. Reproduced 5/5 on a real ledger (COE 2026-09-02).
//
// The lock is an advisory flock on a sidecar keyed by <clone>/.git/ox-sync
// (fileutil.LockPath puts the physical file in the OS tmpdir, so nothing is
// ever left inside the clone). flock is released by the kernel when the
// holder dies, so a crashed process cannot wedge the clone and no stale-lock
// reaper exists. It is advisory: a human running raw git in the clone does
// not take it, which is the pre-existing state of the world.
//
// Not re-entrant. A caller already inside WithRepoLock must not call it
// again for the same clone (the in-process mutex would deadlock, and the
// deadline would then surface it as ErrRepoLockBusy). Keep read-only
// plumbing (status, rev-parse, log, ls-files, show) outside the lock.
func WithRepoLock(ctx context.Context, repoPath string, fn func() error) error {
	return fileutil.WithFileLockTimeout(ctx, RepoLockTarget(repoPath), RepoLockTimeout, fn)
}

// RepoLockTarget is the path the clone's advisory lock is keyed on. Exposed
// so tests can assert two callers contend on the same sidecar.
func RepoLockTarget(repoPath string) string {
	return filepath.Join(repoPath, ".git", "ox-sync")
}

// IsRepoLockBusy reports whether err means another ox process held the
// clone for longer than RepoLockTimeout (or the caller's context expired
// while waiting). Callers treat this as "skip this cycle", never as a repo
// fault — the clone is healthy, just in use.
func IsRepoLockBusy(err error) bool {
	if err == nil {
		return false
	}
	var timeout *fileutil.ErrLockTimeout
	return errors.As(err, &timeout) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}
