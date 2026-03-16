package main

import (
	"encoding/json"
	"fmt"
	"io"
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
	require.NoError(t, os.WriteFile(filepath.Join(state.SessionPath, ledgerFileRaw), []byte(`{"test":true}`), 0644))

	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(projectRoot))
	t.Cleanup(func() { os.Chdir(origDir) })

	return projectRoot, state
}

// setForceFlag sets the --force flag on agentCmd for testing and resets it on cleanup.
func setForceFlag(t *testing.T, value bool) {
	t.Helper()
	require.NoError(t, agentCmd.PersistentFlags().Set("force", fmt.Sprintf("%t", value)))
	t.Cleanup(func() { _ = agentCmd.PersistentFlags().Set("force", "false") })
}

func TestAbortNotRecording(t *testing.T) {
	cfg = &config.Config{}
	projectRoot := setupSessionTestProject(t)

	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(projectRoot))
	defer os.Chdir(origDir)

	setForceFlag(t, true)

	inst := &agentinstance.Instance{AgentID: "OxTest"}
	err := runAgentSessionAbort(inst, agentCmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no active session")
}

func TestAbortClearsRecordingState(t *testing.T) {
	projectRoot, _ := setupAbortTest(t)

	require.True(t, session.IsRecording(projectRoot))

	setForceFlag(t, true)

	inst := &agentinstance.Instance{AgentID: "OxAbrt"}
	err := runAgentSessionAbort(inst, agentCmd, nil)
	require.NoError(t, err)

	// if .recording.json survives, next session start fails with "already recording"
	assert.False(t, session.IsRecording(projectRoot), ".recording.json must be cleared after abort")
}

func TestAbortRemovesSessionFolder(t *testing.T) {
	_, state := setupAbortTest(t)

	_, err := os.Stat(state.SessionPath)
	require.NoError(t, err)

	setForceFlag(t, true)

	inst := &agentinstance.Instance{AgentID: "OxAbrt"}
	err = runAgentSessionAbort(inst, agentCmd, nil)
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

	setForceFlag(t, true)

	inst := &agentinstance.Instance{AgentID: "OxAbrt"}
	err := runAgentSessionAbort(inst, agentCmd, nil)
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

	// --force defaults to false, so no need to set it
	inst := &agentinstance.Instance{AgentID: "OxAbrt"}
	err := runAgentSessionAbort(inst, agentCmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "destructive")
}

// TestAbortForceViaCobraFlag is a regression test for the bug where --force
// was rejected by cobra before reaching the abort handler. The flag must be
// registered on agentCmd and readable via cobra's flag API.
func TestAbortForceViaCobraFlag(t *testing.T) {
	projectRoot, _ := setupAbortTest(t)

	cli.SetNoInteractive(true)
	t.Cleanup(func() { cli.SetNoInteractive(false) })

	require.True(t, session.IsRecording(projectRoot))

	// set --force via cobra flag (simulates what cobra does when parsing CLI args)
	setForceFlag(t, true)

	inst := &agentinstance.Instance{AgentID: "OxAbrt"}
	err := runAgentSessionAbort(inst, agentCmd, nil)
	require.NoError(t, err, "--force via cobra flag should skip confirmation")

	assert.False(t, session.IsRecording(projectRoot), "session should be aborted")
}

func TestAbortDifferentAgent_CannotAbortOtherAgentSession(t *testing.T) {
	projectRoot, state := setupAbortTest(t)

	// Agent A (OxAbrt) has an active recording from setupAbortTest
	require.True(t, session.IsRecording(projectRoot))
	assert.Equal(t, "OxAbrt", state.AgentID)

	setForceFlag(t, true)

	// Agent B calls abort with no args — agent-scoped, so B cannot see or abort A's session
	instB := &agentinstance.Instance{AgentID: "OxOthr"}
	err := runAgentSessionAbort(instB, agentCmd, nil)
	require.Error(t, err, "abort should fail when agent has no active session")
	assert.Contains(t, err.Error(), "no active session")

	// A's recording should still be active (untouched by B)
	assert.True(t, session.IsRecordingForAgent(projectRoot, "OxAbrt"),
		"A's recording should still be active after B's failed abort")

	// A's session folder should still exist
	_, err = os.Stat(state.SessionPath)
	assert.NoError(t, err, "A's session folder should still exist")
}

