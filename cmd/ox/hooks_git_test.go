package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", dir)
	require.NoError(t, cmd.Run(), "git init")
	return dir
}

func TestInstallGitHooks_NewFile(t *testing.T) {
	gitRoot := createTestGitRepo(t)

	err := InstallGitHooks(gitRoot)
	require.NoError(t, err)

	hookPath := filepath.Join(gitRoot, ".git", "hooks", "prepare-commit-msg")
	content, err := os.ReadFile(hookPath)
	require.NoError(t, err)

	assert.Contains(t, string(content), oxHookMarkerStart)
	assert.Contains(t, string(content), "ox hooks commit-msg")
	assert.Contains(t, string(content), oxHookMarkerEnd)
	assert.Contains(t, string(content), "#!/bin/sh")

	// verify executable permissions
	info, err := os.Stat(hookPath)
	require.NoError(t, err)
	assert.True(t, info.Mode()&0111 != 0, "hook should be executable")
}

func TestInstallGitHooks_AppendsToExisting(t *testing.T) {
	gitRoot := createTestGitRepo(t)

	hookPath := filepath.Join(gitRoot, ".git", "hooks", "prepare-commit-msg")
	existing := "#!/bin/sh\necho 'existing hook'\n"
	require.NoError(t, os.WriteFile(hookPath, []byte(existing), 0755))

	err := InstallGitHooks(gitRoot)
	require.NoError(t, err)

	content, err := os.ReadFile(hookPath)
	require.NoError(t, err)

	// existing content preserved
	assert.Contains(t, string(content), "existing hook")
	// ox section appended
	assert.Contains(t, string(content), oxHookMarkerStart)
	assert.Contains(t, string(content), "ox hooks commit-msg")
}

func TestInstallGitHooks_Idempotent(t *testing.T) {
	gitRoot := createTestGitRepo(t)

	require.NoError(t, InstallGitHooks(gitRoot))
	require.NoError(t, InstallGitHooks(gitRoot))

	hookPath := filepath.Join(gitRoot, ".git", "hooks", "prepare-commit-msg")
	content, err := os.ReadFile(hookPath)
	require.NoError(t, err)

	// should only contain one ox section
	count := 0
	for i := 0; i < len(string(content)); i++ {
		if i+len(oxHookMarkerStart) <= len(string(content)) && string(content)[i:i+len(oxHookMarkerStart)] == oxHookMarkerStart {
			count++
		}
	}
	assert.Equal(t, 1, count, "should have exactly one ox section")
}

func TestUninstallGitHooks_RemovesOxSection(t *testing.T) {
	gitRoot := createTestGitRepo(t)

	hookPath := filepath.Join(gitRoot, ".git", "hooks", "prepare-commit-msg")
	existing := "#!/bin/sh\necho 'user hook'\n"
	require.NoError(t, os.WriteFile(hookPath, []byte(existing), 0755))

	require.NoError(t, InstallGitHooks(gitRoot))
	require.NoError(t, UninstallGitHooks(gitRoot))

	content, err := os.ReadFile(hookPath)
	require.NoError(t, err)

	assert.Contains(t, string(content), "user hook")
	assert.NotContains(t, string(content), oxHookMarkerStart)
	assert.NotContains(t, string(content), "ox hooks commit-msg")
}

func TestUninstallGitHooks_RemovesFileWhenOnlyOx(t *testing.T) {
	gitRoot := createTestGitRepo(t)

	require.NoError(t, InstallGitHooks(gitRoot))
	require.NoError(t, UninstallGitHooks(gitRoot))

	hookPath := filepath.Join(gitRoot, ".git", "hooks", "prepare-commit-msg")
	_, err := os.Stat(hookPath)
	assert.True(t, os.IsNotExist(err), "hook file should be removed when only ox content")
}

func TestUninstallGitHooks_NoopWhenMissing(t *testing.T) {
	gitRoot := createTestGitRepo(t)
	assert.NoError(t, UninstallGitHooks(gitRoot))
}

func TestHasGitHooks(t *testing.T) {
	gitRoot := createTestGitRepo(t)

	assert.False(t, HasGitHooks(gitRoot))

	require.NoError(t, InstallGitHooks(gitRoot))
	assert.True(t, HasGitHooks(gitRoot))

	require.NoError(t, UninstallGitHooks(gitRoot))
	assert.False(t, HasGitHooks(gitRoot))
}

func TestInstallGitHooks_RespectsHooksPath(t *testing.T) {
	gitRoot := createTestGitRepo(t)

	// set core.hooksPath to a custom directory
	customHooksDir := filepath.Join(gitRoot, "custom-hooks")
	require.NoError(t, os.MkdirAll(customHooksDir, 0755))

	cmd := exec.Command("git", "-C", gitRoot, "config", "core.hooksPath", customHooksDir)
	require.NoError(t, cmd.Run())

	require.NoError(t, InstallGitHooks(gitRoot))

	// should be in custom dir, not .git/hooks
	customHookPath := filepath.Join(customHooksDir, "prepare-commit-msg")
	defaultHookPath := filepath.Join(gitRoot, ".git", "hooks", "prepare-commit-msg")

	assert.FileExists(t, customHookPath)
	_, err := os.Stat(defaultHookPath)
	assert.True(t, os.IsNotExist(err), "should not exist in default hooks dir")
}
