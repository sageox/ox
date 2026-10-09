package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/cursorpaths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPersistCursorSourceBoundary_PreservesFirstOverlapAndPrimeWrite(t *testing.T) {
	isolateSessionMarkerDir(t)
	const nativeID = "123e4567-e89b-12d3-a456-426614174000"
	first := CursorSourceBoundary{WorkspacePath: "/workspace", SourcePath: "/source", GenerationID: "generation-a", KnownZero: true}
	marker, err := persistCursorSourceBoundary(nativeID, string(agentx.CursorEventBeforeSubmitPrompt), first, false)
	require.NoError(t, err)
	require.NotNil(t, marker.CursorSourceBoundary)
	assert.Equal(t, "generation-a", marker.CursorSourceBoundary.GenerationID)
	assert.False(t, marker.IsPrimed(), "boundary-only marker must not suppress a real prime")

	second := first
	second.GenerationID = "generation-b"
	second.Offset = 10
	marker, err = persistCursorSourceBoundary(nativeID, string(agentx.CursorEventSessionStart), second, false)
	require.NoError(t, err)
	assert.Equal(t, "generation-a", marker.CursorSourceBoundary.GenerationID, "overlapping sessionStart keeps earliest boundary")

	require.NoError(t, WriteSessionMarker(&SessionMarker{
		AgentID: nativeID[:6], AgentSessionID: nativeID, PrimedAt: time.Now(),
	}))
	stored, err := ReadSessionMarker(nativeID)
	require.NoError(t, err)
	require.True(t, stored.IsPrimed())
	require.NotNil(t, stored.CursorSourceBoundary)
	assert.Equal(t, "generation-a", stored.CursorSourceBoundary.GenerationID, "ordinary prime write must preserve pending boundary")
}

func TestPersistCursorSourceBoundary_BindsFirstPromptGenerationWithoutReplacingSessionStartBoundary(t *testing.T) {
	isolateSessionMarkerDir(t)
	const nativeID = "123e4567-e89b-12d3-a456-426614174003"
	sessionStart := CursorSourceBoundary{
		WorkspacePath: "/workspace", SourcePath: "/source", Offset: 7,
		SourcePrefixSHA256: "session-start-prefix",
	}
	_, err := persistCursorSourceBoundary(nativeID, string(agentx.CursorEventSessionStart), sessionStart, false)
	require.NoError(t, err)

	firstPrompt := sessionStart
	firstPrompt.GenerationID = "generation-one"
	firstPrompt.Offset = 19 // later prompt EOF must not replace sessionStart's boundary
	firstPrompt.SourcePrefixSHA256 = "later-prefix"
	marker, err := persistCursorSourceBoundary(nativeID, string(agentx.CursorEventBeforeSubmitPrompt), firstPrompt, false)
	require.NoError(t, err)
	require.NotNil(t, marker.CursorSourceBoundary)
	assert.Equal(t, "generation-one", marker.CursorSourceBoundary.GenerationID)
	assert.Equal(t, int64(7), marker.CursorSourceBoundary.Offset)
	assert.Equal(t, "session-start-prefix", marker.CursorSourceBoundary.SourcePrefixSHA256)

	secondPrompt := firstPrompt
	secondPrompt.GenerationID = "generation-two"
	secondPrompt.Offset = 29
	secondPrompt.SourcePrefixSHA256 = "new-generation-prefix"
	marker, err = persistCursorSourceBoundary(nativeID, string(agentx.CursorEventBeforeSubmitPrompt), secondPrompt, false)
	require.NoError(t, err)
	assert.Equal(t, "generation-two", marker.CursorSourceBoundary.GenerationID)
	assert.Equal(t, int64(29), marker.CursorSourceBoundary.Offset)
	assert.Equal(t, "new-generation-prefix", marker.CursorSourceBoundary.SourcePrefixSHA256)
}

