package orchestrators

import (
	"context"
	"testing"

	"github.com/sageox/ox/pkg/agentx"
	"github.com/stretchr/testify/assert"
)

func TestOpenClawAgent_Detect(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"ORCHESTRATOR_ENV=openclaw", map[string]string{"ORCHESTRATOR_ENV": "openclaw"}, true},
		{"OPENCLAW_STATE_DIR set", map[string]string{"OPENCLAW_STATE_DIR": "/home/user/.openclaw"}, true},
		{"OPENCLAW_HOME set", map[string]string{"OPENCLAW_HOME": "/opt/openclaw"}, true},
		{"OPENCLAW_GATEWAY_TOKEN set", map[string]string{"OPENCLAW_GATEWAY_TOKEN": "secret"}, true},
		{"no env vars", map[string]string{}, false},
		{"other ORCHESTRATOR_ENV", map[string]string{"ORCHESTRATOR_ENV": "gastown"}, false},
	}

	agent := NewOpenClawAgent()
	ctx := context.Background()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := agentx.NewMockEnvironment(tt.env)
			got, err := agent.Detect(ctx, env)
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestOpenClawAgent_Identity(t *testing.T) {
	agent := NewOpenClawAgent()
	assert.Equal(t, agentx.AgentTypeOpenClaw, agent.Type())
	assert.Equal(t, "OpenClaw", agent.Name())
	assert.Equal(t, agentx.RoleOrchestrator, agent.Role())
}
