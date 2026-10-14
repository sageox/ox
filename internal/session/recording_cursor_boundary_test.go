package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func TestStartRecording_KnownZeroBoundaryPersists(t *testing.T) {
	projectRoot := setupRecordingTest(t, t.TempDir())

	state, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID:            "OxCur0",
		AgentSessionID:     "native-conversation",
		AdapterName:        "cursor",
		Username:           "testuser",
		StartOffsetKnown:   true,
		SourcePrefixSHA256: emptySHA256,
		WatchMode:          "tail",
	})
	require.NoError(t, err)
	assert.Empty(t, state.SessionFile, "a missing source stays pending")
	assert.Zero(t, state.ParentPID, "Cursor hooks do not own conversation liveness")
	assert.True(t, state.StartOffsetKnown)
	assert.Zero(t, state.StartOffset)
	assert.Zero(t, state.SourceOffset)
	assert.Equal(t, emptySHA256, state.SourcePrefixSHA256)

	reloaded, err := LoadRecordingStateForAgent(projectRoot, state.AgentID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.True(t, reloaded.StartOffsetKnown)
	assert.Zero(t, reloaded.StartOffset)
	assert.Zero(t, reloaded.SourceOffset)
	assert.Equal(t, emptySHA256, reloaded.SourcePrefixSHA256)
}

func TestStartRecordingCursor_InitializesBeforePublication(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "published", true: "initialization fails"}[fail], func(t *testing.T) {
			root := setupRecordingTest(t, t.TempDir())
			initializationErr := errors.New("injected initialization failure")
			var path string
			state, err := StartRecording(root, StartRecordingOptions{
				AgentID: "Oxcur1", AdapterName: "cursor", StartOffsetKnown: true,
				SourcePrefixSHA256: emptySHA256, WatchMode: "tail",
				BeforePublish: func(s *RecordingState) error {
					path = s.SessionPath
					_, err := os.Stat(recordingStatePath(path))
					require.ErrorIs(t, err, os.ErrNotExist, "capture must not discover the session before initialization")
					if fail {
						return initializationErr
					}
					w, err := NewRawWriter(filepath.Join(path, "raw.jsonl"), root)
					require.NoError(t, err)
					require.NoError(t, w.WriteRaw(map[string]any{"type": "header", "metadata": map[string]any{"agent_type": "cursor"}}))
					return w.CloseAndSync()
				},
			})
			if fail {
				require.ErrorIs(t, err, initializationErr)
				_, err = os.Stat(recordingStatePath(path))
				require.ErrorIs(t, err, os.ErrNotExist)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, state)
			raw, err := os.ReadFile(filepath.Join(path, "raw.jsonl"))
			require.NoError(t, err)
			assert.Contains(t, string(raw), `"agent_type":"cursor"`)
		})
	}
}

func TestBindInitialRecordingSource_IsAtomicAndIdempotent(t *testing.T) {
	projectRoot := setupRecordingTest(t, t.TempDir())
	state, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID:        "OxCur1",
		AgentSessionID: "native-conversation",
		AdapterName:    "cursor",
		Username:       "testuser",
		// This simulates a legacy active state with no persisted boundary.
		// Known-zero Cursor states are intentionally not rebound to a later EOF.
		WatchMode: "tail",
	})
	require.NoError(t, err)

	source := filepath.Join(t.TempDir(), "conversation.jsonl")
	require.NoError(t, os.WriteFile(source, []byte("old row\nnew row\n"), 0o600))
	binding := InitialSourceBinding{
		AgentSessionID:     "native-conversation",
		SessionFile:        source,
		StartOffset:        int64(len("old row\n")),
		StartOffsetKnown:   true,
		SourcePrefixSHA256: "prefix-sha",
	}

	bound, err := BindInitialRecordingSource(projectRoot, state.AgentID, binding)
	require.NoError(t, err)
	assert.Equal(t, source, bound.SessionFile)
	assert.Equal(t, binding.StartOffset, bound.StartOffset)
	assert.Equal(t, binding.StartOffset, bound.SourceOffset)
	assert.True(t, bound.StartOffsetKnown)
	assert.Equal(t, binding.SourcePrefixSHA256, bound.SourcePrefixSHA256)

	again, err := BindInitialRecordingSource(projectRoot, state.AgentID, binding)
	require.NoError(t, err)
	assert.Equal(t, bound.StartOffset, again.StartOffset)

	conflicting := binding
	conflicting.StartOffset++
	_, err = BindInitialRecordingSource(projectRoot, state.AgentID, conflicting)
	require.ErrorContains(t, err, "source boundary conflict")

	reloaded, err := LoadRecordingStateForAgent(projectRoot, state.AgentID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Equal(t, binding.StartOffset, reloaded.SourceOffset, "conflicting hook must not overwrite first boundary")
	assert.Equal(t, binding.SourcePrefixSHA256, reloaded.SourcePrefixSHA256)
}

