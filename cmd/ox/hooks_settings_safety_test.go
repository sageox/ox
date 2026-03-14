//go:build !short

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

// TestAllInstalledHooksHaveOxGuard verifies that every ox hook command installed
// by InstallProjectClaudeHooks uses "command -v ox" to gracefully handle missing CLI.
func TestAllInstalledHooksHaveOxGuard(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0755))

	err := InstallProjectClaudeHooks(tmpDir)
	require.NoError(t, err)

	settings, err := readProjectClaudeSettings(tmpDir)
	require.NoError(t, err)

	for eventName, entries := range settings.Hooks {
		for _, entry := range entries {
			for _, hook := range entry.Hooks {
				if hook.Type != hookType {
					continue
				}
				assert.Contains(t, hook.Command, "command -v ox",
					"hook in %s (matcher=%q) must guard against missing ox CLI", eventName, entry.Matcher)
				assert.Contains(t, hook.Command, "|| true",
					"hook in %s (matcher=%q) must not fail if ox errors", eventName, entry.Matcher)
			}
		}
	}
}

// TestHookFallbackMessage verifies the fallback message shown when ox is not installed.
func TestHookFallbackMessage(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0755))

	err := InstallProjectClaudeHooks(tmpDir)
	require.NoError(t, err)

	settings, err := readProjectClaudeSettings(tmpDir)
	require.NoError(t, err)

	for eventName, entries := range settings.Hooks {
		for _, entry := range entries {
			for _, hook := range entry.Hooks {
				if hook.Type != hookType {
					continue
				}
				// the else branch should contain a helpful install message
				assert.Contains(t, hook.Command, "else echo",
					"hook in %s (matcher=%q) should echo fallback when ox missing", eventName, entry.Matcher)
				assert.Contains(t, hook.Command, "github.com/sageox/ox",
					"fallback message in %s (matcher=%q) should point to install URL", eventName, entry.Matcher)
			}
		}
	}
}

// TestInstallProjectHooksIdempotent verifies that running install multiple times
// produces the same result without duplicating hooks.
func TestInstallProjectHooksIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0755))

	// install three times
	for i := 0; i < 3; i++ {
		err := InstallProjectClaudeHooks(tmpDir)
		require.NoError(t, err, "install attempt %d should succeed", i+1)
	}

	settings, err := readProjectClaudeSettings(tmpDir)
	require.NoError(t, err)

	// count ox hooks per matcher - should be exactly 1 each
	for _, entry := range settings.Hooks[claudeSessionStart] {
		oxCount := 0
		for _, hook := range entry.Hooks {
			if isAnyOxCommand(hook.Command) {
				oxCount++
			}
		}
		assert.Equal(t, 1, oxCount,
			"matcher %q should have exactly 1 ox hook after repeated installs, got %d", entry.Matcher, oxCount)
	}

	for _, entry := range settings.Hooks[claudePreCompact] {
		oxCount := 0
		for _, hook := range entry.Hooks {
			if isAnyOxCommand(hook.Command) {
				oxCount++
			}
		}
		assert.Equal(t, 1, oxCount,
			"PreCompact should have exactly 1 ox hook after repeated installs, got %d", oxCount)
	}
}

