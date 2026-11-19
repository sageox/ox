package agentwork

import (
	"os"
	"path/filepath"
	"testing"

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

// A batch committed but not read back is not an OK capture: the cursor moved,
// the live stream is told its size, and the batch is never spooled.
func TestHookCaptureReportsABatchItCouldNotReadBack(t *testing.T) {
	skipAsRoot(t)
	f := newCatchUpFixture(t)
	require.NoError(t, session.UpdateRecordingStateForAgent(f.projectRoot, f.agentID, func(s *session.RecordingState) {
		s.CommandRedactionVersion = 1
	}))
	f.say("assistant", "answer the stream lost")
	before, err := os.Stat(f.rawPath())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(f.rawPath(), 0o200))
	t.Cleanup(func() { _ = os.Chmod(f.rawPath(), 0o600) })
	var spooled int
	var lost []int64
	var statuses []string

	capture, err := f.capture(session.HookCaptureOptions{
		Spool:      func(*session.RecordingState, session.AppendedBatch) { spooled++ },
		SpoolLost:  func(_ *session.RecordingState, n int64, _ error) { lost = append(lost, n) },
		MarkStatus: func(_ *session.RecordingState, status string) { statuses = append(statuses, status) },
	})

	require.NoError(t, err)
	require.NoError(t, os.Chmod(f.rawPath(), 0o600))
	after, err := os.Stat(f.rawPath())
	require.NoError(t, err)
	assert.Equal(t, session.HookStatusSpoolLost, capture.Status)
	assert.False(t, capture.Failed(), "the batch is committed: the cursor moved")
	assert.Greater(t, capture.ToOffset, capture.FromOffset)
	assert.Zero(t, spooled)
	assert.Equal(t, []int64{after.Size() - before.Size()}, lost)
	assert.Equal(t, []string{session.HookStatusSpoolLost}, statuses)
}
