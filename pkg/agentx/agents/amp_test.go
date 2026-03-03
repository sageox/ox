package agents

import (
	"context"
	"testing"

	"github.com/sageox/ox/pkg/agentx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAmpDetect(t *testing.T) {
	ctx := context.Background()
	agent := NewAmpAgent()

	tests := []struct {
		name     string
		envVars  map[string]string
		expected bool
	}{
		{"AMP=1", map[string]string{"AMP": "1"}, true},
		{"AMP_AGENT=1", map[string]string{"AMP_AGENT": "1"}, true},
		{"AMP_THREAD_URL set", map[string]string{"AMP_THREAD_URL": "https://ampcode.com/threads/abc123"}, true},
		{"AGENT_ENV=amp", map[string]string{"AGENT_ENV": "amp"}, true},
		{"no env vars", map[string]string{}, false},
		{"unrelated env", map[string]string{"CURSOR_AGENT": "1"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := agentx.NewMockEnvironment(tt.envVars)
			detected, err := agent.Detect(ctx, env)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, detected)
		})
	}
}

func TestAmpMetadata(t *testing.T) {
	agent := NewAmpAgent()

	assert.Equal(t, agentx.AgentTypeAmp, agent.Type())
	assert.Equal(t, "Amp", agent.Name())
	assert.Equal(t, "https://ampcode.com", agent.URL())
	assert.Equal(t, agentx.RoleAgent, agent.Role())
	assert.True(t, agent.SupportsXDGConfig())
	assert.Contains(t, agent.ContextFiles(), "AGENTS.md")
}

func TestAmpCapabilities(t *testing.T) {
	agent := NewAmpAgent()
	caps := agent.Capabilities()

	assert.True(t, caps.MCPServers)
	assert.True(t, caps.SystemPrompt)
	assert.True(t, caps.ProjectContext)
	assert.True(t, caps.CustomCommands)
}

func TestAmpUserConfigPath(t *testing.T) {
	agent := NewAmpAgent()

	env := &agentx.MockEnvironment{
		Config: "/home/test/.config",
	}
	path, err := agent.UserConfigPath(env)
	require.NoError(t, err)
	assert.Equal(t, "/home/test/.config/amp", path)
}

func TestAmpDetectVersion(t *testing.T) {
	ctx := context.Background()
	agent := NewAmpAgent()

	t.Run("detects version from cli", func(t *testing.T) {
		env := &agentx.MockEnvironment{
			ExecOutputs: map[string][]byte{
				"amp": []byte("0.3.1\n"),
			},
		}
		assert.Equal(t, "0.3.1", agent.DetectVersion(ctx, env))
	})

	t.Run("binary not found", func(t *testing.T) {
		env := &agentx.MockEnvironment{}
		assert.Equal(t, "", agent.DetectVersion(ctx, env))
	})
}

func TestAmpIsInstalled(t *testing.T) {
	ctx := context.Background()
	agent := NewAmpAgent()

	t.Run("found in PATH", func(t *testing.T) {
		env := &agentx.MockEnvironment{
			PathBinaries: map[string]string{"amp": "/usr/local/bin/amp"},
		}
		installed, err := agent.IsInstalled(ctx, env)
		require.NoError(t, err)
		assert.True(t, installed)
	})

	t.Run("config dir exists", func(t *testing.T) {
		env := &agentx.MockEnvironment{
			Config:       "/home/test/.config",
			ExistingDirs: map[string]bool{"/home/test/.config/amp": true},
		}
		installed, err := agent.IsInstalled(ctx, env)
		require.NoError(t, err)
		assert.True(t, installed)
	})

	t.Run("not installed", func(t *testing.T) {
		env := &agentx.MockEnvironment{
			Config: "/home/test/.config",
		}
		installed, err := agent.IsInstalled(ctx, env)
		require.NoError(t, err)
		assert.False(t, installed)
	})
}

func TestAmpSessionID(t *testing.T) {
	agent := NewAmpAgent()
	assert.True(t, agent.SupportsSession())

	t.Run("returns thread URL from env var", func(t *testing.T) {
		env := agentx.NewMockEnvironment(map[string]string{
			"AMP_THREAD_URL": "https://ampcode.com/threads/abc123",
		})
		assert.Equal(t, "https://ampcode.com/threads/abc123", agent.SessionID(env))
	})

	t.Run("returns empty when env var not set", func(t *testing.T) {
		env := agentx.NewMockEnvironment(nil)
		assert.Equal(t, "", agent.SessionID(env))
	})
}
