//go:build integration

// Tests for the hook → prime subprocess → session recording pipeline.
// These verify that firing SessionStart via ox agent hook correctly starts
// session recording with a valid ParentPID and no stderr warnings.
//
// Unlike most integration tests, these do NOT require a real Claude Code
// binary — they exercise the ox CLI subprocess chain directly.
package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/sageox/ox/tests/integration/agents/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHookSessionStart_RecordingState verifies that firing a SessionStart hook
// creates a .recording.json with a ParentPID that is actually alive.
//
// This is a regression test for a bug where:
//   - The hook runs inside a transient bash shell spawned by the agent
//   - os.Getppid() in any hook subprocess returns the bash PID, which dies immediately
//   - The session appears as orphan/ghost because the tracked PID is dead
//
// The fix uses proc.FindAgentAncestorPID() to walk the process tree and find the
// long-lived agent process (e.g., claude), rather than relying on os.Getppid().
func TestHookSessionStart_RecordingState(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := common.SetupTestEnvironment(t)

	// Step 1: run ox agent prime to bootstrap (creates agent instance, marker, etc.)
	primeOutput := runOxPrime(t, env)
	agentID := extractAgentID(t, primeOutput)
	require.NotEmpty(t, agentID, "prime should generate an agent_id")

	// Step 2: fire SessionStart hook (simulates what Claude Code does)
	// This internally spawns `ox agent prime` as a subprocess.
	sessionID := "test-session-" + agentID
	hookStdout, hookStderr := fireOxHookCaptureBoth(t, env, "SessionStart", sessionID)
	_ = hookStdout

	// Assertion 1: no "path cannot be empty" warning on stderr
	// This was Bug 1 — safety-net call used empty agentID because ctx.Marker
	// wasn't reloaded after prime subprocess.
	assert.NotContains(t, hookStderr, "path cannot be empty",
		"stderr should not contain 'path cannot be empty' warning — marker should be reloaded after prime")

	// Step 3: find .recording.json and verify ParentPID
	matches := findFilesRecursive(env.RootDir, ".recording.json")
	require.NotEmpty(t, matches, ".recording.json should exist after SessionStart hook")

	for _, path := range matches {
		data, err := os.ReadFile(path)
		require.NoError(t, err)

		var state struct {
			AgentID   string `json:"agent_id"`
			ParentPID int    `json:"parent_pid"`
		}
		require.NoError(t, json.Unmarshal(data, &state))

		if state.AgentID == "" {
			continue
		}

		t.Logf("recording state: agent_id=%s parent_pid=%d (from %s)", state.AgentID, state.ParentPID, path)

		// Assertion 2: ParentPID must be non-zero
		assert.Greater(t, state.ParentPID, 0,
			"recording should have a non-zero ParentPID")

		// Assertion 3: ParentPID must be alive (our test process, simulating Claude Code)
		// The hook's parent is the test process. With OX_PARENT_PID fix, prime
		// records the hook's parent (= test process) rather than the hook itself.
		// The test process is definitely alive — we're running it right now.
		if state.ParentPID > 0 {
			alive := isPIDAlive(state.ParentPID)
			assert.True(t, alive,
				"ParentPID %d should be alive — it should be the long-lived agent process, not the transient hook",
				state.ParentPID)
		}
	}
}

// TestHookSessionStart_NoStaleWarnings verifies that the hook → prime flow
// doesn't produce stderr warnings that would confuse the agent or user.
func TestHookSessionStart_NoStaleWarnings(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := common.SetupTestEnvironment(t)

	// prime first to get an agent ID
	_ = runOxPrime(t, env)

	sessionID := "test-warn-check"
	_, stderr := fireOxHookCaptureBoth(t, env, "SessionStart", sessionID)

	// no warning about empty agent ID
	assert.NotContains(t, stderr, "path cannot be empty",
		"no empty-path warning — marker reloaded after prime subprocess")

	// no warning about session recording failure
	assert.NotContains(t, stderr, "session recording failed to start",
		"session recording should succeed or be idempotent")
}

// fireOxHookCaptureBoth fires an ox agent hook and captures stdout and stderr separately.
// Unlike fireOxHook which uses CombinedOutput, this lets us assert on stderr warnings.
func fireOxHookCaptureBoth(t *testing.T, env *common.TestEnvironment, eventName, sessionID string) (string, string) {
	t.Helper()

	hookInput := map[string]interface{}{
		"session_id":      sessionID,
		"hook_event_name": eventName,
	}
	stdinData, err := json.Marshal(hookInput)
	require.NoError(t, err)

	cmd := exec.Command(env.OxBinaryPath, "agent", "hook", eventName)
	cmd.Dir = env.ProjectDir
	cmd.Env = append(env.EnvVars,
		"AGENT_ENV=claude-code",
		fmt.Sprintf("CLAUDE_CODE_SESSION_ID=%s", sessionID),
	)
	cmd.Stdin = bytes.NewReader(stdinData)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		t.Logf("hook %s exit error (may be ok): %v\nstdout: %s\nstderr: %s",
			eventName, err, stdout.String(), stderr.String())
	}

	return stdout.String(), stderr.String()
}

// TestHookSessionStart_ThenAfterTool_FindsRecording verifies the full lifecycle:
// SessionStart creates a recording, then PostToolUse (AfterTool) can find it.
// This catches path resolution mismatches between the two hook phases.
func TestHookSessionStart_ThenAfterTool_FindsRecording(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := common.SetupTestEnvironment(t)

	// Step 1: prime to bootstrap
	primeOutput := runOxPrime(t, env)
	agentID := extractAgentID(t, primeOutput)
	require.NotEmpty(t, agentID)

	// Step 2: fire SessionStart (creates recording)
	sessionID := "test-lifecycle-" + agentID
	_, stderr := fireOxHookCaptureBoth(t, env, "SessionStart", sessionID)
	assert.NotContains(t, stderr, "session recording failed to start")

	// Step 3: verify .recording.json exists
	recordings := findFilesRecursive(env.RootDir, ".recording.json")
	require.NotEmpty(t, recordings, ".recording.json must exist after SessionStart")

	// Step 4: fire PostToolUse (should find the recording and not error)
	_, afterToolStderr := fireOxHookCaptureBoth(t, env, "PostToolUse", sessionID)
	assert.NotContains(t, afterToolStderr, "no active session",
		"PostToolUse should find the recording created by SessionStart")
	assert.NotContains(t, afterToolStderr, "path cannot be empty",
		"PostToolUse should have a valid agent ID from marker")
}

// isPIDAlive checks if a process is alive using kill(pid, 0).
func isPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
