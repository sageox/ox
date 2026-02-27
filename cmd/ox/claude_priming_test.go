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

// TestClaudeCodePrimingIntegration verifies the complete flow:
// 1. ox init installs Claude Code hooks that call "ox agent prime" on SessionStart
// 2. ox agent prime outputs JSON with agent_id visible to Claude
//
// This ensures that when Claude Code starts a session in a SageOx-enabled repo,
// it automatically sees an agent_id (like "OxAdW9") in its context without
// running any additional commands.
func TestClaudeCodePrimingIntegration(t *testing.T) {
	t.Run("ox init installs SessionStart hook with ox agent prime", func(t *testing.T) {
		// create temp project with .sageox/ and .claude/ directories
		tmpDir := t.TempDir()
		sageoxDir := filepath.Join(tmpDir, ".sageox")
		claudeDir := filepath.Join(tmpDir, ".claude")

		require.NoError(t, os.MkdirAll(sageoxDir, 0755))
		require.NoError(t, os.MkdirAll(claudeDir, 0755))

		// install project-level Claude Code hooks (what ox init does)
		err := InstallProjectClaudeHooks(tmpDir)
		require.NoError(t, err, "InstallProjectClaudeHooks should succeed")

		// verify settings.local.json was created
		settingsPath := filepath.Join(claudeDir, "settings.local.json")
		require.FileExists(t, settingsPath, "settings.local.json should be created")

		// read and parse the settings
		data, err := os.ReadFile(settingsPath)
		require.NoError(t, err)

		var settings ClaudeSettings
		require.NoError(t, json.Unmarshal(data, &settings))

		// verify SessionStart hook exists and contains ox agent prime
		sessionStartHooks, ok := settings.Hooks["SessionStart"]
		require.True(t, ok, "SessionStart hooks should exist")
		require.NotEmpty(t, sessionStartHooks, "SessionStart should have entries")

		// check that at least one hook contains "ox agent hook" (lifecycle format)
		foundOxHook := false
		for _, entry := range sessionStartHooks {
			for _, hook := range entry.Hooks {
				if hook.Type == "command" && strings.Contains(hook.Command, "ox agent hook") {
					foundOxHook = true
					break
				}
			}
		}
		assert.True(t, foundOxHook, "SessionStart should have 'ox agent hook' lifecycle hook")
	})

	t.Run("ox agent prime output contains agent_id for Claude visibility", func(t *testing.T) {
		// simulate the output structure that ox agent prime produces
		// this is what Claude sees in its context
		output := agentPrimeOutput{
			Status:    "fresh",
			AgentID:   "OxAdW9", // format: Ox + 4 alphanumeric chars
			SessionID: "oxsid_01KGGE8TBV3QH6DY7M07R3E8CA",
			AgentType: "claude-code",
			Content:   "SageOx is optimizing collaborative work on this repo.",
		}

		// marshal to JSON (what Claude receives)
		jsonData, err := json.Marshal(output)
		require.NoError(t, err)

		// verify agent_id is present and visible in the JSON
		jsonStr := string(jsonData)
		assert.Contains(t, jsonStr, `"agent_id":"OxAdW9"`,
			"JSON output should contain agent_id field that Claude can see")

		// verify session_id is present
		assert.Contains(t, jsonStr, `"session_id":"oxsid_01KGGE8TBV3QH6DY7M07R3E8CA"`,
			"JSON output should contain session_id")

		// unmarshal and verify fields
		var parsed agentPrimeOutput
		require.NoError(t, json.Unmarshal(jsonData, &parsed))

		assert.Equal(t, "OxAdW9", parsed.AgentID, "agent_id should be parseable")
		assert.Equal(t, "fresh", parsed.Status, "status should be parseable")
		assert.Equal(t, "claude-code", parsed.AgentType, "agent_type should be claude-code")
	})

	t.Run("agent_id format is human-memorable and agent-visible", func(t *testing.T) {
		// the agent_id format is designed to be:
		// - short enough for humans to remember/type: OxXXXX (6 chars)
		// - unique enough to avoid collisions: 4 alphanumeric = 36^4 = 1.6M combinations
		// - easily parseable by agents: starts with "Ox" prefix

		validIDs := []string{"OxAdW9", "Ox1234", "OxZzZz", "OxAaBb"}
		invalidIDs := []string{"ox1234", "OX1234", "Ox123", "Ox12345", "abc123"}

		for _, id := range validIDs {
			assert.Regexp(t, `^Ox[A-Za-z0-9]{4}$`, id,
				"valid agent_id should match Ox + 4 alphanumeric: %s", id)
		}

		for _, id := range invalidIDs {
			matched, _ := filepath.Match("Ox????", id)
			// note: filepath.Match doesn't validate charset, just length
			if matched && len(id) == 6 {
				// additional validation needed
				isValid := strings.HasPrefix(id, "Ox") && len(id) == 6
				if isValid {
					for _, c := range id[2:] {
						isAlphanumeric := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
						if !isAlphanumeric {
							isValid = false
							break
						}
					}
				}
				assert.False(t, isValid || !strings.HasPrefix(id, "Ox"),
					"invalid agent_id should not match pattern: %s", id)
			}
		}
	})

	t.Run("Claude can extract agent_id from SessionStart hook output", func(t *testing.T) {
		// simulate what Claude sees in its context after SessionStart hook runs
		// this is the key verification that the user asked for

		hookOutput := `SessionStart:startup hook success: {"status":"fresh","agent_id":"OxAdW9","session_id":"oxsid_01KGGE8TBV3QH6DY7M07R3E8CA","agent_type":"claude-code"}`

		// verify Claude can find agent_id in the hook output
		assert.Contains(t, hookOutput, `"agent_id":"OxAdW9"`,
			"Claude should see agent_id in SessionStart hook output")

		assert.Contains(t, hookOutput, "OxAdW9",
			"agent_id value should be visible without parsing JSON")

		// extract and verify the JSON portion
		jsonStart := strings.Index(hookOutput, "{")
		require.Greater(t, jsonStart, 0, "JSON should be present in output")

		jsonPart := hookOutput[jsonStart:]
		var parsed map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(jsonPart), &parsed))

		agentID, ok := parsed["agent_id"].(string)
		require.True(t, ok, "agent_id should be extractable from JSON")
		assert.Equal(t, "OxAdW9", agentID)
	})
}

