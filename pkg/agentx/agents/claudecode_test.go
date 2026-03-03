package agents

import (
	"testing"

	"github.com/sageox/ox/pkg/agentx"
	"github.com/stretchr/testify/assert"
)

func TestClaudeCodeEventPhases(t *testing.T) {
	agent := NewClaudeCodeAgent()
	phases := agent.EventPhases()

	// all 7 Claude Code lifecycle events should be mapped
	assert.Len(t, phases, 7)

	assert.Equal(t, agentx.PhaseStart, phases[agentx.HookEventSessionStart])
	assert.Equal(t, agentx.PhaseEnd, phases[agentx.HookEventSessionEnd])
	assert.Equal(t, agentx.PhaseBeforeTool, phases[agentx.HookEventPreToolUse])
	assert.Equal(t, agentx.PhaseAfterTool, phases[agentx.HookEventPostToolUse])
	assert.Equal(t, agentx.PhasePrompt, phases[agentx.HookEventUserPromptSubmit])
	assert.Equal(t, agentx.PhaseStop, phases[agentx.HookEventStop])
	assert.Equal(t, agentx.PhaseCompact, phases[agentx.HookEventPreCompact])
}

func TestClaudeCodeAgentENVAliases(t *testing.T) {
	agent := NewClaudeCodeAgent()
	aliases := agent.AgentENVAliases()

	assert.Contains(t, aliases, "claude-code")
	assert.Contains(t, aliases, "claudecode")
	assert.Contains(t, aliases, "claude")
	assert.Equal(t, "claude-code", aliases[0], "first alias should be canonical")
}

func TestClaudeCodeImplementsLifecycleEventMapper(t *testing.T) {
	var _ agentx.LifecycleEventMapper = (*ClaudeCodeAgent)(nil)
}

func TestClaudeCodeSessionID(t *testing.T) {
	agent := NewClaudeCodeAgent()
	assert.True(t, agent.SupportsSession())

	t.Run("returns session ID from env var", func(t *testing.T) {
		env := agentx.NewMockEnvironment(map[string]string{
			"CLAUDE_CODE_SESSION_ID": "sess_abc123",
		})
		assert.Equal(t, "sess_abc123", agent.SessionID(env))
	})

	t.Run("returns empty when env var not set", func(t *testing.T) {
		env := agentx.NewMockEnvironment(nil)
		assert.Equal(t, "", agent.SessionID(env))
	})
}
