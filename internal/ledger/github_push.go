package ledger

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/gitutil"
)

// PushFunc is a function that pushes the ledger to remote with retry logic.
// The caller provides this so the ledger package doesn't depend on gitutil/endpoint.
type PushFunc func(ctx context.Context, ledgerPath string) error

// CommitAndPushGitHubData stages data/github/, commits, and pushes to the ledger.
// The pushFn handles retry logic and conflict resolution (accept-theirs for data/github/).
func CommitAndPushGitHubData(ctx context.Context, ledgerPath, owner, repo string, result *SyncResult, pushFn PushFunc) error {
	dataDir := GitHubDataDir(ledgerPath)

	// skip if data dir doesn't exist (nothing to stage)
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		return nil
	}

	var parts []string
	if result.PRTotal > 0 {
		parts = append(parts, fmt.Sprintf("%d PRs", result.PRTotal))
	}
	if result.IssueTotal > 0 {
		parts = append(parts, fmt.Sprintf("%d issues", result.IssueTotal))
	}
	commitMsg := fmt.Sprintf("github: sync %s from %s/%s", strings.Join(parts, ", "), owner, repo)
	relDir := filepath.ToSlash(filepath.Join("data", "github"))

	// Keep stage, validation, and commit under ADR-030's cross-process lock.
	// PushWithRetry takes the same non-reentrant lock only if it must pull, so
	// publication runs after this critical section.
	if err := gitutil.WithRepoLock(ctx, ledgerPath, func() error {
		if output, err := gitutil.RunGit(ctx, ledgerPath, "add", "--sparse", "--", relDir); err != nil {
			return fmt.Errorf("git add failed: %s: %w", output, err)
		}
		if err := gitutil.ValidateStagedLedgerCommit(ctx, ledgerPath, relDir); err != nil {
			return err
		}
		output, err := gitutil.RunGit(ctx, ledgerPath, "commit", "--no-verify", "-m", commitMsg, "--", relDir)
		if err != nil && !strings.Contains(output, "nothing to commit") {
			return fmt.Errorf("git commit failed: %s: %w", output, err)
		}
		return nil
	}); err != nil {
		return err
	}

	// push with caller-provided retry logic
	return pushFn(ctx, ledgerPath)
}
