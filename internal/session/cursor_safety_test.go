package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorControlDrainsPausedRowsBeforeResume(t *testing.T) {
	f := newCursorCaptureFixture(t, "", "", true)
	ctx := context.Background()
	pause := func(state *RecordingState) {
		now := time.Now().UTC()
		state.SuspendedAt = &now
		state.Lifecycle = append(state.Lifecycle, LifecycleEvent{Action: LifecycleActionPause, At: now, Seq: state.EntryCount})
	}
	resume := func(state *RecordingState) {
		state.SuspendedAt = nil
		state.Lifecycle = append(state.Lifecycle, LifecycleEvent{Action: LifecycleActionResume, Seq: state.EntryCount})
	}
	require.NoError(t, UpdateCursorRecordingControl(ctx, f.projectRoot, f.state, f.homeDir, nil, LifecycleActionPause, pause))
	err := UpdateCursorRecordingControl(ctx, f.projectRoot, f.state, f.homeDir, nil, LifecycleActionResume, resume)
	require.ErrorIs(t, err, ErrCursorSourcePending)
	assert.NotNil(t, loadCursorCaptureState(t, f).SuspendedAt)

	row := `{"role":"user"}` + "\n"
	require.NoError(t, os.MkdirAll(filepath.Dir(f.sourcePath), 0o700))
	require.NoError(t, os.WriteFile(f.sourcePath, []byte(row), 0o600))
	reader := &fixedCursorIncrementalReader{next: int64(len(row)), entries: []adapters.RawEntry{{Role: "user", Content: "private paused message"}}}
	err = UpdateCursorRecordingControl(ctx, f.projectRoot, f.state, f.homeDir, reader, LifecycleActionResume, resume)
	require.ErrorIs(t, err, ErrCursorFinalDrainPending)
	state := loadCursorCaptureState(t, f)
	assert.NotNil(t, state.SuspendedAt)
	assert.Equal(t, 1, state.EntryCount, "failed resume checkpoints the private row without ending its exclusion")
	err = UpdateRecordingStateForAgent(f.projectRoot, state.AgentID, resume)
	require.ErrorIs(t, err, ErrCursorFinalDrainPending, "generic marker updates cannot bypass the drain")

	complete := row + `{"type":"turn_ended","status":"success"}` + "\n"
	require.NoError(t, os.WriteFile(f.sourcePath, []byte(complete), 0o600))
	reader = &fixedCursorIncrementalReader{next: int64(len(complete))}
	require.NoError(t, UpdateCursorRecordingControl(ctx, f.projectRoot, f.state, f.homeDir, reader, LifecycleActionResume, resume))
	state = loadCursorCaptureState(t, f)
	assert.Nil(t, state.SuspendedAt)
	assert.True(t, IsSeqExcluded(0, BuildSegmentRanges(state.Lifecycle)))
	assert.False(t, IsSeqExcluded(1, BuildSegmentRanges(state.Lifecycle)))

	// A caller cannot commit an invalid control after capture succeeds.
	err = UpdateCursorRecordingControl(ctx, f.projectRoot, f.state, f.homeDir, reader, LifecycleActionPause, func(state *RecordingState) {
		pause(state)
		now := time.Now()
		state.StoppedAt = &now
	})
	require.ErrorIs(t, err, ErrNotRecording)
	assert.Nil(t, loadCursorCaptureState(t, f).SuspendedAt)
}

func TestCursorPromptRefusesUnprovenSourceWithoutReservingTurn(t *testing.T) {
	for _, kind := range []string{"empty generation", "invalid boundary", "missing acknowledged source", "changed prefix", "removed marker"} {
		t.Run(kind, func(t *testing.T) {
			prefix := `{"role":"user"}` + "\n"
			f := newCursorCaptureFixture(t, prefix, "", false)
			generation := "new-turn"
			switch kind {
			case "empty generation":
				generation = ""
			case "invalid boundary":
				f.state.StartOffsetKnown = false
				require.NoError(t, SaveRecordingState(f.projectRoot, f.state))
			case "missing acknowledged source":
				require.NoError(t, os.Remove(f.sourcePath))
			case "changed prefix":
				require.NoError(t, os.WriteFile(f.sourcePath, []byte(`{"role":"tool"}`+"\n"), 0o600))
			case "removed marker":
				require.NoError(t, os.Remove(recordingStatePath(f.sessionPath)))
			}
			before, _ := os.ReadFile(recordingStatePath(f.sessionPath))
			require.Error(t, RecordCursorPrompt(context.Background(), f.projectRoot, f.state, f.homeDir, generation))
			after, _ := os.ReadFile(recordingStatePath(f.sessionPath))
			assert.Equal(t, before, after)
		})
	}
	assert.Equal(t, 1, cursorCompletedTurns([]byte("{\"type\":\"turn_ended\",\"status\":\"success\"}\n{\"type\":\"turn_ended\"")), "a torn final row cannot acknowledge a submitted turn")
}

