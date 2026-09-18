package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/stretchr/testify/require"
)

// --- Live recordings are changed by locked, narrow updates ---
//
// A capture batch commits its cursor and its pending credential-redaction
// checkpoint together under the state lock. Every other writer of a live
// .recording.json has to go through that same lock and touch only its own
// fields, or it reverts the commit with a copy it loaded earlier.

func startLockedUpdateRecording(t *testing.T) (projectRoot string, state *RecordingState) {
	t.Helper()
	projectRoot = setupRecordingTest(t, t.TempDir())
	state, err := StartRecording(projectRoot, StartRecordingOptions{
		AgentID: "OxLock1", AdapterName: "claude-code", Username: "testuser",
	})
	require.NoError(t, err)
	return projectRoot, state
}

// holdStateLock parks a capture batch inside its state commit. release lets it
// finish; the returned channel reports how the commit ended.
func holdStateLock(t *testing.T, state *RecordingState, commit func(*RecordingState)) (release func(), done <-chan error) {
	t.Helper()
	held := make(chan struct{})
	gate := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- MutateRecordingStateFile(recordingStatePath(state.SessionPath), func(current *RecordingState) error {
			close(held)
			<-gate
			commit(current)
			return nil
		})
	}()
	<-held
	return func() { close(gate) }, result
}

func requireBlocked(t *testing.T, finished <-chan error, what string) {
	t.Helper()
	select {
	case <-finished:
		t.Fatalf("%s ran while a capture batch held the state lock", what)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestLiveRecordingWritersWaitForAnInFlightCaptureCommit verifies each narrow
// writer queues behind a capture commit and then preserves what it committed.
// Failure prevented: bookkeeping written from a stale copy regressing the
// capture cursor and forgetting a pending credential redaction, so the matching
// credential output is later captured unredacted.
func TestLiveRecordingWritersWaitForAnInFlightCaptureCommit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		write  func(projectRoot string, state *RecordingState) error
		verify func(t *testing.T, latest *RecordingState)
	}{
		{
			name: "path-addressed update",
			write: func(_ string, state *RecordingState) error {
				return UpdateRecordingStateAt(state.SessionPath, state.SessionID, func(current *RecordingState) {
					current.LifecycleRegistrationState = "confirmed"
				})
			},
			verify: func(t *testing.T, latest *RecordingState) {
				require.Equal(t, "confirmed", latest.LifecycleRegistrationState)
			},
		},
		{
			name: "plan reverse-link",
			write: func(projectRoot string, state *RecordingState) error {
				return AppendProducedPlan(projectRoot, state.SessionPath, "plan-slug")
			},
			verify: func(t *testing.T, latest *RecordingState) {
				require.Equal(t, []string{"plan-slug"}, latest.ProducedPlans)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot, state := startLockedUpdateRecording(t)
			release, committed := holdStateLock(t, state, func(current *RecordingState) {
				current.SourceOffset = 4096
				current.PendingCommandRedactions = map[string]string{"call_1": "aws-secret-key"}
			})

			written := make(chan error, 1)
			go func() { written <- tc.write(projectRoot, state) }()
			requireBlocked(t, written, tc.name)

			release()
			require.NoError(t, <-committed)
			require.NoError(t, <-written)

			latest, err := LoadRecordingStateForAgent(projectRoot, state.AgentID)
			require.NoError(t, err)
			require.NotNil(t, latest)
			tc.verify(t, latest)
			require.Equal(t, int64(4096), latest.SourceOffset, "the write reverted the committed cursor")
			require.Equal(t, map[string]string{"call_1": "aws-secret-key"}, latest.PendingCommandRedactions,
				"the write forgot a pending credential redaction")
		})
	}
}

