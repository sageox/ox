package agentwork

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// commitAs commits one batch to raw.jsonl in a folder rec records, as every
// daemon capture path does, and returns what the append handed on.
func commitAs(t *testing.T, rec session.RecordingState) (dir string, batch session.AppendedBatch) {
	t.Helper()
	dir, _, rw := newAppendFixture(t, rec)
	batch, err := rw.AppendRecordingBatch(filepath.Join(dir, recordingMarker),
		[]session.Entry{{Type: session.EntryTypeUser, Content: "a turn the first recording committed"}}, 10)
	require.NoError(t, err)
	require.NotEmpty(t, batch.Bytes, "precondition: the batch was committed")
	return dir, batch
}

// Failure prevented (ox#1961 review): the daemon reads the recording marker
// again after a commit, for the manual-publishing choice a stop writes there.
// Marker writers do not take the raw lock, so another recording can take the
// folder over in between, and the batch was spooled into that recording's
// stream (C4). Only the stream of the recording that committed the batch may
// take it; anyone else's loses nothing, so nothing is declared there either.
func TestSpoolCommittedKeepsABatchOutOfAStreamItWasNotCommittedFor(t *testing.T) {
	pausedAt := time.Now()
	cases := []struct {
		name   string
		rec    session.RecordingState
		arrive func(t *testing.T, dir string)
	}{
		{"another recording took the folder over and prepared its own stream", session.RecordingState{SessionID: watcherLiveSessionID},
			func(t *testing.T, dir string) {
				writeRecordingState(t, filepath.Join(dir, recordingMarker), session.RecordingState{SessionID: otherLiveSessionID, CommandRedactionVersion: 1})
				prepareLive(t, dir, otherLiveSessionID)
			}},
		{"a paused batch whose recording was cleared", session.RecordingState{SessionID: watcherLiveSessionID, SuspendedAt: &pausedAt},
			func(t *testing.T, dir string) {
				prepareLive(t, dir, watcherLiveSessionID)
				require.NoError(t, os.Remove(filepath.Join(dir, recordingMarker)))
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, batch := commitAs(t, tc.rec)
			tc.arrive(t, dir)

			spooled, err := spoolCommitted(slog.Default(), dir, "", batch)

			require.NoError(t, err, "no stream lost anything")
			assert.False(t, spooled)
			assert.Empty(t, spoolOf(t, dir), "the batch reached a stream it was not committed for")
			assert.NoFileExists(t, filepath.Join(dir, "spool-lost"), "a batch that is not the stream's is not its loss")
		})
	}
}

// Failure prevented (ox#1961 review): when the recording could not be read
// again after its commit (a concurrent clear removed it, it could not be
// parsed, or the folder now holds another recording), the step logged at
// Debug and returned. The cursor was already past the batch, so the stream
// skipped it with no gap in its place. The batch is declared in the stream of
// the recording that committed it.
func TestSpoolCommittedDeclaresABatchWhoseRecordingCannotBeReloaded(t *testing.T) {
	cases := []struct {
		name string
		lose func(t *testing.T, marker string)
	}{
		{"the marker was cleared", func(t *testing.T, marker string) {
			require.NoError(t, os.Remove(marker))
		}},
		{"the marker cannot be parsed", func(t *testing.T, marker string) {
			require.NoError(t, os.WriteFile(marker, []byte("{not a recording"), 0o600))
		}},
		{"another recording took the folder before preparing a stream", func(t *testing.T, marker string) {
			writeRecordingState(t, marker, session.RecordingState{SessionID: otherLiveSessionID, CommandRedactionVersion: 1})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, batch := commitAs(t, session.RecordingState{SessionID: watcherLiveSessionID})
			prepareLive(t, dir, watcherLiveSessionID)
			tc.lose(t, filepath.Join(dir, recordingMarker))

			spooled, err := spoolCommitted(slog.Default(), dir, "", batch)

			require.Error(t, err, "the batch was neither spooled nor declared")
			assert.False(t, spooled)
			assert.Empty(t, spoolOf(t, dir), "precondition: nothing was spooled")
			g := nextLiveGap(t, dir)
			assert.Equal(t, "spool_write_failed", g.Reason)
			assert.Equal(t, int64(len(batch.Bytes)), g.Bytes, "the gap must be the size of the batch the stream never got")
		})
	}
}