func TestPersistCursorSourceBoundary_RefreshesNewGenerationOnlyWhenInactive(t *testing.T) {
	isolateSessionMarkerDir(t)
	const nativeID = "123e4567-e89b-12d3-a456-426614174000"
	first := CursorSourceBoundary{WorkspacePath: "/workspace", SourcePath: "/source", GenerationID: "generation-a", Offset: 3}
	_, err := persistCursorSourceBoundary(nativeID, string(agentx.CursorEventBeforeSubmitPrompt), first, false)
	require.NoError(t, err)
	second := first
	second.GenerationID = "generation-b"
	second.Offset = 9

	marker, err := persistCursorSourceBoundary(nativeID, string(agentx.CursorEventBeforeSubmitPrompt), second, true)
	require.NoError(t, err)
	assert.Equal(t, "generation-a", marker.CursorSourceBoundary.GenerationID)

	marker, err = persistCursorSourceBoundary(nativeID, string(agentx.CursorEventBeforeSubmitPrompt), second, false)
	require.NoError(t, err)
	assert.Equal(t, "generation-b", marker.CursorSourceBoundary.GenerationID)
	assert.Equal(t, int64(9), marker.CursorSourceBoundary.Offset)
}

func TestStartSessionRecordingCursor_UsesSavedBoundaryAndKeepsNativeIdentity(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("OX_XDG_DISABLE", "")
	f := newDraftLedgerFixture(t)
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	t.Chdir(f.projectRoot)
	defaultLedger, err := ledger.DefaultPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(defaultLedger), 0o755))
	runGit(t, f.projectRoot, "clone", "--quiet", f.barePath, defaultLedger)
	oldCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = oldCfg })
	oxConfigSetRepo(t, "session_recording", "auto")

	source, err := cursorpaths.SessionPath(home, f.projectRoot, cursorConversationID)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(source), 0o700))
	require.NoError(t, os.WriteFile(source, []byte("old\nnew\n"), 0o600))
	hash, err := cursorPrefixSHA256(source, int64(len("old\n")))
	require.NoError(t, err)
	boundary := &CursorSourceBoundary{
		WorkspacePath: f.projectRoot, SourcePath: source, Offset: int64(len("old\n")),
		SourcePrefixSHA256: hash, GenerationID: "generation-a",
	}
	status := startSessionRecording(f.projectRoot, "OxCur3", "cursor", "", "", cursorConversationID, boundary)
	require.NotNil(t, status)
	state, err := session.LoadRecordingStateForAgent(f.projectRoot, "OxCur3")
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, "cursor", state.AdapterName)
	assert.Equal(t, "tail", state.WatchMode)
	assert.Equal(t, cursorConversationID, state.AgentSessionID)
	assert.Equal(t, source, state.SessionFile)
	assert.True(t, state.StartOffsetKnown)
	assert.Equal(t, int64(len("old\n")), state.StartOffset)
	assert.Equal(t, state.StartOffset, state.SourceOffset)
	assert.Equal(t, hash, state.SourcePrefixSHA256)

	other := *boundary
	other.SourcePath = filepath.Join(t.TempDir(), "other.jsonl")
	require.NoError(t, os.WriteFile(other.SourcePath, []byte("other\n"), 0o600))
	other.Offset = 0
	require.Nil(t, startSessionRecording(f.projectRoot, "OxCur3", "cursor", "", "", "123e4567-e89b-12d3-a456-426614174001", &other))
	reloaded, err := session.LoadRecordingStateForAgent(f.projectRoot, "OxCur3")
	require.NoError(t, err)
	assert.Equal(t, cursorConversationID, reloaded.AgentSessionID, "a second chat cannot rebind an existing Cursor recording")
	assert.Equal(t, source, reloaded.SessionFile)

	pendingPath, err := cursorpaths.SessionPath(home, f.projectRoot, "123e4567-e89b-12d3-a456-426614174002")
	require.NoError(t, err)
	pending := &CursorSourceBoundary{
		WorkspacePath: f.projectRoot, SourcePath: pendingPath,
		SourcePending: true, KnownZero: true, GenerationID: "generation-pending", SourcePrefixSHA256: cursorEmptyPrefixSHA256,
	}
	require.NotNil(t, startSessionRecording(f.projectRoot, "OxCur4", "cursor", "", "", "123e4567-e89b-12d3-a456-426614174002", pending))
	pendingState, err := session.LoadRecordingStateForAgent(f.projectRoot, "OxCur4")
	require.NoError(t, err)
	require.NotNil(t, pendingState)
	assert.Empty(t, pendingState.SessionFile)
	assert.True(t, pendingState.StartOffsetKnown)
	assert.Zero(t, pendingState.StartOffset)
	assert.Zero(t, pendingState.SourceOffset)
	assert.Equal(t, cursorEmptyPrefixSHA256, pendingState.SourcePrefixSHA256)
}
