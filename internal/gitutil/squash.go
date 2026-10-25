package gitutil

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// beforeSquashCommitHook runs just before the squash commit. Tests use it to
// cancel the caller's context at that moment.
var beforeSquashCommitHook func()

// SquashUnpushed collapses every unpushed local commit into one commit with
// the same tree: HEAD's tree committed onto upstream, never the index, so
// anything only staged stays staged (#1442). The branch moves to it in one
// compare-and-swap. The pre-squash tip is kept under refs/ox-backup/ so the
// per-commit history can always be recovered. A branch that has diverged from
// its upstream is refused: its tree committed onto upstream would revert the
// coworker's commits it lacks. Nothing to do when zero or one commit is
// unpushed.
//
// The caller must hold WithRepoLock, so the daemon's pull cannot rebase the
// branch while the squash moves it.
func SquashUnpushed(ctx context.Context, repoPath, commitMsg string) error {
	upCtx, upCancel := context.WithTimeout(ctx, 5*time.Second)
	upstream, err := RunGit(upCtx, repoPath, "rev-parse", "--verify", "@{upstream}")
	upCancel()
	if err != nil {
		return fmt.Errorf("no upstream tracking ref: %w", err)
	}
	upstream = strings.TrimSpace(upstream)

	// the checks below and the final compare-and-swap all use this one read
	originalCtx, originalCancel := context.WithTimeout(ctx, 5*time.Second)
	original, originalErr := RunGit(originalCtx, repoPath, "rev-parse", "--verify", "HEAD")
	originalCancel()
	if originalErr != nil {
		return fmt.Errorf("resolve original HEAD: %w", originalErr)
	}
	original = strings.TrimSpace(original)

	ancestorCtx, ancestorCancel := context.WithTimeout(ctx, 5*time.Second)
	_, ancestorErr := RunGit(ancestorCtx, repoPath, "merge-base", "--is-ancestor", upstream, original)
	ancestorCancel()
	if ancestorErr != nil {
		return fmt.Errorf("upstream is not an ancestor of HEAD (branch diverged; pull first): %w", ancestorErr)
	}

	countCtx, countCancel := context.WithTimeout(ctx, 5*time.Second)
	countOut, err := RunGit(countCtx, repoPath, "rev-list", "--count", upstream+".."+original)
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

	// the snapshot validates every blob the collapsed delta touches, one
	// git cat-file each: give it time proportional to that delta
	filesCtx, filesCancel := context.WithTimeout(ctx, 2*time.Minute)
	changed, err := RunGit(filesCtx, repoPath, "diff", "--name-only", upstream, original)
	filesCancel()
	if err != nil {
		return fmt.Errorf("count changed files for squash budget: %w", err)
	}

	// with a rebase recorded as in progress, `git rebase --abort` would reset
	// the branch and drop the squash
	if err := IsSafeForGitOps(repoPath); err != nil {
		return fmt.Errorf("unsafe Ledger commit: %w", err)
	}
	if beforeSquashCommitHook != nil {
		beforeSquashCommitHook()
	}
	// detached from the caller's cancellation: validating a long backlog can
	// outlast the caller's deadline, which would then kill every attempt
	squashCtx, squashCancel := context.WithTimeout(context.WithoutCancel(ctx), squashBudget(len(strings.Fields(changed))))
	defer squashCancel()
	if err := commitLedgerTree(squashCtx, repoPath, original+"^{tree}", upstream, original, commitMsg, nil); err != nil {
		return fmt.Errorf("validate squash or commit: %w", err)
	}
	return nil
}

// squashBudget is how long the collapsed snapshot commit may take: a floor
// plus 50ms per changed file for the per-blob validation, capped so a runaway
// still ends. 6,282 files (measured 2026-10-08) gets about five and a half
// minutes; the flat 10s it replaced killed that validation every time.
func squashBudget(changedFiles int) time.Duration {
	budget := 30*time.Second + time.Duration(changedFiles)*50*time.Millisecond
	return min(budget, 15*time.Minute)
}
