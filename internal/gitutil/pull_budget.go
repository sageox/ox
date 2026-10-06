package gitutil

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// Pull time budget. `git pull --rebase --autostash` replays every unpushed
// commit, so its runtime grows with how far ahead of upstream the clone is: a
// ledger 550 commits ahead takes 1 to 10 minutes, far past the 60s cycle
// timeout. The cycle timeout still bounds the fetch (a hung network fails
// inside 60s); only the local rebase replay gets the scaled budget.
const (
	pullBaseTimeout = 60 * time.Second
	pullPerCommit   = time.Second
	pullMaxTimeout  = 15 * time.Minute

	// longBacklogAhead is the unpushed-commit count at which the scaled budget
	// (base + 30s) is worth detaching from the cycle deadline for.
	longBacklogAhead = 30

	// abortRecoveryTimeout bounds the abort ladder that runs after a pull
	// timeout; the pull context is already expired by then.
	abortRecoveryTimeout = 30 * time.Second
)

// PullBudget returns how long a pull may run when the clone is `ahead`
// commits ahead of upstream: base + perCommit*ahead, capped.
func PullBudget(ahead int) time.Duration {
	if ahead < 0 {
		ahead = 0
	}
	budget := pullBaseTimeout + time.Duration(ahead)*pullPerCommit
	return min(budget, pullMaxTimeout)
}

// CommitsAhead returns the number of local commits not on upstream. Cheap
// (`rev-list --count`); any failure (no upstream, unborn branch) reads as 0 so
// the pull keeps the default budget.
func CommitsAhead(ctx context.Context, repoPath string) int {
	out, err := RunGit(ctx, repoPath, "rev-list", "--count", "@{u}..HEAD")
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// PullContext returns the context for `git pull --rebase`. For a small backlog
// it is the cycle context unchanged. For a long one it is detached from the
// cycle's DEADLINE (but not its cancellation) and given PullBudget(ahead), so a
// legitimately long rebase is not killed mid-replay every cycle.
//
// shutdown is an optional long-lived context (the daemon's) that cancels the
// pull even after the cycle context has already expired, which parent alone
// cannot report. Pass nil when there is none.
func PullContext(parent context.Context, ahead int, shutdown context.Context) (context.Context, context.CancelFunc) {
	if ahead < longBacklogAhead {
		return parent, func() {}
	}
	budget := PullBudget(ahead)
	if dl, ok := parent.Deadline(); ok && time.Until(dl) >= budget {
		return parent, func() {}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), budget)
	// honor daemon shutdown / caller cancel, but not the cycle deadline
	stop := context.AfterFunc(parent, func() {
		if errors.Is(parent.Err(), context.Canceled) {
			cancel()
		}
	})
	stopShutdown := func() bool { return true }
	if shutdown != nil {
		stopShutdown = context.AfterFunc(shutdown, cancel)
	}
	return ctx, func() {
		stop()
		stopShutdown()
		cancel()
	}
}

// RecoverPullTimeoutInRebase runs when the pull context expired. The pull's
// process group was killed, which leaves .git/rebase-merge (or rebase-apply)
// behind. Left alone, the next cycle sees a "wedged" rebase and every session
// commit fails until it ages out; abort it now, in the same cycle, rescuing
// any commits that exist only on a detached HEAD first.
//
// Returns whether a rebase was found and whether it was cleared.
func RecoverPullTimeoutInRebase(parent context.Context, path, repoName string, ahead int, logger *slog.Logger) (found bool, abortErr error) {
	if !IsRebaseInProgress(path) {
		return false, nil
	}
	// the pull context is expired; recovery needs its own budget
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), abortRecoveryTimeout)
	defer cancel()
	rescueRef, abortErr := RescueIfNeededThenAbort(ctx, path, "pull timed out during rebase", logger)
	if abortErr != nil {
		logger.Error("pull timed out during rebase; abort failed",
			"op", "pull_timeout_abort_failed", "repo", repoName, "ahead", ahead,
			"rescue_ref", rescueRef, "error", abortErr)
		return true, fmt.Errorf("abort rebase after pull timeout: %w", abortErr)
	}
	logger.Warn("pull timed out during rebase; aborted",
		"op", "pull_timeout_rebase_aborted", "repo", repoName, "ahead", ahead, "rescue_ref", rescueRef)
	return true, nil
}

// PullTimedOut reports whether a failed pull failed because its context
// deadline expired (as opposed to a git error or a caller cancel).
func PullTimedOut(pullCtx context.Context, pullErr error) bool {
	return pullErr != nil && pullCtx.Err() != nil && errors.Is(context.Cause(pullCtx), context.DeadlineExceeded)
}
