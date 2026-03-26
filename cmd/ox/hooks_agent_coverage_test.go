package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetAgent_CaseInsensitive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		wantName string
		wantNil  bool
	}{
		{"Claude", "Claude", false},
		{"claude", "Claude", false},
		{"CLAUDE", "Claude", false},
		{"OpenCode", "OpenCode", false},
		{"opencode", "OpenCode", false},
		{"Gemini", "Gemini", false},
		{"gemini", "Gemini", false},
		{"Codex", "Codex", false},
		{"codex", "Codex", false},
		{"CodePuppy", "CodePuppy", false},
		{"codepuppy", "CodePuppy", false},
		{"nonexistent", "", true},
		{"", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			agent := GetAgent(tt.input)
			if tt.wantNil {
				assert.Nil(t, agent)
			} else {
				require.NotNil(t, agent)
				assert.Equal(t, tt.wantName, agent.Name())
			}
		})
	}
}

func TestAgentRegistry_AllRegistered(t *testing.T) {
	t.Parallel()

	expectedNames := []string{"Claude", "OpenCode", "Gemini", "Codex", "CodePuppy"}
	assert.Len(t, AgentRegistry, len(expectedNames))

	for _, name := range expectedNames {
		found := false
		for _, agent := range AgentRegistry {
			if agent.Name() == name {
				found = true
				break
			}
		}
		assert.True(t, found, "agent %q should be in registry", name)
	}
}

func TestCodexAgent_SupportsHooksFalse(t *testing.T) {
	t.Parallel()

	codex := &CodexAgent{}
	assert.False(t, codex.SupportsHooks(), "Codex uses AGENTS.md, not hooks")
}

func TestCodexAgent_InstallUninstallNoOp(t *testing.T) {
	t.Parallel()

	codex := &CodexAgent{}
	assert.NoError(t, codex.Install(false))
	assert.NoError(t, codex.Install(true))
	assert.NoError(t, codex.Uninstall(false))
	assert.NoError(t, codex.Uninstall(true))
}

func TestCodexAgent_ListReturnsAgentsMD(t *testing.T) {
	t.Parallel()

	codex := &CodexAgent{}
	status := codex.List()
	_, hasKey := status["AGENTS.md"]
	assert.True(t, hasKey, "Codex list should report AGENTS.md status")
}

func TestAgentNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		agent Agent
		name  string
	}{
		{&ClaudeAgent{}, "Claude"},
		{&OpenCodeAgent{}, "OpenCode"},
		{&GeminiAgent{}, "Gemini"},
		{&CodexAgent{}, "Codex"},
		{&CodePuppyAgent{}, "CodePuppy"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.name, tt.agent.Name())
		})
	}
}

func TestAgentSupportsHooks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		agent    Agent
		supports bool
	}{
		{"Claude", &ClaudeAgent{}, true},
		{"OpenCode", &OpenCodeAgent{}, true},
		{"Gemini", &GeminiAgent{}, true},
		{"Codex", &CodexAgent{}, false},
		{"CodePuppy", &CodePuppyAgent{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.supports, tt.agent.SupportsHooks())
		})
	}
}