// TestInstallPreservesExistingHooks verifies non-ox hooks survive install.
func TestInstallPreservesExistingHooks(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0755))

	// pre-populate with existing hooks from other tools
	existingSettings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "echo 'my custom hook'"},
					},
				},
			},
			"PreToolUse": []interface{}{
				map[string]interface{}{
					"matcher": "Edit",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "echo 'before edit'"},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(existingSettings, "", "  ")
	settingsPath := filepath.Join(claudeDir, "settings.json")
	require.NoError(t, os.WriteFile(settingsPath, data, 0644))

	// install ox hooks
	err := InstallProjectClaudeHooks(tmpDir)
	require.NoError(t, err)

	// re-read settings
	settings, err := readProjectClaudeSettings(tmpDir)
	require.NoError(t, err)

	// PreToolUse hooks should be untouched
	preToolUse := settings.Hooks["PreToolUse"]
	require.Len(t, preToolUse, 1, "PreToolUse should be preserved")
	assert.Equal(t, "Edit", preToolUse[0].Matcher)
	assert.Equal(t, "echo 'before edit'", preToolUse[0].Hooks[0].Command)

	// custom SessionStart hook should be preserved alongside ox hooks
	sessionStart := settings.Hooks[claudeSessionStart]
	require.NotEmpty(t, sessionStart)

	foundCustom := false
	foundOx := false
	for _, entry := range sessionStart {
		for _, hook := range entry.Hooks {
			if strings.Contains(hook.Command, "my custom hook") {
				foundCustom = true
			}
			if strings.Contains(hook.Command, "ox agent hook") {
				foundOx = true
			}
		}
	}
	assert.True(t, foundCustom, "custom SessionStart hook should be preserved")
	assert.True(t, foundOx, "ox lifecycle hook should be added")
}

// TestReadCorruptedSettingsJSON verifies graceful handling of malformed JSON.
func TestReadCorruptedSettingsJSON(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{
			name:    "valid JSON",
			content: `{"hooks": {}}`,
			wantErr: false,
		},
		{
			name:    "empty file",
			content: "",
			wantErr: false,
		},
		{
			name:    "truncated JSON",
			content: `{"hooks": {"SessionStart": [{"matcher": "`,
			wantErr: true,
		},
		{
			name:    "invalid JSON syntax",
			content: `{hooks: invalid}`,
			wantErr: true,
		},
		{
			name:    "JSON with trailing comma",
			content: `{"hooks": {},}`,
			wantErr: true,
		},
		{
			name:    "valid but no hooks key",
			content: `{"permissions": {"allow": []}}`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			claudeDir := filepath.Join(tmpDir, ".claude")
			require.NoError(t, os.MkdirAll(claudeDir, 0755))

			settingsPath := filepath.Join(claudeDir, "settings.json")
			require.NoError(t, os.WriteFile(settingsPath, []byte(tt.content), 0644))

			settings, _, err := readSharedClaudeSettings(tmpDir)
			if tt.wantErr {
				assert.Error(t, err, "should error on: %s", tt.name)
			} else {
				assert.NoError(t, err, "should not error on: %s", tt.name)
				assert.NotNil(t, settings)
			}
		})
	}
}

// TestInstallOnCorruptedSettingsDoesNotSilentlyCorrupt verifies that installing
// hooks on a corrupted settings file returns an error rather than silently
// overwriting with partial data.
func TestInstallOnCorruptedSettingsDoesNotSilentlyCorrupt(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0755))

	corruptedContent := `{"permissions": {"allow": ["Bash(npm:*)"]}, "hooks": {INVALID`
	settingsPath := filepath.Join(claudeDir, "settings.json")
	require.NoError(t, os.WriteFile(settingsPath, []byte(corruptedContent), 0644))

	// install should fail, not silently overwrite
	err := InstallProjectClaudeHooks(tmpDir)
	assert.Error(t, err, "should not silently overwrite corrupted settings")

	// original content should be unchanged
	data, readErr := os.ReadFile(settingsPath)
	require.NoError(t, readErr)
	assert.Equal(t, corruptedContent, string(data), "corrupted file should be untouched on error")
}

// TestWriteProducesValidJSON verifies the output is always valid, parseable JSON.
func TestWriteProducesValidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0755))

	err := InstallProjectClaudeHooks(tmpDir)
	require.NoError(t, err)

	settingsPath := filepath.Join(claudeDir, "settings.json")
	data, err := os.ReadFile(settingsPath)
	require.NoError(t, err)

	// verify it's valid JSON
	var parsed interface{}
	err = json.Unmarshal(data, &parsed)
	assert.NoError(t, err, "output must be valid JSON")

	// verify it round-trips cleanly
	remarshaled, err := json.MarshalIndent(parsed, "", "  ")
	assert.NoError(t, err)

	var reparsed interface{}
	assert.NoError(t, json.Unmarshal(remarshaled, &reparsed))
}