func TestCursorCaptureRefusesDamagedCheckpointAndReader(t *testing.T) {
	for _, kind := range []string{"missing marker", "directory marker", "malformed marker", "missing workspace", "other workspace", "stopped", "invalid boundary", "missing reader", "reader error", "invalid offset", "unadvanced entries", "canceled before read", "canceled after read", "source replaced by symlink", "malformed raw", "raw directory", "raw symlink loop", "journal directory"} {
		t.Run(kind, func(t *testing.T) {
			source := `{"role":"user"}` + "\n"
			f := newCursorCaptureFixture(t, "", source, false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			root := f.projectRoot
			fixed := &fixedCursorIncrementalReader{next: int64(len(source)), entries: []adapters.RawEntry{{Role: "user", Content: "new message"}}}
			var reader adapters.IncrementalReader = fixed
			switch kind {
			case "missing marker", "directory marker", "malformed marker":
				p := recordingStatePath(f.sessionPath)
				require.NoError(t, os.Remove(p))
				switch kind {
				case "directory marker":
					require.NoError(t, os.Mkdir(p, 0o700))
				case "malformed marker":
					require.NoError(t, os.WriteFile(p, []byte("{"), 0o600))
				}
			case "missing workspace":
				root = filepath.Join(t.TempDir(), "missing")
			case "other workspace":
				root = t.TempDir()
			case "stopped":
				now := time.Now()
				f.state.StoppedAt = &now
				require.NoError(t, SaveRecordingState(root, f.state))
			case "invalid boundary":
				f.state.SourcePrefixSHA256 = "not-a-hash"
				require.NoError(t, SaveRecordingState(root, f.state))
			case "missing reader":
				reader = nil
			case "reader error":
				fixed.err = errors.New("private data must not escape")
			case "invalid offset":
				fixed.next = int64(len(source) + 1)
			case "unadvanced entries":
				fixed.next = 0
			case "canceled before read":
				cancel()
			case "canceled after read":
				fixed.mutate = func(string) { cancel() }
			case "source replaced by symlink":
				other := filepath.Join(t.TempDir(), "another-conversation")
				require.NoError(t, os.WriteFile(other, []byte(source), 0o600))
				fixed.mutate = func(path string) {
					require.NoError(t, os.Remove(path))
					require.NoError(t, os.Symlink(other, path))
				}
			case "malformed raw":
				require.NoError(t, os.WriteFile(f.rawPath, []byte("not-json\n"), 0o600))
			case "raw directory":
				require.NoError(t, os.Mkdir(f.rawPath, 0o700))
			case "raw symlink loop":
				require.NoError(t, os.Symlink(f.rawPath, f.rawPath))
			case "journal directory":
				require.NoError(t, os.Mkdir(f.rawPath+".append.json", 0o700))
			}
			markerBefore, _ := os.ReadFile(recordingStatePath(f.sessionPath))
			rawBefore, _ := os.ReadFile(f.rawPath)
			result, err := DrainCursorSource(ctx, root, f.sessionPath, f.homeDir, reader, false)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "private data")
			assert.Nil(t, result)
			markerAfter, _ := os.ReadFile(recordingStatePath(f.sessionPath))
			rawAfter, _ := os.ReadFile(f.rawPath)
			assert.Equal(t, markerBefore, markerAfter, "failed capture cannot advance its checkpoint")
			assert.Equal(t, rawBefore, rawAfter, "failed capture preserves existing entries")
		})
	}
}

