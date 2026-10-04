package gitutil

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PushOpts configures push behavior for PushWithRetry.
type PushOpts struct {
	// AutoResolvePrefixes lists path prefixes where accept-theirs conflict
	// resolution is safe (e.g., "data/github/", "data/murmurs/").
	// Empty means no auto-resolve — rebase failures abort immediately.
	AutoResolvePrefixes []string

	// AutoResolveDenyPrefixes lists path prefixes excluded from auto-resolution.
	// These carve out exceptions from AutoResolvePrefixes using most-specific-wins
	// semantics — e.g., deny "data/proprietary/" while allowing "data/".
	AutoResolveDenyPrefixes []string

	// PrePush is called before the push loop starts (after lock/LFS checks).
	// Use for credential refresh or other caller-specific setup.
	// Non-nil errors are logged as warnings but do not prevent the push attempt.
	PrePush func(repoPath string) error

	// ReconcileLFS is called when a push fails with "LFS objects are missing".
	// If set, PushWithRetry calls this instead of failing permanently, then
	// retries the push once. This allows the caller to wire lfs.ReconcileUnpushedPointers
	// (which strips orphaned pointer stubs and squashes history) without creating
	// an import cycle between gitutil and lfs.
	// Returns (true, nil) if reconciliation made changes worth retrying.
	// Returns (false, err) if reconciliation failed — err is logged and the
	// original push error is returned to the caller with the reconciliation
	// error appended for diagnostics.
	ReconcileLFS func(repoPath string) (changed bool, err error)

	// OnUnresolvedConflicts is called when pull --rebase halts AND
	// AutoResolvePrefixes-based accept-theirs cannot resolve every conflicted
	// path. Receives the list of conflicted paths. If it returns (true, nil),
	// the rebase has been resolved (rebase --continue ran inside the callback)
	// and PushWithRetry continues the retry loop. If it returns (false, nil) or
	// (false, err), PushWithRetry aborts the rebase and returns an error.
	//
	// Use this to wire higher-tier resolution (e.g. LLM merge) without coupling
	// gitutil to those packages.
	OnUnresolvedConflicts func(ctx context.Context, repoPath string, paths []string) (resolved bool, err error)

	// MaxRetries is the number of push attempts. Zero means use default (3).
	// To attempt exactly once with no retries, set to 1.
	MaxRetries int

	// OpTimeout is the timeout per git operation (default 60s).
	OpTimeout time.Duration

	// Logger for push diagnostics (defaults to slog.Default).
	Logger *slog.Logger

	// SuspendWhenWedged turns on the push circuit breaker for this call: a push
	// rejected for missing LFS objects that ReconcileLFS cannot repair suspends
	// further pushes to the repo (ErrPushWedged), and those pushes are skipped
	// until the backoff elapses. For long-lived callers — the daemon, whose many
	// pushers would otherwise repeat the same rejection and repair every tick.
	// User-initiated callers leave it false: a retry after the user fixed
	// something is a new situation and must always reach the remote.
	SuspendWhenWedged bool

	// breaker overrides the process-wide push circuit breaker (and implies
	// SuspendWhenWedged). Tests inject one with a fake clock; production leaves
	// it nil.
	breaker *pushBreaker
}

func (o *PushOpts) maxRetries() int {
	if o.MaxRetries > 0 {
		return o.MaxRetries
	}
	return 3
}

func (o *PushOpts) opTimeout() time.Duration {
	if o.OpTimeout > 0 {
		return o.OpTimeout
	}
	return 60 * time.Second
}

func (o *PushOpts) logger() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.Default()
}

// pushBreaker returns the breaker this push consults, or nil when the caller
// did not opt in. A nil breaker is inert: every method below is a no-op on it.
func (o *PushOpts) pushBreaker() *pushBreaker {
	switch {
	case o.breaker != nil:
		return o.breaker
	case o.SuspendWhenWedged:
		return defaultPushBreaker
	default:
		return nil
	}
}

// ErrPushWedged is returned (wrapped) by PushWithRetry when a repo's push is
// known to be blocked by LFS objects the remote does not have and the LFS repair
// could not unblock it. While the breaker is open PushWithRetry returns this
// without running git, so every pusher stops re-running the same expensive
// rejected push and repair. Match it with errors.Is.
var ErrPushWedged = errors.New("ledger push wedged")

const (
	// pushWedgeBackoffBase is how long the breaker stays open after it first
	// trips. Each further trip without an intervening successful push doubles it.
	pushWedgeBackoffBase = 5 * time.Minute
	// pushWedgeBackoffMax caps the doubling so a wedge a human fixes resumes
	// within an hour.
	pushWedgeBackoffMax = time.Hour
)

