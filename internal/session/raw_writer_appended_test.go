package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func writeAppendFixture(t *testing.T, state RecordingState) (raw, statePath string) {
	t.Helper()
	dir := t.TempDir()
	raw, statePath = filepath.Join(dir, "raw.jsonl"), filepath.Join(dir, ".recording.json")
	data, err := json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(statePath, data, 0600))
	return raw, statePath
}

// LastAppended must return the file's own bytes for exactly the last batch:
// not the header before it and not a carrier footer stamped after it. Live
// streaming spools these bytes, so any drift is a server/local divergence.
func TestLastAppendedIsExactlyTheCommittedBatchBytes(t *testing.T) {
	raw, statePath := writeAppendFixture(t, RecordingState{CommandRedactionVersion: 1})
	require.NoError(t, os.WriteFile(raw, []byte(`{"type":"header"}`+"\n"), 0600))

	w, err := NewRawWriter(raw, "")
	require.NoError(t, err)
	require.NoError(t, w.AppendRecordingBatch(statePath, []Entry{{Type: EntryTypeUser, Content: "hello"}, {Type: EntryTypeAssistant, Content: "hi"}}, 10))
	_, err = w.file.WriteString(`{"type":"footer"}` + "\n") // a later stamp must not leak into the batch
	require.NoError(t, err)
	require.NoError(t, w.Close())

	all, err := os.ReadFile(raw)
	require.NoError(t, err)
	header, footer := len(`{"type":"header"}`+"\n"), len(`{"type":"footer"}`+"\n")
	batch, ok, err := w.LastAppended()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, string(all[header:len(all)-footer]), string(batch.Bytes))
	require.False(t, batch.Suspended)
}

func TestLastAppendedReportsPauseFromTheStateAtCommit(t *testing.T) {
	now := time.Now()
	raw, statePath := writeAppendFixture(t, RecordingState{CommandRedactionVersion: 1, SuspendedAt: &now})
	w, err := NewRawWriter(raw, "")
	require.NoError(t, err)
	require.NoError(t, w.AppendRecordingBatch(statePath, []Entry{{Type: EntryTypeUser, Content: "secret while paused"}}, 10))
	require.NoError(t, w.Close())
	batch, ok, err := w.LastAppended()
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, batch.Suspended, "a batch committed while paused must be flagged so it is never streamed")
}

func TestLastAppendedIsEmptyAfterAFailedAppend(t *testing.T) {
	raw, statePath := writeAppendFixture(t, RecordingState{CommandRedactionVersion: 1, SourceOffset: 50})
	w, err := NewRawWriter(raw, "")
	require.NoError(t, err)
	require.Error(t, w.AppendRecordingBatch(statePath, []Entry{{Type: EntryTypeUser, Content: "x"}}, 10)) // cursor did not advance
	require.NoError(t, w.Close())
	_, ok, err := w.LastAppended()
	require.NoError(t, err)
	require.False(t, ok)
}
