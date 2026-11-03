package gitutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/secretguard"
	"github.com/stretchr/testify/require"
)

func TestImmutablePlanBlobRequiresSecretGuard(t *testing.T) {
	for _, path := range []string{"data/plans/draft/plan.md", "data/plans/group/draft/plan.md", "data/plans/plan.md"} {
		t.Run(path, func(t *testing.T) {
			require.True(t, IsPlanTextPath(path))
			require.ErrorContains(t, ValidateLedgerBlob(path, []byte(syntheticAWSKey)), "refused")
			require.NoError(t, ValidateLedgerBlob(path, []byte("# Ordinary deployment plan\n")))
		})
	}
	for _, path := range []string{"data/github/plan.md", "data/plans-other/draft/plan.md", "data/plans/draft/notes.md"} {
		require.False(t, IsPlanTextPath(path))
		require.NoError(t, ValidateLedgerBlob(path, []byte(syntheticAWSKey)), "unrelated paths retain their existing scope")
	}
}

func TestPlanSnapshotRejectsSecretInIndexDespiteCleanWorktree(t *testing.T) {
	repo := newSnapshotRepo(t)
	const rel = "data/plans/draft/plan.md"
	writeGitutilFixture(t, repo, rel, syntheticAWSKey)
	gitInRepo(t, repo, "add", "--", rel)
	writeGitutilFixture(t, repo, rel, "# Clean working copy\n")
	head := gitInRepo(t, repo, "rev-parse", "HEAD")
	ctx := context.Background()
	require.NoError(t, WithRepoLock(ctx, repo, func() error {
		committed, err := CommitLedgerSnapshot(ctx, repo, "save plan", rel)
		require.ErrorContains(t, err, "refused")
		require.False(t, committed)
		return nil
	}))
	require.Equal(t, head, gitInRepo(t, repo, "rev-parse", "HEAD"))
}

func TestCommittedPlanScanCannotTrustWorktreeOrFinalDiff(t *testing.T) {
	for _, mode := range []string{"clean worktree", "redacted later commit", "deleted later commit", "no upstream"} {
		t.Run(mode, func(t *testing.T) {
			useFreshFixtureRules(t)
			const rel = "data/plans/draft/plan.md"
			ledger := publicationLedger(t, rel, "# Plan\n"+syntheticAWSKey)
			abs := filepath.Join(ledger, rel)
			switch mode {
			case "deleted later commit":
				require.NoError(t, os.Remove(abs))
			case "no upstream":
				gitInRepo(t, ledger, "branch", "--unset-upstream")
			default:
				require.NoError(t, os.WriteFile(abs, []byte("# Ordinary plan\n"), 0600))
			}
			if mode == "redacted later commit" || mode == "deleted later commit" {
				gitInRepo(t, ledger, "add", "--", rel)
				gitInRepo(t, ledger, "commit", "-m", "clean plan")
			}
			require.ErrorContains(t, ValidateSessionPublication(context.Background(), ledger), "content refused")
		})
	}
}

func TestPlanPublicationUsesCurrentRules(t *testing.T) {
	useFreshFixtureRules(t)
	ledger := publicationLedger(t, "data/plans/group/draft/plan.md", "# Ordinary plan\nfuture_synthetic")
	require.NoError(t, ValidateSessionPublication(context.Background(), ledger))
	rules, err := secretguard.NewRules([]byte("[[rules]]\nid='new-format'\nregex='future_[a-z]+'\n"))
	require.NoError(t, err)
	freshSessionPublicationRules = func(context.Context) (*secretguard.RuleSet, error) { return rules, nil }
	require.ErrorContains(t, ValidateSessionPublication(context.Background(), ledger), "current rules")
}

func TestPushWithRetryCannotOmitPlanSecretGuard(t *testing.T) {
	useFreshFixtureRules(t)
	ledger, bare := initBareRemoteRepo(t)
	remoteBefore := gitInRepo(t, bare, "rev-parse", "HEAD")
	const rel = "data/plans/draft/plan.md"
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(ledger, rel)), 0700))
	addCommit(t, ledger, rel, syntheticAWSKey, "plan from alternate writer")
	// Even a permissive optional callback cannot bypass immutable validation.
	opts := PushOpts{ValidatePush: func(context.Context, string) (bool, error) { return false, nil }}
	require.ErrorContains(t, PushWithRetry(context.Background(), ledger, opts), "content refused")
	require.Equal(t, remoteBefore, gitInRepo(t, bare, "rev-parse", "HEAD"))
}