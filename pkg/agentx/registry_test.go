package agentx

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// mockLifecycleAgent implements Agent and LifecycleEventMapper for testing.
type mockLifecycleAgent struct {
	mockAgent
	phases  EventPhaseMap
	aliases []string
}

func (a *mockLifecycleAgent) EventPhases() EventPhaseMap       { return a.phases }
func (a *mockLifecycleAgent) AgentENVAliases() []string        { return a.aliases }

func TestBuildEventPhaseMap(t *testing.T) {
	// create a fresh registry with a lifecycle-aware agent
	reg := NewRegistry().(*registry)
	agent := &mockLifecycleAgent{
		mockAgent: mockAgent{
			agentType: AgentTypeClaudeCode,
			name:      "Claude Code",
			role:      RoleAgent,
		},
		phases: EventPhaseMap{
			HookEventSessionStart: PhaseStart,
			HookEventPreCompact:   PhaseCompact,
			HookEventPostToolUse:  PhaseAfterTool,
		},
		aliases: []string{"claude-code", "claudecode", "claude"},
	}
	reg.Register(agent)

	result := reg.buildEventPhaseMap()

	// all three aliases should resolve
	for _, alias := range []string{"claude-code", "claudecode", "claude"} {
		phases, ok := result[alias]
		assert.True(t, ok, "alias %q should be in map", alias)
		assert.Equal(t, PhaseStart, phases[HookEventSessionStart])
		assert.Equal(t, PhaseCompact, phases[HookEventPreCompact])
		assert.Equal(t, PhaseAfterTool, phases[HookEventPostToolUse])
	}
}

func TestResolveAgentENV(t *testing.T) {
	reg := NewRegistry().(*registry)
	agent := &mockLifecycleAgent{
		mockAgent: mockAgent{
			agentType: AgentTypeClaudeCode,
			name:      "Claude Code",
			role:      RoleAgent,
		},
		aliases: []string{"claude-code", "claudecode", "claude"},
	}
	reg.Register(agent)

	assert.Equal(t, AgentTypeClaudeCode, reg.resolveAgentENV("claude-code"))
	assert.Equal(t, AgentTypeClaudeCode, reg.resolveAgentENV("claudecode"))
	assert.Equal(t, AgentTypeClaudeCode, reg.resolveAgentENV("claude"))
	assert.Equal(t, AgentTypeUnknown, reg.resolveAgentENV("nonexistent"))
}

func TestHookSupportMatrix(t *testing.T) {
	reg := NewRegistry().(*registry)

	// agent with hooks
	reg.Register(&mockLifecycleAgent{
		mockAgent: mockAgent{
			agentType: AgentTypeClaudeCode,
			name:      "Claude Code",
			role:      RoleAgent,
		},
		phases: EventPhaseMap{
			HookEventSessionStart: PhaseStart,
			HookEventPostToolUse:  PhaseAfterTool,
			HookEventPreCompact:   PhaseCompact,
		},
		aliases: []string{"claude-code"},
	})

	// agent without hooks
	reg.Register(&mockAgent{
		agentType: AgentTypeCursor,
		name:      "Cursor",
		role:      RoleAgent,
	})

	matrix := reg.hookSupportMatrix()

	// only the lifecycle agent should appear
	assert.Len(t, matrix, 1)
	entry := matrix[0]
	assert.Equal(t, AgentTypeClaudeCode, entry.AgentType)
	assert.Equal(t, "Claude Code", entry.AgentName)

	// check phase→event inversion
	assert.Contains(t, entry.Phases[PhaseStart], HookEventSessionStart)
	assert.Contains(t, entry.Phases[PhaseAfterTool], HookEventPostToolUse)
	assert.Contains(t, entry.Phases[PhaseCompact], HookEventPreCompact)

	// phases the agent doesn't support should be empty
	assert.Empty(t, entry.Phases[PhaseEnd])
	assert.Empty(t, entry.Phases[PhasePrompt])
}

func TestHookSupportMatrix_MultipleEventsPerPhase(t *testing.T) {
	reg := NewRegistry().(*registry)

	// windsurf-style: multiple events map to same phase
	reg.Register(&mockLifecycleAgent{
		mockAgent: mockAgent{
			agentType: AgentTypeWindsurf,
			name:      "Windsurf",
			role:      RoleAgent,
		},
		phases: EventPhaseMap{
			WindsurfEventPreReadCode:   PhaseBeforeTool,
			WindsurfEventPreRunCommand: PhaseBeforeTool,
			WindsurfEventPostWriteCode: PhaseAfterTool,
		},
		aliases: []string{"windsurf"},
	})

	matrix := reg.hookSupportMatrix()
	assert.Len(t, matrix, 1)

	entry := matrix[0]
	// before_tool should have 2 events
	assert.Len(t, entry.Phases[PhaseBeforeTool], 2)
	assert.Contains(t, entry.Phases[PhaseBeforeTool], WindsurfEventPreReadCode)
	assert.Contains(t, entry.Phases[PhaseBeforeTool], WindsurfEventPreRunCommand)
}

func TestBuildEventPhaseMap_NoLifecycleAgents(t *testing.T) {
	reg := NewRegistry().(*registry)
	// register a basic agent without LifecycleEventMapper
	reg.Register(&mockAgent{
		agentType: AgentTypeCursor,
		name:      "Cursor",
		role:      RoleAgent,
	})

	result := reg.buildEventPhaseMap()
	assert.Empty(t, result)
}
