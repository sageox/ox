package main

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

// seedSacredPlans writes n plan dirs (data/plans/<slug>/plan.md) and commits
// them, so they form the parent tree the sacred-deletion guard diffs against.
func seedSacredPlans(t *testing.T, repo string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		dir := filepath.Join(repo, "data", "plans", fmt.Sprintf("2026-06-%02d-plan", i+1))
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "plan.md"),
			[]byte(fmt.Sprintf("# plan %d\n", i)), 0o644))
	}
	mustRunGit(t, repo, "add", "data/plans")
	mustRunGit(t, repo, "commit", "-m", "seed plans")
}

// TestCommitLedgerSnapshot_RefusesSacredMassDeletion reproduces the 2026-08-25
// Ox Dot wipe at the guard boundary: an index that stages deletion of every
// saved plan must be REFUSED and nothing committed — the sacred trees stay in
// HEAD.
//
// Failure prevented: a sparse/GC-reconciled index whose bulk deletions get
// snapshotted into a ledger commit and pushed to origin (ADR-024). Without
// assertNoSacredMassDeletion this commit succeeds and the plans are gone from
// the tip — exactly what happened to repo_019d56e0.
func TestCommitLedgerSnapshot_RefusesSacredMassDeletion(t *testing.T) {
	skipIntegration(t)
	repo := newLedgerTestRepo(t)
	ctx := context.Background()

	seedSacredPlans(t, repo, sacred.MassDeleteThreshold+5)
	headBefore, err := runIsolatedGit(t, repo, "rev-parse", "HEAD")
	require.NoError(t, err)

	// stage deletion of ALL plans — what the reconcile did before the bare commit
	mustRunGit(t, repo, "rm", "-r", "data/plans")

	committed, err := commitLedgerSnapshot(ctx, repo,
		"chore: add .sageox/.gitignore to exclude daemon cache files")
	require.Error(t, err, "a mass sacred deletion must be refused")
	assert.False(t, committed)
	assert.Contains(t, err.Error(), "sacred")

	// no commit happened; the plans are still in HEAD
	headAfter, err := runIsolatedGit(t, repo, "rev-parse", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, headBefore, headAfter, "HEAD must not advance on a refused wipe")
	tree, err := runIsolatedGit(t, repo, "ls-tree", "-r", "--name-only", "HEAD", "--", "data/plans")
	require.NoError(t, err)
	assert.Contains(t, tree, "plan.md", "sacred plans must remain in HEAD after refusal")
}

// TestCommitLedgerSnapshot_AllowsSmallSacredDeletion proves the guard does not
// block routine churn: deleting a single plan (well under the threshold) commits
// normally, so an `ox plan` delete keeps working.
func TestCommitLedgerSnapshot_AllowsSmallSacredDeletion(t *testing.T) {
	skipIntegration(t)
	repo := newLedgerTestRepo(t)
	ctx := context.Background()

	seedSacredPlans(t, repo, 3)
	mustRunGit(t, repo, "rm", "-r", "data/plans/2026-06-01-plan")

	committed, err := commitLedgerSnapshot(ctx, repo, "Delete plan 2026-06-01-plan")
	require.NoError(t, err)
	assert.True(t, committed, "a single-plan delete is normal churn and must commit")
}

// TestCommitLedgerSnapshot_OverrideAllowsSacredMassDeletion proves the explicit
// escape hatch works for a deliberate bulk removal — and, with the guard thereby
// disabled, that the wipe otherwise commits (the "red" the guard turns green).
func TestCommitLedgerSnapshot_OverrideAllowsSacredMassDeletion(t *testing.T) {
	skipIntegration(t)
	repo := newLedgerTestRepo(t)
	ctx := context.Background()

	seedSacredPlans(t, repo, sacred.MassDeleteThreshold+5)
	mustRunGit(t, repo, "rm", "-r", "data/plans")

	t.Setenv(sacred.OverrideEnv, "1")
	committed, err := commitLedgerSnapshot(ctx, repo, "chore: intentional bulk plan removal")
	require.NoError(t, err)
	assert.True(t, committed, "override must let a deliberate bulk removal through")
}
