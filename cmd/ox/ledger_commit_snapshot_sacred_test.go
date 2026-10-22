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

// seedSacredSessions writes n session dirs (sessions/<name>/meta.json) and
// commits them, so they form the parent tree the sacred-deletion guard diffs
// against.
func seedSacredSessions(t *testing.T, repo string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		dir := filepath.Join(repo, "sessions", fmt.Sprintf("2026-06-%02dT00-00-test-Ox%04d", i+1, i))
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), []byte("{}\n"), 0o644))
	}
	mustRunGit(t, repo, "add", "sessions")
	mustRunGit(t, repo, "commit", "-m", "seed sessions")
}

// TestCommitLedgerSnapshot_RefusesSacredMassDeletion replays the 2026-08-25
// Ox Dot wipe at the guard boundary: an index that stages deletion of every
// session must be REFUSED and nothing committed — the sacred tree stays in
// HEAD. The wipe took plans; this commit leaves data/plans/ out
// (allButPlansPathspec), so sessions stand in for them.
//
// Failure prevented: a sparse/GC-reconciled index whose bulk deletions get
// snapshotted into a ledger commit and pushed to origin (ADR-024). Without
// assertNoSacredMassDeletion this commit succeeds and the sessions are gone
// from the tip, as the plans were from repo_019d56e0's.
func TestCommitLedgerSnapshot_RefusesSacredMassDeletion(t *testing.T) {
	skipIntegration(t)
	repo := newLedgerTestRepo(t)
	ctx := context.Background()

	seedSacredSessions(t, repo, sacred.MassDeleteThreshold+5)
	headBefore, err := runIsolatedGit(t, repo, "rev-parse", "HEAD")
	require.NoError(t, err)

	// stage deletion of ALL sessions — what the reconcile did to plans before the bare commit
	mustRunGit(t, repo, "rm", "-r", "-q", "sessions")

	committed, err := commitLedgerSnapshot(ctx, repo,
		"chore: add .sageox/.gitignore to exclude daemon cache files")
	require.Error(t, err, "a mass sacred deletion must be refused")
	assert.False(t, committed)
	assert.Contains(t, err.Error(), "sacred")

	// no commit happened; the sessions are still in HEAD
	headAfter, err := runIsolatedGit(t, repo, "rev-parse", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, headBefore, headAfter, "HEAD must not advance on a refused wipe")
	tree, err := runIsolatedGit(t, repo, "ls-tree", "-r", "--name-only", "HEAD", "--", "sessions")
	require.NoError(t, err)
	assert.Contains(t, tree, "meta.json", "sacred sessions must remain in HEAD after refusal")
}

// TestCommitLedgerSnapshot_AllowsSmallSacredDeletion proves the guard does not
// block routine churn: deleting a single session (well under the threshold)
// commits normally.
func TestCommitLedgerSnapshot_AllowsSmallSacredDeletion(t *testing.T) {
	skipIntegration(t)
	repo := newLedgerTestRepo(t)
	ctx := context.Background()

	seedSacredSessions(t, repo, 3)
	mustRunGit(t, repo, "rm", "-r", "-q", "sessions/2026-06-01T00-00-test-Ox0000")

	committed, err := commitLedgerSnapshot(ctx, repo, "Delete session 2026-06-01T00-00-test-Ox0000")
	require.NoError(t, err)
	assert.True(t, committed, "a single-session delete is normal churn and must commit")
}