func TestCursorFinalizationRetainsCarrierHeaderAndRefusesLostCheckpoint(t *testing.T) {
	source := `{"role":"user"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
	f := newCursorCaptureFixture(t, "", source, false)
	header := `{"type":"header","metadata":{"session_id":"carrier-identity","schema_version":"1"}}` + "\n"
	require.NoError(t, os.WriteFile(f.rawPath, []byte(header), 0o600))
	reader := &fixedCursorIncrementalReader{next: int64(len(source)), entries: []adapters.RawEntry{{Role: "user", Content: "kept entry"}}}
	_, err := finalizeCursorFixture(t, f, reader)
	require.NoError(t, err)
	row, finalized, err := cursorCaptureHeader(f.rawPath)
	require.NoError(t, err)
	require.True(t, finalized)
	assert.Equal(t, "header", row["type"])
	assert.Equal(t, "carrier-identity", row["metadata"].(map[string]any)["session_id"])
	before, err := os.ReadFile(f.rawPath)
	require.NoError(t, err)
	require.NoError(t, os.Remove(recordingStatePath(f.sessionPath)))
	reader.err = errors.New("must not replay an already finalized source")
	state, err := finalizeCursorFixture(t, f, reader)
	require.Error(t, err)
	assert.Nil(t, state)
	after, err := os.ReadFile(f.rawPath)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestCursorRecordingSourceRejectsInvalidBoundaryAndIdentity(t *testing.T) {
	for _, kind := range []string{"nil", "unknown offset", "invalid hash", "missing home", "wrong conversation"} {
		t.Run(kind, func(t *testing.T) {
			f := newCursorCaptureFixture(t, "", "", true)
			state, home := f.state, f.homeDir
			switch kind {
			case "nil":
				state = nil
			case "unknown offset":
				state.StartOffsetKnown = false
			case "invalid hash":
				state.SourcePrefixSHA256 = "abcd"
			case "missing home":
				home = filepath.Join(home, "missing")
			case "wrong conversation":
				state.AgentSessionID = "../another-chat"
			}
			path, err := ValidateCursorRecordingSource(home, state)
			require.Error(t, err)
			assert.Empty(t, path)
		})
	}
}

func TestCursorSourceSnapshotRejectsUnsafeFileKindsAndSize(t *testing.T) {
	for _, kind := range []string{"directory", "too large", "symlink loop"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "export.jsonl")
			switch kind {
			case "directory":
				require.NoError(t, os.Mkdir(path, 0o700))
			case "too large":
				file, err := os.Create(path)
				require.NoError(t, err)
				require.NoError(t, file.Truncate(cursorCaptureMaxBytes+1))
				require.NoError(t, file.Close())
			case "symlink loop":
				require.NoError(t, os.Symlink(path, path))
			}
			snapshot, err := readCursorSourceSnapshot(path)
			require.Error(t, err)
			assert.Nil(t, snapshot)
			assert.NotContains(t, err.Error(), path)
		})
	}
}

func TestCursorBindingAndCheckpointRejectInvalidProgress(t *testing.T) {
	f := newCursorCaptureFixture(t, "", "", false)
	valid := InitialSourceBinding{AgentSessionID: f.state.AgentSessionID, SessionFile: f.sourcePath, StartOffsetKnown: true, SourcePrefixSHA256: f.state.SourcePrefixSHA256}
	for _, kind := range []string{"empty agent", "empty native ID", "empty file", "unknown offset", "negative offset", "missing file", "directory", "missing recording"} {
		t.Run(kind, func(t *testing.T) {
			binding, id := valid, f.state.AgentID
			switch kind {
			case "empty agent":
				id = ""
			case "empty native ID":
				binding.AgentSessionID = ""
			case "empty file":
				binding.SessionFile = ""
			case "unknown offset":
				binding.StartOffsetKnown = false
			case "negative offset":
				binding.StartOffset = -1
			case "missing file":
				binding.SessionFile += ".missing"
			case "directory":
				binding.SessionFile = t.TempDir()
			case "missing recording":
				id = "OxMissing"
			}
			state, err := BindInitialRecordingSource(f.projectRoot, id, binding)
			require.Error(t, err)
			assert.Nil(t, state)
		})
	}
	for _, tc := range []struct {
		name   string
		path   string
		offset int64
		delta  int
	}{{"missing path", "", 0, 0}, {"negative offset", f.sessionPath, -1, 0}, {"negative count", f.sessionPath, 1, -1}, {"missing marker", t.TempDir(), 1, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, CheckpointRecordingState(f.projectRoot, tc.path, tc.offset, tc.delta))
		})
	}
	state := loadCursorCaptureState(t, f)
	assert.Zero(t, state.EntryCount)
	assert.Zero(t, state.SourceOffset)
}
