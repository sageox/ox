package gitutil

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
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
func PushWithRetry(ctx context.Context, repoPath string, opts PushOpts) error {
	log := opts.logger()

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

	for attempt := 1; attempt <= maxRetries; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, opTimeout)
		outStr, err := RunGit(attemptCtx, repoPath, "push", "--quiet")
		cancel()
		if err == nil {
			return nil
		}

		// LFS objects missing — try reconciliation before giving up.
		// ReconcileLFS strips orphaned pointer stubs and squashes history so the
		// poisoned blobs no longer appear in the push pack. One shot only.
		if strings.Contains(outStr, lfsObjectsMissing) {
			if opts.ReconcileLFS != nil {
				log.Info("push failed (LFS objects missing), attempting reconciliation", "attempt", attempt)
				changed, reconcileErr := opts.ReconcileLFS(repoPath)
				if reconcileErr != nil {
					log.Warn("lfs reconciliation failed", "error", reconcileErr)
					// don't surface reconciliation internals to the user —
					// they see the push error, we log the reconciliation error
					return fmt.Errorf("git push failed (not retryable): %s", outStr)
				}
				if changed {
					log.Info("lfs reconciliation made changes, retrying push")
					continue // retry immediately
				}
			}
			return fmt.Errorf("git push failed (not retryable): %s", outStr)
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
			if IsRebaseInProgress(repoPath) {
				abortCtx, abortCancel := context.WithTimeout(ctx, opTimeout)
				_, _ = RunGit(abortCtx, repoPath, "rebase", "--abort")
				abortCancel()
			}

			pullCtx, pullCancel := context.WithTimeout(ctx, opTimeout)
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

	return nil // unreachable
}
