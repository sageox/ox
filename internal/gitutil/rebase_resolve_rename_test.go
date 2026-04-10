package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Rename/rename conflicts ---

// TestResolveRebase_RenameRenameConflict verifies that auto-resolve handles
// the case where both branches rename the same file to different names.
// Failure prevented: rename/rename conflicts wedge the ledger permanently,
// requiring manual intervention to unstick.
func TestResolveRebase_RenameRenameConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git rebase operations")
	}

	bareDir, oursClone, theirsClone := setupRenameRepos(t)

	// both branches rename the same file to different names
	// simulates two machines running migration with different content hashes
	renameFile(t, oursClone, "data/github/2026/04/07/pr/458.json", "data/github/2026/04/07/pr/458-aaaa1111.json")
	gitInRepo(t, oursClone, "add", "-A")
	gitInRepo(t, oursClone, "commit", "-m", "local migration")

	renameFile(t, theirsClone, "data/github/2026/04/07/pr/458.json", "data/github/2026/04/07/pr/458-bbbb2222.json")
	gitInRepo(t, theirsClone, "add", "-A")
	gitInRepo(t, theirsClone, "commit", "-m", "remote migration")
	gitInRepo(t, theirsClone, "push", "origin", "HEAD")

	// fetch and start rebase (will conflict with rename/rename)
	gitInRepo(t, oursClone, "fetch", "origin")
	startRebase(t, oursClone)
	require.True(t, IsRebaseInProgress(oursClone), "rebase should be in progress")

	err := ResolveRebaseAcceptTheirs(context.Background(), oursClone, []string{"data/github/"})
	assert.NoError(t, err, "rename/rename conflict should be auto-resolved")
	assert.False(t, IsRebaseInProgress(oursClone), "rebase should be complete")

	// verify at least one of the renamed files exists (the conflict is resolved)
	_, errA := os.Stat(filepath.Join(oursClone, "data/github/2026/04/07/pr/458-aaaa1111.json"))
	_, errB := os.Stat(filepath.Join(oursClone, "data/github/2026/04/07/pr/458-bbbb2222.json"))
	assert.True(t, errA == nil || errB == nil, "at least one renamed file should exist after resolution")

	_ = bareDir
}

// --- B. Rename/delete conflicts ---

// TestResolveRebase_RenameDeleteConflict verifies that auto-resolve handles
// the case where one branch renames a file and the other deletes it.
// Failure prevented: rename/delete conflicts leave rebase stuck with
// "does not have their version" errors from git checkout --theirs.
func TestResolveRebase_RenameDeleteConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git rebase operations")
	}

	_, oursClone, theirsClone := setupRenameRepos(t)

	// ours: delete the file
	os.Remove(filepath.Join(oursClone, "data/github/2026/04/07/pr/459.json"))
	gitInRepo(t, oursClone, "add", "-A")
	gitInRepo(t, oursClone, "commit", "-m", "delete pr 459")

	// theirs: rename the file (migration)
	renameFile(t, theirsClone, "data/github/2026/04/07/pr/459.json", "data/github/2026/04/07/pr/459-cccc3333.json")
	gitInRepo(t, theirsClone, "add", "-A")
	gitInRepo(t, theirsClone, "commit", "-m", "remote migration of 459")
	gitInRepo(t, theirsClone, "push", "origin", "HEAD")

	gitInRepo(t, oursClone, "fetch", "origin")
	startRebase(t, oursClone)
	require.True(t, IsRebaseInProgress(oursClone), "rebase should be in progress")

	err := ResolveRebaseAcceptTheirs(context.Background(), oursClone, []string{"data/github/"})
	assert.NoError(t, err, "rename/delete conflict should be auto-resolved")
	assert.False(t, IsRebaseInProgress(oursClone), "rebase should be complete")
}

// --- C. Mixed rename + content conflicts ---

