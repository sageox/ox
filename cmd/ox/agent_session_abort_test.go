package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupAbortTest creates a project with an active recording and changes cwd.
func setupAbortTest(t *testing.T) (string, *session.RecordingState) {
	t.Helper()

	cfg = &config.Config{}

	projectRoot := setupSessionTestProject(t)

	state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
		AgentID:     "OxAbrt",
		AdapterName: "test",
	})
	require.NoError(t, err)

	// populate session folder
	require.NoError(t, os.WriteFile(filepath.Join(state.SessionPath, "raw.jsonl"), []byte(`{"test":true}`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(state.SessionPath, "events.jsonl"), []byte(`{"event":true}`), 0644))

	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(projectRoot))
	t.Cleanup(func() { os.Chdir(origDir) })

	return projectRoot, state
}

func TestAbortNotRecording(t *testing.T) {
	cfg = &config.Config{}
	projectRoot := setupSessionTestProject(t)

	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(projectRoot))
	defer os.Chdir(origDir)

	inst := &agentinstance.Instance{AgentID: "OxTest"}
	err := runAgentSessionAbort(inst, []string{"--force"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no active session")
}

func TestAbortClearsRecordingState(t *testing.T) {
	projectRoot, _ := setupAbortTest(t)

	require.True(t, session.IsRecording(projectRoot))

	inst := &agentinstance.Instance{AgentID: "OxAbrt"}
	err := runAgentSessionAbort(inst, []string{"--force"})
	require.NoError(t, err)

	// if .recording.json survives, next session start fails with "already recording"
	assert.False(t, session.IsRecording(projectRoot), ".recording.json must be cleared after abort")
}

func TestAbortRemovesSessionFolder(t *testing.T) {
	_, state := setupAbortTest(t)

	_, err := os.Stat(state.SessionPath)
	require.NoError(t, err)

	inst := &agentinstance.Instance{AgentID: "OxAbrt"}
	err = runAgentSessionAbort(inst, []string{"--force"})
	require.NoError(t, err)

	// entire folder must be gone so doctor doesn't detect orphaned data
	_, err = os.Stat(state.SessionPath)
	assert.True(t, os.IsNotExist(err), "session folder should be removed after abort")
}

func TestAbortEmptySessionPathDoesNotDeleteCwd(t *testing.T) {
	projectRoot, state := setupAbortTest(t)

	// corrupt .recording.json: clear SessionPath to simulate damaged state
	corruptState := fmt.Sprintf(`{"agent_id":"OxAbrt","started_at":"%s","adapter_name":"test","session_path":""}`,
		state.StartedAt.Format(time.RFC3339))
	recordingPath := filepath.Join(state.SessionPath, ".recording.json")
	require.NoError(t, os.WriteFile(recordingPath, []byte(corruptState), 0644))

	inst := &agentinstance.Instance{AgentID: "OxAbrt"}
	err := runAgentSessionAbort(inst, []string{"--force"})
	// abort may succeed or error — either is fine, but cwd must survive
	_ = err

	// the critical assertion: cwd was not deleted
	_, statErr := os.Stat(projectRoot)
	assert.NoError(t, statErr, "project root must not be deleted when SessionPath is empty")
}

func TestAbortRequiresForce(t *testing.T) {
	setupAbortTest(t)

	// simulate non-interactive (agent/pipe) — requires --force
	cli.SetNoInteractive(true)
	t.Cleanup(func() { cli.SetNoInteractive(false) })

	inst := &agentinstance.Instance{AgentID: "OxAbrt"}
	err := runAgentSessionAbort(inst, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "destructive")
}
