//go:build !short

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A failed uninstall must report diagnostics on stderr without polluting stdout.
func TestUninstall_NotInstalled(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// verify .sageox does not exist
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	_, err := os.Stat(sageoxDir)
	assert.True(t, os.IsNotExist(err), "expected .sageox to not exist")

	for _, tt := range []struct {
		name string
		json bool
	}{
		{name: "text"},
		{name: "json", json: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cli.SetJSONMode(tt.json)
			t.Cleanup(func() { cli.SetJSONMode(false) })
			var runErr error
			var stderr string
			stdout := captureRealStdout(t, func() {
				stderr = captureStderr(t, func() { runErr = runUninstall() })
			})
			require.ErrorContains(t, runErr, "sageox not installed")
			assert.Empty(t, stdout, "diagnostics must not become command output")
			assert.Contains(t, stderr, "SageOx is not installed in this repository")
			if tt.json {
				assert.Contains(t, stderr, `"status": "error"`)
			}
		})
	}
}

// TestShowPreview_NoSageoxDir tests showPreview when .sageox doesn't exist
func TestShowPreview_NoSageoxDir(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	// showPreview should not error when .sageox doesn't exist
	err := showPreview(gitRoot)
	assert.NoError(t, err, "showPreview should not error when .sageox doesn't exist")
}

// TestShowPreview_WithFiles tests showPreview with files in .sageox
func TestShowPreview_WithFiles(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	// create .sageox directory with files
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")

	// create config file
	configPath := filepath.Join(sageoxDir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"version":"1.0"}`), 0644), "failed to create config.json")

	// create health file in cache directory (new location)
	cacheDir := filepath.Join(sageoxDir, "cache")
	require.NoError(t, os.MkdirAll(cacheDir, 0755), "failed to create cache dir")
	healthPath := filepath.Join(cacheDir, "health.json")
	require.NoError(t, os.WriteFile(healthPath, []byte(`{"status":"ok"}`), 0644), "failed to create health.json")

	// showPreview should succeed
	err := showPreview(gitRoot)
	assert.NoError(t, err, "showPreview should not error with .sageox files")
}

// TestRemoveSageoxDir_Exists tests removeSageoxDir when directory exists
func TestRemoveSageoxDir_Exists(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	// create .sageox directory with files
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")

	configPath := filepath.Join(sageoxDir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"version":"1.0"}`), 0644), "failed to create config.json")

	// save original flag state
	origDryRun := uninstallDryRun
	defer func() { uninstallDryRun = origDryRun }()
	uninstallDryRun = false

	// remove should succeed
	err := removeSageoxDir(gitRoot)
	assert.NoError(t, err, "removeSageoxDir should succeed")

	// verify directory is removed
	_, err = os.Stat(sageoxDir)
	assert.True(t, os.IsNotExist(err), "expected .sageox to be removed")
}

