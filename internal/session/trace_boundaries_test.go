package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/trace/model"
	"github.com/stretchr/testify/require"
)

const traceBoundaryID = "8e6b4570-b823-4c19-a561-4511a6e89cad"
const traceBoundarySecondID = "0639f3f0-29ac-47a9-852c-cce42b108dfe"

func traceBoundaryEnvironment(t *testing.T, enabled bool) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("OX_XDG_DISABLE", "")
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "trace:\n  enabled: false\n"
	if enabled {
		content = "trace:\n  enabled: true\n"
	}
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))
	t.Setenv("OX_USER_CONFIG", path)
}

func TestTraceBoundariesOptInFrozenAtStart(t *testing.T) {
	for _, tc := range []struct {
		name, agent, adapter string
		enabled, want        bool
	}{
		{"opted-in", "claude-code", "claude-code", true, true},
		{"adapter-fallback", "", "claude-code", true, true},
		{"prime-adapter-alias", "", "claude", true, true},
		{"explicit-agent-alias", "claude", "claude-code", true, true},
		{"display-name", "Claude Code", "claude", true, true},
		{"alias-opted-out", "", "claude", false, false},
		{"opted-out", "claude-code", "claude-code", false, false},
		{"other-agent", "codex", "claude-code", true, false},
		{"other-agent-with-alias", "codex", "claude", true, false},
		{"other-adapter", "", "codex", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			traceBoundaryEnvironment(t, tc.enabled)
			state := &RecordingState{SessionPath: t.TempDir(), AgentType: tc.agent, AdapterName: tc.adapter, AgentSessionID: traceBoundaryID, StartedAt: time.Now()}
			state.initializeTraceCapture()
			require.Equal(t, tc.want, state.Trace != nil)
			if tc.want {
				require.Equal(t, "start", state.Trace.Boundaries[0].Action)
				require.Contains(t, state.Trace.Boundaries[0].Offsets, traceBoundaryID)
			}
		})
	}
}

func TestTraceBoundariesLifecycleAndCarrier(t *testing.T) {
	traceBoundaryEnvironment(t, true)
	now := time.Now().UTC()
	state := &RecordingState{SessionPath: t.TempDir(), AgentType: "claude-code", AgentSessionID: traceBoundaryID, StartedAt: now}
	state.RecordNativeSession(traceBoundaryID, "startup", now)
	state.initializeTraceCapture()
	dir := filepath.Join(paths.TraceSpoolDir(), traceBoundaryID)
	require.NoError(t, os.MkdirAll(dir, 0700))
	for i, action := range []string{"pause", "resume", "stop"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "traces.jsonl"), make([]byte, (i+1)*10), 0600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "logs.jsonl"), make([]byte, (i+1)*20), 0600))
		state.RecordTraceBoundary(action, now.Add(time.Duration(i+1)*time.Second))
		require.Equal(t, model.Offsets{Spans: int64((i + 1) * 10), Events: int64((i + 1) * 20)}, state.Trace.Boundaries[i+1].Offsets[traceBoundaryID])
	}
	state.RecordTraceBoundary("stop", now.Add(time.Hour))
	require.Len(t, state.Trace.Boundaries, 4, "retry must retain original stop offsets")
	raw := filepath.Join(t.TempDir(), "raw.jsonl")
	require.NoError(t, os.WriteFile(raw, []byte("{\"type\":\"header\",\"metadata\":{\"agent_type\":\"claude-code\"}}\n"), 0600))
	require.NoError(t, StampRawCarrier(raw, CarrierStamp{TraceCapture: state.Trace}))
	stored, err := ReadSessionFromPath(raw)
	require.NoError(t, err)
	require.NotNil(t, stored.Meta)
	require.Equal(t, state.Trace, stored.Meta.TraceCapture)
	data, err := json.Marshal(state)
	require.NoError(t, err)
	var roundtrip RecordingState
	require.NoError(t, json.Unmarshal(data, &roundtrip))
	require.Equal(t, state.Trace, roundtrip.Trace)
}

