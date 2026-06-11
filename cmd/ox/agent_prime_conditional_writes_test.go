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

// These tests verify defense-in-depth: the functions themselves guard against
// invalid inputs, so even if a caller's gate is accidentally removed, no
// garbage files are created and no data is lost.

func TestWriteSessionMarker_RejectsEmptyAgentSessionID(t *testing.T) {
	// Intent: writing a marker with empty session ID must fail,
	// so even if the caller's gate is removed, no garbage file is created.
	err := WriteSessionMarker(&SessionMarker{
		AgentID:        "OxTest",
		SessionID:      "oxsid_test",
		AgentSessionID: "", // empty
		PrimedAt:       time.Now(),
	})
	require.Error(t, err, "WriteSessionMarker must reject empty AgentSessionID")
	assert.Contains(t, err.Error(), "agent session ID is required")
}

func TestWriteSessionMarker_RoundTrip(t *testing.T) {
	// Intent: a written marker must be readable with all fields preserved.
	// This is the contract that session start/stop relies on.
	agentSessionID := "roundtrip-test-" + time.Now().Format("20060102150405.000")
	t.Cleanup(func() { DeleteSessionMarker(agentSessionID) })

	marker := &SessionMarker{
		AgentID:        "OxRoundTrip",
		SessionID:      "oxsid_rt",
		AgentSessionID: agentSessionID,
		PrimedAt:       time.Now().Truncate(time.Second),
	}
	require.NoError(t, WriteSessionMarker(marker))

	read, err := ReadSessionMarker(agentSessionID)
	require.NoError(t, err)
	require.NotNil(t, read)
	assert.Equal(t, "OxRoundTrip", read.AgentID)
	assert.Equal(t, "oxsid_rt", read.SessionID)
	assert.Equal(t, agentSessionID, read.AgentSessionID)
}

func TestReadSessionMarker_EmptyIDReturnsNil(t *testing.T) {
	// Intent: reading with empty ID is a safe no-op, not an error.
	marker, err := ReadSessionMarker("")
	assert.NoError(t, err)
	assert.Nil(t, marker)
}

func TestWriteToAgentEnvFile_WritesExpectedVars(t *testing.T) {
	// Intent: when env file writing is triggered, the exported vars
	// must include SAGEOX_AGENT_ID so agents can identify themselves.
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, "agent.env")
	t.Setenv("CLAUDE_ENV_FILE", envFile)

	err := WriteToAgentEnvFile(map[string]string{
		"SAGEOX_AGENT_ID":   "OxEnvTest",
		"SAGEOX_SESSION_ID": "oxsid_envtest",
	})
	require.NoError(t, err)

	content, err := os.ReadFile(envFile)
	require.NoError(t, err)
	assert.Contains(t, string(content), `export SAGEOX_AGENT_ID="OxEnvTest"`,
		"env file must export SAGEOX_AGENT_ID for downstream agent tools")
	assert.Contains(t, string(content), `export SAGEOX_SESSION_ID="oxsid_envtest"`)
}

func TestWriteToAgentEnvFile_NoOpWithoutEnvVar(t *testing.T) {
	// Intent: if CLAUDE_ENV_FILE is not set, env file writing is a no-op
	// (not an error). This prevents failures in non-Claude environments.
	t.Setenv("CLAUDE_ENV_FILE", "")

	err := WriteToAgentEnvFile(map[string]string{
		"SAGEOX_AGENT_ID": "OxNoOp",
	})
	assert.NoError(t, err, "should be a no-op without CLAUDE_ENV_FILE")
}
