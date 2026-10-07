package gitutil

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// SquashUnpushed collapses every unpushed local commit into one commit with
// the same tree. The pre-squash tip is kept under refs/ox-backup/ so the
// per-commit history can always be recovered. A branch that has diverged from
// its upstream is refused: a soft reset to an upstream HEAD does not contain
// would commit the index back over the commits HEAD lacks, reverting a
// coworker's work. Nothing to do when zero or one commit is unpushed.
//
// The caller must hold WithRepoLock: CommitLedgerSnapshot relies on it.
func SquashUnpushed(ctx context.Context, repoPath, commitMsg string) error {
	upCtx, upCancel := context.WithTimeout(ctx, 5*time.Second)
	upstream, err := RunGit(upCtx, repoPath, "rev-parse", "--verify", "@{upstream}")
	upCancel()
	if err != nil {
		return fmt.Errorf("no upstream tracking ref: %w", err)
	}
	upstream = strings.TrimSpace(upstream)

	ancestorCtx, ancestorCancel := context.WithTimeout(ctx, 5*time.Second)
	_, ancestorErr := RunGit(ancestorCtx, repoPath, "merge-base", "--is-ancestor", upstream, "HEAD")
	ancestorCancel()
	if ancestorErr != nil {
		return fmt.Errorf("upstream is not an ancestor of HEAD (branch diverged; pull first): %w", ancestorErr)
	}

	originalCtx, originalCancel := context.WithTimeout(ctx, 5*time.Second)
	original, originalErr := RunGit(originalCtx, repoPath, "rev-parse", "--verify", "HEAD")
	originalCancel()
	if originalErr != nil {
		return fmt.Errorf("resolve original HEAD: %w", originalErr)
	}
	original = strings.TrimSpace(original)

	countCtx, countCancel := context.WithTimeout(ctx, 5*time.Second)
	countOut, err := RunGit(countCtx, repoPath, "rev-list", "--count", upstream+"..HEAD")
	countCancel()
	if err != nil {
		// not "nothing to squash": a caller that asked for a squash must learn
		// it did not happen, or the push stays wedged with no error
		return fmt.Errorf("count unpushed commits: %w", err)
	}
	if count := strings.TrimSpace(countOut); count == "0" || count == "1" {
		return nil
	}

	// keep the full history reachable before any ref moves
	backupRef := fmt.Sprintf("refs/ox-backup/pre-squash-%d", time.Now().UnixNano())
	backupCtx, backupCancel := context.WithTimeout(ctx, 5*time.Second)
	_, err = RunGit(backupCtx, repoPath, "update-ref", backupRef, original)
	backupCancel()
	if err != nil {
		return fmt.Errorf("keep pre-squash tip under %s: %w", backupRef, err)
	}

	resetCtx, resetCancel := context.WithTimeout(ctx, 5*time.Second)
	_, err = RunGit(resetCtx, repoPath, "reset", "--soft", upstream)
	resetCancel()
	if err != nil {
		return fmt.Errorf("reset --soft: %w", err)
	}

	squashCtx, squashCancel := context.WithTimeout(ctx, 10*time.Second)
	_, err = CommitLedgerSnapshot(squashCtx, repoPath, commitMsg)
	squashCancel()
	if err != nil {
		return restoreSoftReset(ctx, repoPath, original, fmt.Errorf("validate squash or commit: %w", err))
	}
	return nil
}

// restoreSoftReset moves HEAD back to original after a failed squash so the
// unpushed commits are not left collapsed into the index.
func restoreSoftReset(ctx context.Context, repoPath, original string, cause error) error {
	rollbackCtx, rollbackCancel := context.WithTimeout(ctx, 5*time.Second)
	defer rollbackCancel()
	if _, err := RunGit(rollbackCtx, repoPath, "reset", "--soft", original); err != nil {
		return fmt.Errorf("%w; restore original HEAD: %w", cause, err)
	}
	return cause
}
