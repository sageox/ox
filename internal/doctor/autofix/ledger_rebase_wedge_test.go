package autofix

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// afGit runs git in dir with a fixed identity. Git isolation (no global/system
// config, no gpgsign prompt) comes from gitenv_test.go's testenv blank-import.
func afGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...) // git in a temp dir, not the ox CLI
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), // safe: hermetic env for git (not ox) subprocess in a temp dir; ox is never spawned here
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// makeZombieLedger builds a real repo wedged by a structurally-incomplete
// .git/rebase-merge dir — only an `autostash` entry, no head-name/orig-head —
// the exact shape `git rebase --abort` cannot clear (bd ox-j3cl). When stale is
// true the state dir is backdated past gitutil.StaleRebaseThreshold.
func makeZombieLedger(t *testing.T, stale bool) string {
	t.Helper()
	repo := t.TempDir()
	afGit(t, repo, "init", "--initial-branch=main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "f.txt"), []byte("base\n"), 0o644))
	afGit(t, repo, "add", "f.txt")
	afGit(t, repo, "commit", "-m", "base")

	// a real autostash object, faithful to production
	require.NoError(t, os.WriteFile(filepath.Join(repo, "f.txt"), []byte("dirty\n"), 0o644))
	stashOID := afGit(t, repo, "stash", "create")
	afGit(t, repo, "checkout", "--", "f.txt")

	stateDir := filepath.Join(repo, ".git", "rebase-merge")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "autostash"), []byte(stashOID+"\n"), 0o644))
	require.True(t, gitutil.IsRebaseInProgress(repo))

	if stale {
		old := time.Now().Add(-time.Hour)
		require.NoError(t, os.Chtimes(filepath.Join(stateDir, "autostash"), old, old))
		require.NoError(t, os.Chtimes(stateDir, old, old))
	}
	return repo
}

// TestRepairLedgerRebaseWedge_ClearsStaleZombie is the autofix regression for bd
// ox-j3cl: UNATTENDED — no human, no `ox doctor --fix` — the daemon must clear a
// stale zombie rebase dir so background sync resumes. The reported incident sat
// suspended six weeks because the only recovery primitive (git rebase --abort)
// could not clear this shape.
func TestRepairLedgerRebaseWedge_ClearsStaleZombie(t *testing.T) {
	if testing.Short() {
		t.Skip("short: spawns git subprocesses")
	}
	repo := makeZombieLedger(t, true)

	res := repairLedgerRebaseWedge(context.Background(), repo, repo)

	assert.Equal(t, StatusFixed, res.Status, "stale zombie must be auto-cleared: %+v", res)
	assert.False(t, gitutil.IsRebaseInProgress(repo), "rebase state must be gone after autofix")
}

// TestRepairLedgerRebaseWedge_LeavesFreshRebase: a fresh rebase is almost always
// the daemon's own in-flight pull. Autofix must NOT abort it out from under the
// pull. Failure prevented: the unattended ticker racing the sync loop and
// corrupting a live rebase.
func TestRepairLedgerRebaseWedge_LeavesFreshRebase(t *testing.T) {
	if testing.Short() {
		t.Skip("short: spawns git subprocesses")
	}
	repo := makeZombieLedger(t, false) // mtime = now → fresh

	res := repairLedgerRebaseWedge(context.Background(), repo, repo)

	assert.Equal(t, StatusClean, res.Status, "a fresh rebase must be left alone")
	assert.True(t, gitutil.IsRebaseInProgress(repo), "fresh rebase must NOT be cleared")
}

// TestRepairLedgerRebaseWedge_CleanRepo: no rebase in progress → StatusClean,
// no side effects. Guards the idempotent no-op path the ticker hits every cycle.
func TestRepairLedgerRebaseWedge_CleanRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("short: spawns git subprocesses")
	}
	repo := t.TempDir()
	afGit(t, repo, "init", "--initial-branch=main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "f.txt"), []byte("x\n"), 0o644))
	afGit(t, repo, "add", "f.txt")
	afGit(t, repo, "commit", "-m", "base")

	res := repairLedgerRebaseWedge(context.Background(), repo, repo)
	assert.Equal(t, StatusClean, res.Status)
}
