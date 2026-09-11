package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExtractOxCommands_AgentHookFallback proves the off-PATH diagnostic added
// to every hook command's else-branch (internal/constants/agent.go's
// oxNotOnPathFallback) does not introduce a false positive in the doctor's
// hook-command validator. Before this test existed, the constraint was
// checked by hand; this pins it so a future edit to the fallback text can't
// silently reintroduce a false "invalid command" warning.
func TestExtractOxCommands_AgentHookFallback(t *testing.T) {
	commands := []string{
		constants.OxPrimeCommand,
		constants.OxPrimeCommandClaudeCode,
		constants.OxPrimeCommandClaudeCodeIdempotent,
		constants.OxPrimeCommandGemini,
		constants.OxPrimeCommandAmp,
		constants.OxPrimeCommandPi,
		fmt.Sprintf(constants.OxHookCommandClaudeCodeTemplate, "SessionStart"),
		fmt.Sprintf(constants.OxHookCommandCodexTemplate, "SessionStart"),
		fmt.Sprintf(constants.OxHookCommandGeminiTemplate, "SessionStart"),
	}

	for _, cmd := range commands {
		extracted := extractOxCommands(cmd)
		require.NotEmpty(t, extracted, "expected at least one ox command extracted from %q", cmd)
		for _, e := range extracted {
			assert.True(t, isValidOxCommand(e),
				"hook command fallback text produced a false-positive invalid command %q from %q", e, cmd)
		}
	}
}

// TestCheckHookCommands_AgentHookFallback drives the real checkHookCommands
// doctor check end-to-end against a settings.json using the updated
// Claude Code hook constant, proving the longer off-PATH else-branch still
// reads as a clean, valid hook to `ox doctor`.
func TestCheckHookCommands_AgentHookFallback(t *testing.T) {
	tempHome := t.TempDir()
	claudeDir := filepath.Join(tempHome, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0755))

	settings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"matcher": "",
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": constants.OxPrimeCommandClaudeCode,
						},
					},
				},
			},
		},
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0644))

	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempHome)
	defer os.Setenv("HOME", originalHome)

	result := checkHookCommands()

	assert.True(t, result.passed, "expected passed result, got passed=%v warning=%v detail=%s", result.passed, result.warning, result.detail)
	assert.False(t, result.warning, "expected no warning for the off-PATH fallback text, got detail=%s", result.detail)
}