// TestUpgradeLegacyHooksToCurrentFormat verifies that old-format hooks
// (ox agent prime with matchers) are replaced by new lifecycle hooks (ox agent hook).
func TestUpgradeLegacyHooksToCurrentFormat(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0755))

	// write legacy-format hooks (ox agent prime with matchers)
	legacySettings := ClaudeSettings{
		Hooks: map[string][]ClaudeHookEntry{
			claudeSessionStart: {
				{
					Matcher: "startup",
					Hooks: []ClaudeHook{
						{Type: hookType, Command: "if command -v ox >/dev/null 2>&1; then AGENT_ENV=claude-code ox agent prime --idempotent 2>&1 || true; fi"},
					},
				},
				{
					Matcher: "resume",
					Hooks: []ClaudeHook{
						{Type: hookType, Command: "if command -v ox >/dev/null 2>&1; then AGENT_ENV=claude-code ox agent prime --idempotent 2>&1 || true; fi"},
					},
				},
				{
					Matcher: "clear",
					Hooks: []ClaudeHook{
						{Type: hookType, Command: "if command -v ox >/dev/null 2>&1; then AGENT_ENV=claude-code ox agent prime 2>&1 || true; fi"},
					},
				},
				{
					Matcher: "compact",
					Hooks: []ClaudeHook{
						{Type: hookType, Command: "if command -v ox >/dev/null 2>&1; then AGENT_ENV=claude-code ox agent prime 2>&1 || true; fi"},
					},
				},
			},
			claudePreCompact: {
				{
					Matcher: "",
					Hooks: []ClaudeHook{
						{Type: hookType, Command: "ox agent prime"},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(legacySettings, "", "  ")
	settingsPath := filepath.Join(claudeDir, "settings.json")
	require.NoError(t, os.WriteFile(settingsPath, data, 0644))

	// install upgrades to new lifecycle format
	err := InstallProjectClaudeHooks(tmpDir)
	require.NoError(t, err)

	// read updated settings
	settings, err := readProjectClaudeSettings(tmpDir)
	require.NoError(t, err)

	// verify SessionStart has new lifecycle hook
	sessionStart := settings.Hooks[claudeSessionStart]
	require.NotEmpty(t, sessionStart)

	foundLifecycleHook := false
	foundOldPrime := false
	for _, entry := range sessionStart {
		for _, hook := range entry.Hooks {
			if strings.Contains(hook.Command, "ox agent hook SessionStart") {
				foundLifecycleHook = true
			}
			if isOxPrimeCommand(hook.Command) {
				foundOldPrime = true
			}
		}
	}
	assert.True(t, foundLifecycleHook, "SessionStart should have lifecycle hook after upgrade")
	assert.False(t, foundOldPrime, "old ox agent prime should be removed after upgrade")

	// verify PreCompact also got upgraded
	preCompact := settings.Hooks[claudePreCompact]
	require.NotEmpty(t, preCompact)

	foundLifecycleHook = false
	foundOldPrime = false
	for _, entry := range preCompact {
		for _, hook := range entry.Hooks {
			if strings.Contains(hook.Command, "ox agent hook PreCompact") {
				foundLifecycleHook = true
			}
			if isOxPrimeCommand(hook.Command) {
				foundOldPrime = true
			}
		}
	}
	assert.True(t, foundLifecycleHook, "PreCompact should have lifecycle hook after upgrade")
	assert.False(t, foundOldPrime, "old ox agent prime should be removed from PreCompact")
}

// TestInstallOnEmptyProject verifies hook installation on a fresh project
// with no .claude directory.
func TestInstallOnEmptyProject(t *testing.T) {
	tmpDir := t.TempDir()
	// no .claude directory exists yet

	err := InstallProjectClaudeHooks(tmpDir)
	require.NoError(t, err)

	// .claude directory should be created
	settingsPath := filepath.Join(tmpDir, ".claude", "settings.json")
	require.FileExists(t, settingsPath)

	// should be valid JSON with hooks
	data, err := os.ReadFile(settingsPath)
	require.NoError(t, err)

	var parsed map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &parsed))

	var hooks map[string][]ClaudeHookEntry
	require.NoError(t, json.Unmarshal(parsed["hooks"], &hooks))
	assert.NotEmpty(t, hooks[claudeSessionStart])
	assert.NotEmpty(t, hooks[claudePreCompact])
}

// TestSettingsJSONHookPreservation verifies that installing hooks does not
// lose hook entries for other event types.
func TestSettingsJSONHookPreservation(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0755))

	// settings with multiple hook event types
	existingSettings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"PreToolUse": []interface{}{
				map[string]interface{}{
					"matcher": "Edit",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "echo guard"},
					},
				},
			},
			"PostToolUse": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "echo done"},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(existingSettings, "", "  ")
	settingsPath := filepath.Join(claudeDir, "settings.json")
	require.NoError(t, os.WriteFile(settingsPath, data, 0644))

	err := InstallProjectClaudeHooks(tmpDir)
	require.NoError(t, err)

	// read result
	settings, err := readProjectClaudeSettings(tmpDir)
	require.NoError(t, err)

	// all original hook types must still exist
	assert.NotEmpty(t, settings.Hooks["PreToolUse"], "PreToolUse should be preserved")
	assert.NotEmpty(t, settings.Hooks["PostToolUse"], "PostToolUse should be preserved")

	// new hooks should be added
	assert.NotEmpty(t, settings.Hooks[claudeSessionStart], "SessionStart should be added")
	assert.NotEmpty(t, settings.Hooks[claudePreCompact], "PreCompact should be added")

	// verify PreToolUse content is intact
	assert.Equal(t, "Edit", settings.Hooks["PreToolUse"][0].Matcher)
	assert.Equal(t, "echo guard", settings.Hooks["PreToolUse"][0].Hooks[0].Command)
}