// pushBreaker is a per-repo circuit breaker for pushes rejected as "LFS objects
// are missing" that the reconcile callback could not repair. State is in memory
// only: a daemon restart is a fresh attempt, which is the right default.
type pushBreaker struct {
	now func() time.Time

	mu    sync.Mutex
	state map[string]pushWedgeState // keyed by cleaned absolute repo path
}

type pushWedgeState struct {
	trips int
	until time.Time
}

func newPushBreaker(now func() time.Time) *pushBreaker {
	return &pushBreaker{now: now, state: make(map[string]pushWedgeState)}
}

// defaultPushBreaker is shared by every pusher in the process, because the
// wedge belongs to the repo, not to whichever caller happened to hit it first.
var defaultPushBreaker = newPushBreaker(time.Now)

// PushWedgedUntil reports whether pushes to repoPath are currently suspended and
// until when. Read-only, for status surfaces and callers that want to skip work
// that only matters when a push can follow.
func PushWedgedUntil(repoPath string) (time.Time, bool) {
	return defaultPushBreaker.blockedUntil(repoPath)
}

func pushBreakerKey(repoPath string) string {
	if abs, err := filepath.Abs(repoPath); err == nil {
		repoPath = abs
	}
	return filepath.Clean(repoPath)
}

// blockedUntil returns the end of the open window, or false once it has elapsed.
// An elapsed window leaves the trip count in place: the next push is a probe,
// and failing it doubles the backoff instead of restarting at the base.
func (b *pushBreaker) blockedUntil(repoPath string) (time.Time, bool) {
	if b == nil {
		return time.Time{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.state[pushBreakerKey(repoPath)]
	if !ok || !b.now().Before(st.until) {
		return time.Time{}, false
	}
	return st.until, true
}

// trip opens the breaker and returns when it will next allow a probe push.
func (b *pushBreaker) trip(repoPath string) time.Time {
	if b == nil {
		return time.Time{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	key := pushBreakerKey(repoPath)
	st := b.state[key]
	st.trips++
	backoff := pushWedgeBackoffMax
	if shift := st.trips - 1; shift < 10 {
		backoff = min(pushWedgeBackoffBase<<shift, pushWedgeBackoffMax)
	}
	st.until = b.now().Add(backoff)
	b.state[key] = st
	return st.until
}

// clear forgets the wedge: a push got through, so whatever blocked it is gone.
func (b *pushBreaker) clear(repoPath string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.state, pushBreakerKey(repoPath))
}

// permanentPatterns are error strings that indicate retrying won't help.
var permanentPatterns = []string{
	"Permission denied",
	"could not read Username",
	"Authentication failed",
	"invalid credentials",
	"repository not found",
	"The requested URL returned error: 403",
	"HTTP 403",
}

// lfsObjectsMissing is the GitLab pre-receive error for orphaned LFS pointers.
// Handled separately from permanentPatterns because ReconcileLFS can fix it.
const lfsObjectsMissing = "LFS objects are missing"

// PushWithRetry pushes a git repo to its remote with pre-flight checks,
// retry, conflict resolution, and backoff.
//
// SAFETY: Force push (--force, --force-with-lease) is banned. All push
// conflicts are resolved via pull --rebase. Our git remotes reject force
// pushes server-side, so any force push attempt would fail anyway.
//
// Pre-flight: lock/rebase safety, LFS config cleanup, optional credential refresh.
//
// Retry loop: up to MaxRetries attempts with linear backoff (1s, 2s, 3s...).
// On non-fast-forward rejection: pulls with --rebase --autostash, optionally
// auto-resolves conflicts for paths in AutoResolvePrefixes.
// The retry pull acquires WithRepoLock; callers and conflict hooks must not
// acquire that same non-reentrant lock around this call or inside the hook.
func PushWithRetry(ctx context.Context, repoPath string, opts PushOpts) error {
	log := opts.logger()
	breaker := opts.pushBreaker()

	// Before anything that costs: a wedged repo rejects this push for the same
	// reason it rejected the last one, and the repair that follows takes minutes.
	if until, wedged := breaker.blockedUntil(repoPath); wedged {
		log.Debug("push skipped: ledger push wedged", "repo", repoPath, "until", until.Format(time.RFC3339))
		return fmt.Errorf("%w: skipping push until %s", ErrPushWedged, until.Format(time.RFC3339))
	}

	// pre-flight: check for lock files and broken rebase state
	if err := IsSafeForGitOps(repoPath); err != nil {
		return fmt.Errorf("repo blocked: %w", err)
	}

	// pre-flight: strip lfs.repositoryformatversion if set by git-lfs
	StripLFSConfig(repoPath)

	// caller-provided pre-push hook (e.g., credential refresh)
	if opts.PrePush != nil {
		if err := opts.PrePush(repoPath); err != nil {
			log.Warn("pre-push hook failed", "error", err)
		}
	}

	maxRetries := opts.maxRetries()
	opTimeout := opts.opTimeout()
	var lastOut string

	for attempt := 1; attempt <= maxRetries; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, opTimeout)
		outStr, err := RunGit(attemptCtx, repoPath, "push", "--quiet")
		cancel()
		if err == nil {
			breaker.clear(repoPath)
			return nil
		}
		lastOut = outStr

		// LFS objects missing — try reconciliation before giving up.
		// ReconcileLFS strips orphaned pointer stubs and squashes history so the
		// poisoned blobs no longer appear in the push pack.
		//
		// When the repair errors or changes nothing the push cannot succeed on
		// any retry, so trip the breaker: every pusher then skips this repo until
		// the backoff elapses instead of repeating the rejection and the repair.
		// Without a ReconcileLFS callback nothing was tried, so nothing is
		// remembered — another pusher that does have a repair must still get its
		// chance.
		if strings.Contains(outStr, lfsObjectsMissing) {
			if opts.ReconcileLFS == nil {
				return fmt.Errorf("git push failed (not retryable): %s", outStr)
			}
			log.Info("push failed (LFS objects missing), attempting reconciliation", "attempt", attempt)
			changed, reconcileErr := opts.ReconcileLFS(repoPath)
			if reconcileErr != nil {
				log.Warn("lfs reconciliation failed", "error", reconcileErr)
				// don't surface reconciliation internals to the user —
				// they see the push error, we log the reconciliation error
				return tripPushWedge(log, breaker, repoPath, outStr, "reconcile_failed")
			}
			if changed {
				log.Info("lfs reconciliation made changes, retrying push")
				continue // retry immediately
			}
			return tripPushWedge(log, breaker, repoPath, outStr, "reconcile_no_change")
		}

		// fail fast on permanent errors
		for _, pattern := range permanentPatterns {
			if strings.Contains(outStr, pattern) {
				if strings.Contains(outStr, "403") {
					return fmt.Errorf("git push failed: access denied (HTTP 403). Try 'ox login' to refresh credentials, or verify you have push access to this repository: %s", outStr)
				}
				return fmt.Errorf("git push failed (not retryable): %s", outStr)
			}
		}

		// rebase first — non-fast-forward is the most common retry case and
		// must be checked before LFS since push output can contain both
		// "non-fast-forward" and credential noise like "failed to store: -25300"
		isNonFF := strings.Contains(outStr, "non-fast-forward") || strings.Contains(outStr, "rejected")

		if isNonFF {
			log.Info("push failed (non-fast-forward), rebasing", "attempt", attempt, "output", outStr)
			if attempt == maxRetries {
				return fmt.Errorf("git push failed after %d attempts: %s", maxRetries, outStr)
			}
			// Keep the pull, rebase resolution, and autostash restoration under
			// the same clone lock as daemon pulls and doctor repairs.
			rebaseErr := WithRepoLock(ctx, repoPath, func() error {
				if IsRebaseInProgress(repoPath) {
					abortCtx, abortCancel := context.WithTimeout(ctx, opTimeout)
					_, _ = RunGit(abortCtx, repoPath, "rebase", "--abort")
					abortCancel()
				}

				pullCtx, pullCancel := context.WithTimeout(ctx, opTimeout)
				// A prior pull may have left autostash conflicts without an
				// active rebase. Never send those to the positional resolver.
				if _, err := ResolveAutostashConflicts(pullCtx, repoPath, opts.AutoResolvePrefixes, opts.AutoResolveDenyPrefixes); err != nil {
					pullCancel()
					return fmt.Errorf("restore autostash before pull: %w", err)
				}
				pullOut, pullErr := RunGit(pullCtx, repoPath, "pull", "--rebase", "--autostash", "--quiet")
				pullCancel()
				if pullErr != nil {
					if len(opts.AutoResolvePrefixes) > 0 {
						resolveCtx, resolveCancel := context.WithTimeout(ctx, opTimeout)
						resolveErr := ResolveRebaseAcceptTheirs(resolveCtx, repoPath, opts.AutoResolvePrefixes, opts.AutoResolveDenyPrefixes)
						resolveCancel()
						if resolveErr != nil {
							log.Debug("rebase auto-resolve failed", "error", resolveErr)

							// give the caller a chance to resolve via a higher tier
							// (e.g. LLM merge) before we abort. The hook owns the
							// rebase --continue if it succeeds.
							hookResolved := false
							if opts.OnUnresolvedConflicts != nil {
								pathsCtx, pathsCancel := context.WithTimeout(ctx, opTimeout)
								conflicted, listErr := listConflictedFiles(pathsCtx, repoPath)
								pathsCancel()
								// if we can't enumerate conflicts, the hook can't make
								// an informed decision (it'd see an empty list and
								// either falsely report "resolved" or operate on
								// stale state). Skip the hook and abort the rebase
								// rather than guess.
								if listErr != nil {
									log.Warn("listing conflicted files failed; skipping resolve hook", "error", listErr)
									abortCtx, abortCancel := context.WithTimeout(ctx, opTimeout)
									_, _ = RunGit(abortCtx, repoPath, "rebase", "--abort")
									abortCancel()
									return fmt.Errorf("git pull --rebase failed during retry: %s (could not list conflicts: %w)", pullOut, listErr)
								}
								hookCtx, hookCancel := context.WithTimeout(ctx, opTimeout)
								resolved, hookErr := opts.OnUnresolvedConflicts(hookCtx, repoPath, conflicted)
								hookCancel()
								// only treat as resolved when the hook succeeded AND
								// signaled resolved. a hook that returned an error
								// MAY have left the rebase index half-staged; we
								// must abort rather than continue retrying.
								if hookErr != nil {
									log.Warn("OnUnresolvedConflicts hook failed", "error", hookErr)
								} else if resolved {
									log.Info("resolved rebase conflicts via OnUnresolvedConflicts hook", "paths", conflicted)
									hookResolved = true
								}
							}

							if !hookResolved {
								abortCtx, abortCancel := context.WithTimeout(ctx, opTimeout)
								_, _ = RunGit(abortCtx, repoPath, "rebase", "--abort")
								abortCancel()
								return fmt.Errorf("git pull --rebase failed during retry: %s", pullOut)
							}
						} else {
							log.Info("auto-resolved rebase conflicts", "strategy", "accept-theirs")
						}
					} else {
						// no auto-resolve configured — abort and fail
						abortCtx, abortCancel := context.WithTimeout(ctx, opTimeout)
						_, _ = RunGit(abortCtx, repoPath, "rebase", "--abort")
						abortCancel()
						return fmt.Errorf("git pull --rebase failed during retry: %s", pullOut)
					}
				}
				// A successful pull (or rebase --continue) can still leave conflicts
				// when applying the autostash. Do not retry the push in that state.
				recoveryCtx, recoveryCancel := context.WithTimeout(ctx, opTimeout)
				defer recoveryCancel()
				if _, err := ResolveAutostashConflicts(recoveryCtx, repoPath, opts.AutoResolvePrefixes, opts.AutoResolveDenyPrefixes); err != nil {
					return fmt.Errorf("restore autostash after pull: %w", err)
				}
				return nil
			})
			if rebaseErr != nil {
				return rebaseErr
			}
		} else {
			if attempt == maxRetries {
				return fmt.Errorf("git push failed after %d attempts: %s", maxRetries, outStr)
			}
			log.Info("push failed, retrying", "attempt", attempt, "output", outStr)
		}

		// linear backoff before retry
		select {
		case <-time.After(time.Duration(attempt) * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Reached only when the last attempt was spent on a rejected push that
	// reconcile reported fixing: the retry the repair earned never ran. Report
	// the failure rather than a success the caller would act on by pruning the
	// only copy of the data.
	if strings.Contains(lastOut, lfsObjectsMissing) {
		return tripPushWedge(log, breaker, repoPath, lastOut, "reconcile_exhausted_attempts")
	}
	return fmt.Errorf("git push failed after %d attempts: %s", maxRetries, lastOut)
}

// tripPushWedge opens the breaker for repoPath and returns the error for the
// push that caused it. Logs exactly one warning per trip. With a nil breaker
// (caller did not opt in) it only formats the error.
func tripPushWedge(log *slog.Logger, breaker *pushBreaker, repoPath, pushOutput, reason string) error {
	if breaker == nil {
		// not opted in: report the rejection as before, remember nothing
		return fmt.Errorf("git push failed (not retryable): %s", pushOutput)
	}
	until := breaker.trip(repoPath)
	log.Warn("ledger push wedged: LFS objects missing and repair did not unblock it, suspending pushes",
		"repo", repoPath, "reason", reason, "until", until.Format(time.RFC3339))
	return fmt.Errorf("git push failed (not retryable): %s: %w", pushOutput, ErrPushWedged)
}
