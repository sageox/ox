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

// The batch AppendRecordingBatch returns must be the file's own bytes for
// exactly that batch: not the header before it, and not a footer stamped after
// it. Live streaming spools these bytes, so any drift is a server/local
// divergence.
func TestAppendRecordingBatchReturnsExactlyTheCommittedBytes(t *testing.T) {
	t.Parallel()
	raw, statePath := writeAppendFixture(t, RecordingState{CommandRedactionVersion: 1})
	require.NoError(t, os.WriteFile(raw, []byte(`{"type":"header"}`+"\n"), 0600))

	w, err := NewRawWriter(raw, "")
	require.NoError(t, err)
	batch, err := w.AppendRecordingBatch(statePath, []Entry{{Type: EntryTypeUser, Content: "hello"}, {Type: EntryTypeAssistant, Content: "hi"}}, 10)
	require.NoError(t, err)
	_, err = w.file.WriteString(`{"type":"footer"}` + "\n") // a later stamp must not leak into the batch
	require.NoError(t, err)
	require.NoError(t, w.Close())

	all, err := os.ReadFile(raw)
	require.NoError(t, err)
	header, footer := len(`{"type":"header"}`+"\n"), len(`{"type":"footer"}`+"\n")
	require.Equal(t, string(all[header:len(all)-footer]), string(batch.Bytes))
	require.False(t, batch.Suspended)
}

// Failure prevented: the capture paths used to read a committed batch back
// from raw.jsonl to spool it, after the cursor had moved past it. A file that
// could not be read then cost the live stream the batch with no gap line. The
// batch now comes from the append itself, so a raw.jsonl that cannot be read
// back still yields every committed byte.
func TestAppendRecordingBatchNeverReadsRawJSONLBack(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("reason: root ignores file permissions, so an unreadable raw.jsonl cannot be simulated")
	}
	raw, statePath := writeAppendFixture(t, RecordingState{CommandRedactionVersion: 1})
	require.NoError(t, os.WriteFile(raw, []byte(`{"type":"header"}`+"\n"), 0600))
	w, err := NewRawWriter(raw, "")
	require.NoError(t, err)
	require.NoError(t, os.Chmod(raw, 0o200)) // appendable, never readable
	t.Cleanup(func() { _ = os.Chmod(raw, 0o600) })

	batch, err := w.AppendRecordingBatch(statePath, []Entry{{Type: EntryTypeUser, Content: "committed while unreadable"}}, 10)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	require.NoError(t, os.Chmod(raw, 0o600))
	all, err := os.ReadFile(raw)
	require.NoError(t, err)
	require.Equal(t, string(all[len(`{"type":"header"}`+"\n"):]), string(batch.Bytes))
}

func TestAppendRecordingBatchReportsPauseFromTheStateAtCommit(t *testing.T) {
	t.Parallel()
	now := time.Now()
	raw, statePath := writeAppendFixture(t, RecordingState{CommandRedactionVersion: 1, SuspendedAt: &now})
	w, err := NewRawWriter(raw, "")
	require.NoError(t, err)
	batch, err := w.AppendRecordingBatch(statePath, []Entry{{Type: EntryTypeUser, Content: "secret while paused"}}, 10)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.NotEmpty(t, batch.Bytes)
	require.True(t, batch.Suspended, "a batch committed while paused must be flagged so it is never streamed")
}

// A failed batch hands nothing on, and the bytes of a failed batch never leak
// into the next one.
func TestAppendRecordingBatchReturnsNothingForAFailedAppend(t *testing.T) {
	t.Parallel()
	raw, statePath := writeAppendFixture(t, RecordingState{CommandRedactionVersion: 1, SourceOffset: 50})
	w, err := NewRawWriter(raw, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })

	batch, err := w.AppendRecordingBatch(statePath, []Entry{{Type: EntryTypeUser, Content: "x"}}, 10) // cursor did not advance
	require.Error(t, err)
	require.Empty(t, batch.Bytes)

	before, err := os.ReadFile(raw)
	require.NoError(t, err)
	batch, err = w.AppendRecordingBatch(statePath, []Entry{{Type: EntryTypeUser, Content: "next"}}, 60)
	require.NoError(t, err)
	after, err := os.ReadFile(raw)
	require.NoError(t, err)
	require.Equal(t, string(after[len(before):]), string(batch.Bytes))
}