// TestResolveRebase_RenameAndContentConflictMixed verifies handling when
// some files have rename conflicts and others have content conflicts.
// Failure prevented: partial resolution where content conflicts resolve
// but rename conflicts wedge the rebase.
func TestResolveRebase_RenameAndContentConflictMixed(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git rebase operations")
	}

	bareDir := filepath.Join(t.TempDir(), "bare.git")
	oursClone := filepath.Join(t.TempDir(), "ours")
	theirsClone := filepath.Join(t.TempDir(), "theirs")

	gitInRepo(t, t.TempDir(), "init", "--bare", "--initial-branch=main", bareDir)

	// initial commit
	gitInRepo(t, t.TempDir(), "clone", bareDir, theirsClone)
	gitInRepo(t, theirsClone, "config", "user.name", "test")
	gitInRepo(t, theirsClone, "config", "user.email", "test@test.com")
	require.NoError(t, os.MkdirAll(filepath.Join(theirsClone, "data/github/pr"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(theirsClone, "data/github/pr/100.json"), []byte(`{"number":100}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(theirsClone, "data/github/pr/101.json"), []byte(`{"number":101}`), 0o644))
	gitInRepo(t, theirsClone, "add", ".")
	gitInRepo(t, theirsClone, "commit", "-m", "initial")
	gitInRepo(t, theirsClone, "push", "origin", "HEAD")

	// clone ours
	gitInRepo(t, t.TempDir(), "clone", bareDir, oursClone)
	gitInRepo(t, oursClone, "config", "user.name", "test")
	gitInRepo(t, oursClone, "config", "user.email", "test@test.com")

	// ours: rename 100.json (migration) + modify 101.json (content change)
	renameFile(t, oursClone, "data/github/pr/100.json", "data/github/pr/100-aaaa.json")
	require.NoError(t, os.WriteFile(filepath.Join(oursClone, "data/github/pr/101.json"), []byte(`{"number":101,"local":true}`), 0o644))
	gitInRepo(t, oursClone, "add", "-A")
	gitInRepo(t, oursClone, "commit", "-m", "local changes")

	// theirs: rename 100.json differently + modify 101.json differently
	renameFile(t, theirsClone, "data/github/pr/100.json", "data/github/pr/100-bbbb.json")
	require.NoError(t, os.WriteFile(filepath.Join(theirsClone, "data/github/pr/101.json"), []byte(`{"number":101,"remote":true}`), 0o644))
	gitInRepo(t, theirsClone, "add", "-A")
	gitInRepo(t, theirsClone, "commit", "-m", "remote changes")
	gitInRepo(t, theirsClone, "push", "origin", "HEAD")

	gitInRepo(t, oursClone, "fetch", "origin")
	startRebase(t, oursClone)
	require.True(t, IsRebaseInProgress(oursClone))

	err := ResolveRebaseAcceptTheirs(context.Background(), oursClone, []string{"data/github/"})
	assert.NoError(t, err, "mixed rename+content conflicts should be auto-resolved")
	assert.False(t, IsRebaseInProgress(oursClone))
}

// --- D. Unsafe rename conflicts still rejected ---

// TestResolveRebase_RenameOutsideSafePrefixRejected verifies that rename
// conflicts outside safe prefixes are still rejected.
// Failure prevented: rename-aware resolution accidentally bypasses safety checks.
func TestResolveRebase_RenameOutsideSafePrefixRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git rebase operations")
	}

	bareDir := filepath.Join(t.TempDir(), "bare.git")
	oursClone := filepath.Join(t.TempDir(), "ours")
	theirsClone := filepath.Join(t.TempDir(), "theirs")

	gitInRepo(t, t.TempDir(), "init", "--bare", "--initial-branch=main", bareDir)

	gitInRepo(t, t.TempDir(), "clone", bareDir, theirsClone)
	gitInRepo(t, theirsClone, "config", "user.name", "test")
	gitInRepo(t, theirsClone, "config", "user.email", "test@test.com")
	require.NoError(t, os.MkdirAll(filepath.Join(theirsClone, "sessions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(theirsClone, "sessions/important.jsonl"), []byte("base"), 0o644))
	gitInRepo(t, theirsClone, "add", ".")
	gitInRepo(t, theirsClone, "commit", "-m", "initial")
	gitInRepo(t, theirsClone, "push", "origin", "HEAD")

	gitInRepo(t, t.TempDir(), "clone", bareDir, oursClone)
	gitInRepo(t, oursClone, "config", "user.name", "test")
	gitInRepo(t, oursClone, "config", "user.email", "test@test.com")

	// both sides rename sessions file
	renameFile(t, oursClone, "sessions/important.jsonl", "sessions/important-local.jsonl")
	gitInRepo(t, oursClone, "add", "-A")
	gitInRepo(t, oursClone, "commit", "-m", "local rename")

	renameFile(t, theirsClone, "sessions/important.jsonl", "sessions/important-remote.jsonl")
	gitInRepo(t, theirsClone, "add", "-A")
	gitInRepo(t, theirsClone, "commit", "-m", "remote rename")
	gitInRepo(t, theirsClone, "push", "origin", "HEAD")

	gitInRepo(t, oursClone, "fetch", "origin")
	startRebase(t, oursClone)
	require.True(t, IsRebaseInProgress(oursClone))

	err := ResolveRebaseAcceptTheirs(context.Background(), oursClone, []string{"data/github/"})
	assert.Error(t, err, "rename conflicts outside safe prefix should be rejected")
	assert.Contains(t, err.Error(), "not under safe auto-resolve prefixes")
	assert.True(t, IsRebaseInProgress(oursClone), "rebase should remain for caller to handle")
}

// --- Helpers ---

// setupRenameRepos creates a bare repo with two clones, both having initial
// files that can be renamed. Returns (bareDir, oursClone, theirsClone).
func setupRenameRepos(t *testing.T) (string, string, string) {
	t.Helper()

	bareDir := filepath.Join(t.TempDir(), "bare.git")
	oursClone := filepath.Join(t.TempDir(), "ours")
	theirsClone := filepath.Join(t.TempDir(), "theirs")

	gitInRepo(t, t.TempDir(), "init", "--bare", "--initial-branch=main", bareDir)

	// initial commit via theirs clone
	gitInRepo(t, t.TempDir(), "clone", bareDir, theirsClone)
	gitInRepo(t, theirsClone, "config", "user.name", "test")
	gitInRepo(t, theirsClone, "config", "user.email", "test@test.com")

	require.NoError(t, os.MkdirAll(filepath.Join(theirsClone, "data/github/2026/04/07/pr"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(theirsClone, "data/github/2026/04/07/pr/458.json"), []byte(`{"number":458,"state":"open"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(theirsClone, "data/github/2026/04/07/pr/459.json"), []byte(`{"number":459,"state":"open"}`), 0o644))
	gitInRepo(t, theirsClone, "add", ".")
	gitInRepo(t, theirsClone, "commit", "-m", "initial")
	gitInRepo(t, theirsClone, "push", "origin", "HEAD")

	// clone ours
	gitInRepo(t, t.TempDir(), "clone", bareDir, oursClone)
	gitInRepo(t, oursClone, "config", "user.name", "test")
	gitInRepo(t, oursClone, "config", "user.email", "test@test.com")

	return bareDir, oursClone, theirsClone
}

// renameFile moves a file within a git repo (without staging).
func renameFile(t *testing.T, repoDir, oldPath, newPath string) {
	t.Helper()
	abs := filepath.Join(repoDir, oldPath)
	dest := filepath.Join(repoDir, newPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o755))
	require.NoError(t, os.Rename(abs, dest))
}

// startRebase initiates a rebase that's expected to conflict.
func startRebase(t *testing.T, repoDir string) {
	t.Helper()
	cmd := exec.Command("git", "rebase", "origin/main")
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), // safe: git subprocess in temp dir, not ox CLI
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=commit.gpgsign", "GIT_CONFIG_VALUE_0=false",
	)
	_ = cmd.Run() // expected to fail with conflict
}
