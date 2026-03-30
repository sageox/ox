package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProcessSession_InvalidAdapter(t *testing.T) {
	t.Parallel()
	state := &session.RecordingState{
		AdapterName: "nonexistent-adapter-xyz",
		SessionFile: "/tmp/fake-session.jsonl",
		AgentID:     "test-agent",
	}
	result, err := processSession(t.TempDir(), state)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "adapter not found")
	assert.Nil(t, result)
}

func TestProcessSession_NonexistentSessionFile(t *testing.T) {
	t.Parallel()
	state := &session.RecordingState{
		AdapterName: "generic",
		SessionFile: "/tmp/nonexistent-session-file-12345.jsonl",
		AgentID:     "test-agent",
	}
	result, err := processSession(t.TempDir(), state)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read session")
	assert.Nil(t, result)
}

func TestProcessSession_EmptySessionFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "empty.jsonl")
	require.NoError(t, os.WriteFile(sessionFile, []byte(""), 0644))

	state := &session.RecordingState{
		AdapterName: "generic",
		SessionFile: sessionFile,
		AgentID:     "test-agent",
	}
	result, err := processSession(dir, state)
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, 0, result.EntryCount)
}
