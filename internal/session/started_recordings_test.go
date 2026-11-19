package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startForProvenance starts a hook-mode Claude Code recording the way prime
// does, with the ox data directory (where the registry lives) isolated.
func startForProvenance(t *testing.T, agentID string) (projectRoot string, state *RecordingState) {
	t.Helper()
	projectRoot, _ = createTestSessionProject(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	state, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID: agentID, AdapterName: "claude-code", Username: "person-a", WatchMode: "hook", WorkspacePath: projectRoot,
	})
	require.NoError(t, err)
	return projectRoot, state
}

// plantMarker writes a recording marker into dir, as a pull of the Ledger
// checkout or the repository's legacy sessions/ folder could.
func plantMarker(t *testing.T, dir string, fields map[string]any) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	data, err := json.Marshal(fields)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, recordingFile), data, 0o600))
}

// plantedBeside is a folder in the live recording's own sessions folder whose
// name sorts ahead of it, so a first-match lookup meets the plant first.
func plantedBeside(live *RecordingState) string {
	return filepath.Join(filepath.Dir(live.SessionPath), "0000-planted")
}

func markerIn(t *testing.T, markers []RecordingMarker, dir string) RecordingMarker {
	t.Helper()
	for _, mk := range markers {
		if canonicalPath(mk.Dir) == canonicalPath(dir) {
			return mk
		}
	}
	require.Failf(t, "no marker found", "dir %s", dir)
	return RecordingMarker{}
}

// Failure prevented: the daemon's hook catch-up reads the transcript a marker
// names and appends it to raw.jsonl. Markers live where a repository writer
// can plant one, and its content (session_path, session_id, agent_id) is the
// writer's to choose. Only a recording ox started here, found in the folder
// it was started in, counts as started.
func TestRecordingMarkersOnlyTrustWhatOxStartedHere(t *testing.T) {
	tests := []struct {
		name string
		// arrange returns the folder whose marker must not count as started
		arrange     func(t *testing.T, live *RecordingState) string
		liveTrusted bool
	}{
		{
			name: "planted under the live agent's ID",
			arrange: func(t *testing.T, live *RecordingState) string {
				dir := plantedBeside(live)
				plantMarker(t, dir, map[string]any{
					"agent_id": live.AgentID, "session_id": "ses_planted", "session_path": dir,
					"adapter_name": "claude-code", "watch_mode": "hook",
				})
				return dir
			},
			liveTrusted: true,
		},
		{
			name: "forged: names the live recording's folder and IDs",
			arrange: func(t *testing.T, live *RecordingState) string {
				dir := plantedBeside(live)
				plantMarker(t, dir, map[string]any{
					"agent_id": live.AgentID, "session_id": live.SessionID, "session_path": live.SessionPath,
					"adapter_name": "claude-code", "watch_mode": "hook",
				})
				return dir
			},
			liveTrusted: true,
		},
		{
			name: "re-planted in the folder after the recording ended",
			arrange: func(t *testing.T, live *RecordingState) string {
				require.NoError(t, ClearRecordingStateAt(live.SessionPath, live.SessionID))
				plantMarker(t, live.SessionPath, map[string]any{
					"agent_id": live.AgentID, "session_id": live.SessionID, "session_path": live.SessionPath,
					"adapter_name": "claude-code", "watch_mode": "hook",
				})
				return live.SessionPath
			},
		},
		{
			// everything after the catch-up's check would act on the folder
			// the content names, not the one that was checked
			name: "rewritten in place to name another folder",
			arrange: func(t *testing.T, live *RecordingState) string {
				plantMarker(t, live.SessionPath, map[string]any{
					"agent_id": live.AgentID, "session_id": live.SessionID, "session_path": plantedBeside(live),
					"adapter_name": "claude-code", "watch_mode": "hook",
				})
				return live.SessionPath
			},
		},
		{
			name: "started by an ox that kept no registry",
			arrange: func(t *testing.T, live *RecordingState) string {
				t.Setenv("XDG_DATA_HOME", t.TempDir())
				return live.SessionPath
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectRoot, live := startForProvenance(t, "OxProv")
			untrusted := tt.arrange(t, live)

			markers, err := LoadRecordingMarkers(projectRoot)
			require.NoError(t, err)

			assert.False(t, markerIn(t, markers, untrusted).Started, "a marker ox did not start in its folder counts as started")
			if tt.liveTrusted {
				assert.True(t, markerIn(t, markers, live.SessionPath).Started, "the recording ox started is not trusted")
			}
		})
	}
}

