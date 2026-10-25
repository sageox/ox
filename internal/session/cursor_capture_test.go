package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/session/cursorpaths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cursorCaptureConversationID = "123e4567-e89b-12d3-a456-426614174099"

type fixedCursorIncrementalReader struct {
	entries []adapters.RawEntry
	next    int64
	err     error
	mutate  func(string)
	entered chan<- struct{}
	release <-chan struct{}

	calls   int
	path    string
	offsets []int64
}

func (r *fixedCursorIncrementalReader) ReadFromOffset(path string, offset int64) ([]adapters.RawEntry, int64, error) {
	r.calls++
	r.path = path
	r.offsets = append(r.offsets, offset)
	if r.entered != nil {
		r.entered <- struct{}{}
	}
	if r.release != nil {
		<-r.release
	}
	if r.mutate != nil {
		r.mutate(path)
	}
	return append([]adapters.RawEntry(nil), r.entries...), r.next, r.err
}

type cursorCaptureFixture struct {
	projectRoot string
	homeDir     string
	sourcePath  string
	sessionPath string
	rawPath     string
	state       *RecordingState
	prefix      []byte
}

func newCursorCaptureFixture(t *testing.T, prefix, suffix string, pending bool) *cursorCaptureFixture {
	t.Helper()
	projectRoot := setupRecordingTest(t, t.TempDir())
	homeDir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	sourcePath, err := cursorpaths.SessionPath(homeDir, projectRoot, cursorCaptureConversationID)
	require.NoError(t, err)
	if !pending {
		require.NoError(t, os.MkdirAll(filepath.Dir(sourcePath), 0o755))
		require.NoError(t, os.WriteFile(sourcePath, []byte(prefix+suffix), 0o600))
	}

	opts := StartRecordingOptions{
		AgentID:            "OxCursorCapture",
		AgentSessionID:     cursorCaptureConversationID,
		AdapterName:        "cursor",
		OutputFile:         filepath.Join(t.TempDir(), "session.md"),
		WorkspacePath:      projectRoot,
		WatchMode:          "tail",
		StartOffsetKnown:   true,
		SourcePrefixSHA256: cursorEmptyPrefixSHA256ForTest(),
	}
	if !pending {
		opts.SessionFile = sourcePath
		opts.StartOffset = int64(len(prefix))
		opts.SourcePrefixSHA256 = cursorSourceHash([]byte(prefix))
	}
	state, err := StartRecording(projectRoot, opts)
	require.NoError(t, err)
	return &cursorCaptureFixture{
		projectRoot: projectRoot, homeDir: homeDir, sourcePath: sourcePath,
		sessionPath: state.SessionPath, rawPath: filepath.Join(state.SessionPath, "raw.jsonl"),
		state: state, prefix: []byte(prefix),
	}
}

func cursorEmptyPrefixSHA256ForTest() string { return cursorSourceHash(nil) }

func drainCursorFixture(t *testing.T, f *cursorCaptureFixture, reader adapters.IncrementalReader, final bool) (*CursorCaptureResult, error) {
	t.Helper()
	var result *CursorCaptureResult
	err := fileutil.WithFileLock(context.Background(), f.rawPath, func() error {
		var err error
		result, err = DrainCursorSource(context.Background(), f.projectRoot, f.sessionPath, f.homeDir, reader, final)
		return err
	})
	return result, err
}

func loadCursorCaptureState(t *testing.T, f *cursorCaptureFixture) *RecordingState {
	t.Helper()
	state, err := LoadRecordingStateForAgent(f.projectRoot, f.state.AgentID)
	require.NoError(t, err)
	require.NotNil(t, state)
	return state
}

