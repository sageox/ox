package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstallCodexHooks_CreatesFile(t *testing.T) {
	tmpDir := t.TempDir()
	codexDir := filepath.Join(tmpDir, ".codex")
	require.NoError(t, os.MkdirAll(codexDir, 0755))

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	err := installCodexHooks(false)
	require.NoError(t, err)

	hooksPath := filepath.Join(codexDir, "hooks.json")
	data, err := os.ReadFile(hooksPath)
	require.NoError(t, err)

	var config CodexHooksConfig
	require.NoError(t, json.Unmarshal(data, &config))

	entries := config.Hooks["SessionStart"]
	require.NotEmpty(t, entries, "expected SessionStart hooks")

	// should have ox hooks
	var foundOx bool
	for _, entry := range entries {
		for _, hook := range entry.Hooks {
			if strings.Contains(hook.Command, "ox agent hook") {
				foundOx = true
				assert.Equal(t, "Loading SageOx context", hook.StatusMessage)
			}
			assert.Equal(t, "command", hook.Type)
		}
	}
	assert.True(t, foundOx, "expected ox hook")
}

func TestInstallCodexHooks_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	codexDir := filepath.Join(tmpDir, ".codex")
	require.NoError(t, os.MkdirAll(codexDir, 0755))

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	// install twice
	require.NoError(t, installCodexHooks(false))
	require.NoError(t, installCodexHooks(false))

	hooksPath := filepath.Join(codexDir, "hooks.json")
	data, err := os.ReadFile(hooksPath)
	require.NoError(t, err)

	var config CodexHooksConfig
	require.NoError(t, json.Unmarshal(data, &config))

	entries := config.Hooks["SessionStart"]

	// count ox hooks — should be exactly 1
	oxCount := 0
	for _, entry := range entries {
		for _, hook := range entry.Hooks {
			if strings.Contains(hook.Command, "ox agent hook") {
				oxCount++
			}
		}
	}
	assert.Equal(t, 1, oxCount, "expected exactly 1 ox hook after double install")
}

func TestInstallCodexHooks_PreservesExistingHooks(t *testing.T) {
	tmpDir := t.TempDir()
	codexDir := filepath.Join(tmpDir, ".codex")
	require.NoError(t, os.MkdirAll(codexDir, 0755))

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	// pre-populate with a custom user hook
	existing := `{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "echo custom-hook",
            "statusMessage": "Custom hook"
          }
        ]
      }
    ]
  }
}`
	hooksPath := filepath.Join(codexDir, "hooks.json")
	require.NoError(t, os.WriteFile(hooksPath, []byte(existing), 0644))

	require.NoError(t, installCodexHooks(false))

	data, err := os.ReadFile(hooksPath)
	require.NoError(t, err)

	assert.Contains(t, string(data), "custom-hook", "existing hook must survive install")
	assert.Contains(t, string(data), "ox agent hook", "ox hook must be added")
}

func TestInstallCodexHooks_PreservesNonHookKeys(t *testing.T) {
	tmpDir := t.TempDir()
	codexDir := filepath.Join(tmpDir, ".codex")
	require.NoError(t, os.MkdirAll(codexDir, 0755))

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	// pre-populate with non-hook key
	existing := `{
  "someOtherKey": "someValue",
  "hooks": {}
}`
	hooksPath := filepath.Join(codexDir, "hooks.json")
	require.NoError(t, os.WriteFile(hooksPath, []byte(existing), 0644))

	require.NoError(t, installCodexHooks(false))

	data, err := os.ReadFile(hooksPath)
	require.NoError(t, err)

	assert.Contains(t, string(data), "someOtherKey", "non-hook keys must survive")
	assert.Contains(t, string(data), "someValue", "non-hook values must survive")
}

func TestUninstallCodexHooks_RemovesOxOnly(t *testing.T) {
	tmpDir := t.TempDir()
	codexDir := filepath.Join(tmpDir, ".codex")
	require.NoError(t, os.MkdirAll(codexDir, 0755))

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	// pre-populate with a custom user hook + ox hooks
	existing := `{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "echo custom-hook"
          }
        ]
      }
    ]
  }
}`
	hooksPath := filepath.Join(codexDir, "hooks.json")
	require.NoError(t, os.WriteFile(hooksPath, []byte(existing), 0644))

	// install ox hooks alongside existing
	require.NoError(t, installCodexHooks(false))

	// uninstall ox hooks
	require.NoError(t, uninstallCodexHooks(false))

	data, err := os.ReadFile(hooksPath)
	require.NoError(t, err)

	assert.NotContains(t, string(data), "ox agent hook", "ox hook must be removed")
	assert.Contains(t, string(data), "custom-hook", "user hooks must survive uninstall")
}

func TestUninstallCodexHooks_NoFile(t *testing.T) {
	tmpDir := t.TempDir()
	codexDir := filepath.Join(tmpDir, ".codex")
	require.NoError(t, os.MkdirAll(codexDir, 0755))

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	// should not error when no hooks file
	err := uninstallCodexHooks(false)
	assert.NoError(t, err)
}

func TestUninstallCodexHooks_RemovesFileWhenEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	codexDir := filepath.Join(tmpDir, ".codex")
	require.NoError(t, os.MkdirAll(codexDir, 0755))

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	// write only ox hooks
	hooksPath := filepath.Join(codexDir, "hooks.json")
	onlyOx := `{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "if command -v ox >/dev/null 2>&1; then AGENT_ENV=codex ox agent hook SessionStart 2>&1 || true; fi"
          }
        ]
      }
    ]
  }
}`
	require.NoError(t, os.WriteFile(hooksPath, []byte(onlyOx), 0644))

	require.NoError(t, uninstallCodexHooks(false))

	_, err := os.Stat(hooksPath)
	assert.True(t, os.IsNotExist(err), "hooks.json should be removed when empty after uninstall")
}