// TestMergeHookEntriesEdgeCases tests merge behavior for tricky scenarios.
func TestMergeHookEntriesEdgeCases(t *testing.T) {
	t.Run("empty new list preserves all existing", func(t *testing.T) {
		existing := []ClaudeHookEntry{
			{Matcher: "startup", Hooks: []ClaudeHook{{Command: "echo hi", Type: "command"}}},
		}
		result := mergeHookEntries(existing, []ClaudeHookEntry{})
		assert.Len(t, result, 1)
		assert.Equal(t, "echo hi", result[0].Hooks[0].Command)
	})

	t.Run("both empty returns empty", func(t *testing.T) {
		result := mergeHookEntries([]ClaudeHookEntry{}, []ClaudeHookEntry{})
		assert.Empty(t, result)
	})

	t.Run("multiple non-ox hooks preserved in same entry", func(t *testing.T) {
		existing := []ClaudeHookEntry{
			{
				Matcher: "startup",
				Hooks: []ClaudeHook{
					{Command: "echo first", Type: "command"},
					{Command: "echo second", Type: "command"},
					{Command: "ox agent prime --old", Type: "command"},
				},
			},
		}
		new := []ClaudeHookEntry{
			{Matcher: "startup", Hooks: []ClaudeHook{{Command: "ox agent prime --new", Type: "command"}}},
		}

		result := mergeHookEntries(existing, new)
		assert.Len(t, result, 1)
		// should have: echo first, echo second, ox agent prime --new (old ox removed)
		assert.Len(t, result[0].Hooks, 3, "should preserve 2 custom hooks + 1 new ox hook")

		commands := make([]string, 0)
		for _, h := range result[0].Hooks {
			commands = append(commands, h.Command)
		}
		assert.Contains(t, commands, "echo first")
		assert.Contains(t, commands, "echo second")
		assert.Contains(t, commands, "ox agent prime --new")
		assert.NotContains(t, commands, "ox agent prime --old")
	})

	t.Run("nil existing treated as empty", func(t *testing.T) {
		new := []ClaudeHookEntry{
			{Matcher: "startup", Hooks: []ClaudeHook{{Command: "ox prime", Type: "command"}}},
		}
		result := mergeHookEntries(nil, new)
		assert.Len(t, result, 1)
	})
}