func TestTraceBoundariesNewNativeAndUnsafe(t *testing.T) {
	traceBoundaryEnvironment(t, true)
	now := time.Now().UTC()
	state := &RecordingState{SessionPath: t.TempDir(), AgentType: "claude-code", AgentSessionID: traceBoundaryID, StartedAt: now}
	state.RecordNativeSession(traceBoundaryID, "startup", now)
	state.initializeTraceCapture()
	state.RecordNativeSession(traceBoundaryID, "resume", now)
	require.Len(t, state.Trace.Boundaries, 1)
	state.RecordNativeSession(traceBoundarySecondID, "clear", now)
	require.Len(t, state.Trace.Boundaries, 2)
	require.Equal(t, "native-session", state.Trace.Boundaries[1].Action)
	require.Len(t, state.Trace.Boundaries[1].Offsets, 2)
	require.NoError(t, os.MkdirAll(paths.TraceSpoolDir(), 0700))
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(paths.TraceSpoolDir(), traceBoundaryID)))
	state.RecordTraceBoundary("pause", now)
	require.NotContains(t, state.Trace.Boundaries[2].Offsets, traceBoundaryID)
	require.Contains(t, state.Trace.Boundaries[2].Offsets, traceBoundarySecondID)
	require.Equal(t, []string{"boundary-unavailable:pause"}, state.Trace.Errors)
}

func TestTraceBoundariesStartRecordingPersistence(t *testing.T) {
	projectRoot := setupRecordingTest(t, t.TempDir())
	traceBoundaryEnvironment(t, true)
	source := filepath.Join(t.TempDir(), "source.jsonl")
	require.NoError(t, os.WriteFile(source, []byte("{}\n"), 0600))
	state, err := StartRecording(projectRoot, StartRecordingOptions{AgentID: "OxTrace", AdapterName: "claude-code", AgentSessionID: traceBoundaryID, SessionFile: source, Username: "test"})
	require.NoError(t, err)
	stored, err := ReadRecordingStateFile(state.SessionPath)
	require.NoError(t, err)
	require.NotNil(t, stored.Trace)
	require.Len(t, stored.Trace.Boundaries, 1)
	require.Equal(t, "start", stored.Trace.Boundaries[0].Action)
	require.Contains(t, stored.Trace.Boundaries[0].Offsets, traceBoundaryID)
	// Disabling affects future recordings; this recording retains its consent and
	// original boundaries so finalization can attach what was already captured.
	require.NoError(t, os.WriteFile(os.Getenv("OX_USER_CONFIG"), []byte("trace:\n  enabled: false\n"), 0600))
	stored.RecordTraceBoundary("stop", time.Now())
	require.Len(t, stored.Trace.Boundaries, 2)
}

func TestTraceBoundariesDoNotTrustRecoveredNativeIDs(t *testing.T) {
	traceBoundaryEnvironment(t, true)
	state := &RecordingState{SessionPath: t.TempDir(), AgentType: "claude-code", AgentSessionID: traceBoundaryID, StartedAt: time.Now()}
	state.initializeTraceCapture()
	state.AgentSessionID = traceBoundarySecondID
	state.NativeSessions = []NativeSession{{ID: traceBoundarySecondID}}
	state.RecordTraceBoundary("stop", time.Now())
	require.NotNil(t, state.Trace)
	require.Contains(t, state.Trace.Boundaries[1].Offsets, traceBoundaryID)
	require.NotContains(t, state.Trace.Boundaries[1].Offsets, traceBoundarySecondID)
}

