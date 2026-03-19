package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunHooksCommitMsg_SkipsMergeSource(t *testing.T) {
	prevSource, prevFile := hooksCommitMsgSource, hooksCommitMsgFile
	t.Cleanup(func() {
		hooksCommitMsgSource = prevSource
		hooksCommitMsgFile = prevFile
	})

	hooksCommitMsgSource = "merge"
	hooksCommitMsgFile = "/tmp/nonexistent"

	err := runHooksCommitMsg(nil, nil)
	assert.NoError(t, err)
}

func TestRunHooksCommitMsg_NoopWhenNotInitialized(t *testing.T) {
	prevSource, prevFile := hooksCommitMsgSource, hooksCommitMsgFile
	t.Cleanup(func() {
		hooksCommitMsgSource = prevSource
		hooksCommitMsgFile = prevFile
	})

	dir := t.TempDir()
	msgFile := filepath.Join(dir, "COMMIT_EDITMSG")
	require.NoError(t, os.WriteFile(msgFile, []byte("test commit\n"), 0644))

	// save and restore cwd
	origDir, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(origDir) })
	require.NoError(t, os.Chdir(dir))

	hooksCommitMsgSource = ""
	hooksCommitMsgFile = msgFile

	err = runHooksCommitMsg(nil, nil)
	assert.NoError(t, err)

	// message should be unchanged
	content, err := os.ReadFile(msgFile)
	require.NoError(t, err)
	assert.Equal(t, "test commit\n", string(content))
}

func TestBuildSessionURL(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *config.ProjectConfig
		sessionName string
		expected    string
	}{
		{
			name:        "valid URL",
			cfg:         &config.ProjectConfig{RepoID: "repo_01abc", Endpoint: "https://sageox.ai"},
			sessionName: "2026-01-06T14-32-ryan-Ox7f3a",
			expected:    "https://sageox.ai/repo/repo_01abc/sessions/2026-01-06T14-32-ryan-Ox7f3a/view",
		},
		{
			name:        "empty repo ID",
			cfg:         &config.ProjectConfig{RepoID: "", Endpoint: "https://sageox.ai"},
			sessionName: "session-1",
			expected:    "",
		},
		{
			name:        "empty session name",
			cfg:         &config.ProjectConfig{RepoID: "repo_01abc", Endpoint: "https://sageox.ai"},
			sessionName: "",
			expected:    "",
		},
		{
			name:        "nil config",
			cfg:         nil,
			sessionName: "test",
			expected:    "",
		},
		{
			name:        "normalizes www prefix",
			cfg:         &config.ProjectConfig{RepoID: "repo_01abc", Endpoint: "https://www.sageox.ai"},
			sessionName: "session-1",
			expected:    "https://sageox.ai/repo/repo_01abc/sessions/session-1/view",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildSessionURL(tt.cfg, tt.sessionName)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestSessionLookupUsesAgentID verifies that when SAGEOX_AGENT_ID is set,
// LoadRecordingStateForAgent is used instead of the first-found LoadRecordingState.
// This prevents linking the wrong session when multiple agents record concurrently.
func TestSessionLookupUsesAgentID(t *testing.T) {
	projectRoot := t.TempDir()
	sessionsDir := filepath.Join(projectRoot, "sessions")

	// isolate from real XDG/cache paths so only projectRoot/sessions is searched
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	// create two concurrent recording states (agent A and agent B)
	agentASession := "2026-03-19T10-00-user-OxAAAA"
	agentBSession := "2026-03-19T10-01-user-OxBBBB"

	for _, tc := range []struct {
		sessionName string
		agentID     string
	}{
		{agentASession, "OxAAAA"},
		{agentBSession, "OxBBBB"},
	} {
		dir := filepath.Join(sessionsDir, tc.sessionName)
		require.NoError(t, os.MkdirAll(dir, 0755))
		state := session.RecordingState{
			AgentID:     tc.agentID,
			StartedAt:   time.Now(),
			SessionPath: dir,
		}
		data, err := json.Marshal(state)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".recording.json"), data, 0644))
	}

	// with SAGEOX_AGENT_ID=OxBBBB, should find agent B's session specifically
	t.Setenv("SAGEOX_AGENT_ID", "OxBBBB")
	state, err := session.LoadRecordingStateForAgent(projectRoot, "OxBBBB")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, "OxBBBB", state.AgentID)
	assert.Contains(t, state.SessionPath, agentBSession)

	// with SAGEOX_AGENT_ID=OxAAAA, should find agent A's session
	t.Setenv("SAGEOX_AGENT_ID", "OxAAAA")
	state, err = session.LoadRecordingStateForAgent(projectRoot, "OxAAAA")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, "OxAAAA", state.AgentID)
	assert.Contains(t, state.SessionPath, agentASession)

	// without SAGEOX_AGENT_ID, first-found returns one of them (non-deterministic order)
	t.Setenv("SAGEOX_AGENT_ID", "")
	state, err = session.LoadRecordingState(projectRoot)
	require.NoError(t, err)
	require.NotNil(t, state, "first-found should return some recording")
}

