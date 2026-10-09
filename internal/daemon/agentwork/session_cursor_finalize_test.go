package agentwork

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/cursorpaths"
	"github.com/sageox/ox/internal/trace/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cursorFinalizeConversationID = "123e4567-e89b-12d3-a456-426614174066"

func TestDetectOrphanedForAgent_CursorWaitsForNativeStop(t *testing.T) {
	ledgerPath := t.TempDir()
	sessionsDir := filepath.Join(ledgerPath, ".sageox", "cache", "sessions")
	handler := NewSessionFinalizeHandlerForTest(slog.New(slog.DiscardHandler))
	deadHeartbeatPID := 99999999

	newCursorRecording := func(t *testing.T, name string, stopped bool) (string, string) {
		t.Helper()
		baseDir := sessionsDir
		if stopped {
			// Ledger-resident raw data is already finalized capture input, so this
			// exercises agent-exit eligibility without needing a native adapter.
			baseDir = filepath.Join(ledgerPath, "sessions")
		}
		sessionDir := filepath.Join(baseDir, name)
		require.NoError(t, os.MkdirAll(sessionDir, 0o700))
		state := session.RecordingState{
			AgentID: "OxCursorExit", AdapterName: "cursor", SessionPath: sessionDir,
			ParentPID: 0, StartedAt: time.Now().Add(-time.Hour),
		}
		if stopped {
			now := time.Now().UTC()
			state.StoppedAt = &now
		}
		recPath := filepath.Join(sessionDir, recordingMarker)
		writeRecordingState(t, recPath, state)
		rawPath := filepath.Join(sessionDir, artifactRaw)
		require.NoError(t, os.WriteFile(rawPath, []byte(`{"_meta":{"agent_type":"cursor"}}`+"\n"+`{"type":"assistant","content":"captured"}`+"\n"), 0o600))
		return recPath, rawPath
	}

	t.Run("dead transient hook process does not finalize active conversation", func(t *testing.T) {
		recPath, rawPath := newCursorRecording(t, "active-cursor", false)
		before, err := os.ReadFile(rawPath)
		require.NoError(t, err)

		assert.Empty(t, handler.DetectOrphanedForAgent(ledgerPath, "OxCursorExit", deadHeartbeatPID))
		assert.FileExists(t, recPath)
		after, err := os.ReadFile(rawPath)
		require.NoError(t, err)
		assert.Equal(t, before, after)
	})

	t.Run("native stop remains immediately eligible", func(t *testing.T) {
		recPath, _ := newCursorRecording(t, "stopped-cursor", true)
		items := handler.DetectOrphanedForAgent(ledgerPath, "OxCursorExit", deadHeartbeatPID)
		require.Len(t, items, 1)
		assert.Contains(t, items[0].DedupKey, "stopped-cursor")
		assert.NoFileExists(t, recPath)
	})
}

func TestRecoverRawFromSessionFile_CursorPendingThenFinalized(t *testing.T) {
	installCursorWatcherAdapter(t)
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("HOME", home)
	projectRoot := t.TempDir()
	sessionDir := t.TempDir()
	source, err := cursorpaths.SessionPath(home, projectRoot, cursorFinalizeConversationID)
	require.NoError(t, err)
	recPath := filepath.Join(sessionDir, recordingMarker)
	rawPath := filepath.Join(sessionDir, artifactRaw)
	stoppedAt := time.Now().UTC()
	nativeSeenAt := stoppedAt.Add(-time.Minute)
	traceCapture := &model.Capture{Boundaries: []model.Boundary{{
		Action: "stop", At: stoppedAt,
		Offsets: map[string]model.Offsets{cursorFinalizeConversationID: {Spans: 12, Events: 34}},
	}}}
	state := session.RecordingState{
		AgentID: "OxCursorFinalize", AgentSessionID: cursorFinalizeConversationID,
		AdapterName: "cursor", WatchMode: "tail", WorkspacePath: projectRoot,
		SessionPath: sessionDir, ParentPID: 99999999, StartOffsetKnown: true,
		SourcePrefixSHA256: watcherSHA256(nil), StartedAt: time.Now().Add(-time.Hour),
		StoppedAt: &stoppedAt, Trace: traceCapture,
		NativeSessions: []lfs.NativeSession{{
			ID: cursorFinalizeConversationID, Source: "cursor",
			FirstSeen: nativeSeenAt, LastSeen: stoppedAt,
		}},
	}
	writeRecordingState(t, recPath, state)

	recovered, err := recoverRawFromSessionFile(slog.New(slog.DiscardHandler), recPath, sessionDir, rawPath)
	require.ErrorIs(t, err, session.ErrCursorSourcePending)
	assert.False(t, recovered)
	assert.FileExists(t, recPath, "pending native export remains available to detector retry")
	assert.NoFileExists(t, rawPath)

	sourceBody := `{"role":"user","content":"late prompt"}` + "\n" +
		`{"role":"assistant","content":"late response"}` + "\n" +
		`{"type":"turn_ended","status":"success"}` + "\n"
	require.NoError(t, os.MkdirAll(filepath.Dir(source), 0o755))
	require.NoError(t, os.WriteFile(source, []byte(sourceBody), 0o600))
	recovered, err = recoverRawFromSessionFile(slog.New(slog.DiscardHandler), recPath, sessionDir, rawPath)
	require.NoError(t, err)
	assert.True(t, recovered)
	assert.FileExists(t, recPath, "caller owns marker cleanup after finalized native recovery")
	raw, readErr := os.ReadFile(rawPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(raw), `"cursor_finalized":true`)
	assert.Contains(t, string(raw), "late prompt")
	assert.Contains(t, string(raw), "late response")
	stored, err := session.ReadSessionFromPath(rawPath)
	require.NoError(t, err)
	require.NotNil(t, stored.Meta)
	assert.Len(t, stored.Entries, 2)
	assert.Equal(t, state.NativeSessions, stored.Meta.NativeSessions)
	require.NotNil(t, stored.Meta.StoppedAt)
	assert.Equal(t, stoppedAt, *stored.Meta.StoppedAt)
	assert.Equal(t, traceCapture, stored.Meta.TraceCapture)

	// The cursor-finalized marker makes retry safe even if the native source
	// gains rows before the detector's caller removes .recording.json.
	before := append([]byte(nil), raw...)
	require.NoError(t, os.WriteFile(source, append([]byte(sourceBody), []byte(`{"role":"assistant","content":"must not replay"}`+"\n")...), 0o600))
	recovered, err = recoverRawFromSessionFile(slog.New(slog.DiscardHandler), recPath, sessionDir, rawPath)
	require.NoError(t, err)
	assert.True(t, recovered)
	after, readErr := os.ReadFile(rawPath)
	require.NoError(t, readErr)
	assert.Equal(t, before, after)
}

