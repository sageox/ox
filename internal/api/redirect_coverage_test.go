package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectVCS_Git(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0755))

	assert.Equal(t, vcsGit, detectVCS(dir))
}

func TestDetectVCS_SVN(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".svn"), 0755))

	assert.Equal(t, vcsSVN, detectVCS(dir))
}

func TestDetectVCS_None(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	assert.Equal(t, vcsNone, detectVCS(dir))
}

func TestDetectVCS_GitTakesPrecedence(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".svn"), 0755))

	// git check comes first in the code
	assert.Equal(t, vcsGit, detectVCS(dir))
}

func TestDetectVCS_GitFileNotDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// .git as a file (e.g., git worktree) should not match since detectVCS checks IsDir
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /somewhere"), 0644))

	assert.Equal(t, vcsNone, detectVCS(dir))
}

func TestVcsMove_NoVCS_FallsBackToRename(t *testing.T) {
	t.Parallel()
	dir := t.TempDir() // no .git or .svn

	oldPath := filepath.Join(dir, "old.txt")
	newPath := filepath.Join(dir, "new.txt")
	require.NoError(t, os.WriteFile(oldPath, []byte("content"), 0644))

	err := vcsMove(dir, oldPath, newPath)
	assert.NoError(t, err)

	// old file should be gone, new file should exist
	_, err = os.Stat(oldPath)
	assert.True(t, os.IsNotExist(err), "old file should not exist after move")

	data, err := os.ReadFile(newPath)
	require.NoError(t, err)
	assert.Equal(t, "content", string(data))
}

func TestVcsRemove_NoVCS_FallsBackToOsRemove(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	path := filepath.Join(dir, "doomed.txt")
	require.NoError(t, os.WriteFile(path, []byte("bye"), 0644))

	err := vcsRemove(dir, path)
	assert.NoError(t, err)

	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "file should be removed")
}

func TestVcsAdd_NoVCS_Noop(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	path := filepath.Join(dir, "new.txt")
	require.NoError(t, os.WriteFile(path, []byte("new"), 0644))

	// vcsAdd with no VCS is a no-op (returns nil)
	err := vcsAdd(dir, path)
	assert.NoError(t, err)
}

func TestHandleRedirect_NilInput(t *testing.T) {
	t.Parallel()

	assert.NoError(t, HandleRedirect("/tmp", nil))
}

func TestHandleRedirect_EmptyProjectRoot_ReturnsNil(t *testing.T) {
	t.Parallel()

	info := &RedirectInfo{
		Repo: &RedirectMapping{From: "old", To: "new"},
	}
	assert.NoError(t, HandleRedirect("", info))
}

func TestHandleRedirect_RepoAndTeamMergeLogging(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".sageox"), 0755))

	info := &RedirectInfo{
		Repo: &RedirectMapping{From: "repo_old", To: "repo_new"},
		Team: &RedirectMapping{From: "team_old", To: "team_new"},
	}

	// should not error even if config update fails (best-effort)
	err := HandleRedirect(dir, info)
	assert.NoError(t, err)
}

func TestRenameRepoMarker_MarkerExists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sageoxDir := filepath.Join(dir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	// RenameRepoMarker strips "repo_" prefix for filenames:
	// ID "repo_old" -> file ".repo_old"
	oldMarker := map[string]string{
		"repo_id":  "repo_old",
		"endpoint": "https://sageox.ai",
	}
	data, _ := json.Marshal(oldMarker)
	oldPath := filepath.Join(sageoxDir, ".repo_old")
	require.NoError(t, os.WriteFile(oldPath, data, 0644))

	err := RenameRepoMarker(dir, "repo_old", "repo_new")
	assert.NoError(t, err)

	// old marker should be gone
	_, err = os.Stat(oldPath)
	assert.True(t, os.IsNotExist(err), "old marker should not exist")

	// new marker should exist with updated repo_id
	newPath := filepath.Join(sageoxDir, ".repo_new")
	newData, err := os.ReadFile(newPath)
	require.NoError(t, err)

	var newMarker map[string]interface{}
	require.NoError(t, json.Unmarshal(newData, &newMarker))
	assert.Equal(t, "repo_new", newMarker["repo_id"])
}

func TestRenameRepoMarker_NoOldMarker(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sageoxDir := filepath.Join(dir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	// no old marker exists - should not error
	err := RenameRepoMarker(dir, "repo_nonexistent", "repo_new")
	assert.NoError(t, err)
}