// TestConstantsHaveCommandVGuard verifies all ox prime command constants
// include the "command -v ox" guard pattern.
func TestConstantsHaveCommandVGuard(t *testing.T) {
	commands := map[string]string{
		"OxPrimeCommand":                  oxPrimeLegacy,
		"OxPrimeCommandClaudeCode":        oxPrimeCommand,
		"OxPrimeCommandClaudeCodeIdempot": oxPrimeCommandIdempotent,
		"OxPrimeCommandGemini":            oxPrimeCommandGemini,
	}

	for name, cmd := range commands {
		t.Run(name, func(t *testing.T) {
			assert.Contains(t, cmd, "command -v ox >/dev/null 2>&1",
				"%s must check for ox CLI availability", name)
			assert.Contains(t, cmd, "else echo",
				"%s must provide fallback message", name)
			assert.Contains(t, cmd, "|| true",
				"%s must not fail on ox errors", name)
		})
	}
}

// TestConstantsFallbackMessage verifies the fallback message content.
func TestConstantsFallbackMessage(t *testing.T) {
	commands := []string{
		oxPrimeLegacy,
		oxPrimeCommand,
		oxPrimeCommandIdempotent,
		oxPrimeCommandGemini,
	}

	for _, cmd := range commands {
		assert.Contains(t, cmd, "github.com/sageox/ox",
			"fallback should point to install URL")
		assert.Contains(t, cmd, "SageOx",
			"fallback should mention SageOx")
	}
}

// TestInstallPreservesBdHooks verifies that bd prime hooks in settings.json
// survive ox hook installation.
func TestInstallPreservesBdHooks(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0755))

	// pre-populate with bd prime hooks (like the real settings.json)
	existing := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": "if command -v bd >/dev/null 2>&1; then bd prime 2>&1 || true; fi",
						},
					},
				},
			},
			"PreCompact": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": "if command -v bd >/dev/null 2>&1; then bd prime 2>&1 || true; fi",
						},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0644))

	// install ox hooks
	err := InstallProjectClaudeHooks(tmpDir)
	require.NoError(t, err)

	settings, err := readProjectClaudeSettings(tmpDir)
	require.NoError(t, err)

	// bd hooks should still be present
	foundBd := false
	foundOx := false
	for _, entry := range settings.Hooks[claudeSessionStart] {
		for _, hook := range entry.Hooks {
			if strings.Contains(hook.Command, "bd prime") {
				foundBd = true
			}
			if strings.Contains(hook.Command, "ox agent hook") {
				foundOx = true
			}
		}
	}
	assert.True(t, foundBd, "bd prime hook should survive ox install")
	assert.True(t, foundOx, "ox hook should be added")
}

// TestInstallPreservesPermissions verifies that a "permissions" key in
// settings.json survives ox hook installation.
func TestInstallPreservesPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0755))

	// pre-populate with permissions
	existing := map[string]interface{}{
		"permissions": map[string]interface{}{
			"allow": []interface{}{"Bash(npm:*)", "Read"},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	settingsPath := filepath.Join(claudeDir, "settings.json")
	require.NoError(t, os.WriteFile(settingsPath, data, 0644))

	// install ox hooks
	err := InstallProjectClaudeHooks(tmpDir)
	require.NoError(t, err)

	// read raw to check permissions survived
	rawData, err := os.ReadFile(settingsPath)
	require.NoError(t, err)

	var parsed map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rawData, &parsed))

	// permissions key must exist
	require.Contains(t, parsed, "permissions", "permissions key must survive install")

	var perms map[string]interface{}
	require.NoError(t, json.Unmarshal(parsed["permissions"], &perms))
	allowList, ok := perms["allow"].([]interface{})
	require.True(t, ok)
	assert.Len(t, allowList, 2)

	// hooks should also exist
	require.Contains(t, parsed, "hooks", "hooks key must be added")
}

