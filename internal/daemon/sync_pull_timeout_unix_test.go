//go:build !windows

package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/manifest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeAheadAndBehindClone builds a bare remote plus a local clone that has one
// unpushed commit while the remote advanced on a DIFFERENT file, so a rebase
// replays cleanly. Returns the clone path.
func makeAheadAndBehindClone(t *testing.T) string {
	t.Helper()
	isolateCredentials(t)

	bareDir := filepath.Join(t.TempDir(), "origin.git")
	out, err := runGitOut(t, t.TempDir(), "init", "--bare", "--initial-branch=main", bareDir)
	require.NoError(t, err, out)

	commit := func(dir, file, msg string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte(msg), 0o644))
		out, err := runGitOut(t, dir, "add", "-A")
		require.NoError(t, err, out)
		out, err = runGitOut(t, dir, "commit", "-m", msg)
		require.NoError(t, err, out)
	}

	seed := t.TempDir()
	out, err = runGitOut(t, t.TempDir(), "clone", bareDir, seed)
	require.NoError(t, err, out)
	commit(seed, "seed.txt", "seed")
	out, err = runGitOut(t, seed, "push", "origin", "HEAD:main")
	require.NoError(t, err, out)

	local := t.TempDir()
	out, err = runGitOut(t, t.TempDir(), "clone", bareDir, local)
	require.NoError(t, err, out)
	commit(local, "local.txt", "local unpushed")

	other := t.TempDir()
	out, err = runGitOut(t, t.TempDir(), "clone", bareDir, other)
	require.NoError(t, err, out)
	commit(other, "remote.txt", "remote advance")
	out, err = runGitOut(t, other, "push", "origin", "HEAD:main")
	require.NoError(t, err, out)
	return local
}

// installSlowRebasePull puts a fake `git` first on PATH that turns `git pull`
// into a real interactive rebase parked on an `exec sleep` step. That is the
// production shape: pull spawns a rebase child that is still replaying when the
// pull context expires. Every other git subcommand passes through unchanged.
func installSlowRebasePull(t *testing.T, repo string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)

	script := `#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = pull ]; then
    cd '` + repo + `' || exit 1
    GIT_SEQUENCE_EDITOR="sed -i.bak '1i\\
exec sleep 60'" exec '` + realGit + `' rebase -i --autostash '@{u}'
  fi
done
exec '` + realGit + `' "$@"
`
	binDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// Failure prevented: the 60s pull timeout killed `git pull` but left its
// `git rebase` state (and index.lock) behind; the next cycle's abort failed
// and the ledger stayed wedged forever. After a timeout the repo must be clean
// in the SAME cycle, with the unpushed commit intact.
func TestPullManagedRepo_TimeoutDuringRebaseLeavesCleanRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations and a deliberate pull timeout")
	}
	local := makeAheadAndBehindClone(t)
	installSlowRebasePull(t, local)

	s := newTestScheduler(t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	result := s.pullManagedRepo(ctx, ManagedRepoPullOpts{
		RepoPath:     local,
		RepoName:     "ledger",
		ResolveRules: []manifest.ResolveRule{{Mode: manifest.ResolveModeAuto, Path: "data/"}},
		Logger:       discardLogger(),
	})

	require.Error(t, result.Err)
	assert.True(t, errors.Is(result.Err, context.DeadlineExceeded) || strings.Contains(result.Err.Error(), "pull timed out"),
		"error must say the pull timed out: %v", result.Err)
	assert.Nil(t, result.Issue, "a cleanly aborted timeout is not a stuck-rebase issue")

	assert.NoDirExists(t, filepath.Join(local, ".git", "rebase-merge"), "rebase state must be cleared in the same cycle")
	assert.NoDirExists(t, filepath.Join(local, ".git", "rebase-apply"))
	assert.NoFileExists(t, filepath.Join(local, ".git", "index.lock"), "no orphan may keep the index lock")

	// the unpushed commit survived and the branch is still attached
	out, err := runGitOut(t, local, "rev-list", "--count", "@{u}..HEAD")
	require.NoError(t, err, out)
	assert.Equal(t, "1", strings.TrimSpace(out))
	branch, err := runGitOut(t, local, "symbolic-ref", "--short", "HEAD")
	require.NoError(t, err, branch)
	assert.Equal(t, "main", strings.TrimSpace(branch))
}