// TestSessionLookupFallbackWithoutAgentID verifies that when SAGEOX_AGENT_ID
// is not set (human git commit), workspace-based matching finds the correct session.
func TestSessionLookupFallbackWithoutAgentID(t *testing.T) {
	projectRoot := t.TempDir()
	sessionsDir := filepath.Join(projectRoot, "sessions")

	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	// create a single recording state with WorkspacePath set
	sessionName := "2026-03-19T10-00-user-OxTest"
	dir := filepath.Join(sessionsDir, sessionName)
	require.NoError(t, os.MkdirAll(dir, 0755))
	state := session.RecordingState{
		AgentID:       "OxTest",
		StartedAt:     time.Now(),
		SessionPath:   dir,
		WorkspacePath: projectRoot,
	}
	data, err := json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".recording.json"), data, 0644))

	// no agent ID set — should find via workspace match
	t.Setenv("SAGEOX_AGENT_ID", "")
	found, err := session.LoadRecordingStateForWorkspace(projectRoot, projectRoot)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "OxTest", found.AgentID)
}

// TestSessionLookupByWorkspace_IgnoresOtherWorktrees verifies that workspace
// matching does NOT pick up sessions from a different worktree.
func TestSessionLookupByWorkspace_IgnoresOtherWorktrees(t *testing.T) {
	projectRoot := t.TempDir()
	sessionsDir := filepath.Join(projectRoot, "sessions")

	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	// create two recordings: one for this workspace, one for a different worktree
	for _, tc := range []struct {
		sessionName   string
		agentID       string
		workspacePath string
	}{
		{"2026-03-19T10-00-user-OxHERE", "OxHERE", projectRoot},
		{"2026-03-19T10-01-user-OxOTHR", "OxOTHR", "/some/other/worktree"},
	} {
		dir := filepath.Join(sessionsDir, tc.sessionName)
		require.NoError(t, os.MkdirAll(dir, 0755))
		st := session.RecordingState{
			AgentID:       tc.agentID,
			StartedAt:     time.Now(),
			SessionPath:   dir,
			WorkspacePath: tc.workspacePath,
		}
		data, err := json.Marshal(st)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".recording.json"), data, 0644))
	}

	// workspace match should return OxHERE, not OxOTHR
	found, err := session.LoadRecordingStateForWorkspace(projectRoot, projectRoot)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "OxHERE", found.AgentID)
}

// TestSessionLookupByWorkspace_SymlinkResolution verifies that workspace
// matching resolves symlinks before comparing. On macOS, /tmp → /private/tmp
// causes findProjectRoot and recording start to store different path forms.
func TestSessionLookupByWorkspace_SymlinkResolution(t *testing.T) {
	// create a real directory and a symlink to it
	realDir := t.TempDir()
	symlinkParent := t.TempDir()
	symlinkPath := filepath.Join(symlinkParent, "linked-workspace")
	require.NoError(t, os.Symlink(realDir, symlinkPath))

	sessionsDir := filepath.Join(realDir, "sessions")

	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	// recording was saved with the symlink path (as findProjectRoot might return)
	sessionName := "2026-03-19T10-00-user-OxSYML"
	dir := filepath.Join(sessionsDir, sessionName)
	require.NoError(t, os.MkdirAll(dir, 0755))
	st := session.RecordingState{
		AgentID:       "OxSYML",
		StartedAt:     time.Now(),
		SessionPath:   dir,
		WorkspacePath: symlinkPath, // stored via symlink
	}
	data, err := json.Marshal(st)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".recording.json"), data, 0644))

	// lookup uses the resolved real path (as another call to findProjectRoot might return)
	found, err := session.LoadRecordingStateForWorkspace(realDir, realDir)
	require.NoError(t, err)
	require.NotNil(t, found, "should match despite symlink vs real path difference")
	assert.Equal(t, "OxSYML", found.AgentID)

	// and the reverse: stored as real, looked up via symlink
	st.WorkspacePath = realDir
	data, err = json.Marshal(st)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".recording.json"), data, 0644))

	found, err = session.LoadRecordingStateForWorkspace(realDir, symlinkPath)
	require.NoError(t, err)
	require.NotNil(t, found, "should match despite real vs symlink path difference")
	assert.Equal(t, "OxSYML", found.AgentID)
}
