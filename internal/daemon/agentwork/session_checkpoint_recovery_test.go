package agentwork

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type checkpointRecoveryAdapter struct {
	timestamp time.Time
	single    bool
}

func (a *checkpointRecoveryAdapter) Name() string { return "codex" }
func (a *checkpointRecoveryAdapter) Detect() bool { return false }
func (a *checkpointRecoveryAdapter) FindSessionFile(adapters.SessionLookup) (string, error) {
	return "", adapters.ErrSessionNotFound
}
func (a *checkpointRecoveryAdapter) Read(string) ([]adapters.RawEntry, error) { return nil, nil }
func (a *checkpointRecoveryAdapter) ReadMetadata(string) (*adapters.SessionMetadata, error) {
	return nil, nil
}
func (a *checkpointRecoveryAdapter) Watch(context.Context, string) (<-chan adapters.RawEntry, error) {
	return nil, adapters.ErrWatchNotSupported
}
func (a *checkpointRecoveryAdapter) ReadFromOffset(path string, offset int64) ([]adapters.RawEntry, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, 0); err != nil {
		return nil, offset, err
	}
	var entries []adapters.RawEntry
	nextOffset := offset
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		entries = append(entries, adapters.RawEntry{Role: "assistant", Content: scanner.Text(), Timestamp: a.timestamp})
		if a.single {
			nextOffset += int64(len(scanner.Bytes()) + 1)
			break
		}
	}
	info, err := f.Stat()
	if err != nil {
		return nil, offset, err
	}
	if !a.single {
		nextOffset = info.Size()
	}
	return entries, nextOffset, scanner.Err()
}

func TestSessionWatcherRestart_RollsBackSealedUncheckpointedFirstBatch(t *testing.T) {
	home := t.TempDir()
	adapter := &checkpointRecoveryAdapter{timestamp: time.Now().UTC().Truncate(time.Second)}
	original, err := adapters.GetAdapter("codex")
	require.NoError(t, err)
	adapters.Unregister("codex")
	adapters.Register(adapter)
	t.Cleanup(func() {
		adapters.Unregister("codex")
		adapters.Register(original)
	})

	sourceDir := filepath.Join(home, ".codex", "sessions")
	require.NoError(t, os.MkdirAll(sourceDir, 0700))
	source := filepath.Join(sourceDir, "native.jsonl")
	require.NoError(t, os.WriteFile(source, []byte("first\nsecond\n"), 0600))
	cachePath := t.TempDir()
	rawPath := filepath.Join(cachePath, artifactRaw)
	entries := []session.Entry{
		{Type: session.EntryTypeAssistant, Content: "first", Timestamp: adapter.timestamp},
		{Type: session.EntryTypeAssistant, Content: "second", Timestamp: adapter.timestamp},
	}
	state := session.RecordingState{
		AgentID: "OxWatcherRecover", AdapterName: "codex", WatchMode: "tail",
		SessionFile: source, SessionPath: cachePath, ParentPID: os.Getpid(),
		StartedAt: time.Now().Add(-time.Hour), SourceOffset: 0, StartOffset: 0, EntryCount: 0,
	}
	writeRecordingState(t, filepath.Join(cachePath, recordingMarker), state)
	writeSealedUncheckpointedBatch(t, rawPath, 0, int64(len("first\nsecond\n")), entries)
	require.FileExists(t, rawPath+".append.json")

	mgr := NewSessionWatcherManager(slog.Default())
	mgr.SetHomeDirForTest(home)
	t.Cleanup(mgr.StopAll)
	require.NoError(t, mgr.StartWatch("restart-recovery", source, "codex", t.TempDir(), cachePath))
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(filepath.Join(cachePath, recordingMarker))
		if err != nil {
			return false
		}
		var got session.RecordingState
		return json.Unmarshal(data, &got) == nil && got.SourceOffset == int64(len("first\nsecond\n")) && got.EntryCount == 2
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, 2, countRawJSONLEntries(t, rawPath))
	require.Eventually(t, func() bool {
		_, err := os.Stat(rawPath + ".append.json")
		return os.IsNotExist(err)
	}, time.Second, 10*time.Millisecond)
}

func TestSessionWatcher_RejectsAcknowledgedEntriesWithoutSourceCursor(t *testing.T) {
	home, source, cachePath, _ := checkpointWatcherFixture(t)
	state := loadCheckpointState(t, cachePath)
	state.EntryCount = 1
	writeRecordingState(t, filepath.Join(cachePath, recordingMarker), state)

	mgr := NewSessionWatcherManager(slog.Default())
	mgr.SetHomeDirForTest(home)
	t.Cleanup(mgr.StopAll)
	err := mgr.StartWatch("missing-cursor", source, "codex", t.TempDir(), cachePath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "acknowledged entries without a source cursor")
}

