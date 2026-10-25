package agentwork

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/require"
)

type checkpointFailureReader struct {
	onRead func(int64)
}

func (r checkpointFailureReader) ReadFromOffset(_ string, offset int64) ([]adapters.RawEntry, int64, error) {
	r.onRead(offset)
	if offset > 0 {
		return nil, offset, nil
	}
	return []adapters.RawEntry{{Role: "user", Content: "retry-this-batch"}}, 1, nil
}

// Failure prevented: a failed checkpoint leaves appended entries behind, so a
// restart from the old durable cursor writes them a second time.
func TestPollSessionCheckpointFailureDoesNotDuplicateOnRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("short: exercises real poll intervals and restart")
	}
	dir := t.TempDir()
	raw := filepath.Join(dir, "raw.jsonl")
	prior := []byte("{\"type\":\"header\",\"metadata\":{}}\n")
	require.NoError(t, os.WriteFile(raw, prior, 0600))
	marker := filepath.Join(dir, recordingMarker)
	state, err := json.Marshal(session.RecordingState{SessionPath: dir})
	require.NoError(t, err)
	// A directory in place of the marker fails reads on every platform.
	require.NoError(t, os.Mkdir(marker, 0700))
	mgr := newTestWatcherManager(t)
	aw := &activeWatcher{cachePath: dir, sessionName: "checkpoint-test"}
	run := func() {
		rw, err := session.NewRawWriter(raw, "")
		require.NoError(t, err)
		defer rw.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		reader := checkpointFailureReader{onRead: func(offset int64) {
			if offset > 0 {
				cancel()
			}
		}}
		mgr.pollSession(ctx, aw, reader, rw, 0)
	}
	run()
	got, err := os.ReadFile(raw)
	require.NoError(t, err)
	require.Equal(t, string(prior), string(got), "failed publication must preserve the prior raw prefix without an uncheckpointed batch")
	require.NoError(t, os.Remove(marker))
	require.NoError(t, os.WriteFile(marker, state, 0600))
	run()
	got, err = os.ReadFile(raw)
	require.NoError(t, err)
	require.Contains(t, string(got), "retry-this-batch")
	require.Equal(t, 1, countOccurrences(t, raw, "retry-this-batch"))
	recovered, err := session.ReadRecordingStateFile(dir)
	require.NoError(t, err)
	require.EqualValues(t, 1, recovered.SourceOffset)
	require.Equal(t, 1, recovered.EntryCount)
}

type catchUpCheckpointFailureAdapter struct {
	*testAdapter
	blockPublication func()
}

func (a catchUpCheckpointFailureAdapter) ReadFromOffset(_ string, offset int64) ([]adapters.RawEntry, int64, error) {
	a.blockPublication()
	return []adapters.RawEntry{{Role: "user", Content: "catch-up batch"}}, offset + 1, nil
}

// Catch-up runs before polling and must use the same failure handling: neither
// a disappearing marker nor a blocked replacement can leave a duplicate batch.
func TestCatchUpCheckpointFailurePreservesPrefix(t *testing.T) {
	for _, failure := range []string{"missing-marker", "blocked-publication"} {
		t.Run(failure, func(t *testing.T) {
			if failure == "blocked-publication" && (runtime.GOOS == "windows" || os.Geteuid() == 0) {
				t.Skip("requires enforced Unix directory permissions; Windows sharing is covered in session tests")
			}
			dir := t.TempDir()
			marker := filepath.Join(dir, recordingMarker)
			writeRecordingState(t, marker, session.RecordingState{SourceOffset: 1})
			raw := filepath.Join(dir, "raw.jsonl")
			prefix := []byte("{\"type\":\"header\"}\n")
			require.NoError(t, os.WriteFile(raw, prefix, 0600))
			adapter := catchUpCheckpointFailureAdapter{testAdapter: &testAdapter{name: "codex"}, blockPublication: func() {
				if failure == "missing-marker" {
					require.NoError(t, os.Remove(marker))
				} else {
					require.NoError(t, os.Chmod(dir, 0500))
					t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
				}
			}}
			mgr := newTestWatcherManager(t)
			aw := &activeWatcher{cachePath: dir, sessionName: "catch-up", done: make(chan struct{})}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			mgr.wg.Add(1)
			mgr.runWatcher(ctx, aw, adapter, raw)
			require.NoError(t, ctx.Err(), "failed checkpoint must stop immediately")
			data, err := os.ReadFile(raw)
			require.NoError(t, err)
			require.Equal(t, prefix, data)
			if failure == "blocked-publication" {
				stored, err := session.ReadRecordingStateFile(dir)
				require.NoError(t, err)
				require.EqualValues(t, 1, stored.SourceOffset)
				require.Zero(t, stored.EntryCount)
			}
		})
	}
}
