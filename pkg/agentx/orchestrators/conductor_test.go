package orchestrators

import (
	"context"
	"testing"

	"github.com/sageox/ox/pkg/agentx"
	"github.com/stretchr/testify/assert"
)

func TestConductorAgent_Detect(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"ORCHESTRATOR_ENV=conductor", map[string]string{"ORCHESTRATOR_ENV": "conductor"}, true},
		{"CONDUCTOR_WORKSPACE_NAME set", map[string]string{"CONDUCTOR_WORKSPACE_NAME": "myworkspace"}, true},
		{"CONDUCTOR_WORKSPACE_PATH set", map[string]string{"CONDUCTOR_WORKSPACE_PATH": "/path/to/workspace"}, true},
		{"CFBundleIdentifier match", map[string]string{"__CFBundleIdentifier": "com.conductor.app"}, true},
		{"no env vars", map[string]string{}, false},
		{"wrong CFBundleIdentifier", map[string]string{"__CFBundleIdentifier": "com.other.app"}, false},
	}

	agent := NewConductorAgent()
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

func TestConductorAgent_Identity(t *testing.T) {
	agent := NewConductorAgent()
	assert.Equal(t, agentx.AgentTypeConductor, agent.Type())
	assert.Equal(t, "Conductor", agent.Name())
	assert.Equal(t, agentx.RoleOrchestrator, agent.Role())
}
