//go:build !short

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupBareRemote creates a bare git repo that can serve as a remote origin.
// Returns the path to the bare repo.
func setupBareRemote(t *testing.T) string {
	t.Helper()
	bareDir := filepath.Join(t.TempDir(), "remote.git")

	cmd := exec.Command("git", "init", "--bare", bareDir)
	require.NoError(t, cmd.Run(), "failed to create bare remote")

	return bareDir
}

// cloneFromBare clones the bare remote into a new working directory.
// Returns the path to the clone.
func cloneFromBare(t *testing.T, bareDir string) string {
	t.Helper()
	cloneDir := filepath.Join(t.TempDir(), "clone")

	cmd := exec.Command("git", "clone", bareDir, cloneDir)
	require.NoError(t, cmd.Run(), "failed to clone bare remote")

	// configure git identity in the clone
	for _, args := range [][]string{
		{"config", "user.name", "Test User"},
		{"config", "user.email", "test@example.com"},
	} {
		c := exec.Command("git", args...)
		c.Dir = cloneDir
		require.NoError(t, c.Run())
	}

	return cloneDir
}

// commitFile creates a file, stages it, and commits in the given repo dir.
func commitFile(t *testing.T, repoDir, filename, content, message string) {
	t.Helper()

	if dir := filepath.Dir(filepath.Join(repoDir, filename)); dir != repoDir {
		require.NoError(t, os.MkdirAll(dir, 0755))
	}

	require.NoError(t, os.WriteFile(filepath.Join(repoDir, filename), []byte(content), 0644))

	cmd := exec.Command("git", "add", filename)
	cmd.Dir = repoDir
	require.NoError(t, cmd.Run())

	cmd = exec.Command("git", "commit", "-m", message)
	cmd.Dir = repoDir
	require.NoError(t, cmd.Run())
}

// pushToRemote pushes the current branch to origin.
func pushToRemote(t *testing.T, repoDir string) {
	t.Helper()

	// detect the current branch name
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	require.NoError(t, err)
	branch := string(out[:len(out)-1]) // trim newline

	cmd = exec.Command("git", "push", "origin", branch)
	cmd.Dir = repoDir
	require.NoError(t, cmd.Run(), "failed to push to remote")
}

func TestCheckRemoteSageoxExists_FoundOnTracking(t *testing.T) {
	skipIntegration(t)

	bareDir := setupBareRemote(t)
	cloneDir := cloneFromBare(t, bareDir)

	// create .sageox/ dir in the clone and push it
	commitFile(t, cloneDir, ".sageox/config.json", `{"version":"1"}`, "add sageox config")
	pushToRemote(t, cloneDir)

	// clone again to get a fresh clone with up-to-date tracking refs
	freshClone := cloneFromBare(t, bareDir)

	found, stale, err := checkRemoteSageoxExists(freshClone)
	require.NoError(t, err)
	assert.True(t, found, "expected .sageox/ to be found on remote tracking ref")
	assert.False(t, stale, "expected tracking refs to be up to date")
}

func TestCheckRemoteSageoxExists_NotFound(t *testing.T) {
	skipIntegration(t)

	bareDir := setupBareRemote(t)
	cloneDir := cloneFromBare(t, bareDir)

	// push something that is NOT .sageox/
	commitFile(t, cloneDir, "README.md", "# Hello", "add readme")
	pushToRemote(t, cloneDir)

	freshClone := cloneFromBare(t, bareDir)

	found, stale, err := checkRemoteSageoxExists(freshClone)
	require.NoError(t, err)
	assert.False(t, found, "expected .sageox/ to NOT be found on remote")
	assert.False(t, stale, "expected tracking refs to be up to date")
}

func TestCheckRemoteSageoxExists_NoRemote(t *testing.T) {
	skipIntegration(t)

	// create a repo with no remote
	repoDir := testGitRepo(t)

	_, _, err := checkRemoteSageoxExists(repoDir)
	assert.Error(t, err, "expected error when no remote configured")
}

func TestCheckRemoteSageoxExists_Stale(t *testing.T) {
	skipIntegration(t)

	bareDir := setupBareRemote(t)

	// first clone: push initial content
	firstClone := cloneFromBare(t, bareDir)
	commitFile(t, firstClone, "README.md", "# Hello", "initial commit")
	pushToRemote(t, firstClone)

	// second clone: this one will go stale
	staleClone := cloneFromBare(t, bareDir)

	// push .sageox/ from first clone WITHOUT fetching in staleClone
	commitFile(t, firstClone, ".sageox/config.json", `{"version":"1"}`, "add sageox config")
	pushToRemote(t, firstClone)

	// staleClone has not fetched, so its tracking refs are behind
	found, stale, err := checkRemoteSageoxExists(staleClone)
	require.NoError(t, err)

	// either found=true (if the commit object happened to be available) or stale=true
	// the important thing is we don't return found=false, stale=false
	if !found {
		assert.True(t, stale, "expected stale=true when local tracking refs are behind remote")
	}
}
