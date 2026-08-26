package autofix

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/sacred"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSacredTestRepo makes a real git repo with an identity and a base commit.
// Uses a SageOx-owned test domain per the test-email-domains hard rule.
func newSacredTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	afGit(t, repo, "init", "--initial-branch=main")
	afGit(t, repo, "config", "user.name", "Test")
	afGit(t, repo, "config", "user.email", "sacred-test@test.sageox.ai")
	afGit(t, repo, "config", "commit.gpgsign", "false")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644))
	afGit(t, repo, "add", "base.txt")
	afGit(t, repo, "commit", "-m", "base")
	return repo
}

func seedSacredPlansAF(t *testing.T, repo string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		dir := filepath.Join(repo, "data", "plans", fmt.Sprintf("2026-06-%02d-plan", i+1))
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "plan.md"), []byte("x\n"), 0o644))
	}
	afGit(t, repo, "add", "data/plans")
	afGit(t, repo, "commit", "-m", "seed plans")
}

// TestScanLedgerSacredDeletions_FlagsHistoricalWipe: a commit that deleted every
// plan sits in history. The detector must surface it (StatusFound) even though
// the wipe already landed — this is the belt to the commit guard's suspenders,
// catching a wipe that reached history via an old binary or a force-push.
func TestScanLedgerSacredDeletions_FlagsHistoricalWipe(t *testing.T) {
	repo := newSacredTestRepo(t)
	seedSacredPlansAF(t, repo, sacred.MassDeleteThreshold+5)
	afGit(t, repo, "rm", "-r", "data/plans")
	afGit(t, repo, "commit", "-m", "chore: add .sageox/.gitignore to exclude daemon cache files")

	res := scanLedgerSacredDeletions(context.Background(), repo, "/fake/repo")
	assert.Equal(t, StatusFound, res.Status, "a historical sacred wipe must be surfaced")
	assert.Contains(t, res.Summary, "sacred mass-deletion")
}

// TestScanLedgerSacredDeletions_CleanWhenNoWipe: a ledger that only ever added
// plans has nothing to flag.
func TestScanLedgerSacredDeletions_CleanWhenNoWipe(t *testing.T) {
	repo := newSacredTestRepo(t)
	seedSacredPlansAF(t, repo, 3)
	res := scanLedgerSacredDeletions(context.Background(), repo, "/fake/repo")
	assert.Equal(t, StatusClean, res.Status)
}

// TestScanLedgerSacredDeletions_IgnoresSmallDeletion: deleting a single plan is
// routine churn (below the threshold) and must not be flagged.
func TestScanLedgerSacredDeletions_IgnoresSmallDeletion(t *testing.T) {
	repo := newSacredTestRepo(t)
	seedSacredPlansAF(t, repo, 3)
	afGit(t, repo, "rm", "-r", "data/plans/2026-06-01-plan")
	afGit(t, repo, "commit", "-m", "Delete plan 2026-06-01-plan")
	res := scanLedgerSacredDeletions(context.Background(), repo, "/fake/repo")
	assert.Equal(t, StatusClean, res.Status)
}