func TestBindInitialRecordingSource_RejectsDifferentNativeIdentity(t *testing.T) {
	projectRoot := setupRecordingTest(t, t.TempDir())
	state, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID: "OxCur2", AgentSessionID: "conversation-a", AdapterName: "cursor", Username: "testuser",
	})
	require.NoError(t, err)
	source := filepath.Join(t.TempDir(), "conversation.jsonl")
	require.NoError(t, os.WriteFile(source, []byte("row\n"), 0o600))

	_, err = BindInitialRecordingSource(projectRoot, state.AgentID, InitialSourceBinding{
		AgentSessionID: "conversation-b", SessionFile: source, StartOffsetKnown: true,
	})
	require.ErrorContains(t, err, "native agent session identity conflict")

	reloaded, err := LoadRecordingStateForAgent(projectRoot, state.AgentID)
	require.NoError(t, err)
	assert.Empty(t, reloaded.SessionFile)
}

func TestBindInitialRecordingSource_DoesNotReplacePendingKnownZero(t *testing.T) {
	projectRoot := setupRecordingTest(t, t.TempDir())
	emptySHA256 := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	state, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID: "OxKnown0", AgentSessionID: "native-known-zero", OutputFile: filepath.Join(projectRoot, "out.md"),
		StartOffsetKnown: true, SourcePrefixSHA256: emptySHA256,
	})
	require.NoError(t, err)
	source := filepath.Join(t.TempDir(), "cursor.jsonl")
	require.NoError(t, os.WriteFile(source, []byte("already-exported\n"), 0o600))

	_, err = BindInitialRecordingSource(projectRoot, state.AgentID, InitialSourceBinding{
		AgentSessionID: state.AgentSessionID, SessionFile: source, StartOffset: int64(len("already-exported\n")),
		StartOffsetKnown: true, SourcePrefixSHA256: "later-eof-hash",
	})
	require.ErrorContains(t, err, "source boundary conflict")

	reloaded, err := LoadRecordingStateForAgent(projectRoot, state.AgentID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Empty(t, reloaded.SessionFile)
	assert.True(t, reloaded.StartOffsetKnown)
	assert.Zero(t, reloaded.StartOffset)
	assert.Zero(t, reloaded.SourceOffset)
	assert.Equal(t, emptySHA256, reloaded.SourcePrefixSHA256)
}

func TestBindInitialRecordingSource_AttachesPendingKnownZeroAtSameBoundary(t *testing.T) {
	projectRoot := setupRecordingTest(t, t.TempDir())
	state, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID: "OxKnown0Attach", AgentSessionID: "native-known-zero-attach", OutputFile: filepath.Join(projectRoot, "out.md"),
		StartOffsetKnown: true, SourcePrefixSHA256: emptySHA256,
	})
	require.NoError(t, err)
	source := filepath.Join(t.TempDir(), "cursor.jsonl")
	require.NoError(t, os.WriteFile(source, []byte("first exported row\n"), 0o600))

	bound, err := BindInitialRecordingSource(projectRoot, state.AgentID, InitialSourceBinding{
		AgentSessionID: state.AgentSessionID, SessionFile: source, StartOffsetKnown: true,
		SourcePrefixSHA256: emptySHA256,
	})
	require.NoError(t, err)
	assert.Equal(t, source, bound.SessionFile)
	assert.True(t, bound.StartOffsetKnown)
	assert.Zero(t, bound.StartOffset)
	assert.Zero(t, bound.SourceOffset)
	assert.Equal(t, emptySHA256, bound.SourcePrefixSHA256)
}