func TestDrainCursorSource_CapturesOnlyAfterBoundaryInFaithfulOrder(t *testing.T) {
	old := `{"type":"turn_ended","status":"success"}` + "\n"
	suffix := `{"role":"user"}` + "\n" + `{"role":"assistant"}` + "\n" + `{"role":"tool"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
	f := newCursorCaptureFixture(t, old, suffix, false)
	reader := &fixedCursorIncrementalReader{
		next: int64(len(old + suffix)),
		entries: []adapters.RawEntry{
			{Role: "user", Content: "fresh prompt"}, // unknown native timestamp stays zero
			{Role: "assistant", Content: "fresh response"},
			{Role: "tool", ToolName: "shell", ToolInput: "AKIAIOSFODNN7EXAMPLE"},
		},
	}

	result, err := drainCursorFixture(t, f, reader, false)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 3, result.Entries)
	assert.True(t, result.Complete)
	assert.Equal(t, f.sourcePath, reader.path)
	assert.Equal(t, []int64{int64(len(old))}, reader.offsets)

	state := loadCursorCaptureState(t, f)
	assert.Equal(t, int64(len(old+suffix)), state.SourceOffset)
	assert.Equal(t, 3, state.EntryCount)
	assert.Equal(t, cursorSourceHash([]byte(old+suffix)), state.SourcePrefixSHA256)

	data, err := os.ReadFile(f.rawPath)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "old")
	assert.NotContains(t, string(data), "AKIAIOSFODNN7EXAMPLE")
	entries, _, err := completeRawRecords(data)
	require.NoError(t, err)
	require.Len(t, entries, 3)
	assert.Equal(t, "user", entries[0]["type"])
	assert.Equal(t, "assistant", entries[1]["type"])
	assert.Equal(t, "tool", entries[2]["type"])
	assert.Equal(t, "0001-01-01T00:00:00Z", entries[0]["ts"])
	assert.Empty(t, entries[2]["tool_output"])
	assert.Contains(t, entries[2]["tool_input"], "[REDACTED")
}

func TestDrainCursorSource_MetadataOnlyAdvancesOffset(t *testing.T) {
	source := `{"type":"metadata","model":"fixture"}` + "\n"
	f := newCursorCaptureFixture(t, "", source, false)
	reader := &fixedCursorIncrementalReader{next: int64(len(source))}

	result, err := drainCursorFixture(t, f, reader, false)
	require.NoError(t, err)
	assert.Zero(t, result.Entries)
	assert.False(t, result.Complete)
	state := loadCursorCaptureState(t, f)
	assert.Equal(t, int64(len(source)), state.SourceOffset)
	assert.Zero(t, state.EntryCount)
}

func TestDrainCursorSource_ReportsIncompleteUntilTerminalLine(t *testing.T) {
	old := `{"type":"turn_ended","status":"success"}` + "\n"
	partial := `{"role":"assistant"}`
	f := newCursorCaptureFixture(t, old, partial, false)
	reader := &fixedCursorIncrementalReader{next: int64(len(old))}

	result, err := drainCursorFixture(t, f, reader, true)
	require.NoError(t, err, "an incomplete final export is a successful pending observation")
	assert.False(t, result.Complete)
	assert.Zero(t, result.Entries)

	completeSuffix := partial + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
	require.NoError(t, os.WriteFile(f.sourcePath, []byte(old+completeSuffix), 0o600))
	reader.entries = []adapters.RawEntry{{Role: "assistant", Content: "late response"}}
	reader.next = int64(len(old + completeSuffix))
	result, err = drainCursorFixture(t, f, reader, true)
	require.NoError(t, err)
	assert.True(t, result.Complete)
	assert.Equal(t, 1, result.Entries)
}

func TestDrainCursorSource_RefusesChangedSourceWithoutMutatingStateOrRaw(t *testing.T) {
	old := `{"type":"turn_ended","status":"success"}` + "\n"
	suffix := `{"role":"user"}` + "\n"
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, f *cursorCaptureFixture, reader *fixedCursorIncrementalReader)
	}{
		{
			name: "replacement during adapter read",
			prepare: func(t *testing.T, _ *cursorCaptureFixture, reader *fixedCursorIncrementalReader) {
				t.Helper()
				reader.mutate = func(path string) {
					require.NoError(t, os.WriteFile(path, []byte(old+`{"role":"replacement"}`+"\n"), 0o600))
				}
			},
		},
		{
			name: "shrink before read",
			prepare: func(t *testing.T, f *cursorCaptureFixture, _ *fixedCursorIncrementalReader) {
				t.Helper()
				require.NoError(t, os.WriteFile(f.sourcePath, []byte(`{"role":"short"}`+"\n"), 0o600))
			},
		},
		{
			name: "persisted prefix hash mismatch",
			prepare: func(t *testing.T, f *cursorCaptureFixture, _ *fixedCursorIncrementalReader) {
				t.Helper()
				require.NoError(t, UpdateRecordingStateForAgent(f.projectRoot, f.state.AgentID, func(state *RecordingState) {
					state.SourcePrefixSHA256 = cursorSourceHash([]byte("different acknowledged prefix"))
				}))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCursorCaptureFixture(t, old, suffix, false)
			beforeRaw := []byte(`{"type":"user","content":"already captured"}` + "\n")
			require.NoError(t, os.WriteFile(f.rawPath, beforeRaw, 0o600))
			reader := &fixedCursorIncrementalReader{
				next: int64(len(old + suffix)), entries: []adapters.RawEntry{{Role: "user", Content: "new"}},
			}
			tc.prepare(t, f, reader)

			_, err := drainCursorFixture(t, f, reader, false)
			require.ErrorIs(t, err, ErrCursorSourceChanged)
			state := loadCursorCaptureState(t, f)
			assert.Equal(t, int64(len(old)), state.SourceOffset)
			assert.Zero(t, state.EntryCount)
			afterRaw, readErr := os.ReadFile(f.rawPath)
			require.NoError(t, readErr)
			assert.Equal(t, beforeRaw, afterRaw)
		})
	}
}

func TestDrainCursorSource_PendingKnownZeroBindsWhenExportAppears(t *testing.T) {
	f := newCursorCaptureFixture(t, "", "", true)
	reader := &fixedCursorIncrementalReader{}
	_, err := drainCursorFixture(t, f, reader, false)
	require.ErrorIs(t, err, ErrCursorSourcePending)
	state := loadCursorCaptureState(t, f)
	assert.Empty(t, state.SessionFile)
	assert.Zero(t, state.SourceOffset)
	assert.Equal(t, cursorEmptyPrefixSHA256ForTest(), state.SourcePrefixSHA256)

	source := `{"role":"user"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
	require.NoError(t, os.MkdirAll(filepath.Dir(f.sourcePath), 0o755))
	require.NoError(t, os.WriteFile(f.sourcePath, []byte(source), 0o600))
	reader.entries = []adapters.RawEntry{{Role: "user", Content: "first prompt"}}
	reader.next = int64(len(source))
	result, err := drainCursorFixture(t, f, reader, false)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Entries)
	state = loadCursorCaptureState(t, f)
	assert.Equal(t, f.sourcePath, state.SessionFile)
	assert.Equal(t, int64(len(source)), state.SourceOffset)
	assert.Equal(t, 1, state.EntryCount)
}

