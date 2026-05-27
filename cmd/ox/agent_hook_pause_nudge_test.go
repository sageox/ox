package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// suspendedNudgeProject builds a minimal initialized project root + cache
// directory wired the same way the hook tests do (XDG-rooted, .sageox/config.json
// with repo_id and endpoint set so the canonical sessions write path resolves).
func suspendedNudgeProject(t *testing.T) string {
	t.Helper()
	cacheDir := t.TempDir()
	projectRoot := t.TempDir()

	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	cfg := `{"config_version":"2","repo_id":"test-repo-pause","endpoint":"http://test.sageox.local","session_publishing":"manual"}`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(cfg), 0644))

	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("HOME", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	t.Setenv("XDG_DATA_HOME", cacheDir)
	return projectRoot
}

func TestEmitSuspendedNudge_WhileSuspended(t *testing.T) {
	projectRoot := suspendedNudgeProject(t)
	agentID := "Oxa7b3"

	state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID:     agentID,
		AdapterName: "claude-code",
		Username:    "testuser",
	})
	require.NoError(t, err)
	paused := time.Now().UTC().Add(-3 * time.Minute)
	state.SuspendedAt = &paused
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
		s.SuspendedAt = &paused
	}))

	var buf bytes.Buffer
	emitSuspendedNudge(&buf, projectRoot, agentID)

	got := buf.String()
	assert.Contains(t, got, "<system-reminder>")
	assert.Contains(t, got, "Recording SUSPENDED")
	assert.Contains(t, got, "/ox-session-resume")
}

func TestEmitSuspendedNudge_NotSuspended(t *testing.T) {
	projectRoot := suspendedNudgeProject(t)
	agentID := "Oxa7b3"

	_, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID:     agentID,
		AdapterName: "claude-code",
		Username:    "testuser",
	})
	require.NoError(t, err)

	var buf bytes.Buffer
	emitSuspendedNudge(&buf, projectRoot, agentID)
	assert.Empty(t, buf.String(), "no nudge when recording is active (not suspended)")
}

func TestEmitSuspendedNudge_NoRecording(t *testing.T) {
	projectRoot := suspendedNudgeProject(t)

	var buf bytes.Buffer
	emitSuspendedNudge(&buf, projectRoot, "Oxnone")
	assert.Empty(t, buf.String())
}

func TestEmitSuspendedNudge_EmptyArgs(t *testing.T) {
	var buf bytes.Buffer
	emitSuspendedNudge(&buf, "", "Oxa")
	emitSuspendedNudge(&buf, "/tmp", "")
	assert.Empty(t, buf.String())
}