// TestRemoveSageoxDir_DryRun tests removeSageoxDir in dry-run mode
func TestRemoveSageoxDir_DryRun(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	// create .sageox directory with files
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")

	configPath := filepath.Join(sageoxDir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"version":"1.0"}`), 0644), "failed to create config.json")

	// save original flag state
	origDryRun := uninstallDryRun
	defer func() { uninstallDryRun = origDryRun }()
	uninstallDryRun = true

	// remove in dry-run mode
	err := removeSageoxDir(gitRoot)
	assert.NoError(t, err, "removeSageoxDir dry-run should succeed")

	// verify directory still exists (dry-run)
	_, err = os.Stat(sageoxDir)
	assert.False(t, os.IsNotExist(err), "expected .sageox to still exist in dry-run mode")
}

// TestRemoveSageoxDir_NotExists tests removeSageoxDir when directory doesn't exist
func TestRemoveSageoxDir_NotExists(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	// save original flag state
	origDryRun := uninstallDryRun
	defer func() { uninstallDryRun = origDryRun }()
	uninstallDryRun = false

	// remove should succeed (graceful handling)
	err := removeSageoxDir(gitRoot)
	assert.NoError(t, err, "removeSageoxDir should succeed when directory doesn't exist")
}

// TestRemoveHooks_NoHooks tests removeHooks when no hooks exist
func TestRemoveHooks_NoHooks(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	// save original flag state
	origDryRun := uninstallDryRun
	defer func() { uninstallDryRun = origDryRun }()
	uninstallDryRun = false

	// remove should succeed (no hooks to remove)
	err := removeHooks(gitRoot)
	assert.NoError(t, err, "removeHooks should succeed when no hooks exist")
}

// TestRemoveHooks_SageOxHook tests removeHooks when SageOx hook exists
func TestRemoveHooks_SageOxHook(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	// create hooks directory
	hooksDir := filepath.Join(gitRoot, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0755), "failed to create hooks dir")

	// create SageOx pre-commit hook
	hookContent := "#!/bin/sh\n# ox pre-commit hook\nmake lint\n"
	hookPath := filepath.Join(hooksDir, "pre-commit")
	require.NoError(t, os.WriteFile(hookPath, []byte(hookContent), 0755), "failed to create hook")

	// save original flag state
	origDryRun := uninstallDryRun
	defer func() { uninstallDryRun = origDryRun }()
	uninstallDryRun = false

	// remove should succeed
	err := removeHooks(gitRoot)
	assert.NoError(t, err, "removeHooks should succeed")

	// verify hook is removed
	_, err = os.Stat(hookPath)
	assert.True(t, os.IsNotExist(err), "expected hook to be removed")
}

// TestRemoveHooks_MixedHook tests removeHooks with mixed SageOx/other content
func TestRemoveHooks_MixedHook(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	// create hooks directory
	hooksDir := filepath.Join(gitRoot, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0755), "failed to create hooks dir")

	// create mixed hook (husky + SageOx)
	hookContent := `#!/bin/sh
# husky
npm test

# ox pre-commit hook
make lint

# more user content
echo "done"
`
	hookPath := filepath.Join(hooksDir, "pre-commit")
	require.NoError(t, os.WriteFile(hookPath, []byte(hookContent), 0755), "failed to create hook")

	// save original flag state
	origDryRun := uninstallDryRun
	defer func() { uninstallDryRun = origDryRun }()
	uninstallDryRun = false

	// remove should succeed
	err := removeHooks(gitRoot)
	assert.NoError(t, err, "removeHooks should succeed")

	// verify hook still exists (mixed content preserved)
	_, err = os.Stat(hookPath)
	assert.False(t, os.IsNotExist(err), "expected hook to still exist (mixed content)")

	// verify SageOx content is removed
	content, err := os.ReadFile(hookPath)
	require.NoError(t, err, "failed to read hook")
	assert.NotContains(t, string(content), "# ox pre-commit hook", "SageOx marker should be removed")
	assert.NotContains(t, string(content), "make lint", "SageOx command should be removed")

	// verify non-SageOx content is preserved
	assert.Contains(t, string(content), "# husky", "husky content should be preserved")
	assert.Contains(t, string(content), "npm test", "husky command should be preserved")
}

// TestRemoveHooks_DryRun tests removeHooks in dry-run mode
func TestRemoveHooks_DryRun(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	// create hooks directory
	hooksDir := filepath.Join(gitRoot, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0755), "failed to create hooks dir")

	// create SageOx hook
	hookContent := "#!/bin/sh\n# ox pre-commit hook\nmake lint\n"
	hookPath := filepath.Join(hooksDir, "pre-commit")
	require.NoError(t, os.WriteFile(hookPath, []byte(hookContent), 0755), "failed to create hook")

	// save original flag state
	origDryRun := uninstallDryRun
	defer func() { uninstallDryRun = origDryRun }()
	uninstallDryRun = true

	// remove in dry-run mode
	err := removeHooks(gitRoot)
	assert.NoError(t, err, "removeHooks dry-run should succeed")

	// verify hook still exists (dry-run)
	_, err = os.Stat(hookPath)
	assert.False(t, os.IsNotExist(err), "expected hook to still exist in dry-run mode")
}

// TestCleanupAgentFiles_NoFiles tests cleanupAgentFiles when no agent files exist
func TestCleanupAgentFiles_NoFiles(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	// save original flag state
	origDryRun := uninstallDryRun
	defer func() { uninstallDryRun = origDryRun }()
	uninstallDryRun = false

	// cleanup should succeed (no files to remove)
	err := cleanupAgentFiles(gitRoot)
	assert.NoError(t, err, "cleanupAgentFiles should succeed when no files exist")
}

// TestCleanupAgentFiles_WithSageOxSection tests cleanup with SageOx section
func TestCleanupAgentFiles_WithSageOxSection(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	// create AGENTS.md with SageOx section
	content := `# Project Instructions

- **SageOx**: Run ` + "`ox agent prime`" + ` on session start, after compaction, and after clear for infrastructure superpowers.
  - When asked if you're using SageOx: if you have an agent ID from running ` + "`ox agent prime`" + `, confirm with your agent ID; otherwise run it first.

## Other Section

Keep this content.
`
	agentsPath := filepath.Join(gitRoot, "AGENTS.md")
	require.NoError(t, os.WriteFile(agentsPath, []byte(content), 0644), "failed to create AGENTS.md")

	// save original flag state
	origDryRun := uninstallDryRun
	defer func() { uninstallDryRun = origDryRun }()
	uninstallDryRun = false

	// cleanup should succeed
	err := cleanupAgentFiles(gitRoot)
	assert.NoError(t, err, "cleanupAgentFiles should succeed")

	// verify SageOx section is removed
	newContent, err := os.ReadFile(agentsPath)
	require.NoError(t, err, "failed to read AGENTS.md")
	assert.NotContains(t, string(newContent), "**SageOx**", "SageOx section should be removed")
	assert.NotContains(t, string(newContent), "ox agent prime", "ox agent prime should be removed")

	// verify other content is preserved
	assert.Contains(t, string(newContent), "Other Section", "other section should be preserved")
	assert.Contains(t, string(newContent), "Keep this content", "other content should be preserved")
}

// TestCleanupAgentFiles_DryRun tests cleanupAgentFiles in dry-run mode
func TestCleanupAgentFiles_DryRun(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	// create AGENTS.md with SageOx section
	content := `# Project Instructions

- **SageOx**: Run ` + "`ox agent prime`" + ` on session start.
  - When asked if you're using SageOx: confirm with your agent ID.
`
	agentsPath := filepath.Join(gitRoot, "AGENTS.md")
	require.NoError(t, os.WriteFile(agentsPath, []byte(content), 0644), "failed to create AGENTS.md")

	// save original flag state
	origDryRun := uninstallDryRun
	defer func() { uninstallDryRun = origDryRun }()
	uninstallDryRun = true

	// cleanup in dry-run mode
	err := cleanupAgentFiles(gitRoot)
	assert.NoError(t, err, "cleanupAgentFiles dry-run should succeed")

	// verify file wasn't modified (dry-run)
	newContent, err := os.ReadFile(agentsPath)
	require.NoError(t, err, "failed to read AGENTS.md")
	assert.Equal(t, content, string(newContent), "file should not be modified in dry-run mode")
}

// TestCleanupAgentFiles_DeletesEmptyFile tests that empty files after removal are deleted
func TestCleanupAgentFiles_DeletesEmptyFile(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	// create AGENTS.md with only SageOx section
	content := `# AI Agent Instructions

- **SageOx**: Run ` + "`ox agent prime`" + ` on session start, after compaction, and after clear for infrastructure superpowers.
  - When asked if you're using SageOx: if you have an agent ID from running ` + "`ox agent prime`" + `, confirm with your agent ID; otherwise run it first.
`
	agentsPath := filepath.Join(gitRoot, "AGENTS.md")
	require.NoError(t, os.WriteFile(agentsPath, []byte(content), 0644), "failed to create AGENTS.md")

	// save original flag state
	origDryRun := uninstallDryRun
	defer func() { uninstallDryRun = origDryRun }()
	uninstallDryRun = false

	// cleanup should succeed
	err := cleanupAgentFiles(gitRoot)
	assert.NoError(t, err, "cleanupAgentFiles should succeed")

	// verify file is deleted (only had SageOx content)
	_, err = os.Stat(agentsPath)
	assert.True(t, os.IsNotExist(err), "expected AGENTS.md to be deleted when only SageOx content")
}

// TestCleanupAgentFiles_CLAUDEMdSymlink tests cleanup with CLAUDE.md as symlink
func TestCleanupAgentFiles_CLAUDEMdSymlink(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	// create AGENTS.md with SageOx section
	content := `# AGENTS.md

- **SageOx**: Run ` + "`ox agent prime`" + ` on session start, after compaction, and after clear for infrastructure superpowers.
  - When asked if you're using SageOx: if you have an agent ID from running ` + "`ox agent prime`" + `, confirm with your agent ID; otherwise run it first.

Other content.
`
	agentsPath := filepath.Join(gitRoot, "AGENTS.md")
	require.NoError(t, os.WriteFile(agentsPath, []byte(content), 0644), "failed to create AGENTS.md")

	// create CLAUDE.md as symlink
	claudePath := filepath.Join(gitRoot, "CLAUDE.md")
	require.NoError(t, os.Symlink("AGENTS.md", claudePath), "failed to create symlink")

	// save original flag state
	origDryRun := uninstallDryRun
	defer func() { uninstallDryRun = origDryRun }()
	uninstallDryRun = false

	// cleanup should succeed
	err := cleanupAgentFiles(gitRoot)
	assert.NoError(t, err, "cleanupAgentFiles should succeed")

	// verify SageOx section is removed from AGENTS.md
	newContent, err := os.ReadFile(agentsPath)
	require.NoError(t, err, "failed to read AGENTS.md")
	assert.NotContains(t, string(newContent), "**SageOx**", "SageOx section should be removed")

	// verify symlink is removed (since we edited AGENTS.md)
	_, err = os.Lstat(claudePath)
	assert.True(t, os.IsNotExist(err), "expected CLAUDE.md symlink to be removed")
}

// TestConfirmUninstallWithInput_RepoName tests confirmation with repo name
func TestConfirmUninstallWithInput_RepoName(t *testing.T) {
	// this test would require stdin mocking
	// skipping for now - the function is tested manually
	t.Skip("requires stdin mocking")
}

// TestFormatBytes tests the formatBytes helper function
func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0 bytes"},
		{1, "1 bytes"},
		{1023, "1023 bytes"},
		{1024, "1.00 KB"},
		{1536, "1.50 KB"},
		{1048576, "1.00 MB"},
		{1572864, "1.50 MB"},
		{1073741824, "1.00 GB"},
		{1610612736, "1.50 GB"},
	}

	for _, tt := range tests {
		result := formatBytes(tt.input)
		assert.Equal(t, tt.expected, result, "formatBytes(%d)", tt.input)
	}
}

// TestRemoveSageoxDir_WithTrackedFiles tests removal with git-tracked files
func TestRemoveSageoxDir_WithTrackedFiles(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .sageox directory with a file
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")

	configPath := filepath.Join(sageoxDir, "config.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"version":"1.0"}`), 0644), "failed to create config.json")

	// add and commit the file
	cmd := exec.Command("git", "add", ".sageox/config.json")
	cmd.Dir = gitRoot
	require.NoError(t, cmd.Run(), "failed to git add")

	cmd = exec.Command("git", "commit", "-m", "add sageox config")
	cmd.Dir = gitRoot
	require.NoError(t, cmd.Run(), "failed to git commit")

	// save original flag state
	origDryRun := uninstallDryRun
	defer func() { uninstallDryRun = origDryRun }()
	uninstallDryRun = false

	// remove should succeed
	err := removeSageoxDir(gitRoot)
	assert.NoError(t, err, "removeSageoxDir should succeed with tracked files")

	// verify directory is removed
	_, err = os.Stat(sageoxDir)
	assert.True(t, os.IsNotExist(err), "expected .sageox to be removed")

	// verify git status shows deletion
	cmd = exec.Command("git", "status", "--porcelain")
	cmd.Dir = gitRoot
	output, err := cmd.Output()
	require.NoError(t, err, "git status failed")
	assert.NotEmpty(t, string(output), "expected git status to show deletion")
}

