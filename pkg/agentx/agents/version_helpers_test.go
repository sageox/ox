package agents

import (
	"context"
	"testing"

	"github.com/sageox/ox/pkg/agentx"
	"github.com/stretchr/testify/assert"
)

func TestExtractVersion(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple semver", "1.0.26", "1.0.26"},
		{"prefixed with v", "v1.0.26", "1.0.26"},
		{"claude output", "claude v1.0.26\n", "1.0.26"},
		{"aider output", "aider v0.82.1\n", "0.82.1"},
		{"goose output", "goose version 1.0.0\nbuilt from abc123", "1.0.0"},
		{"multiline", "App version: 2.3.4\nBuild: 12345\n", "2.3.4"},
		{"no version", "no version here", ""},
		{"empty", "", ""},
		{"partial semver", "1.0", ""},
		{"large version", "42.123.456", "42.123.456"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractVersion(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestVersionFromCommand(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		env := &agentx.MockEnvironment{
			ExecOutputs: map[string][]byte{
				"claude": []byte("1.0.26\n"),
			},
		}
		ver := versionFromCommand(ctx, env, "claude", "--version")
		assert.Equal(t, "1.0.26", ver)
	})

	t.Run("command not found", func(t *testing.T) {
		env := &agentx.MockEnvironment{}
		ver := versionFromCommand(ctx, env, "nonexistent", "--version")
		assert.Equal(t, "", ver)
	})

	t.Run("no version in output", func(t *testing.T) {
		env := &agentx.MockEnvironment{
			ExecOutputs: map[string][]byte{
				"tool": []byte("some output without version\n"),
			},
		}
		ver := versionFromCommand(ctx, env, "tool", "--version")
		assert.Equal(t, "", ver)
	})
}

func TestVersionFromPackageJSON(t *testing.T) {
	t.Run("valid package.json", func(t *testing.T) {
		env := &agentx.MockEnvironment{
			Files: map[string][]byte{
				"/home/test/.cursor/package.json": []byte(`{"name": "cursor", "version": "0.44.11"}`),
			},
		}
		ver := versionFromPackageJSON(env, "/home/test/.cursor/package.json")
		assert.Equal(t, "0.44.11", ver)
	})

	t.Run("file not found", func(t *testing.T) {
		env := &agentx.MockEnvironment{}
		ver := versionFromPackageJSON(env, "/nonexistent/package.json")
		assert.Equal(t, "", ver)
	})

	t.Run("invalid json", func(t *testing.T) {
		env := &agentx.MockEnvironment{
			Files: map[string][]byte{
				"/home/test/.cursor/package.json": []byte(`not json`),
			},
		}
		ver := versionFromPackageJSON(env, "/home/test/.cursor/package.json")
		assert.Equal(t, "", ver)
	})

	t.Run("no version field", func(t *testing.T) {
		env := &agentx.MockEnvironment{
			Files: map[string][]byte{
				"/home/test/.cursor/package.json": []byte(`{"name": "cursor"}`),
			},
		}
		ver := versionFromPackageJSON(env, "/home/test/.cursor/package.json")
		assert.Equal(t, "", ver)
	})
}

func TestClaudeCodeDetectVersion(t *testing.T) {
	ctx := context.Background()
	agent := NewClaudeCodeAgent()

	t.Run("detects version from cli", func(t *testing.T) {
		env := &agentx.MockEnvironment{
			ExecOutputs: map[string][]byte{
				"claude": []byte("1.0.26\n"),
			},
		}
		assert.Equal(t, "1.0.26", agent.DetectVersion(ctx, env))
	})

	t.Run("binary not found", func(t *testing.T) {
		env := &agentx.MockEnvironment{}
		assert.Equal(t, "", agent.DetectVersion(ctx, env))
	})
}

func TestCursorDetectVersion(t *testing.T) {
	ctx := context.Background()
	agent := NewCursorAgent()

	t.Run("detects version from package.json", func(t *testing.T) {
		env := &agentx.MockEnvironment{
			Home: "/home/test",
			Files: map[string][]byte{
				"/home/test/.cursor/package.json": []byte(`{"version": "0.44.11"}`),
			},
		}
		assert.Equal(t, "0.44.11", agent.DetectVersion(ctx, env))
	})

	t.Run("no package.json", func(t *testing.T) {
		env := &agentx.MockEnvironment{Home: "/home/test"}
		assert.Equal(t, "", agent.DetectVersion(ctx, env))
	})
}

func TestWindsurfDetectVersion(t *testing.T) {
	ctx := context.Background()
	agent := NewWindsurfAgent()

	t.Run("detects version from package.json", func(t *testing.T) {
		env := &agentx.MockEnvironment{
			Home: "/home/test",
			Files: map[string][]byte{
				"/home/test/.codeium/windsurf/package.json": []byte(`{"version": "1.2.3"}`),
			},
		}
		assert.Equal(t, "1.2.3", agent.DetectVersion(ctx, env))
	})
}

func TestAiderDetectVersion(t *testing.T) {
	ctx := context.Background()
	agent := NewAiderAgent()

	t.Run("detects version from cli", func(t *testing.T) {
		env := &agentx.MockEnvironment{
			ExecOutputs: map[string][]byte{
				"aider": []byte("aider v0.82.1\n"),
			},
		}
		assert.Equal(t, "0.82.1", agent.DetectVersion(ctx, env))
	})
}

func TestNoopDetectVersion(t *testing.T) {
	ctx := context.Background()
	env := &agentx.MockEnvironment{}

	// agents that return "" for DetectVersion
	agents := []agentx.Agent{
		NewCopilotAgent(),
		NewClineAgent(),
		NewCodyAgent(),
		NewContinueAgent(),
		NewCodePuppyAgent(),
		NewKiroAgent(),
	}

	for _, agent := range agents {
		t.Run(agent.Name(), func(t *testing.T) {
			assert.Equal(t, "", agent.DetectVersion(ctx, env))
		})
	}
}
