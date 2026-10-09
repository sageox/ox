package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/session/cursorpaths"
	"github.com/sageox/ox/internal/session/pipeline"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newCursorManualStartFixture(t *testing.T) (*cursorHostFixture, *agentinstance.Instance) {
	t.Helper()
	f := newCursorHostFixture(t)
	t.Setenv("SAGEOX_DAEMON", "false")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(http.NotFound))
	t.Cleanup(server.Close)
	t.Setenv("SAGEOX_ENDPOINT", server.URL)
	project, err := config.LoadProjectConfig(f.root)
	require.NoError(t, err)
	project.Endpoint = server.URL
	require.NoError(t, config.SaveProjectConfig(f.root, project))
	require.Nil(t, daemon.TryConnect(), "manual tests must not contact a live daemon")
	inst := &agentinstance.Instance{
		AgentID: "Oxmanual", AgentType: "cursor",
		ServerSessionID: "oxsid_cursor_manual_test",
		CreatedAt:       time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}
	store, err := getInstanceStore(f.root)
	require.NoError(t, err)
	require.NoError(t, store.Add(inst))
	return f, inst
}

func cursorManualMarker(f *cursorHostFixture, inst *agentinstance.Instance) *SessionMarker {
	return &SessionMarker{
		AgentID: inst.AgentID, AgentSessionID: cursorConversationID, PrimedAt: time.Now(),
		CursorSourceBoundary: &CursorSourceBoundary{
			WorkspacePath: f.root, SourcePath: f.source, KnownZero: true,
			SourcePrefixSHA256: cursorEmptyPrefixSHA256,
		},
	}
}

func writeCursorManualMarker(t *testing.T, marker *SessionMarker) {
	t.Helper()
	_, err := UpdateSessionMarker(marker.AgentSessionID, func(current *SessionMarker) error {
		current.CursorSourceBoundary = marker.CursorSourceBoundary
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, WriteSessionMarker(marker))
}

// Explicit start uses the current export EOF, even when prime saved an older
// prompt boundary. Stop must preserve that choice without replaying old turns.
func TestCursorManualStartStopUsesNativeIdentityAndExplicitBoundary(t *testing.T) {
	if testing.Short() {
		t.Skip("exercises subprocess or multi-step recording lifecycle")
	}
	for _, history := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty export", true: "previous turns"}[history], func(t *testing.T) {
			f, inst := newCursorManualStartFixture(t)
			runGit(t, f.root, "init")
			prefix := ""
			if history {
				prefix = strings.ReplaceAll(cursorHostUser+cursorHostAnswer+cursorHostTerminal, "first prompt", "PRE_START_PRIVATE")
			}
			f.write(t, prefix)
			writeCursorManualMarker(t, cursorManualMarker(f, inst))
			// Cursor's shared desktop process cannot identify or own this chat.
			t.Setenv("OX_PARENT_PID", "12345")
			require.NoError(t, session.MarkExplicitStop(f.root, inst.AgentID))
			output := captureStdoutForPlanCLI(t, func() {
				require.NoError(t, runAgentSessionStart(inst, []string{"--title", "Manual Cursor recording"}))
			})
			var started pipeline.StartOutput
			require.NoError(t, json.Unmarshal([]byte(output), &started))
			assert.True(t, started.Success)
			assert.Equal(t, "cursor", started.Adapter)
			assert.Equal(t, "Manual Cursor recording", started.Title)
			assert.Empty(t, started.SessionFile, "native capture must not ask for a generic drop file")
			assert.False(t, session.HasExplicitStop(f.root, inst.AgentID))

			state, err := session.LoadRecordingStateForAgent(f.root, inst.AgentID)
			require.NoError(t, err)
			require.NotNil(t, state)
			assert.Equal(t, cursorConversationID, state.AgentSessionID)
			assert.Equal(t, f.root, state.WorkspacePath)
			assert.Equal(t, f.source, state.SessionFile)
			assert.Equal(t, "tail", state.WatchMode)
			assert.Zero(t, state.ParentPID)
			assert.True(t, state.StartOffsetKnown)
			assert.Equal(t, int64(len(prefix)), state.StartOffset)
			assert.Equal(t, int64(len(prefix)), state.SourceOffset)
			hash := sha256.Sum256([]byte(prefix))
			assert.Equal(t, hex.EncodeToString(hash[:]), state.SourcePrefixSHA256)
			rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
			raw, err := session.ReadSessionFromPath(rawPath)
			require.NoError(t, err)
			assert.Empty(t, raw.Entries, "the raw header must exist before publishing recording state")

			err = runAgentSessionStart(inst, nil)
			require.ErrorContains(t, err, "already being recorded")
			f.write(t, prefix+strings.ReplaceAll(cursorHostUser+cursorHostAnswer+cursorHostTerminal, "first prompt", "POST_START_PUBLIC"))
			output = captureStdoutForPlanCLI(t, func() { require.NoError(t, runAgentSessionStop(inst)) })
			var stopped pipeline.StopOutput
			require.NoError(t, json.Unmarshal([]byte(output), &stopped))
			assert.True(t, stopped.Success)
			assert.Equal(t, 2, stopped.EntryCount)
			assert.True(t, session.HasExplicitStop(f.root, inst.AgentID))
			require.NoFileExists(t, filepath.Join(state.SessionPath, ".recording.json"))
			raw, err = session.ReadSessionFromPath(rawPath)
			require.NoError(t, err)
			require.Len(t, raw.Entries, 2)
			assert.Equal(t, "POST_START_PUBLIC", raw.Entries[0]["content"])
			assert.Equal(t, "final answer", raw.Entries[1]["content"])
			data, err := os.ReadFile(rawPath)
			require.NoError(t, err)
			assert.NotContains(t, string(data), "PRE_START_PRIVATE")
		})
	}
}

func TestCursorManualStartRejectsUnresolvedIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("exercises subprocess or multi-step recording lifecycle")
	}
	for _, scenario := range []string{"no markers", "empty agent ID", "unprimed", "other coworker", "missing native ID", "ambiguous chats"} {
		t.Run(scenario, func(t *testing.T) {
			f, inst := newCursorManualStartFixture(t)
			f.write(t, cursorHostUser+cursorHostAnswer+cursorHostTerminal)
			marker := cursorManualMarker(f, inst)
			switch scenario {
			case "no markers":
				require.NoError(t, os.RemoveAll(SessionMarkerDir()))
			case "empty agent ID":
				inst.AgentID = ""
			case "unprimed":
				marker.PrimedAt = time.Time{}
				writeCursorManualMarker(t, marker)
			case "other coworker":
				marker.AgentID = "Oxother"
				writeCursorManualMarker(t, marker)
			case "missing native ID":
				marker.AgentSessionID = ""
				data, err := json.Marshal(marker)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(markerPath(cursorConversationID), data, 0o600))
			case "ambiguous chats":
				writeCursorManualMarker(t, marker)
				marker.AgentSessionID = "223e4567-e89b-12d3-a456-426614174000"
				writeCursorManualMarker(t, marker)
			}
			err := runAgentSessionStart(inst, nil)
			require.ErrorContains(t, err, "missing-native-identity")
			assert.False(t, session.IsRecording(f.root), "identity failure must not publish recording state")
			assert.Zero(t, f.reader.calls.Load(), "identity failure must not read a native conversation")
		})
	}
}

// A shared marker directory can contain another coworker's files, atomic-write
// leftovers, and damaged files. Only completed markers for this coworker count.
func TestCursorManualStartIgnoresUnrelatedMarkerFiles(t *testing.T) {
	f, inst := newCursorManualStartFixture(t)
	f.write(t, "")
	marker := cursorManualMarker(f, inst)
	writeCursorManualMarker(t, marker)
	data, err := json.Marshal(marker)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(SessionMarkerDir(), "same-chat-copy.json"), data, 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(SessionMarkerDir(), "directory.json"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(SessionMarkerDir(), "broken.json"), []byte("{"), 0o600))
	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "missing"), filepath.Join(SessionMarkerDir(), "unreadable.json")))
	marker.AgentSessionID = "223e4567-e89b-12d3-a456-426614174000"
	data, err = json.Marshal(marker)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(SessionMarkerDir(), "in-flight.json.tmp"), data, 0o600))
	marker.PrimedAt = time.Time{}
	writeCursorManualMarker(t, marker)
	marker.AgentID, marker.PrimedAt = "Oxother", time.Now()
	marker.AgentSessionID = "323e4567-e89b-12d3-a456-426614174000"
	writeCursorManualMarker(t, marker)
	captureStdoutForPlanCLI(t, func() { require.NoError(t, runAgentSessionStart(inst, nil)) })
	state, err := session.LoadRecordingStateForAgent(f.root, inst.AgentID)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, cursorConversationID, state.AgentSessionID)
}

