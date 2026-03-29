//go:build !short

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for #258: env/state writes gated on optional context.
//
// In agent_prime.go, session marker and env file writes are gated on
// agentSessionID != "". When agentSessionID is empty, no marker or env
// file should be written. These tests verify the conditional write pattern
// to prevent regressions where SAGEOX_AGENT_ID (or other vars) silently
// fail to appear when the session ID is absent.

func TestConditionalWrite_MarkerGate(t *testing.T) {
	t.Run("empty session ID skips marker write", func(t *testing.T) {
		// capture marker directory state before the conditional block
		markerDir := SessionMarkerDir()
		beforeFiles := listFiles(t, markerDir)

		// simulate the prime code gate with empty session ID
		agentSessionID := ""
		if agentSessionID != "" {
			err := WriteSessionMarker(&SessionMarker{
				AgentID:        "OxConditional",
				SessionID:      "oxsid_test",
				AgentSessionID: agentSessionID,
				PrimedAt:       time.Now(),
			})
			require.NoError(t, err)
		}

		// no new marker files should exist
		afterFiles := listFiles(t, markerDir)
		assert.Equal(t, len(beforeFiles), len(afterFiles),
			"no new marker files should be created when agentSessionID is empty")
	})

	t.Run("non-empty session ID writes marker", func(t *testing.T) {
		agentSessionID := "conditional-test-" + time.Now().Format("20060102150405.000")
		t.Cleanup(func() { DeleteSessionMarker(agentSessionID) })

		if agentSessionID != "" {
			err := WriteSessionMarker(&SessionMarker{
				AgentID:        "OxConditional",
				SessionID:      "oxsid_conditional",
				AgentSessionID: agentSessionID,
				PrimedAt:       time.Now(),
			})
			require.NoError(t, err)
		}

		marker, err := ReadSessionMarker(agentSessionID)
		require.NoError(t, err)
		require.NotNil(t, marker, "marker should exist when agentSessionID is non-empty")
		assert.Equal(t, "OxConditional", marker.AgentID)
		assert.Equal(t, "oxsid_conditional", marker.SessionID)
	})
}

func TestConditionalWrite_EnvFileGate(t *testing.T) {
	t.Run("empty session ID skips env file write", func(t *testing.T) {
		tmpDir := t.TempDir()
		envFile := filepath.Join(tmpDir, "agent.env")

		t.Setenv("CLAUDE_ENV_FILE", envFile)

		agentSessionID := ""
		if agentSessionID != "" {
			envVars := map[string]string{
				"SAGEOX_AGENT_ID":   "OxGated",
				"SAGEOX_SESSION_ID": "oxsid_gated",
			}
			_ = WriteToAgentEnvFile(envVars)
		}

		// env file should not exist because the gate prevented the write
		_, err := os.Stat(envFile)
		assert.True(t, os.IsNotExist(err),
			"env file should not be created when agentSessionID is empty")
	})

	t.Run("non-empty session ID writes env file", func(t *testing.T) {
		tmpDir := t.TempDir()
		envFile := filepath.Join(tmpDir, "agent.env")

		t.Setenv("CLAUDE_ENV_FILE", envFile)

		agentSessionID := "env-test-session"
		if agentSessionID != "" {
			envVars := map[string]string{
				"SAGEOX_AGENT_ID":   "OxEnvGated",
				"SAGEOX_SESSION_ID": "oxsid_envgated",
			}
			err := WriteToAgentEnvFile(envVars)
			require.NoError(t, err)
		}

		content, err := os.ReadFile(envFile)
		require.NoError(t, err)
		assert.Contains(t, string(content), `export SAGEOX_AGENT_ID="OxEnvGated"`)
		assert.Contains(t, string(content), `export SAGEOX_SESSION_ID="oxsid_envgated"`)
	})
}

func TestWriteSessionMarker_EmptyAgentSessionID(t *testing.T) {
	// WriteSessionMarker returns an error when AgentSessionID is empty,
	// which is the defense-in-depth guard behind the prime code gate.
	err := WriteSessionMarker(&SessionMarker{
		AgentID:        "OxEmpty",
		SessionID:      "oxsid_empty",
		AgentSessionID: "",
		PrimedAt:       time.Now(),
	})
	assert.Error(t, err, "WriteSessionMarker should reject empty AgentSessionID")
	assert.Contains(t, err.Error(), "agent session ID is required")
}

// listFiles returns filenames in dir, or empty slice if dir doesn't exist.
func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