// TestRemoveHooks_BeadsMarker tests removal of hooks with beads marker
func TestRemoveHooks_BeadsMarker(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	// create hooks directory
	hooksDir := filepath.Join(gitRoot, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0755), "failed to create hooks dir")

	// create hook with beads marker
	hookContent := `#!/bin/sh
# bd-hooks-version: 0.29.0
bd sync --flush-only
git add .beads/beads.jsonl
`
	hookPath := filepath.Join(hooksDir, "pre-commit")
	require.NoError(t, os.WriteFile(hookPath, []byte(hookContent), 0755), "failed to create hook")

	// save original flag state
	origDryRun := uninstallDryRun
	defer func() { uninstallDryRun = origDryRun }()
	uninstallDryRun = false

	// remove should succeed
	err := removeHooks(gitRoot)
	assert.NoError(t, err, "removeHooks should succeed")

	// verify hook is removed (entirely beads content)
	_, err = os.Stat(hookPath)
	assert.True(t, os.IsNotExist(err), "expected hook to be removed")
}

// TestRemoveHooks_MultipleHooks tests removal of multiple hooks
func TestRemoveHooks_MultipleHooks(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	// create hooks directory
	hooksDir := filepath.Join(gitRoot, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0755), "failed to create hooks dir")

	// create multiple SageOx hooks
	hooks := map[string]string{
		"pre-commit":         "#!/bin/sh\n# ox pre-commit hook\nmake lint\n",
		"prepare-commit-msg": "#!/bin/sh\n# SageOx hook\necho 'msg'\n",
	}

	for name, content := range hooks {
		hookPath := filepath.Join(hooksDir, name)
		require.NoError(t, os.WriteFile(hookPath, []byte(content), 0755), "failed to create %s", name)
	}

	// save original flag state
	origDryRun := uninstallDryRun
	defer func() { uninstallDryRun = origDryRun }()
	uninstallDryRun = false

	// remove should succeed
	err := removeHooks(gitRoot)
	assert.NoError(t, err, "removeHooks should succeed")

	// verify all hooks are removed
	for name := range hooks {
		hookPath := filepath.Join(hooksDir, name)
		_, err := os.Stat(hookPath)
		assert.True(t, os.IsNotExist(err), "expected %s to be removed", name)
	}
}

