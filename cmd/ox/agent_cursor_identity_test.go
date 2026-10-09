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

func TestPrepareCursorHookBoundary_PreservesFirstOverlapAndPrimeWrite(t *testing.T) {
	isolateSessionMarkerDir(t)
	raw, projectRoot, homeDir, sourcePath := cursorInputFixture(t, "")
	input, err := normalizeCursorHookInput(raw, projectRoot, homeDir)
	require.NoError(t, err)
	marker, err := prepareCursorHookBoundary(input, string(agentx.CursorEventBeforeSubmitPrompt))
	require.NoError(t, err)
	require.NotNil(t, marker.CursorSourceBoundary)
	first := *marker.CursorSourceBoundary
	assert.True(t, first.KnownZero)
	assert.False(t, marker.IsPrimed(), "boundary-only marker must not suppress a real prime")

	// The later export cannot advance an overlapping sessionStart's boundary.
	require.NoError(t, os.MkdirAll(filepath.Dir(sourcePath), 0o700))
	require.NoError(t, os.WriteFile(sourcePath, []byte(cursorHostUser+cursorHostTerminal), 0o600))
	input.SourcePending = false
	input.GenerationID = ""
	marker, err = prepareCursorHookBoundary(input, string(agentx.CursorEventSessionStart))
	require.NoError(t, err)
	assert.Equal(t, first, *marker.CursorSourceBoundary)

	require.NoError(t, WriteSessionMarker(&SessionMarker{
		AgentID: "Oxcur1", AgentSessionID: input.ConversationID, PrimedAt: time.Now(),
	}))
	stored, err := ReadSessionMarker(input.ConversationID)
	require.NoError(t, err)
	require.True(t, stored.IsPrimed())
	require.NotNil(t, stored.CursorSourceBoundary)
	assert.Equal(t, first, *stored.CursorSourceBoundary, "ordinary prime write must preserve pending boundary")
}

func TestPrepareCursorHookBoundary_BindsFirstPromptGenerationWithoutReplacingSessionStartBoundary(t *testing.T) {
	isolateSessionMarkerDir(t)
	old := cursorHostUser + cursorHostTerminal
	raw, projectRoot, homeDir, sourcePath := cursorInputFixture(t, old)
	input, err := normalizeCursorHookInput(raw, projectRoot, homeDir)
	require.NoError(t, err)
	input.GenerationID = ""
	marker, err := prepareCursorHookBoundary(input, string(agentx.CursorEventSessionStart))
	require.NoError(t, err)
	startHash := marker.CursorSourceBoundary.SourcePrefixSHA256

	firstPrompt := old + cursorHostUser
	require.NoError(t, os.WriteFile(sourcePath, []byte(firstPrompt), 0o600))
	input.GenerationID = "generation-one"
	marker, err = prepareCursorHookBoundary(input, string(agentx.CursorEventBeforeSubmitPrompt))
	require.NoError(t, err)
	require.NotNil(t, marker.CursorSourceBoundary)
	assert.Equal(t, "generation-one", marker.CursorSourceBoundary.GenerationID)
	assert.Equal(t, int64(len(old)), marker.CursorSourceBoundary.Offset)
	assert.Equal(t, startHash, marker.CursorSourceBoundary.SourcePrefixSHA256)

	secondPrompt := firstPrompt + cursorHostAnswer + cursorHostTerminal + cursorHostUser
	require.NoError(t, os.WriteFile(sourcePath, []byte(secondPrompt), 0o600))
	input.GenerationID = "generation-two"
	marker, err = prepareCursorHookBoundary(input, string(agentx.CursorEventBeforeSubmitPrompt))
	require.NoError(t, err)
	assert.Equal(t, "generation-two", marker.CursorSourceBoundary.GenerationID)
	assert.Equal(t, int64(len(secondPrompt)), marker.CursorSourceBoundary.Offset)
	wantHash, err := cursorPrefixSHA256(sourcePath, int64(len(secondPrompt)))
	require.NoError(t, err)
	assert.Equal(t, wantHash, marker.CursorSourceBoundary.SourcePrefixSHA256)
}

func TestPrepareCursorHookBoundary_RefreshesNewGenerationOnlyWhenInactive(t *testing.T) {
	f := newCursorHostFixture(t)
	f.write(t, cursorHostUser+cursorHostTerminal)
	input, err := normalizeCursorHookInput(f.input(t, "beforeSubmitPrompt", "generation-a").RawBytes, f.root, f.home)
	require.NoError(t, err)
	marker, err := prepareCursorHookBoundary(input, string(agentx.CursorEventBeforeSubmitPrompt))
	require.NoError(t, err)
	first := *marker.CursorSourceBoundary
	state := f.record(t)

	second := cursorHostUser + cursorHostTerminal + cursorHostUser
	f.write(t, second)
	input.GenerationID = "generation-b"
	marker, err = prepareCursorHookBoundary(input, string(agentx.CursorEventBeforeSubmitPrompt))
	require.NoError(t, err)
	assert.Equal(t, first, *marker.CursorSourceBoundary, "a live recording keeps its original source boundary")

	require.NoError(t, session.ClearRecordingStateAt(state.SessionPath, state.SessionID))
	marker, err = prepareCursorHookBoundary(input, string(agentx.CursorEventBeforeSubmitPrompt))
	require.NoError(t, err)
	assert.Equal(t, "generation-b", marker.CursorSourceBoundary.GenerationID)
	assert.Equal(t, int64(len(second)), marker.CursorSourceBoundary.Offset)
}

func TestPrepareCursorHookBoundary_RejectsRebinding(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*cursorNativeInput, *session.RecordingState)
		want   string
	}{
		{"workspace", func(input *cursorNativeInput, _ *session.RecordingState) { input.WorkspacePath += "-other" }, "workspace-mismatch"},
		{"source", func(input *cursorNativeInput, _ *session.RecordingState) { input.SourcePath += "-other" }, "workspace-mismatch"},
		{"recording conversation", func(_ *cursorNativeInput, state *session.RecordingState) {
			state.AgentSessionID = "123e4567-e89b-12d3-a456-426614174001"
		}, "identity-conflict"},
		{"recording adapter", func(_ *cursorNativeInput, state *session.RecordingState) { state.AdapterName = "claude-code" }, "identity-conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCursorHostFixture(t)
			input, err := normalizeCursorHookInput(f.input(t, "beforeSubmitPrompt", "generation-a").RawBytes, f.root, f.home)
			require.NoError(t, err)
			marker, err := prepareCursorHookBoundary(input, string(agentx.CursorEventBeforeSubmitPrompt))
			require.NoError(t, err)
			first := *marker.CursorSourceBoundary
			state := f.record(t)
			require.NoError(t, session.UpdateRecordingStateAt(state.SessionPath, state.SessionID, func(current *session.RecordingState) {
				tc.change(input, current)
			}))
			input.GenerationID = "generation-b"
			_, err = prepareCursorHookBoundary(input, string(agentx.CursorEventBeforeSubmitPrompt))
			require.ErrorContains(t, err, tc.want)
			stored, err := ReadSessionMarker(input.ConversationID)
			require.NoError(t, err)
			assert.Equal(t, first, *stored.CursorSourceBoundary)
		})
	}
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
