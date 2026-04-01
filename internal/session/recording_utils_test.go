package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetSessionName(t *testing.T) {
	t.Run("extracts session name from path", func(t *testing.T) {
		tests := []struct {
			sessionPath string
			expected    string
		}{
			{"/path/to/sessions/2026-01-06T14-30-user-OxA1b2", "2026-01-06T14-30-user-OxA1b2"},
			{"/path/to/sessions/2026-01-06T14-30-user-OxA1b2/", "2026-01-06T14-30-user-OxA1b2"},
			{"2026-01-06T14-30-user-OxA1b2", "2026-01-06T14-30-user-OxA1b2"},
		}

		for _, tc := range tests {
			result := GetSessionName(tc.sessionPath)
			assert.Equal(t, tc.expected, result, "GetSessionName(%q)", tc.sessionPath)
		}
	})
}

// TestCrossEnvRecordingStateRoundTrip verifies that a recording state written
// with one XDG_CACHE_HOME can be found by a process with a different XDG_CACHE_HOME.
// This is the exact scenario that caused the split-path bug: Conductor GUI (no
// XDG_CACHE_HOME) creates recording state in ~/.cache/..., but terminal hooks
// (XDG_CACHE_HOME=~/Library/Caches) couldn't find it.
//
// Derives the effective home from paths.CacheDir() rather than os.UserHomeDir()
// because paths.getHomeDir() is cached via sync.Once — other tests that set HOME
// to temp dirs can cause the cached value to diverge from os.UserHomeDir().
func TestCrossEnvRecordingStateRoundTrip(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS-specific cache path test")
	}

	repoID := "repo-crossenv-test-ephemeral"
	agentID := "OxCrossEnv1"

	// derive the effective home that paths package uses (may differ from
	// os.UserHomeDir() due to sync.Once caching in getHomeDir())
	t.Setenv("XDG_CACHE_HOME", "")
	cacheDir := paths.CacheDir() // <cachedHome>/.cache/sageox
	effectiveHome := filepath.Dir(filepath.Dir(cacheDir))

	// create a project root with .sageox/config.json
	projectRoot := t.TempDir()
	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	configJSON := `{"repo_id":"` + repoID + `"}`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(configJSON), 0600))

	// place recording state in <effectiveHome>/.cache/sageox/sessions/<repoID>/sessions/<name>/
	// this simulates Conductor (no XDG_CACHE_HOME → defaults to ~/.cache)
	cacheSessionsDir := filepath.Join(effectiveHome, ".cache", "sageox", "sessions", repoID, "sessions")
	sessionName := "2026-03-18T16-00-user-" + agentID
	sessionPath := filepath.Join(cacheSessionsDir, sessionName)
	require.NoError(t, os.MkdirAll(sessionPath, 0755))
	t.Cleanup(func() {
		os.RemoveAll(filepath.Join(effectiveHome, ".cache", "sageox", "sessions", repoID))
		os.RemoveAll(filepath.Join(effectiveHome, "Library", "Caches", "sageox", "sessions", repoID))
	})

	state := &RecordingState{
		AgentID:     agentID,
		StartedAt:   time.Now().UTC(),
		AdapterName: "claude-code",
		SessionPath: sessionPath,
		CacheDir:    filepath.Join(effectiveHome, ".cache", "sageox"),
	}
	data, marshalErr := json.MarshalIndent(state, "", "  ")
	require.NoError(t, marshalErr)
	require.NoError(t, os.WriteFile(filepath.Join(sessionPath, ".recording.json"), data, 0600))

	// simulate terminal shell environment (XDG_CACHE_HOME=~/Library/Caches)
	// — SessionCacheDir will now resolve to ~/Library/Caches/sageox/sessions/<repoID>
	// — but the recording state lives in ~/.cache/sageox/sessions/<repoID>/sessions/
	t.Setenv("XDG_CACHE_HOME", filepath.Join(effectiveHome, "Library", "Caches"))

	found, loadErr := LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, loadErr)
	require.NotNil(t, found, "recording state should be found via alternate cache dir scan")
	assert.Equal(t, agentID, found.AgentID)
	assert.Equal(t, filepath.Join(effectiveHome, ".cache", "sageox"), found.CacheDir,
		"CacheDir should reflect the original creating process's cache dir")
}