// TestMigrationCleansUpLocalSettings verifies that ox hooks are removed from
// settings.local.json after installing to settings.json.
func TestMigrationCleansUpLocalSettings(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0755))

	// simulate old-style ox hooks in local settings
	localSettings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": "if command -v ox >/dev/null 2>&1; then ox agent prime 2>&1 || true; fi",
						},
					},
				},
			},
		},
		"permissions": map[string]interface{}{
			"allow": []interface{}{"Bash(grep:*)"},
		},
	}
	data, _ := json.MarshalIndent(localSettings, "", "  ")
	localPath := filepath.Join(claudeDir, "settings.local.json")
	require.NoError(t, os.WriteFile(localPath, data, 0644))

	// install to shared settings
	err := InstallProjectClaudeHooks(tmpDir)
	require.NoError(t, err)

	// local file should still exist (has permissions key)
	require.FileExists(t, localPath)

	// but ox hooks should be removed from local
	localData, err := os.ReadFile(localPath)
	require.NoError(t, err)
	assert.NotContains(t, string(localData), "ox agent prime", "ox hooks should be removed from local")
	assert.Contains(t, string(localData), "permissions", "non-ox content should be preserved in local")
}

// TestMigrationDeletesEmptyLocalSettings verifies that settings.local.json is
// deleted entirely when it only contained ox hooks.
func TestMigrationDeletesEmptyLocalSettings(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0755))

	// local file with only ox hooks
	localSettings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": "if command -v ox >/dev/null 2>&1; then ox agent hook SessionStart 2>&1 || true; fi",
						},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(localSettings, "", "  ")
	localPath := filepath.Join(claudeDir, "settings.local.json")
	require.NoError(t, os.WriteFile(localPath, data, 0644))

	// install to shared settings
	err := InstallProjectClaudeHooks(tmpDir)
	require.NoError(t, err)

	// local file should be deleted (was empty after removing ox hooks)
	_, statErr := os.Stat(localPath)
	assert.True(t, os.IsNotExist(statErr), "empty local settings should be deleted")
}

// TestSharedHookValueValidation verifies doctor detects stale hook commands.
func TestSharedHookValueValidation(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0755))

	// write hooks with a stale command
	staleSettings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": "if command -v ox >/dev/null 2>&1; then ox agent prime 2>&1 || true; fi",
						},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(staleSettings, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0644))

	settings, _, err := readSharedClaudeSettings(tmpDir)
	require.NoError(t, err)

	// verify the hook is detected as an ox hook but not the current command
	expected := oxHookCommandForEvent("SessionStart")
	for _, entry := range settings.Hooks["SessionStart"] {
		for _, hook := range entry.Hooks {
			if hook.Type == hookType && isAnyOxCommand(hook.Command) {
				assert.NotEqual(t, expected, hook.Command, "stale command should differ from current")
			}
		}
	}
}

// TestInstallWritesToSharedNotLocal verifies hooks are written to settings.json,
// not settings.local.json.
func TestInstallWritesToSharedNotLocal(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0755))

	err := InstallProjectClaudeHooks(tmpDir)
	require.NoError(t, err)

	// settings.json should exist with hooks
	sharedPath := filepath.Join(tmpDir, ".claude", "settings.json")
	require.FileExists(t, sharedPath)

	data, err := os.ReadFile(sharedPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "ox agent hook", "hooks should be in settings.json")

	// settings.local.json should NOT be created
	localPath := filepath.Join(tmpDir, ".claude", "settings.local.json")
	_, statErr := os.Stat(localPath)
	assert.True(t, os.IsNotExist(statErr), "settings.local.json should not be created")
}