// TestPlanReverseLinkIsIdempotentAndNeverResurrects pins the two contracts the
// plan link kept when it moved under the lock.
// Failure prevented: duplicate slugs in the session's produced plans, or a ghost
// .recording.json that blocks the agent's next session start.
func TestPlanReverseLinkIsIdempotentAndNeverResurrects(t *testing.T) {
	projectRoot, state := startLockedUpdateRecording(t)

	require.NoError(t, AppendProducedPlan(projectRoot, state.SessionPath, "plan-slug"))
	require.NoError(t, AppendProducedPlan(projectRoot, state.SessionPath, "plan-slug"))
	latest, err := LoadRecordingStateForAgent(projectRoot, state.AgentID)
	require.NoError(t, err)
	require.Equal(t, []string{"plan-slug"}, latest.ProducedPlans)

	// Nothing to link is not an error, and says nothing about the recording.
	require.NoError(t, AppendProducedPlan(projectRoot, "", "plan-slug"))
	require.NoError(t, AppendProducedPlan(projectRoot, state.SessionPath, ""))
	require.ErrorIs(t, AppendProducedPlan("", state.SessionPath, "plan-slug"), ErrEmptyPath)

	require.NoError(t, ClearRecordingStateAt(state.SessionPath, state.SessionID))
	require.NoError(t, AppendProducedPlan(projectRoot, state.SessionPath, "late-plan"))
	_, statErr := os.Stat(recordingStatePath(state.SessionPath))
	require.ErrorIs(t, statErr, os.ErrNotExist, "a plan saved after stop resurrected the recording")
}

// TestLockedUpdatesSurfaceATargetTheyCannotUse verifies an unusable target is an
// error, never "nothing to do".
// Failure prevented: a torn .recording.json silently swallowing plan links and
// registration outcomes; an empty path mutating whatever sits in the cwd.
func TestLockedUpdatesSurfaceATargetTheyCannotUse(t *testing.T) {
	projectRoot, state := startLockedUpdateRecording(t)

	require.ErrorIs(t, UpdateRecordingStateAt("", "ses_any", func(*RecordingState) {}), ErrEmptyPath)
	require.ErrorIs(t, ClearRecordingStateAt("", state.SessionID), ErrEmptyPath)

	require.NoError(t, os.WriteFile(recordingStatePath(state.SessionPath), []byte("{torn"), 0o600))
	require.ErrorContains(t, AppendProducedPlan(projectRoot, state.SessionPath, "plan-slug"), "update recording state")
	require.Error(t, UpdateRecordingStateAt(state.SessionPath, state.SessionID, func(*RecordingState) {}))

	gone := filepath.Join(t.TempDir(), "never-recorded")
	require.ErrorIs(t, UpdateRecordingStateAt(gone, "ses_any", func(*RecordingState) {}), os.ErrNotExist)
}

// TestUpdateLeavesAReplacementRecordingUntouched verifies an update names the
// recording it is for. The caller may have been away (a network signal, a
// prompt), and session names are minute-granular: a recording restarted within
// the minute sits at the very path the caller still holds.
// Failure prevented: bookkeeping about a finished session written onto the new
// one that replaced it.
func TestUpdateLeavesAReplacementRecordingUntouched(t *testing.T) {
	projectRoot, state := startLockedUpdateRecording(t)
	replacementID := state.SessionID + "-restarted"
	require.NoError(t, UpdateRecordingStateAt(state.SessionPath, state.SessionID, func(current *RecordingState) {
		current.SessionID = replacementID
		current.LifecycleRegistrationState = "deferred"
	}))

	err := UpdateRecordingStateAt(state.SessionPath, state.SessionID, func(current *RecordingState) {
		current.LifecycleRegistrationState = "confirmed"
	})
	require.ErrorIs(t, err, ErrRecordingChanged)

	replacement, loadErr := LoadRecordingStateForAgent(projectRoot, state.AgentID)
	require.NoError(t, loadErr)
	require.Equal(t, replacementID, replacement.SessionID)
	require.Equal(t, "deferred", replacement.LifecycleRegistrationState)

	// Negative control: the same update DOES land when it names the recording there.
	require.NoError(t, UpdateRecordingStateAt(state.SessionPath, replacementID, func(current *RecordingState) {
		current.LifecycleRegistrationState = "confirmed"
	}))
	replacement, loadErr = LoadRecordingStateForAgent(projectRoot, state.AgentID)
	require.NoError(t, loadErr)
	require.Equal(t, "confirmed", replacement.LifecycleRegistrationState)
}

