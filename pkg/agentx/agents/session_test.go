package agents

import (
	"testing"

	"github.com/sageox/ox/pkg/agentx"
	"github.com/stretchr/testify/assert"
)

// TestAgentSessionSupport verifies SupportsSession() and SessionID() for all agents.
func TestAgentSessionSupport(t *testing.T) {
	emptyEnv := agentx.NewMockEnvironment(nil)

	t.Run("env-var agents support sessions and read correct var", func(t *testing.T) {
		tests := []struct {
			name   string
			agent  agentx.Agent
			envVar string
		}{
			{"ClaudeCode", NewClaudeCodeAgent(), "CLAUDE_CODE_SESSION_ID"},
			{"Codex", NewCodexAgent(), "CODEX_THREAD_ID"},
			{"Amp", NewAmpAgent(), "AMP_THREAD_URL"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				assert.True(t, tt.agent.SupportsSession())
				env := agentx.NewMockEnvironment(map[string]string{tt.envVar: "test_value"})
				assert.Equal(t, "test_value", tt.agent.SessionID(env))
				assert.Equal(t, "", tt.agent.SessionID(emptyEnv))
			})
		}
	})

	t.Run("hook-only agents support sessions but return empty SessionID", func(t *testing.T) {
		agents := []struct {
			name  string
			agent agentx.Agent
		}{
			{"Cursor", NewCursorAgent()},
			{"Windsurf", NewWindsurfAgent()},
			{"Copilot", NewCopilotAgent()},
			{"Cline", NewClineAgent()},
			{"Kiro", NewKiroAgent()},
			{"Droid", NewDroidAgent()},
			{"OpenCode", NewOpenCodeAgent()},
		}
		for _, tt := range agents {
			t.Run(tt.name, func(t *testing.T) {
				assert.True(t, tt.agent.SupportsSession(),
					"%s should support sessions (via hook stdin)", tt.name)
				assert.Equal(t, "", tt.agent.SessionID(emptyEnv),
					"%s should return empty SessionID (no dedicated env var)", tt.name)
			})
		}
	})

	t.Run("non-session agents do not support sessions", func(t *testing.T) {
		agents := []struct {
			name  string
			agent agentx.Agent
		}{
			{"Aider", NewAiderAgent()},
			{"Goose", NewGooseAgent()},
			{"Cody", NewCodyAgent()},
			{"Continue", NewContinueAgent()},
			{"CodePuppy", NewCodePuppyAgent()},
		}
		for _, tt := range agents {
			t.Run(tt.name, func(t *testing.T) {
				assert.False(t, tt.agent.SupportsSession(),
					"%s should not support sessions", tt.name)
				assert.Equal(t, "", tt.agent.SessionID(emptyEnv))
			})
		}
	})
}
