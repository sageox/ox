package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setCursorIntegrateFlags(t *testing.T, user bool) {
	t.Helper()
	previousCursor, previousUser := integrateCursorFlag, integrateUserFlag
	integrateCursorFlag, integrateUserFlag = true, user
	t.Cleanup(func() {
		integrateCursorFlag, integrateUserFlag = previousCursor, previousUser
	})
}

func TestIntegrateCursorProjectLifecycle(t *testing.T) {
	repoRoot := setupUninstallAllTest(t, map[string]string{"cursor": ".cursor"})
	setCursorIntegrateFlags(t, false)

	unrelated := filepath.Join(repoRoot, ".cursor", "keep.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(unrelated), 0o755))
	require.NoError(t, os.WriteFile(unrelated, []byte("unrelated\n"), 0o644))

	require.NoError(t, runIntegrateInstall(integrateInstallCmd, nil))
	assert.FileExists(t, filepath.Join(repoRoot, ".cursor", "hooks.json"))
	require.NoError(t, runIntegrateUninstall(integrateUninstallCmd, nil))
	assert.NoFileExists(t, filepath.Join(repoRoot, ".cursor", "hooks.json"))
	data, err := os.ReadFile(unrelated)
	require.NoError(t, err)
	assert.Equal(t, "unrelated\n", string(data))
}

func TestIntegrateCursorRejectsUserScope(t *testing.T) {
	repoRoot := setupUninstallAllTest(t, map[string]string{"cursor": ".cursor"})
	setCursorIntegrateFlags(t, true)

	err := runIntegrateInstall(integrateInstallCmd, nil)
	require.ErrorContains(t, err, "user-level Cursor Agents Window integration is unsupported")
	assert.NoFileExists(t, filepath.Join(repoRoot, ".cursor", "hooks.json"))
	err = runIntegrateUninstall(integrateUninstallCmd, nil)
	require.ErrorContains(t, err, "user-level Cursor Agents Window integration is unsupported")
}

func TestIntegrateCursorRequiresProjectRepository(t *testing.T) {
	adapterDir := t.TempDir()
	createFakeAdapterWithHooks(t, adapterDir, "cursor", "0.1.0", "session", ".cursor")
	adapters.Unregister("cursor")
	t.Cleanup(func() { adapters.Unregister("cursor") })
	t.Setenv("OX_ADAPTER_PATH", adapterDir)
	t.Chdir(t.TempDir())
	setCursorIntegrateFlags(t, false)

	err := runIntegrateInstall(integrateInstallCmd, nil)
	require.ErrorContains(t, err, "not in a git repository")
	err = runIntegrateUninstall(integrateUninstallCmd, nil)
	require.ErrorContains(t, err, "not in a git repository")
}

func TestIntegrateCursorFlagIsAnExplicitSelector(t *testing.T) {
	previous := integrateCursorFlag
	integrateCursorFlag = true
	t.Cleanup(func() { integrateCursorFlag = previous })

	assert.True(t, hasAnyAgentFlag())
	installFlag := integrateInstallCmd.Flags().Lookup("cursor")
	uninstallFlag := integrateUninstallCmd.Flags().Lookup("cursor")
	require.NotNil(t, installFlag)
	require.NotNil(t, uninstallFlag)
	assert.False(t, installFlag.Hidden)
	assert.False(t, uninstallFlag.Hidden)
}

func TestUninstallAllIntegrationsRemovesCursor(t *testing.T) {
	repoRoot := setupUninstallAllTest(t, map[string]string{
		"amp":      ".amp",
		"codex":    ".codex",
		"cursor":   ".cursor",
		"gemini":   ".gemini",
		"opencode": ".opencode",
	})
	marker := filepath.Join(repoRoot, ".cursor", "hooks.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(marker), 0o755))
	require.NoError(t, os.WriteFile(marker, []byte("{}\n"), 0o644))

	require.NoError(t, uninstallAllIntegrations(true))
	_, err := os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist, "--all must dispatch Cursor uninstallation")
}