// --- Clearing names the recording it clears ---

// TestClearNamesTheRecordingItRemoves verifies the clear is keyed on the session
// identity, not just the path: session names are minute-granular, so a recording
// restarted within the minute lives at the same path as the one it replaced.
// Failure prevented: a finalizer deleting the NEW recording, leaving a live
// session with no state and nothing capturing it.
func TestClearNamesTheRecordingItRemoves(t *testing.T) {
	projectRoot, state := startLockedUpdateRecording(t)
	replacementID := state.SessionID + "-restarted"
	require.NoError(t, UpdateRecordingStateAt(state.SessionPath, state.SessionID, func(current *RecordingState) {
		current.SessionID = replacementID
	}))

	require.NoError(t, ClearRecordingStateAt(state.SessionPath, state.SessionID))
	survivor, err := LoadRecordingStateForAgent(projectRoot, state.AgentID)
	require.NoError(t, err)
	require.NotNil(t, survivor, "clearing the finalized recording deleted the one that replaced it")
	require.Equal(t, replacementID, survivor.SessionID)

	// Negative control: naming the recording that IS there clears it, twice over.
	require.NoError(t, ClearRecordingStateAt(state.SessionPath, replacementID))
	require.NoError(t, ClearRecordingStateAt(state.SessionPath, replacementID))
	gone, err := LoadRecordingStateForAgent(projectRoot, state.AgentID)
	require.NoError(t, err)
	require.Nil(t, gone)
}

// TestClearRemovesAStateItCannotIdentify verifies a torn state file is still
// cleared. It names no recording, so there is no replacement to protect, and
// leaving it wedges the agent behind a recording nothing can load or stop.
func TestClearRemovesAStateItCannotIdentify(t *testing.T) {
	_, state := startLockedUpdateRecording(t)
	statePath := recordingStatePath(state.SessionPath)
	require.NoError(t, os.WriteFile(statePath, []byte("{torn"), 0o600))

	require.NoError(t, ClearRecordingStateAt(state.SessionPath, state.SessionID))
	_, statErr := os.Stat(statePath)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

// TestClearSurfacesAStateItCannotRead verifies "could not look" is not reported
// as "nothing there". A directory where the state file belongs fails the read on
// every platform.
// Failure prevented: stop reporting a clean finalize while the recording it could
// not inspect is still in place.
func TestClearSurfacesAStateItCannotRead(t *testing.T) {
	sessionPath := t.TempDir()
	require.NoError(t, os.Mkdir(recordingStatePath(sessionPath), 0o700))

	require.ErrorContains(t, ClearRecordingStateAt(sessionPath, "ses_any"), "read recording state")
}

// TestClearWaitsForAnInFlightStateCommit verifies the remove cannot land between
// an updater's read and its atomic write.
// Failure prevented: the updater's rename bringing .recording.json straight back
// after stop removed it -- a ghost recording for a finalized session.
func TestClearWaitsForAnInFlightStateCommit(t *testing.T) {
	_, state := startLockedUpdateRecording(t)
	release, committed := holdStateLock(t, state, func(current *RecordingState) { current.EntryCount++ })

	cleared := make(chan error, 1)
	go func() { cleared <- ClearRecordingStateAt(state.SessionPath, state.SessionID) }()
	requireBlocked(t, cleared, "clear")

	release()
	require.NoError(t, <-committed)
	require.NoError(t, <-cleared)
	_, statErr := os.Stat(recordingStatePath(state.SessionPath))
	require.ErrorIs(t, statErr, os.ErrNotExist, "the in-flight commit resurrected the cleared recording")

	// The lock itself must be free again: a wedged lock would stall every hook.
	require.NoError(t, fileutil.WithFileLockTimeout(context.Background(), recordingStatePath(state.SessionPath), time.Second, func() error { return nil }))
}