// Failure prevented: the hooks and the catch-up's capture find an agent's
// recording by agent ID. A planted marker sorted ahead of the live one, under
// the same agent ID (and even the same session ID and folder), would be the
// recording they read from and append to.
func TestLoadRecordingStateForAgentPrefersTheRecordingStartedHere(t *testing.T) {
	plants := map[string]func(live *RecordingState, dir string) map[string]any{
		"its own folder and session": func(live *RecordingState, dir string) map[string]any {
			return map[string]any{"agent_id": live.AgentID, "session_id": "ses_planted", "session_path": dir, "session_file": "/planted.jsonl"}
		},
		"the live recording's folder and session": func(live *RecordingState, _ string) map[string]any {
			return map[string]any{"agent_id": live.AgentID, "session_id": live.SessionID, "session_path": live.SessionPath, "session_file": "/planted.jsonl"}
		},
	}
	for name, plant := range plants {
		t.Run(name, func(t *testing.T) {
			projectRoot, live := startForProvenance(t, "OxPrefer")
			dir := plantedBeside(live)
			plantMarker(t, dir, plant(live, dir))

			got, err := LoadRecordingStateForAgent(projectRoot, live.AgentID)
			require.NoError(t, err)
			require.NotNil(t, got)

			assert.Equal(t, live.SessionID, got.SessionID)
			assert.Equal(t, live.SessionFile, got.SessionFile, "the planted marker was returned as the agent's recording")
		})
	}
}

// Ghost cleanup and orphan sweeps remove session folders without clearing
// their recordings; the registry must not grow with every one of them.
func TestStartedRecordingRegistryDropsFoldersThatAreGone(t *testing.T) {
	projectRoot, gone := startForProvenance(t, "OxGone")
	require.NoError(t, os.RemoveAll(gone.SessionPath))

	kept, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID: "OxKept", AdapterName: "claude-code", Username: "person-a", WatchMode: "hook", WorkspacePath: projectRoot,
	})
	require.NoError(t, err)

	list, err := readStartedRecordings()
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, kept.SessionID, list[0].SessionID)
}

// A registry that cannot be read proves nothing was started here, so the
// daemon gets an error and catches nothing up. The agent's own lookup, which
// every hook makes, never depends on it.
func TestUnreadableStartedRecordingRegistryFailsClosed(t *testing.T) {
	projectRoot, live := startForProvenance(t, "OxCorrupt")
	require.NoError(t, os.WriteFile(startedRecordingsPath(), []byte("{not json"), 0o600))

	_, err := LoadRecordingMarkers(projectRoot)
	require.Error(t, err)

	got, err := LoadRecordingStateForAgent(projectRoot, live.AgentID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, live.SessionID, got.SessionID)

	// starting a recording never fails for want of a registry entry: only its
	// catch-up is lost, never the recording
	_, err = StartRecording(projectRoot, StartRecordingOptions{
		AgentID: "OxCorrupt2", AdapterName: "claude-code", Username: "person-a", WatchMode: "hook", WorkspacePath: projectRoot,
	})
	require.NoError(t, err)
}

// Failure prevented: the catch-up reads only the transcript a local hook found
// for the recording. A first discovery binds it once, so a marker emptied and
// rediscovered against another session cannot move it; only a hook's
// rediscovery of a replaced transcript does. A recording ox did not start is
// never given a binding.
func TestTranscriptBindingIsSetOnceAndMovedOnlyByRediscovery(t *testing.T) {
	projectRoot, live := startForProvenance(t, "OxBind")
	first, second := filepath.Join(t.TempDir(), "first.jsonl"), filepath.Join(t.TempDir(), "second.jsonl")
	for _, p := range []string{first, second} {
		require.NoError(t, os.WriteFile(p, nil, 0o600))
	}
	source := func() string {
		t.Helper()
		markers, err := LoadRecordingMarkers(projectRoot)
		require.NoError(t, err)
		return markerIn(t, markers, live.SessionPath).Source
	}
	require.Empty(t, source(), "a transcript is bound before any hook found one")

	require.NoError(t, bindStartedRecordingSource(live.SessionPath, live.SessionID, first, false))
	assert.Equal(t, canonicalPath(first), source())

	require.NoError(t, bindStartedRecordingSource(live.SessionPath, live.SessionID, second, false))
	assert.Equal(t, canonicalPath(first), source(), "a second discovery moved the binding")

	require.NoError(t, bindStartedRecordingSource(live.SessionPath, live.SessionID, second, true))
	assert.Equal(t, canonicalPath(second), source(), "a rediscovery did not move the binding")

	require.NoError(t, bindStartedRecordingSource(live.SessionPath, "ses_not_started_here", first, true))
	list, err := readStartedRecordings()
	require.NoError(t, err)
	require.Len(t, list, 1, "a recording ox did not start was given an entry")
	assert.Equal(t, live.SessionID, list[0].SessionID)
}