func TestSessionWatcherRestart_RollsBackSealedUncheckpointedLaterBatch(t *testing.T) {
	home, source, cachePath, _ := checkpointWatcherFixture(t, true)
	require.NoError(t, os.WriteFile(source, []byte("first\nsecond\nthird\n"), 0600))

	mgr := NewSessionWatcherManager(slog.Default())
	mgr.SetHomeDirForTest(home)
	t.Cleanup(mgr.StopAll)
	const watcher = "later-checkpoint-retry"
	require.NoError(t, mgr.StartWatch(watcher, source, "codex", t.TempDir(), cachePath))
	firstOffset := int64(len("first\n"))
	require.Eventually(t, func() bool {
		got := loadCheckpointState(t, cachePath)
		return got.SourceOffset == firstOffset && got.EntryCount == 1
	}, 5*time.Second, 10*time.Millisecond)
	mgr.StopWatch(watcher)

	writeSealedUncheckpointedBatch(t, filepath.Join(cachePath, artifactRaw), firstOffset,
		int64(len("first\nsecond\n")), []session.Entry{{Type: session.EntryTypeAssistant, Content: "second"}})
	require.FileExists(t, filepath.Join(cachePath, artifactRaw)+".append.json")
	require.NoError(t, mgr.StartWatch(watcher, source, "codex", t.TempDir(), cachePath))
	require.Eventually(t, func() bool {
		got := loadCheckpointState(t, cachePath)
		return got.SourceOffset == int64(len("first\nsecond\nthird\n")) && got.EntryCount == 3
	}, 10*time.Second, 10*time.Millisecond)
	assert.Equal(t, 3, countRawJSONLEntries(t, filepath.Join(cachePath, artifactRaw)))
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(cachePath, artifactRaw) + ".append.json")
		return os.IsNotExist(err)
	}, time.Second, 10*time.Millisecond)
}

func checkpointWatcherFixture(t *testing.T, single ...bool) (home, source, cachePath string, adapter *checkpointRecoveryAdapter) {
	t.Helper()
	home = t.TempDir()
	adapter = &checkpointRecoveryAdapter{timestamp: time.Now().UTC().Truncate(time.Second), single: len(single) > 0 && single[0]}
	original, err := adapters.GetAdapter("codex")
	require.NoError(t, err)
	adapters.Unregister("codex")
	adapters.Register(adapter)
	t.Cleanup(func() {
		adapters.Unregister("codex")
		adapters.Register(original)
	})

	sourceDir := filepath.Join(home, ".codex", "sessions")
	require.NoError(t, os.MkdirAll(sourceDir, 0700))
	source = filepath.Join(sourceDir, "native.jsonl")
	require.NoError(t, os.WriteFile(source, []byte("first\nsecond\n"), 0600))
	cachePath = t.TempDir()
	writeRecordingState(t, filepath.Join(cachePath, recordingMarker), session.RecordingState{
		AgentID: "OxWatcherFailure", AdapterName: "codex", WatchMode: "tail",
		SessionFile: source, SessionPath: cachePath, ParentPID: os.Getpid(),
		StartedAt: time.Now().Add(-time.Hour), SourceOffset: 0, StartOffset: 0,
	})
	return home, source, cachePath, adapter
}

func loadCheckpointState(t *testing.T, cachePath string) session.RecordingState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(cachePath, recordingMarker))
	require.NoError(t, err)
	var state session.RecordingState
	require.NoError(t, json.Unmarshal(data, &state))
	return state
}

// writeSealedUncheckpointedBatch models the exact crash boundary after raw
// bytes reach disk but before AppendRecordingBatch publishes the new cursor.
// The next watcher must settle this journal before it reads the source again.
func writeSealedUncheckpointedBatch(t *testing.T, rawPath string, oldOffset, newOffset int64, entries []session.Entry) {
	t.Helper()
	rw, err := session.NewRawWriter(rawPath, "")
	require.NoError(t, err)
	require.NoError(t, rw.BeginAppend(oldOffset, newOffset))
	for i := range entries {
		require.NoError(t, rw.WriteEntry(&entries[i]))
	}
	require.NoError(t, rw.SealAppend())
	require.NoError(t, rw.CloseAndSync())
}