func TestIsStaleRecording_CursorUsesNativeEndAndInactivityNotPID(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stopped    bool
		lastHookAt time.Time
		wantStale  bool
		wantMethod string
	}{
		{
			name: "stopped cursor is immediately finalizable despite live pid", stopped: true,
			lastHookAt: time.Now(), wantStale: true, wantMethod: "native_end_pending",
		},
		{
			name: "recent hook keeps cursor pending despite dead pid", lastHookAt: time.Now(),
			wantStale: false, wantMethod: "cursor_inactivity",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			recPath := filepath.Join(dir, recordingMarker)
			state := map[string]any{
				"adapter_name": "cursor", "agent_id": "OxCursorStale", "parent_pid": 99999999,
				"started_at": time.Now().UTC(), "last_hook_at": tc.lastHookAt.UTC(),
			}
			if tc.stopped {
				state["stopped_at"] = time.Now().UTC()
			}
			data, err := json.Marshal(state)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(recPath, data, 0o600))
			info, err := os.Stat(recPath)
			require.NoError(t, err)
			stale, _, method := isStaleRecording(recPath, info, func(string) int { return os.Getpid() }, slog.New(slog.DiscardHandler))
			assert.Equal(t, tc.wantStale, stale)
			assert.Equal(t, tc.wantMethod, method)
		})
	}
}

func TestRecoverRawFromSessionFile_CursorIncompleteTerminalDefers(t *testing.T) {
	installCursorWatcherAdapter(t)
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("HOME", home)
	projectRoot := t.TempDir()
	sessionDir := t.TempDir()
	source, err := cursorpaths.SessionPath(home, projectRoot, cursorFinalizeConversationID)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(source), 0o755))
	require.NoError(t, os.WriteFile(source, []byte(`{"role":"user","content":"unfinished"}`+"\n"), 0o600))
	recPath := filepath.Join(sessionDir, recordingMarker)
	writeRecordingState(t, recPath, session.RecordingState{
		AgentID: "OxCursorIncomplete", AgentSessionID: cursorFinalizeConversationID,
		AdapterName: "cursor", WatchMode: "tail", WorkspacePath: projectRoot, SessionPath: sessionDir,
		SessionFile: source, StartOffsetKnown: true, SourcePrefixSHA256: watcherSHA256(nil),
	})

	recovered, err := recoverRawFromSessionFile(slog.New(slog.DiscardHandler), recPath, sessionDir, filepath.Join(sessionDir, artifactRaw))
	require.ErrorIs(t, err, session.ErrCursorFinalDrainPending)
	assert.False(t, recovered)
	assert.FileExists(t, recPath)
	assert.Equal(t, 1, countRawJSONLEntries(t, filepath.Join(sessionDir, artifactRaw)), "partial final drain is retained")
	assert.False(t, errors.Is(err, session.ErrNotRecording))
}