func TestHasCodexHooks_NoFile(t *testing.T) {
	tmpDir := t.TempDir()

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	assert.False(t, hasCodexHooks(false))
}

func TestHasCodexHooks_WithOxHooks(t *testing.T) {
	tmpDir := t.TempDir()
	codexDir := filepath.Join(tmpDir, ".codex")
	require.NoError(t, os.MkdirAll(codexDir, 0755))

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	require.NoError(t, installCodexHooks(false))

	assert.True(t, hasCodexHooks(false))
}

func TestListCodexHooks(t *testing.T) {
	tmpDir := t.TempDir()
	codexDir := filepath.Join(tmpDir, ".codex")
	require.NoError(t, os.MkdirAll(codexDir, 0755))

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	status := listCodexHooks()
	assert.False(t, status["Project"])

	require.NoError(t, installCodexHooks(false))

	status = listCodexHooks()
	assert.True(t, status["Project"])
}

func TestCodexHook_StatusMessage(t *testing.T) {
	tmpDir := t.TempDir()
	codexDir := filepath.Join(tmpDir, ".codex")
	require.NoError(t, os.MkdirAll(codexDir, 0755))

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	require.NoError(t, installCodexHooks(false))

	hooksPath := filepath.Join(codexDir, "hooks.json")
	data, err := os.ReadFile(hooksPath)
	require.NoError(t, err)

	// verify statusMessage fields are present in JSON
	assert.Contains(t, string(data), "statusMessage")
	assert.Contains(t, string(data), "Loading SageOx context")
}

func TestCodexAgent_SupportsHooks(t *testing.T) {
	agent := &CodexAgent{}
	assert.True(t, agent.SupportsHooks(), "CodexAgent should support hooks")
}

func TestInstallCodexHooks_CreatesConfigToml(t *testing.T) {
	tmpDir := t.TempDir()
	codexDir := filepath.Join(tmpDir, ".codex")
	require.NoError(t, os.MkdirAll(codexDir, 0755))

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	require.NoError(t, installCodexHooks(false))

	// verify config.toml was created with codex_hooks feature flag
	configPath := filepath.Join(codexDir, "config.toml")
	data, err := os.ReadFile(configPath)
	require.NoError(t, err, "config.toml should be created by install")

	content := string(data)
	assert.Contains(t, content, "codex_hooks", "config.toml must contain codex_hooks")
	assert.Contains(t, content, "true", "codex_hooks must be true")
}

func TestInstallCodexHooks_PreservesExistingConfigToml(t *testing.T) {
	tmpDir := t.TempDir()
	codexDir := filepath.Join(tmpDir, ".codex")
	require.NoError(t, os.MkdirAll(codexDir, 0755))

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	// pre-populate config.toml with user settings
	configPath := filepath.Join(codexDir, "config.toml")
	existing := "model = \"o3\"\n\n[features]\nfast_mode = true\n"
	require.NoError(t, os.WriteFile(configPath, []byte(existing), 0644))

	require.NoError(t, installCodexHooks(false))

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	content := string(data)

	assert.Contains(t, content, "codex_hooks", "codex_hooks must be added")
	assert.Contains(t, content, "fast_mode", "existing features must survive")
	assert.Contains(t, content, "model", "existing top-level keys must survive")
}

func TestUninstallCodexHooks_RemovesConfigTomlFlag(t *testing.T) {
	tmpDir := t.TempDir()
	codexDir := filepath.Join(tmpDir, ".codex")
	require.NoError(t, os.MkdirAll(codexDir, 0755))

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	// install then uninstall — write only ox hooks so file gets fully cleaned
	hooksPath := filepath.Join(codexDir, "hooks.json")
	onlyOx := `{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "if command -v ox >/dev/null 2>&1; then AGENT_ENV=codex ox agent hook SessionStart 2>&1 || true; fi"
          }
        ]
      }
    ]
  }
}`
	require.NoError(t, os.WriteFile(hooksPath, []byte(onlyOx), 0644))

	// manually set feature flag to simulate install
	require.NoError(t, ensureCodexHooksFeatureFlag(false))

	// verify it's set
	configPath := filepath.Join(codexDir, "config.toml")
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "codex_hooks")

	// uninstall
	require.NoError(t, uninstallCodexHooks(false))

	// config.toml should have codex_hooks removed (file may be removed entirely if empty)
	if data, err := os.ReadFile(configPath); err == nil {
		assert.NotContains(t, string(data), "codex_hooks",
			"codex_hooks flag should be removed on uninstall")
	}
	// either file is gone or codex_hooks is gone — both are correct
}

func TestEnsureCodexHooksFeatureFlag_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	codexDir := filepath.Join(tmpDir, ".codex")
	require.NoError(t, os.MkdirAll(codexDir, 0755))

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	require.NoError(t, ensureCodexHooksFeatureFlag(false))
	require.NoError(t, ensureCodexHooksFeatureFlag(false))

	configPath := filepath.Join(codexDir, "config.toml")
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)

	// should only appear once
	count := strings.Count(string(data), "codex_hooks")
	assert.Equal(t, 1, count, "codex_hooks should appear exactly once")
}