func TestAbort_SessionFolderWithReadOnlyFiles(t *testing.T) {
	_, state := setupAbortTest(t)

	// make a file read-only inside session folder
	readOnlyFile := filepath.Join(state.SessionPath, "readonly.dat")
	require.NoError(t, os.WriteFile(readOnlyFile, []byte("protected"), 0444))

	setForceFlag(t, true)

	inst := &agentinstance.Instance{AgentID: "OxAbrt"}
	err := runAgentSessionAbort(inst, agentCmd, nil)
	require.NoError(t, err, "abort should succeed even with read-only files in session folder")

	// session folder should be fully removed
	_, err = os.Stat(state.SessionPath)
	assert.True(t, os.IsNotExist(err),
		"session folder with read-only files should be fully removed after abort")
}

func TestAbortOutputIncludesGuidance(t *testing.T) {
	setupAbortTest(t)
	setForceFlag(t, true)

	// capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	inst := &agentinstance.Instance{AgentID: "OxAbrt"}
	err := runAgentSessionAbort(inst, agentCmd, nil)

	w.Close()
	os.Stdout = oldStdout

	require.NoError(t, err)

	out, _ := io.ReadAll(r)
	var output sessionAbortOutput
	require.NoError(t, json.Unmarshal(out, &output), "output should be valid JSON")
	assert.True(t, output.Success)
	assert.NotEmpty(t, output.Guidance, "abort JSON output must include guidance field")
	assert.Contains(t, output.Guidance, "No further action needed")
}

// --- Tests for abort-by-name (orphaned/non-recording sessions) ---

// makeOrphanSession creates a session folder with a .recording.json pointing to a
// dead PID, simulating an orphaned session (parent process exited without stopping).
func makeOrphanSession(t *testing.T, projectRoot, agentID string) (string, string) {
	t.Helper()

	repoID := getRepoIDOrDefault(projectRoot)
	contextPath := session.GetContextPath(repoID)
	sessionName := fmt.Sprintf("2026-03-15T10-00-testuser-%s", agentID)
	sessionPath := filepath.Join(contextPath, "sessions", sessionName)
	require.NoError(t, os.MkdirAll(sessionPath, 0755))

	// write raw.jsonl with content so it classifies as orphan (not ghost)
	require.NoError(t, os.WriteFile(
		filepath.Join(sessionPath, "raw.jsonl"),
		[]byte("{\"type\":\"header\"}\n{\"type\":\"message\",\"seq\":0}\n"),
		0644,
	))

	// write .recording.json with a dead PID (99999999 — guaranteed not running)
	recState := fmt.Sprintf(`{"agent_id":"%s","started_at":"2026-03-15T10:00:00Z","adapter_name":"test","session_path":"%s","parent_pid":99999999,"entry_count":1}`,
		agentID, sessionPath)
	require.NoError(t, os.WriteFile(
		filepath.Join(sessionPath, ".recording.json"),
		[]byte(recState),
		0644,
	))

	return sessionName, sessionPath
}

func TestAbortByName_OrphanedSession(t *testing.T) {
	cfg = &config.Config{}
	projectRoot := setupSessionTestProject(t)

	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(projectRoot))
	t.Cleanup(func() { os.Chdir(origDir) })

	sessionName, sessionPath := makeOrphanSession(t, projectRoot, "OxDead")

	setForceFlag(t, true)

	// different agent aborts the orphan by name
	inst := &agentinstance.Instance{AgentID: "OxOthr"}
	err := runAgentSessionAbort(inst, agentCmd, []string{sessionName})
	require.NoError(t, err)

	_, err = os.Stat(sessionPath)
	assert.True(t, os.IsNotExist(err), "orphaned session folder should be removed")
}