// Trace authority lives only in the local capture. When that capture cannot
// be written, the recording must carry no trace state rather than unpersisted
// state a later materialize would have to trust.
func TestTraceBoundariesUnpersistableCaptureFailsClosed(t *testing.T) {
	t.Run("relative recording dir", func(t *testing.T) {
		traceBoundaryEnvironment(t, true)
		state := &RecordingState{SessionPath: "relative/session", AgentType: "claude-code", AgentSessionID: traceBoundaryID, StartedAt: time.Now()}
		state.initializeTraceCapture()
		require.Nil(t, state.Trace)
	})
	t.Run("cache unwritable at start", func(t *testing.T) {
		traceBoundaryEnvironment(t, true)
		blocker := filepath.Join(t.TempDir(), "not-a-dir")
		require.NoError(t, os.WriteFile(blocker, nil, 0600))
		t.Setenv("XDG_CACHE_HOME", blocker)
		state := &RecordingState{SessionPath: t.TempDir(), AgentType: "claude-code", AgentSessionID: traceBoundaryID, StartedAt: time.Now()}
		state.initializeTraceCapture()
		require.Nil(t, state.Trace)
	})
	t.Run("cache unwritable at a later boundary", func(t *testing.T) {
		traceBoundaryEnvironment(t, true)
		state := &RecordingState{SessionPath: t.TempDir(), AgentType: "claude-code", AgentSessionID: traceBoundaryID, StartedAt: time.Now()}
		state.initializeTraceCapture()
		require.NotNil(t, state.Trace)
		captures := filepath.Join(paths.CacheDir(), "trace", "recordings")
		require.NoError(t, os.RemoveAll(captures))
		require.NoError(t, os.WriteFile(captures, nil, 0600))
		state.recordTraceBoundary("stop", time.Now())
		require.Nil(t, state.Trace)
	})
}

func TestTraceBoundariesRetryNativeIDAfterFailedSnapshot(t *testing.T) {
	traceBoundaryEnvironment(t, true)
	now := time.Now().UTC()
	require.NoError(t, os.MkdirAll(paths.TraceSpoolDir(), 0700))
	unreadable := filepath.Join(paths.TraceSpoolDir(), traceBoundaryID)
	require.NoError(t, os.Symlink(t.TempDir(), unreadable))
	state := &RecordingState{SessionPath: t.TempDir(), AgentType: "claude-code", AgentSessionID: traceBoundaryID, StartedAt: now}
	state.initializeTraceCapture()
	require.NotContains(t, state.Trace.Boundaries[0].Offsets, traceBoundaryID)

	require.NoError(t, os.Remove(unreadable))
	state.RecordTraceBoundary("stop", now)
	require.Contains(t, state.Trace.Boundaries[1].Offsets, traceBoundaryID)
}

func TestTraceBoundariesPruneStaleAuthorizations(t *testing.T) {
	traceBoundaryEnvironment(t, true)
	stale, live := t.TempDir(), t.TempDir()
	require.NoError(t, writeLocalTraceCapture(stale, &model.Capture{}))
	require.NoError(t, writeLocalTraceCapture(live, &model.Capture{}))
	stalePath, err := localTraceCapturePath(stale)
	require.NoError(t, err)
	old := time.Now().Add(-localTraceAuthorizationMaxAge - time.Hour)
	require.NoError(t, os.Chtimes(stalePath, old, old))
	require.NoError(t, os.RemoveAll(stale))

	pruneLocalTraceAuthorizations(time.Now())

	_, err = readLocalTraceCapture(stale)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = readLocalTraceCapture(live)
	require.NoError(t, err)
}

func TestTraceBoundariesPruneKeepsPendingRecording(t *testing.T) {
	traceBoundaryEnvironment(t, true)
	pending := t.TempDir()
	require.NoError(t, writeLocalTraceCapture(pending, &model.Capture{}))
	path, err := localTraceCapturePath(pending)
	require.NoError(t, err)
	old := time.Now().Add(-localTraceAuthorizationMaxAge - time.Hour)
	require.NoError(t, os.Chtimes(path, old, old))

	pruneLocalTraceAuthorizations(time.Now())

	_, err = readLocalTraceCapture(pending)
	require.NoError(t, err, "a recording still on disk must keep its trace authorization")
}
