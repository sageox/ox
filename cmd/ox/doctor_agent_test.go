package main

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// agentEnvVarsToSave lists all env vars that might indicate an agent context
var agentEnvVarsToSave = []string{
	"AGENT_ENV",
	"CLAUDECODE", // Claude Code sets this to "1"
	"CLAUDE_CODE",
	"CURSOR_AGENT",
	"CURSOR_TRACE_ID",
	"WINDSURF_AGENT",
	"WINDSURF_SESSION",
	"CODEIUM_AGENT",
	"CLINE_TASK_ID",
	"AIDER",
	"AIDER_AGENT",
	"AIDER_SESSION",
	"CODEX_CI",
	"CODEX_SANDBOX",
	"CODEX_THREAD_ID",
	"OPENCODE",
	"OPENCODE_AGENT",
	"CODE_PUPPY",
	"CODE_PUPPY_AGENT",
	"GOOSE",
	"GOOSE_AGENT",
	"COPILOT_AGENT",
	"CONTINUE_AGENT",
	"KIRO",
	"KIRO_AGENT",
	"AMP",
	"AMP_AGENT",
	"CODY_AGENT",
	"MCP_SERVER_NAME",
	"CLAUDE_CODE_ENTRYPOINT",
	"_", // executable path checked by some agents
}

// saveAndClearAgentEnv saves and clears all agent-related env vars
func saveAndClearAgentEnv() map[string]string {
	saved := make(map[string]string)
	for _, v := range agentEnvVarsToSave {
		saved[v] = os.Getenv(v)
		os.Unsetenv(v)
	}
	return saved
}

// restoreAgentEnv restores previously saved env vars
func restoreAgentEnv(saved map[string]string) {
	for k, v := range saved {
		if v == "" {
			os.Unsetenv(k)
		} else {
			os.Setenv(k, v)
		}
	}
}

func TestCheckAgentEnvironment(t *testing.T) {
	saved := saveAndClearAgentEnv()
	defer restoreAgentEnv(saved)

	t.Run("no agent context", func(t *testing.T) {
		result := checkAgentEnvironment()
		assert.True(t, result.skipped, "expected skipped when no agent, got passed=%v skipped=%v warning=%v", result.passed, result.skipped, result.warning)
	})

	t.Run("unrecognized AGENT_ENV", func(t *testing.T) {
		os.Setenv("AGENT_ENV", "unknown-agent")
		defer os.Unsetenv("AGENT_ENV")

		result := checkAgentEnvironment()
		// should be warning (passed=true, warning=true)
		assert.True(t, result.warning, "expected warning for unrecognized AGENT_ENV, got passed=%v warning=%v", result.passed, result.warning)
	})
}

func TestCheckAgentEnvValidity(t *testing.T) {
	origAgentEnv := os.Getenv("AGENT_ENV")
	defer os.Setenv("AGENT_ENV", origAgentEnv)

	t.Run("not set", func(t *testing.T) {
		os.Unsetenv("AGENT_ENV")

		result := checkAgentEnvValidity()
		assert.True(t, result.skipped, "expected skipped when AGENT_ENV not set")
	})

	t.Run("valid agent", func(t *testing.T) {
		os.Setenv("AGENT_ENV", "claude-code")

		result := checkAgentEnvValidity()
		assert.True(t, result.passed, "expected passed for valid AGENT_ENV=claude-code")
		assert.Contains(t, result.message, "canonical: claude")
	})

	t.Run("valid agent case insensitive", func(t *testing.T) {
		os.Setenv("AGENT_ENV", "Claude-Code")

		result := checkAgentEnvValidity()
		assert.True(t, result.passed, "expected passed for valid AGENT_ENV=Claude-Code (case insensitive)")
	})

	t.Run("valid canonical agent slug", func(t *testing.T) {
		os.Setenv("AGENT_ENV", "claude")

		result := checkAgentEnvValidity()
		assert.True(t, result.passed, "expected passed for valid AGENT_ENV=claude")
		assert.Contains(t, result.message, "canonical: claude")
	})

	t.Run("valid canonical code puppy slug", func(t *testing.T) {
		os.Setenv("AGENT_ENV", "code-puppy")

		result := checkAgentEnvValidity()
		assert.True(t, result.passed, "expected passed for valid AGENT_ENV=code-puppy")
		assert.Contains(t, result.message, "canonical: code-puppy")
	})

	t.Run("unknown agent", func(t *testing.T) {
		os.Setenv("AGENT_ENV", "my-custom-agent")

		result := checkAgentEnvValidity()
		assert.True(t, result.warning, "expected warning for unknown AGENT_ENV")
	})
}

