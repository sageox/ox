package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hooks_post_commit_test.go verifies the reverse-index append path: each
// commit during an active recording lands in RecordingState.ProducedCommits.
// Failure prevented: silent regression of the session→commits index that
// `ox session view` Commits section depends on.

func postCommitEnv(t *testing.T) (string, string, string) {
	t.Helper()
	projectRoot, sessionName, agentID := trailerRewriteEnv(t)
	return projectRoot, sessionName, agentID
}

// TestPostCommitHook_AppendsHEADToActiveRecording verifies that, with an
// active recording, runHooksPostCommit appends HEAD's SHA to
// state.ProducedCommits exactly once per call.
func TestPostCommitHook_AppendsHEADToActiveRecording(t *testing.T) {
	projectRoot, sessionName, agentID := postCommitEnv(t)

	sha := commitWithTrailer(t, projectRoot, "first")
	require.NoError(t, runHooksPostCommit(nil, nil))

	state := loadRecordingForTest(t, projectRoot, sessionName)
	require.Equal(t, []string{sha}, state.ProducedCommits,
		"first post-commit must record HEAD sha exactly once")

	// idempotency: re-running on the same HEAD must NOT duplicate
	require.NoError(t, runHooksPostCommit(nil, nil))
	state = loadRecordingForTest(t, projectRoot, sessionName)
	assert.Equal(t, []string{sha}, state.ProducedCommits,
		"running post-commit twice on the same HEAD must not duplicate")

	// second commit appends a second entry
	sha2 := commitWithTrailer(t, projectRoot, "second")
	require.NoError(t, runHooksPostCommit(nil, nil))
	state = loadRecordingForTest(t, projectRoot, sessionName)
	assert.Equal(t, []string{sha, sha2}, state.ProducedCommits)

	_ = agentID
}

// TestPostCommitHook_NoopWithoutRecording verifies the handler exits 0
// and writes nothing when no recording is active. The hook must NEVER
// block a commit.
func TestPostCommitHook_NoopWithoutRecording(t *testing.T) {
	projectRoot, _, agentID := postCommitEnv(t)
	require.NoError(t, session.ClearRecordingStateForAgent(projectRoot, agentID))
	t.Setenv("SAGEOX_AGENT_ID", "")

	commitWithoutTrailer(t, projectRoot, "no recording")
	assert.NoError(t, runHooksPostCommit(nil, nil),
		"post-commit must never error when there is no active recording")
}

// loadRecordingForTest reads the recording state JSON from disk.
// Returns dereferenced struct so tests can compare field-by-field.
func loadRecordingForTest(t *testing.T, projectRoot, sessionName string) session.RecordingState {
	t.Helper()
	path := filepath.Join(projectRoot, "sessions", sessionName, ".recording.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var s session.RecordingState
	require.NoError(t, json.Unmarshal(data, &s))
	return s
}

// (anchor to silence unused import on platforms where time/json aren't otherwise used)
var _ = time.Now
