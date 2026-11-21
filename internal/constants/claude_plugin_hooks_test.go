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

// TestClaudePluginMCPURLMatchesOAuthResource pins the plugin's MCP URL to the
// canonical one. Claude matches it exactly against the server's OAuth
// `resource`, so an alias path breaks sign-in.
func TestClaudePluginMCPURLMatchesOAuthResource(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "claude-plugin", ".mcp.json"))
	require.NoError(t, err)

	var cfg struct {
		MCPServers map[string]struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal(data, &cfg))

	server, ok := cfg.MCPServers["sageox"]
	require.True(t, ok, "sageox MCP server missing")
	assert.Equal(t, "http", server.Type)
	assert.Equal(t, "https://sageox.ai/mcp", server.URL)
}