func TestCursorManualStartRefusesUnavailableSource(t *testing.T) {
	if testing.Short() {
		t.Skip("exercises subprocess or multi-step recording lifecycle")
	}
	for _, tc := range []struct{ name, want string }{
		{"pending export", "source-not-found"},
		{"incomplete export", "boundary-unavailable"},
		{"native ID is a generation", "invalid-source-path"},
		{"directory in place of export", "invalid-source-path"},
		{"other workspace symlink", "invalid-source-path"},
		{"adapter missing", "adapter-missing"},
		{"home unavailable", "source-unreadable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, inst := newCursorManualStartFixture(t)
			marker := cursorManualMarker(f, inst)
			switch tc.name {
			case "incomplete export":
				f.write(t, cursorHostUser+`{"role":"assistant"`)
			case "native ID is a generation":
				marker.AgentSessionID = "generation-123"
			case "directory in place of export":
				require.NoError(t, os.MkdirAll(f.source, 0o700))
			case "other workspace symlink":
				otherSource, err := cursorpaths.SessionPath(f.home, t.TempDir(), cursorConversationID)
				require.NoError(t, err)
				require.NoError(t, os.MkdirAll(filepath.Dir(otherSource), 0o700))
				require.NoError(t, os.WriteFile(otherSource, []byte(cursorHostUser), 0o600))
				require.NoError(t, os.MkdirAll(filepath.Dir(f.source), 0o700))
				require.NoError(t, os.Symlink(otherSource, f.source))
			case "adapter missing":
				adapters.Unregister("cursor")
			case "home unavailable":
				t.Setenv("HOME", "")
			}
			writeCursorManualMarker(t, marker)
			err := runAgentSessionStart(inst, nil)
			require.ErrorContains(t, err, tc.want)
			assert.False(t, session.IsRecording(f.root), "source failure must not publish recording state")
			assert.Zero(t, f.reader.calls.Load(), "a rejected source must not reach the adapter reader")
		})
	}
}

func TestCursorManualStartRejectsMismatchedMarkerBoundary(t *testing.T) {
	if testing.Short() {
		t.Skip("exercises subprocess or multi-step recording lifecycle")
	}
	for _, tc := range []struct{ name, want string }{
		{"another workspace", "workspace-mismatch"},
		{"missing boundary", "boundary-unavailable"},
		{"unavailable workspace", "workspace-mismatch"},
		{"another source", "invalid-source-path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, inst := newCursorManualStartFixture(t)
			otherRoot, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			otherSource, err := cursorpaths.SessionPath(f.home, otherRoot, cursorConversationID)
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(filepath.Dir(otherSource), 0o700))
			require.NoError(t, os.WriteFile(otherSource, []byte(cursorHostUser), 0o600))
			f.write(t, cursorHostUser+cursorHostAnswer+cursorHostTerminal)
			marker := cursorManualMarker(f, inst)
			switch tc.name {
			case "another workspace":
				marker.CursorSourceBoundary.WorkspacePath = otherRoot
				marker.CursorSourceBoundary.SourcePath = otherSource
			case "missing boundary":
				marker.CursorSourceBoundary = nil
			case "unavailable workspace":
				marker.CursorSourceBoundary.WorkspacePath = filepath.Join(otherRoot, "deleted-workspace")
			case "another source":
				marker.CursorSourceBoundary.SourcePath = otherSource
			}
			writeCursorManualMarker(t, marker)
			var startErr error
			captureStdoutForPlanCLI(t, func() { startErr = runAgentSessionStart(inst, nil) })
			require.ErrorContains(t, startErr, tc.want)
			assert.False(t, session.IsRecording(f.root))
			assert.Zero(t, f.reader.calls.Load())
		})
	}
}
