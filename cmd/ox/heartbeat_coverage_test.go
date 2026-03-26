package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- resolveAgentMetadata ---

func TestResolveAgentMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		agentID        string
		envAgentID     string
		envAgentType   string
		wantParent     string
		wantAgentType  string
	}{
		{
			name:          "empty agent ID returns empty",
			agentID:       "",
			envAgentID:    "Ox1234",
			envAgentType:  "claude-code",
			wantParent:    "",
			wantAgentType: "",
		},
		{
			name:          "same ID in env means no parent",
			agentID:       "Ox1234",
			envAgentID:    "Ox1234",
			envAgentType:  "claude-code",
			wantParent:    "",
			wantAgentType: "claude-code",
		},
		{
			name:           "different ID in env means parent detected",
			agentID:        "OxChild",
			envAgentID:     "OxParent",
			envAgentType:   "claude-code",
			wantParent:     "OxParent",
			wantAgentType:  "claude-code",
		},
		{
			name:          "no env vars set",
			agentID:       "Ox1234",
			envAgentID:    "",
			envAgentType:  "",
			wantParent:    "",
			wantAgentType: "",
		},
		{
			name:           "agent type without agent ID env",
			agentID:        "OxABC",
			envAgentID:     "",
			envAgentType:   "cursor",
			wantParent:     "",
			wantAgentType:  "cursor",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// set env vars for this test (not parallel due to env mutation)
			prevAgentID := os.Getenv("SAGEOX_AGENT_ID")
			prevAgentEnv := os.Getenv("AGENT_ENV")
			t.Cleanup(func() {
				os.Setenv("SAGEOX_AGENT_ID", prevAgentID)
				os.Setenv("AGENT_ENV", prevAgentEnv)
			})

			if tt.envAgentID != "" {
				os.Setenv("SAGEOX_AGENT_ID", tt.envAgentID)
			} else {
				os.Unsetenv("SAGEOX_AGENT_ID")
			}
			if tt.envAgentType != "" {
				os.Setenv("AGENT_ENV", tt.envAgentType)
			} else {
				os.Unsetenv("AGENT_ENV")
			}

			parent, agentType := resolveAgentMetadata(tt.agentID)
			assert.Equal(t, tt.wantParent, parent)
			assert.Equal(t, tt.wantAgentType, agentType)
		})
	}
}
