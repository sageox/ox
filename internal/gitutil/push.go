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

	// AllowForceOnLFS enables --force-with-lease when the server rejects
	// due to missing LFS objects. Only safe for append-only repos (ledgers).
	AllowForceOnLFS bool

	// RepairLFS runs RepairMissingLFSObjects before pushing.
	RepairLFS bool

	// PrePush is called before the push loop starts (after lock/LFS checks).
	// Use for credential refresh or other caller-specific setup.
	// Non-nil errors are logged as warnings but do not prevent the push attempt.
	PrePush func(repoPath string) error

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

// PushWithRetry pushes a git repo to its remote with pre-flight checks,
// retry, conflict resolution, and backoff.
//
// Pre-flight: lock/rebase safety, LFS config cleanup, optional LFS repair,
// optional credential refresh.
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

	// pre-flight: repair missing LFS objects that would block push
	if opts.RepairLFS {
		if repaired, err := RepairMissingLFSObjects(ctx, repoPath); err != nil {
			log.Warn("lfs repair failed", "error", err)
		} else if repaired > 0 {
			log.Info("repaired missing LFS pointers before push", "count", repaired)
		}
	}

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
					resolveErr := ResolveRebaseAcceptTheirs(resolveCtx, repoPath, opts.AutoResolvePrefixes)
					resolveCancel()
					if resolveErr != nil {
						log.Debug("rebase auto-resolve failed", "error", resolveErr)
						abortCtx, abortCancel := context.WithTimeout(ctx, opTimeout)
						_, _ = RunGit(abortCtx, repoPath, "rebase", "--abort")
						abortCancel()
						return fmt.Errorf("git pull --rebase failed during retry: %s", pullOut)
					}
					log.Info("auto-resolved rebase conflicts", "strategy", "accept-theirs")
				} else {
					// no auto-resolve configured — abort and fail
					abortCtx, abortCancel := context.WithTimeout(ctx, opTimeout)
					_, _ = RunGit(abortCtx, repoPath, "rebase", "--abort")
					abortCancel()
					return fmt.Errorf("git pull --rebase failed during retry: %s", pullOut)
				}
			}
		} else if opts.AllowForceOnLFS && IsLFSPushError(outStr) {
			// server rejected due to missing LFS objects — force push is safe
			// for append-only repos (ledgers) where history is never rewritten
			log.Info("server rejected push due to missing LFS objects, force pushing")
			forceCtx, forceCancel := context.WithTimeout(ctx, opTimeout)
			forceOut, forceErr := RunGit(forceCtx, repoPath, "push", "--force-with-lease", "--quiet")
			forceCancel()
			if forceErr == nil {
				return nil
			}
			log.Warn("force push also failed", "output", forceOut)
			return fmt.Errorf("git push failed (LFS missing, force push failed): %s", forceOut)
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
