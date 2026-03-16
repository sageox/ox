package ledger

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
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

	// stage all files in data/github/ with --sparse
	addCmd := exec.Command("git", "-C", ledgerPath, "add", "--sparse", dataDir)
	if output, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %s: %w", string(output), err)
	}

	// commit
	var parts []string
	if result.PRTotal > 0 {
		parts = append(parts, fmt.Sprintf("%d PRs", result.PRTotal))
	}
	if result.IssueTotal > 0 {
		parts = append(parts, fmt.Sprintf("%d issues", result.IssueTotal))
	}
	commitMsg := fmt.Sprintf("github: sync %s from %s/%s", strings.Join(parts, ", "), owner, repo)
	commitCmd := exec.Command("git", "-C", ledgerPath, "commit", "--no-verify", "-m", commitMsg)
	if output, err := commitCmd.CombinedOutput(); err != nil {
		if strings.Contains(string(output), "nothing to commit") {
			// still push — a prior run may have committed but failed to push
			return pushFn(ctx, ledgerPath)
		}
		return fmt.Errorf("git commit failed: %s: %w", string(output), err)
	}

	// push with caller-provided retry logic
	return pushFn(ctx, ledgerPath)
}