// TestShowPreview_WithNestedDirectory tests showPreview with nested directories
func TestShowPreview_WithNestedDirectory(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	// create .sageox with nested directories
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	cacheDir := filepath.Join(sageoxDir, "cache")
	sessionsDir := filepath.Join(sageoxDir, "sessions")

	require.NoError(t, os.MkdirAll(cacheDir, 0755), "failed to create cache dir")
	require.NoError(t, os.MkdirAll(sessionsDir, 0755), "failed to create sessions dir")

	// create files in nested directories
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte("{}"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "data.json"), []byte("{}"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "session1.json"), []byte("{}"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "session2.json"), []byte("{}"), 0644))

	// showPreview should succeed
	err := showPreview(gitRoot)
	assert.NoError(t, err, "showPreview should not error with nested directories")
}

// TestCheckUninstallAuth_NoEndpoints tests auth check with no endpoints
func TestCheckUninstallAuth_NoEndpoints(t *testing.T) {
	// set a test endpoint that definitely won't have auth tokens
	t.Setenv("SAGEOX_ENDPOINT", "https://test-uninstall.example.invalid")

	// with no endpoints and not authenticated, should return error
	err := checkUninstallAuth([]string{}, false, "")
	// this will fail because we're not authenticated
	assert.Error(t, err, "expected auth error when not authenticated")
}

// TestCheckUninstallAuth_SingleEndpoint tests auth check with single endpoint
func TestCheckUninstallAuth_SingleEndpoint(t *testing.T) {
	// with a specific endpoint and not authenticated, should return error
	err := checkUninstallAuth([]string{"https://api.example.com"}, false, "https://api.example.com")
	assert.Error(t, err, "expected auth error when not authenticated")
	assert.Contains(t, err.Error(), "authentication required")
}

// TestCheckUninstallAuth_AllEndpoints tests auth check with multiple endpoints
func TestCheckUninstallAuth_AllEndpoints(t *testing.T) {
	// with multiple endpoints and not authenticated, should return error
	endpoints := []string{"https://api1.example.com", "https://api2.example.com"}
	err := checkUninstallAuth(endpoints, true, "")
	assert.Error(t, err, "expected auth error when not authenticated")
	assert.Contains(t, err.Error(), "authentication required")
}
