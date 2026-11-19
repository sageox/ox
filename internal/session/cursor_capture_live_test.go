package session

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rawSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(t, err)
	return info.Size()
}

// Failure prevented: Cursor captures through its own transaction, not
// AppendRecordingBatch, so no committed Cursor bytes ever reached the live
// spool and a streamed Cursor session held only its header on SageOx (C8).
// The transaction must hand back exactly what it appended, flagged paused when
// the recording was paused, so the spool can skip paused content.
func TestDrainCursorSource_ReturnsTheBytesItAppended(t *testing.T) {
	cases := []struct {
		name   string
		paused bool
	}{
		{"recording", false},
		{"paused", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			suffix := `{"role":"user"}` + "\n" + `{"role":"assistant"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
			f := newCursorCaptureFixture(t, "", suffix, false)
			if tc.paused {
				now := time.Now().UTC()
				require.NoError(t, UpdateRecordingStateForAgent(f.projectRoot, f.state.AgentID, func(s *RecordingState) { s.SuspendedAt = &now }))
			}
			before := rawSize(t, f.rawPath)
			reader := &fixedCursorIncrementalReader{
				next:    int64(len(suffix)),
				entries: []adapters.RawEntry{{Role: "user", Content: "prompt"}, {Role: "assistant", Content: "answer"}},
			}

			result, err := drainCursorFixture(t, f, reader, false)
			require.NoError(t, err)

			data, err := os.ReadFile(f.rawPath)
			require.NoError(t, err)
			require.Greater(t, int64(len(data)), before)
			assert.Equal(t, string(data[before:]), string(result.Appended.Bytes), "exactly the committed bytes")
			assert.Equal(t, tc.paused, result.Appended.Suspended)

			// a drain with nothing new appends nothing and hands back nothing
			again, err := drainCursorFixture(t, f, &fixedCursorIncrementalReader{next: int64(len(suffix))}, false)
			require.NoError(t, err)
			assert.Empty(t, again.Appended.Bytes)
		})
	}
}

// The stop path finalizes Cursor through FinalizeCursorCapture, whose final
// drain is the last capture of the session: its bytes must reach the spool too.
func TestFinalizeCursorCapture_ReturnsTheFinalDrainsBytes(t *testing.T) {
	suffix := `{"role":"user"}` + "\n" + `{"type":"turn_ended","status":"success"}` + "\n"
	f := newCursorCaptureFixture(t, "", suffix, false)
	before := rawSize(t, f.rawPath)
	reader := &fixedCursorIncrementalReader{next: int64(len(suffix)), entries: []adapters.RawEntry{{Role: "user", Content: "last words"}}}

	var batch AppendedBatch
	require.NoError(t, fileutil.WithFileLock(context.Background(), f.rawPath, func() error {
		_, b, err := FinalizeCursorCapture(context.Background(), f.projectRoot, f.sessionPath, f.homeDir, reader)
		batch = b
		return err
	}))

	// the finalize rewrite masks pauses and flags the header afterwards; the
	// batch is what the final drain appended, before that rewrite
	assert.Contains(t, string(batch.Bytes), "last words")
	assert.NotContains(t, string(batch.Bytes), "cursor_finalized")
	assert.False(t, batch.Suspended)
	assert.Zero(t, before, "nothing was captured before the final drain")
}
