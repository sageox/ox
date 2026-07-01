//go:build !short

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildZombieRebaseRepo creates a real git repo wedged by a structurally
// incomplete .git/rebase-merge dir — only an `autostash` entry, no
// head-name/orig-head — with HEAD still on `main` and NO unresolved conflicts.
// This is the exact production wedge (bd ox-j3cl): git thinks a rebase is in
// progress, `git rebase --abort` cannot clear it, and — because there are no
// U-state files — the unmerged-paths check reports "no conflicts" and misses it.
func buildZombieRebaseRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	mustRunGit(t, repo, "init", "--initial-branch=main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "session.md"), []byte("base\n"), 0644))
	mustRunGit(t, repo, "add", "session.md")
	mustRunGit(t, repo, "commit", "-m", "base")

	// a real autostash object, faithful to production (stashed off a dirty tree,
	// then the tree restored clean)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "session.md"), []byte("dirty\n"), 0644))
	stashOID, err := runIsolatedGit(t, repo, "stash", "create")
	require.NoError(t, err)
	mustRunGit(t, repo, "checkout", "--", "session.md")

	stateDir := filepath.Join(repo, ".git", "rebase-merge")
	require.NoError(t, os.MkdirAll(stateDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "autostash"),
		[]byte(strings.TrimSpace(stashOID)+"\n"), 0644))
	return repo
}

// TestCheckLedgerStuckOperation_UnmergedPathsMissesZombie pins the DETECTION GAP
// that let the production wedge (bd ox-j3cl) hide: a zombie rebase dir has no
// U-state files, so parseUnmergedPaths returns nothing and the unmerged-paths
// check would report "no conflicts" — yet detectInProgressGitOp correctly sees
// the rebase. The stuck-operation check exists precisely to cover this.
func TestCheckLedgerStuckOperation_UnmergedPathsMissesZombie(t *testing.T) {
	skipIntegration(t)
	repo := buildZombieRebaseRepo(t)

	require.True(t, gitutil.IsRebaseInProgress(repo), "setup should read as a rebase in progress")

	status, err := runIsolatedGit(t, repo, "status", "--porcelain=v1")
	require.NoError(t, err)
	require.Empty(t, parseUnmergedPaths(status+"\n"),
		"a zombie rebase has no U-state files — the exact reason unmerged-paths misses it")

	op, _ := detectInProgressGitOp(repo)
	require.Equal(t, "rebase", op, "stuck-operation detection must still see the rebase")
}

// TestFixLedgerStuckOperation_ClearsZombieRebase is the load-bearing fix
// regression: --fix must clear a zombie rebase dir that `git rebase --abort`
// cannot, leaving HEAD untouched. Without the AbortOrClearRebase quit escalation
// this fails and the ledger stays wedged.
func TestFixLedgerStuckOperation_ClearsZombieRebase(t *testing.T) {
	skipIntegration(t)
	repo := buildZombieRebaseRepo(t)
	headBefore, err := runIsolatedGit(t, repo, "rev-parse", "HEAD")
	require.NoError(t, err)

	r := fixLedgerStuckOperation(repo, "rebase", "rebase state dir present")
	assert.True(t, r.passed, "fix must clear a zombie rebase: %+v", r)
	assert.Contains(t, r.message, "cleared stuck rebase")
	assert.False(t, gitutil.IsRebaseInProgress(repo), "rebase state must be gone after fix")

	headAfter, err := runIsolatedGit(t, repo, "rev-parse", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, headBefore, headAfter, "quit must not move HEAD")
}

// TestStuckOperationFailure_LoudAndActionable pins the no-fix P0 shape: the
// wedge must surface as critical (not a warning buried in the summary) and must
// name the recovery path so a coworker has somewhere to go.
func TestStuckOperationFailure_LoudAndActionable(t *testing.T) {
	t.Parallel()
	r := stuckOperationFailure("Ledger stuck operation", "/tmp/ledger", "rebase", "rebase state dir present")
	assert.False(t, r.passed, "wedge must surface as a failure, not a warning")
	assert.Equal(t, "critical", r.priority)
	assert.Contains(t, r.message, "stuck rebase blocking ledger sync")
	assert.Contains(t, r.detail, "ox doctor --fix",
		"detail must point at the recovery action")
	assert.Equal(t, CheckSlugLedgerStuckOperation, r.slug)
}
