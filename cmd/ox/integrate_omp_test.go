package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/require"
)

func setupUninstallAllTest(t *testing.T, adapterConfigs map[string]string) string {
	t.Helper()

	repoRoot := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = repoRoot
	require.NoError(t, cmd.Run())
	t.Chdir(repoRoot)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))

	adapterDir := t.TempDir()
	for name, configDir := range adapterConfigs {
		createFakeAdapterWithHooks(t, adapterDir, name, "0.1.0", "session", configDir)
		adapters.Unregister(name)
		adapterName := name
		t.Cleanup(func() { adapters.Unregister(adapterName) })
	}
	t.Setenv("OX_ADAPTER_PATH", adapterDir)
	return repoRoot
}

func TestUninstallAllIntegrationsRemovesOMP(t *testing.T) {
	repoRoot := setupUninstallAllTest(t, map[string]string{
		"amp":      ".amp",
		"codex":    ".codex",
		"gemini":   ".gemini",
		"omp":      ".omp",
		"opencode": ".opencode",
	})

	marker := filepath.Join(repoRoot, ".omp", "hooks.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(marker), 0o755))
	require.NoError(t, os.WriteFile(marker, []byte("{}\n"), 0o644))

	require.NoError(t, uninstallAllIntegrations(true))
	_, err := os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist, "--all must dispatch OMP uninstallation")
}

func TestUninstallAllIntegrationsSkipsMissingOMPAdapter(t *testing.T) {
	repoRoot := setupUninstallAllTest(t, map[string]string{
		"amp":      ".amp",
		"codex":    ".codex",
		"gemini":   ".gemini",
		"opencode": ".opencode",
	})

	marker := filepath.Join(repoRoot, ".amp", "hooks.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(marker), 0o755))
	require.NoError(t, os.WriteFile(marker, []byte("{}\n"), 0o644))

	require.NoError(t, uninstallAllIntegrations(true))
	_, err := os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist, "--all must continue when the OMP adapter is unavailable")
}
