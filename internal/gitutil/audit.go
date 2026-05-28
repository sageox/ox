package gitutil

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// abortTimeout is the timeout for git --abort operations. Matches the
// 10s timeout used at existing abort call sites (internal/daemon/sync_managed.go).
const abortTimeout = 10 * time.Second

// AuditableOp is a git operation whose --abort behavior is audited.
type AuditableOp string

const (
	AuditOpRebase     AuditableOp = "rebase"
	AuditOpMerge      AuditableOp = "merge"
	AuditOpCherryPick AuditableOp = "cherry-pick"
)

// AuditAndAbort runs `git <op> --abort` on repoPath with structured logging
// before and after, so silent recovery from a wedged state leaves a clear
// audit trail. Per .claude/rules/daemon-git.md, daemon code must NEVER
// discard uncommitted changes without logging what was discarded.
//
// Pre-abort log fields: op=<op>_abort_pre, repo, reason, head_sha,
// unmerged_count, unmerged_sample (first 3 paths, comma-joined), stash_count.
// Post-abort log fields: op=<op>_abort_post, repo, head_sha_after,
// success=true. On failure: op=<op>_abort_failed, repo, error.
//
// Returns nil if the abort succeeded, or the abort error otherwise. Logger
// MUST be non-nil. Reason is a free-form string describing why the abort
// was triggered (e.g., "auto-resolve failed", "doctor --fix").
func AuditAndAbort(ctx context.Context, repoPath string, op AuditableOp, reason string, logger *slog.Logger) error {
	if logger == nil {
		// Defensive: callers should always supply a logger, but if they
		// don't, fall through to a discard handler rather than panic.
		logger = slog.New(slog.NewTextHandler(discardWriter{}, nil))
	}
	repoName := repoPath

	// capture state before abort
	headSHA := captureHeadSHA(ctx, repoPath)
	unmerged := captureUnmergedFiles(ctx, repoPath)
	stashCount := captureStashCount(ctx, repoPath)

	logger.Info("git_abort_pre",
		"op", string(op)+"_abort_pre",
		"repo", repoName,
		"reason", reason,
		"head_sha", headSHA,
		"unmerged_count", len(unmerged),
		"unmerged_sample", sampleJoin(unmerged, 3),
		"stash_count", stashCount,
	)

	// run abort in a fresh, bounded context — parent ctx may be canceled.
	abortCtx, cancel := context.WithTimeout(context.Background(), abortTimeout)
	defer cancel()

	abortCmd := exec.CommandContext(abortCtx, "git", "-C", repoPath, string(op), "--abort")
	output, abortErr := abortCmd.CombinedOutput()
	if abortErr != nil {
		logger.Error("git_abort_failed",
			"op", string(op)+"_abort_failed",
			"repo", repoName,
			"error", abortErr,
			"output", SanitizeOutput(strings.TrimSpace(string(output))),
			"head_sha", headSHA,
			"unmerged_count", len(unmerged),
			"unmerged_sample", sampleJoin(unmerged, 3),
		)
		return fmt.Errorf("git %s --abort: %w", op, abortErr)
	}

	headAfter := captureHeadSHA(ctx, repoPath)
	logger.Info("git_abort_post",
		"op", string(op)+"_abort_post",
		"repo", repoName,
		"head_sha_after", headAfter,
		"success", true,
	)
	return nil
}

// captureHeadSHA returns the current HEAD SHA, or "unknown" on any failure.
// Best-effort: never blocks or errors out the audit pipeline.
func captureHeadSHA(ctx context.Context, repoPath string) string {
	subCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(subCtx, "git", "-C", repoPath, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// captureUnmergedFiles returns the list of files with unmerged (conflicted)
// index entries via `git diff --name-only --diff-filter=U`. Returns nil on
// any failure.
func captureUnmergedFiles(ctx context.Context, repoPath string) []string {
	subCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(subCtx, "git", "-C", repoPath, "diff", "--name-only", "--diff-filter=U")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// captureStashCount returns the number of entries in `git stash list`.
// Daemon's `git pull --rebase --autostash` can produce a stash entry that
// gets left behind on abort — counting it surfaces silent data parked
// outside the working tree.
func captureStashCount(ctx context.Context, repoPath string) int {
	subCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(subCtx, "git", "-C", repoPath, "stash", "list")
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}

// sampleJoin returns up to n entries from paths joined by ", ".
func sampleJoin(paths []string, n int) string {
	if len(paths) == 0 {
		return ""
	}
	if len(paths) < n {
		n = len(paths)
	}
	return strings.Join(paths[:n], ", ")
}

// discardWriter satisfies io.Writer for the fallback logger.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