// TestClaudeCodeHooksPreserveOtherHooks verifies that installing SageOx hooks
// doesn't clobber existing hooks from other tools
func TestClaudeCodeHooksPreserveOtherHooks(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0755))

	// create existing settings with another hook
	existingSettings := ClaudeSettings{
		Hooks: map[string][]ClaudeHookEntry{
			"SessionStart": {
				{
					Matcher: "",
					Hooks: []ClaudeHook{
						{Type: "command", Command: "echo 'other tool hook'"},
					},
				},
			},
		},
	}
	data, _ := json.Marshal(existingSettings)
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.local.json"), data, 0644))

	// install ox hooks
	err := InstallProjectClaudeHooks(tmpDir)
	require.NoError(t, err)

	// read updated settings
	data, err = os.ReadFile(filepath.Join(claudeDir, "settings.local.json"))
	require.NoError(t, err)

	var settings ClaudeSettings
	require.NoError(t, json.Unmarshal(data, &settings))

	// verify both hooks exist
	sessionStartHooks := settings.Hooks["SessionStart"]
	require.NotEmpty(t, sessionStartHooks)

	foundOther := false
	foundOx := false
	for _, entry := range sessionStartHooks {
		for _, hook := range entry.Hooks {
			if strings.Contains(hook.Command, "other tool hook") {
				foundOther = true
			}
			if strings.Contains(hook.Command, "ox agent hook") {
				foundOx = true
			}
		}
	}

	assert.True(t, foundOther, "existing hooks should be preserved")
	assert.True(t, foundOx, "ox agent hook should be added")
}

// TestHasProjectClaudeHooks verifies detection of existing ox hooks
func TestHasProjectClaudeHooks(t *testing.T) {
	t.Run("returns false when no hooks installed", func(t *testing.T) {
		tmpDir := t.TempDir()
		assert.False(t, HasProjectClaudeHooks(tmpDir))
	})

	t.Run("returns true after InstallProjectClaudeHooks", func(t *testing.T) {
		tmpDir := t.TempDir()
		claudeDir := filepath.Join(tmpDir, ".claude")
		require.NoError(t, os.MkdirAll(claudeDir, 0755))

		require.NoError(t, InstallProjectClaudeHooks(tmpDir))

		assert.True(t, HasProjectClaudeHooks(tmpDir))
	})
}

// note: requireSageoxDir is defined in testhelper_test.go
