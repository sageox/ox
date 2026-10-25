package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeFleetLedger(t *testing.T, staleRebase bool) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "--initial-branch=main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644))
	runGit(t, repo, "add", "base.txt")
	runGit(t, repo, "commit", "-m", "base")
	if !staleRebase {
		return repo
	}

	require.NoError(t, os.WriteFile(filepath.Join(repo, "base.txt"), []byte("dirty\n"), 0o644))
	stashOID := runGit(t, repo, "stash", "create")
	runGit(t, repo, "checkout", "--", "base.txt")
	stateDir := filepath.Join(repo, ".git", "rebase-merge")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "autostash"), []byte(stashOID+"\n"), 0o644))
	old := time.Now().Add(-gitutil.StaleRebaseThreshold - time.Minute)
	require.NoError(t, os.Chtimes(filepath.Join(stateDir, "autostash"), old, old))
	require.NoError(t, os.Chtimes(stateDir, old, old))
	return repo
}

// Failure prevented: doctor only inspects the repo in the current shell, so a
// stale Ledger for a repo that has not been opened recently stays wedged forever.
func TestCheckLedgerFleetHealthPaths_RepairsEveryLocalLedger(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repositories")
	}
	healthy := makeFleetLedger(t, false)
	wedged := makeFleetLedger(t, true)
	require.True(t, gitutil.IsRebaseInProgress(wedged))

	result := checkLedgerFleetHealthPaths([]string{healthy, wedged}, true)

	assert.True(t, result.passed, result.detail)
	assert.Contains(t, result.message, "cleared stale operations in 1 of 2")
	assert.False(t, gitutil.IsRebaseInProgress(wedged))
}

func TestCheckLedgerFleetHealthPaths_ReportsWithoutFix(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repository")
	}
	wedged := makeFleetLedger(t, true)

	result := checkLedgerFleetHealthPaths([]string{wedged}, false)

	assert.False(t, result.passed)
	assert.Equal(t, "critical", result.priority)
	require.Len(t, result.children, 1)
	assert.Equal(t, filepath.Base(wedged), result.children[0].name)
	assert.True(t, gitutil.IsRebaseInProgress(wedged), "read-only check must not clear the rebase")
}
