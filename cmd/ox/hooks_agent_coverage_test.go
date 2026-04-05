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
		{"Pi", "Pi", false},
		{"pi", "Pi", false},
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

	expectedNames := []string{"Claude", "OpenCode", "Gemini", "Codex", "Amp", "Pi"}
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

func TestCodexAgent_SupportsHooksTrue(t *testing.T) {
	t.Parallel()

	codex := &CodexAgent{}
	assert.True(t, codex.SupportsHooks(), "Codex supports hooks via external adapter")
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
		{"Codex", &CodexAgent{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.supports, tt.agent.SupportsHooks())
		})
	}
}