func TestAbortByName_GhostSession(t *testing.T) {
	cfg = &config.Config{}
	projectRoot := setupSessionTestProject(t)

	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(projectRoot))
	t.Cleanup(func() { os.Chdir(origDir) })

	// ghost = dead PID + no substantive data
	repoID := getRepoIDOrDefault(projectRoot)
	contextPath := session.GetContextPath(repoID)
	sessionName := "2026-03-15T10-00-testuser-OxGhst"
	sessionPath := filepath.Join(contextPath, "sessions", sessionName)
	require.NoError(t, os.MkdirAll(sessionPath, 0755))

	recState := fmt.Sprintf(`{"agent_id":"OxGhst","started_at":"2026-03-15T10:00:00Z","adapter_name":"test","session_path":"%s","parent_pid":99999999}`, sessionPath)
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, ".recording.json"), []byte(recState), 0644))

	setForceFlag(t, true)

	inst := &agentinstance.Instance{AgentID: "OxTest"}
	err := runAgentSessionAbort(inst, agentCmd, []string{sessionName})
	require.NoError(t, err)

	_, err = os.Stat(sessionPath)
	assert.True(t, os.IsNotExist(err), "ghost session should be removed")
}

func TestAbortByName_NonRecordingSession(t *testing.T) {
	// session folder with raw.jsonl but no .recording.json = local-only status
	cfg = &config.Config{}
	projectRoot := setupSessionTestProject(t)

	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(projectRoot))
	t.Cleanup(func() { os.Chdir(origDir) })

	repoID := getRepoIDOrDefault(projectRoot)
	contextPath := session.GetContextPath(repoID)
	sessionName := "2026-03-15T10-00-testuser-OxDead"
	sessionPath := filepath.Join(contextPath, "sessions", sessionName)
	require.NoError(t, os.MkdirAll(sessionPath, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, "raw.jsonl"), []byte(`{"type":"header"}`), 0644))

	setForceFlag(t, true)

	inst := &agentinstance.Instance{AgentID: "OxTest"}
	err := runAgentSessionAbort(inst, agentCmd, []string{sessionName})
	require.NoError(t, err)

	_, err = os.Stat(sessionPath)
	assert.True(t, os.IsNotExist(err), "non-recording session folder should be removed")
}

func TestAbortByName_RejectsRecordingSession(t *testing.T) {
	// abort-by-name should reject actively recording sessions (alive PID)
	_, state := setupAbortTest(t)
	sessionName := session.GetSessionName(state.SessionPath)

	setForceFlag(t, true)

	inst := &agentinstance.Instance{AgentID: "OxOthr"}
	err := runAgentSessionAbort(inst, agentCmd, []string{sessionName})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "actively recording")

	// session folder should still exist
	_, err = os.Stat(state.SessionPath)
	assert.NoError(t, err, "recording session should not be removed by named abort")
}

func TestAbortByName_PartialNameResolution(t *testing.T) {
	cfg = &config.Config{}
	projectRoot := setupSessionTestProject(t)

	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(projectRoot))
	t.Cleanup(func() { os.Chdir(origDir) })

	// create an orphaned session, then abort via partial suffix
	_, sessionPath := makeOrphanSession(t, projectRoot, "OxPrtl")

	setForceFlag(t, true)

	inst := &agentinstance.Instance{AgentID: "OxTest"}
	err := runAgentSessionAbort(inst, agentCmd, []string{"OxPrtl"})
	require.NoError(t, err)

	_, err = os.Stat(sessionPath)
	assert.True(t, os.IsNotExist(err), "session should be removed via partial name")
}

func TestAbortByName_NotFound(t *testing.T) {
	cfg = &config.Config{}
	projectRoot := setupSessionTestProject(t)

	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(projectRoot))
	t.Cleanup(func() { os.Chdir(origDir) })

	setForceFlag(t, true)

	inst := &agentinstance.Instance{AgentID: "OxTest"}
	err := runAgentSessionAbort(inst, agentCmd, []string{"nonexistent-session"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session not found")
}

func TestAbortByName_RequiresForce(t *testing.T) {
	cfg = &config.Config{}
	projectRoot := setupSessionTestProject(t)

	origDir, _ := os.Getwd()
	require.NoError(t, os.Chdir(projectRoot))
	t.Cleanup(func() { os.Chdir(origDir) })

	_, sessionPath := makeOrphanSession(t, projectRoot, "OxFrce")

	cli.SetNoInteractive(true)
	t.Cleanup(func() { cli.SetNoInteractive(false) })

	inst := &agentinstance.Instance{AgentID: "OxTest"}
	err := runAgentSessionAbort(inst, agentCmd, []string{"2026-03-15T10-00-testuser-OxFrce"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "destructive")

	// session should still exist
	_, err = os.Stat(sessionPath)
	assert.NoError(t, err, "session should not be removed without --force")
}