// TestMigrationRealisticOldFormat reproduces the real-world settings.local.json
// layout: 4 SessionStart matchers (startup/resume/clear/compact) + PreCompact
// + permissions. All ox hooks must be removed; permissions must survive.
func TestMigrationRealisticOldFormat(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0755))

	// mirrors the actual settings.local.json from a real repo
	localContent := `{
  "hooks": {
    "PreCompact": [
      {
        "matcher": "",
        "hooks": [
          { "type": "command", "command": "if command -v ox >/dev/null 2>&1; then AGENT_ENV=claude-code ox agent prime 2>&1 || true; fi" }
        ]
      }
    ],
    "SessionStart": [
      {
        "matcher": "startup",
        "hooks": [
          { "type": "command", "command": "if command -v ox >/dev/null 2>&1; then AGENT_ENV=claude-code ox agent prime --idempotent 2>&1 || true; fi" }
        ]
      },
      {
        "matcher": "resume",
        "hooks": [
          { "type": "command", "command": "if command -v ox >/dev/null 2>&1; then AGENT_ENV=claude-code ox agent prime --idempotent 2>&1 || true; fi" }
        ]
      },
      {
        "matcher": "clear",
        "hooks": [
          { "type": "command", "command": "if command -v ox >/dev/null 2>&1; then AGENT_ENV=claude-code ox agent prime 2>&1 || true; fi" }
        ]
      },
      {
        "matcher": "compact",
        "hooks": [
          { "type": "command", "command": "if command -v ox >/dev/null 2>&1; then AGENT_ENV=claude-code ox agent prime 2>&1 || true; fi" }
        ]
      }
    ]
  },
  "permissions": {
    "allow": [
      "Bash(grep:*)"
    ]
  }
}`
	localPath := filepath.Join(claudeDir, "settings.local.json")
	require.NoError(t, os.WriteFile(localPath, []byte(localContent), 0644))

	// install to shared (triggers cleanup)
	err := InstallProjectClaudeHooks(tmpDir)
	require.NoError(t, err)

	// local file must still exist (has permissions)
	require.FileExists(t, localPath)

	data, err := os.ReadFile(localPath)
	require.NoError(t, err)

	// no ox commands should remain
	assert.NotContains(t, string(data), "ox agent prime")
	assert.NotContains(t, string(data), "ox agent hook")
	// no hook events should remain (all were ox-only)
	assert.NotContains(t, string(data), "SessionStart")
	assert.NotContains(t, string(data), "PreCompact")
	// permissions must survive
	assert.Contains(t, string(data), "Bash(grep:*)")
}

// TestCleanupPreservesNonOxHookInMixedEntry verifies that when a single hook
// entry contains both an ox hook and a custom hook, cleanup keeps the custom
// hook and only strips the ox one.
func TestCleanupPreservesNonOxHookInMixedEntry(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0755))

	// entry with ox hook and custom hook sharing the same matcher
	localSettings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": "if command -v bd >/dev/null 2>&1; then bd prime 2>&1 || true; fi",
						},
						map[string]interface{}{
							"type":    "command",
							"command": "if command -v ox >/dev/null 2>&1; then ox agent prime 2>&1 || true; fi",
						},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(localSettings, "", "  ")
	localPath := filepath.Join(claudeDir, "settings.local.json")
	require.NoError(t, os.WriteFile(localPath, data, 0644))

	err := cleanupLocalSettingsOxHooks(tmpDir)
	require.NoError(t, err)

	// file should still exist with the bd hook
	require.FileExists(t, localPath)

	result, err := os.ReadFile(localPath)
	require.NoError(t, err)

	assert.Contains(t, string(result), "bd prime", "non-ox hook must survive cleanup")
	assert.NotContains(t, string(result), "ox agent prime", "ox hook must be removed")
}

// TestReadSettingsFileRawHandlesNullJSON verifies that a settings file
// containing the JSON literal "null" doesn't cause nil pointer panics.
func TestReadSettingsFileRawHandlesNullJSON(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0755))

	settingsPath := filepath.Join(claudeDir, "settings.json")
	require.NoError(t, os.WriteFile(settingsPath, []byte("null"), 0644))

	settings, rawMap, err := readSettingsFileRaw(settingsPath)
	require.NoError(t, err)
	assert.NotNil(t, settings)
	assert.NotNil(t, settings.Hooks)

	// writing back should not panic
	err = writeSettingsFileRaw(settingsPath, settings, rawMap, sharedSettingsPerm)
	assert.NoError(t, err)
}
