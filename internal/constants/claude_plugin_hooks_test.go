package constants

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClaudePluginHooksMatchInit keeps the hook commands in
// claude-plugin/.claude-plugin/plugin.json identical to what `ox init` writes
// for Claude Code, so the plugin and init never drift (and `ox doctor` treats
// both as the same ox hook).
func TestClaudePluginHooksMatchInit(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "claude-plugin", ".claude-plugin", "plugin.json"))
	require.NoError(t, err)

	var manifest struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(data, &manifest))

	// SessionStart skips a re-prime; PreCompact must reload after compaction.
	want := map[string]string{
		"SessionStart": OxPrimeCommandClaudeCodeIdempotent,
		"PreCompact":   OxPrimeCommandClaudeCode,
	}
	for event, cmd := range want {
		entries := manifest.Hooks[event]
		require.Len(t, entries, 1, event)
		require.Len(t, entries[0].Hooks, 1, event)
		assert.Equal(t, "command", entries[0].Hooks[0].Type, event)
		assert.Equal(t, cmd, entries[0].Hooks[0].Command, event)
	}
}
