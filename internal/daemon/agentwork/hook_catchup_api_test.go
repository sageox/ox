package agentwork

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A transcript the capture rediscovers (Claude Code starts a new file after
// compaction) is confined like the one it replaces, and refused before it is
// persisted or read.
func TestHookCaptureRefusesARediscoveredTranscriptOutsideTheRoots(t *testing.T) {
	f := newCatchUpFixture(t)
	outside := filepath.Join(t.TempDir(), "session.jsonl")
	b, err := os.ReadFile(f.source)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(outside, b, 0o600))
	f.adapter.found = outside
	require.NoError(t, os.Truncate(f.source, 0)) // shrank below the cursor: rediscover

	capture, err := f.capture(session.HookCaptureOptions{SourceHome: f.home})

	require.ErrorIs(t, err, adapters.ErrSessionFileNotAllowed)
	assert.Equal(t, session.HookStatusSourceRefused, capture.Status)
	assert.Zero(t, f.adapter.reads.Load(), "the rediscovered transcript was read")
	assert.Equal(t, f.source, f.recording().SessionFile, "the refused transcript was persisted")
}

// A batch the live stream could not take is not an OK capture: the cursor
// moved, so nothing will send those bytes again, and the stream has declared
// them lost. The capture still hands the stream exactly what it committed,
// even when raw.jsonl cannot be read back: nothing re-reads it.
func TestHookCaptureReportsABatchTheStreamCouldNotTake(t *testing.T) {
	skipAsRoot(t)
	f := newCatchUpFixture(t)
	require.NoError(t, session.UpdateRecordingStateForAgent(f.projectRoot, f.agentID, func(s *session.RecordingState) {
		s.CommandRedactionVersion = 1
	}))
	f.say("assistant", "answer the stream lost")
	before, err := os.ReadFile(f.rawPath())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(f.rawPath(), 0o200))
	t.Cleanup(func() { _ = os.Chmod(f.rawPath(), 0o600) })
	var handed []session.AppendedBatch
	var statuses []string

	capture, err := f.capture(session.HookCaptureOptions{
		Spool: func(_ *session.RecordingState, b session.AppendedBatch) error {
			handed = append(handed, b)
			return errors.New("the stream could not take it")
		},
		MarkStatus: func(_ *session.RecordingState, status string) { statuses = append(statuses, status) },
	})

	require.NoError(t, err)
	require.NoError(t, os.Chmod(f.rawPath(), 0o600))
	after, err := os.ReadFile(f.rawPath())
	require.NoError(t, err)
	assert.Equal(t, session.HookStatusSpoolLost, capture.Status)
	assert.False(t, capture.Failed(), "the batch is committed: the cursor moved")
	assert.Greater(t, capture.ToOffset, capture.FromOffset)
	require.Len(t, handed, 1)
	assert.Equal(t, string(after[len(before):]), string(handed[0].Bytes), "exactly the committed bytes")
	assert.Equal(t, []string{session.HookStatusSpoolLost}, statuses)
}

// A batch committed while paused is handed on flagged, so the live-spool step
// leaves it out of the stream on purpose instead of declaring it lost.
func TestHookCaptureFlagsAPausedBatch(t *testing.T) {
	f := newCatchUpFixture(t)
	require.NoError(t, session.UpdateRecordingStateForAgent(f.projectRoot, f.agentID, func(s *session.RecordingState) {
		s.CommandRedactionVersion = 1
		paused := time.Now()
		s.SuspendedAt = &paused
	}))
	f.say("assistant", "said while paused")
	var handed []session.AppendedBatch

	capture, err := f.capture(session.HookCaptureOptions{
		Spool: func(_ *session.RecordingState, b session.AppendedBatch) error {
			handed = append(handed, b)
			return nil
		},
	})

	require.NoError(t, err)
	assert.Greater(t, capture.ToOffset, capture.FromOffset, "precondition: the paused batch was committed")
	require.Len(t, handed, 1)
	assert.True(t, handed[0].Suspended, "a paused batch must be flagged so it is never streamed")
	assert.Equal(t, session.HookStatusOK, capture.Status)
}