func TestCheckConflictingAgentEnvVars(t *testing.T) {
	// save all relevant env vars
	envVars := []string{
		"CLAUDE_CODE", "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT",
		"CURSOR_TRACE_ID",
		"WINDSURF_SESSION",
		"CLINE_TASK_ID",
		"AIDER_SESSION",
		"CODEX_CI", "CODEX_SANDBOX", "CODEX_THREAD_ID",
	}
	saved := make(map[string]string)
	for _, v := range envVars {
		saved[v] = os.Getenv(v)
	}
	defer func() {
		for k, v := range saved {
			if v == "" {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, v)
			}
		}
	}()

	// clear all
	for _, v := range envVars {
		os.Unsetenv(v)
	}

	t.Run("no agent vars", func(t *testing.T) {
		result := checkConflictingAgentEnvVars()
		assert.True(t, result.skipped, "expected skipped when no agent env vars")
	})

	t.Run("single agent", func(t *testing.T) {
		os.Setenv("CLAUDE_CODE", "1")

		result := checkConflictingAgentEnvVars()
		assert.True(t, result.passed, "expected passed for single agent env var")

		os.Unsetenv("CLAUDE_CODE")
	})

	t.Run("multiple agents - conflict", func(t *testing.T) {
		os.Setenv("CLAUDE_CODE", "1")
		os.Setenv("CURSOR_TRACE_ID", "abc123")

		result := checkConflictingAgentEnvVars()
		assert.True(t, result.warning, "expected warning for multiple agent env vars")

		os.Unsetenv("CLAUDE_CODE")
		os.Unsetenv("CURSOR_TRACE_ID")
	})

	t.Run("multiple Codex vars only - no conflict", func(t *testing.T) {
		os.Setenv("CODEX_CI", "1")
		os.Setenv("CODEX_THREAD_ID", "thread_123")

		result := checkConflictingAgentEnvVars()
		assert.True(t, result.passed, "expected passed for multiple env vars from same agent")

		os.Unsetenv("CODEX_CI")
		os.Unsetenv("CODEX_THREAD_ID")
	})
}

func TestCheckDaemonInstanceStale(t *testing.T) {
	// this test verifies the function behavior when daemon is not running
	// (it should skip gracefully)
	t.Run("daemon not running", func(t *testing.T) {
		result := checkDaemonInstanceStale(false)
		// should skip when daemon is not running
		assert.True(t, result.skipped, "expected skipped when daemon not running")
		assert.Equal(t, "Daemon agent instances", result.name)
	})
}

func TestFormatInstanceAge(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		{"30 minutes", 30 * time.Minute, "30m"},
		{"59 minutes", 59 * time.Minute, "59m"},
		{"1 hour", 1 * time.Hour, "1h"},
		{"2 hours", 2 * time.Hour, "2h"},
		{"23 hours", 23 * time.Hour, "23h"},
		{"24 hours", 24 * time.Hour, "1d"},
		{"48 hours", 48 * time.Hour, "2d"},
		{"72 hours", 72 * time.Hour, "3d"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatInstanceAge(tt.duration)
			assert.Equal(t, tt.expected, result)
		})
	}
}