func TestDrainCursorSource_ReconcilesDurableAppendAfterCheckpointFailure(t *testing.T) {
	old := `{"type":"turn_ended","status":"success"}` + "\n"
	suffix := `{"role":"user"}` + "\n" + `{"role":"assistant"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
	f := newCursorCaptureFixture(t, old, suffix, false)
	reader := &fixedCursorIncrementalReader{next: int64(len(old + suffix)), entries: []adapters.RawEntry{{Role: "user", Content: "once"}, {Role: "assistant", Content: "only once"}}}

	original := writeRecordingStateAtomically
	writeRecordingStateAtomically = func(string, any, os.FileMode) error { return errors.New("injected checkpoint failure") }
	t.Cleanup(func() { writeRecordingStateAtomically = original })
	_, err := drainCursorFixture(t, f, reader, false)
	writeRecordingStateAtomically = original
	require.ErrorContains(t, err, "checkpoint Cursor capture")
	state := loadCursorCaptureState(t, f)
	assert.Equal(t, int64(len(old)), state.SourceOffset)
	assert.Zero(t, state.EntryCount)

	result, err := drainCursorFixture(t, f, reader, false)
	require.NoError(t, err)
	assert.Equal(t, 2, result.Entries, "reconciled entries are newly acknowledged exactly once")
	data, readErr := os.ReadFile(f.rawPath)
	require.NoError(t, readErr)
	entries, _, parseErr := completeRawRecords(data)
	require.NoError(t, parseErr)
	require.Len(t, entries, 2)
	assert.Equal(t, "once", entries[0]["content"])
	assert.Equal(t, "only once", entries[1]["content"])
}

func TestDrainCursorSource_LinearizesConcurrentLifecycleAfterCheckpoint(t *testing.T) {
	old := `{"type":"turn_ended","status":"success"}` + "\n"
	suffix := `{"role":"user"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
	f := newCursorCaptureFixture(t, old, suffix, false)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	reader := &fixedCursorIncrementalReader{next: int64(len(old + suffix)), entries: []adapters.RawEntry{{Role: "user", Content: "serialized"}}, entered: entered, release: release}

	var drainResult *CursorCaptureResult
	var drainErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		drainResult, drainErr = drainCursorFixture(t, f, reader, false)
	}()
	<-entered
	lifecycleDone := make(chan error, 1)
	go func() {
		lifecycleDone <- UpdateRecordingStateForAgent(f.projectRoot, f.state.AgentID, func(state *RecordingState) {
			now := time.Now().UTC()
			state.SuspendedAt = &now
			state.Lifecycle = append(state.Lifecycle, LifecycleEvent{Action: LifecycleActionPause, At: now, Seq: state.EntryCount})
		})
	}()
	close(release)
	wg.Wait()
	require.NoError(t, drainErr)
	require.NotNil(t, drainResult)
	require.NoError(t, <-lifecycleDone)

	state := loadCursorCaptureState(t, f)
	assert.Equal(t, 1, state.EntryCount)
	require.NotNil(t, state.SuspendedAt)
	require.NotEmpty(t, state.Lifecycle)
	assert.Equal(t, 1, state.Lifecycle[len(state.Lifecycle)-1].Seq)
}
