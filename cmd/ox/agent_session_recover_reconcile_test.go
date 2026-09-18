//go:build !short

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/require"
)

// --- Recovery reconciles the append journal against the CURRENT cursor ---
//
// "Recover" runs against recordings that only look dead. Every path that reads
// or publishes raw.jsonl must first settle the journal, and must do it with the
// cursor as it stands once the capture lock is held -- not the copy loaded
// before the wait.

const committedHookEntry = "committed hook entry"

// commitHookBatchLeavingJournal plays a hook that appended a batch, committed
// its cursor, and died before deleting its journal. It returns the state as it
// was loaded BEFORE that commit, which is what a waiting recover still holds.
func commitHookBatchLeavingJournal(t *testing.T, projectRoot, agentID string) (stale *session.RecordingState, rawPath string) {
	t.Helper()
	stale, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, stale)
	rawPath = filepath.Join(stale.SessionPath, "raw.jsonl")
	nextOffset := stale.SourceOffset + 100
	require.NoError(t, fileutil.WithFileLock(context.Background(), rawPath, func() error {
		writer, err := session.NewRawWriter(rawPath, projectRoot)
		if err != nil {
			return err
		}
		defer writer.Close()
		if err = writer.BeginAppend(stale.SourceOffset, nextOffset); err != nil {
			return err
		}
		if err = writer.WriteEntry(&session.Entry{Type: session.EntryTypeUser, Content: committedHookEntry}); err != nil {
			return err
		}
		if err = writer.SealAppend(); err != nil {
			return err
		}
		return session.UpdateRecordingStateAt(stale.SessionPath, stale.SessionID, func(current *session.RecordingState) {
			current.SourceOffset = nextOffset
			current.EntryCount++
		})
	}))
	return stale, rawPath
}

// writeUnacknowledgedBatch plays a hook that wrote a batch and died BEFORE
// committing its cursor: the bytes are on raw.jsonl, nothing vouches for them.
func writeUnacknowledgedBatch(t *testing.T, projectRoot string, state *session.RecordingState, content string) {
	t.Helper()
	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
	require.NoError(t, fileutil.WithFileLock(context.Background(), rawPath, func() error {
		writer, err := session.NewRawWriter(rawPath, projectRoot)
		if err != nil {
			return err
		}
		defer writer.Close()
		if err = writer.BeginAppend(state.SourceOffset, state.SourceOffset+100); err != nil {
			return err
		}
		if err = writer.WriteEntry(&session.Entry{Type: session.EntryTypeTool, Content: content}); err != nil {
			return err
		}
		return writer.Sync()
	}))
}

// TestRecoverViaNormalStopKeepsABatchCommittedWhileItWaited verifies recovery
// reconciles against the cursor committed while it waited for the capture lock.
// Failure prevented: recover truncating a committed batch out of the session
// because it judged the journal with a cursor loaded before the commit.
func TestRecoverViaNormalStopKeepsABatchCommittedWhileItWaited(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)
	stale, rawPath := commitHookBatchLeavingJournal(t, projectRoot, agentID)

	// The outcome of the wider stop pipeline is not under test; what it did to
	// the committed batch is.
	_ = recoverViaNormalStop(&agentinstance.Instance{AgentID: agentID}, projectRoot, stale)

	after, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	require.Contains(t, string(after), committedHookEntry,
		"recover rolled back a batch whose cursor was already committed")
}

// TestRecoverFromCacheDropsAnUnacknowledgedBatchBeforeReadingIt verifies cache
// recovery settles the journal before it reads what it is about to publish.
// Failure prevented: bytes from a batch that never committed -- so never got its
// credential-redaction checkpoint -- being uploaded to the Ledger.
func TestRecoverFromCacheDropsAnUnacknowledgedBatchBeforeReadingIt(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
	const unacknowledged = "output of a batch that never committed"
	writeUnacknowledgedBatch(t, projectRoot, state, unacknowledged)
	before, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	require.Contains(t, string(before), unacknowledged, "fixture must leave the torn batch on disk")

	require.NoError(t, recoverFromCache(&agentinstance.Instance{AgentID: agentID}, projectRoot, state, rawPath))

	after, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	require.NotContains(t, string(after), unacknowledged)
}

// TestRecoverFromCacheKeepsABatchCommittedWhileItWaited is the other half of the
// same contract: reconciling must not cost a batch that DID commit.
// Failure prevented: cache recovery publishing a session with a committed batch
// truncated away because it held a pre-commit cursor.
func TestRecoverFromCacheKeepsABatchCommittedWhileItWaited(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)
	stale, rawPath := commitHookBatchLeavingJournal(t, projectRoot, agentID)

	require.NoError(t, recoverFromCache(&agentinstance.Instance{AgentID: agentID}, projectRoot, stale, rawPath))

	after, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	require.Contains(t, string(after), committedHookEntry)
}

// TestRecoverFromCacheRefusesAnUnprovableJournal verifies the fail-closed side:
// a journal nothing can vouch for stops recovery, and the recording survives so
// a later attempt (or doctor) still has the cursor needed to judge it.
// Failure prevented: an unreconciled raw.jsonl uploaded anyway, and the state
// that could have proven it deleted on the way out.
func TestRecoverFromCacheRefusesAnUnprovableJournal(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
	require.NoError(t, os.WriteFile(rawPath+".append.json", []byte("not a journal"), 0o600))

	err = recoverFromCache(&agentinstance.Instance{AgentID: agentID}, projectRoot, state, rawPath)
	require.ErrorContains(t, err, "reconcile cached session")

	survivor, loadErr := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, loadErr)
	require.NotNil(t, survivor, "a refused recovery must not clear the recording")
}

// TestRecoverRefusesARecordingRestartedWhileItWaited verifies neither recovery
// path acts on a recording that was replaced at the same path during the wait.
// Failure prevented: recover finalizing, truncating, or clearing a NEW recording
// on the strength of a stale copy of the old one.
func TestRecoverRefusesARecordingRestartedWhileItWaited(t *testing.T) {
	for _, tc := range []struct {
		name    string
		recover func(inst *agentinstance.Instance, projectRoot string, stale *session.RecordingState) error
	}{
		{"normal stop", recoverViaNormalStop},
		{"cache", func(inst *agentinstance.Instance, projectRoot string, stale *session.RecordingState) error {
			return recoverFromCache(inst, projectRoot, stale, filepath.Join(stale.SessionPath, "raw.jsonl"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot, agentID, _ := setupHandleAfterToolTest(t)
			stale, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
			require.NoError(t, err)
			restartedID := stale.SessionID + "-restarted"
			require.NoError(t, session.UpdateRecordingStateAt(stale.SessionPath, stale.SessionID, func(current *session.RecordingState) {
				current.SessionID = restartedID
			}))

			err = tc.recover(&agentinstance.Instance{AgentID: agentID}, projectRoot, stale)
			require.ErrorContains(t, err, "recording changed")

			survivor, loadErr := session.LoadRecordingStateForAgent(projectRoot, agentID)
			require.NoError(t, loadErr)
			require.NotNil(t, survivor, "the restarted recording must survive a refused recovery")
			require.Equal(t, restartedID, survivor.SessionID)
		})
	}
}

// TestRecoverFromCacheSurfacesATranscriptItCannotRead verifies "could not read
// the cached transcript" is an error, not an empty recovery. A directory where
// raw.jsonl belongs fails the read on every platform.
// Failure prevented: recover reporting success for a session it published
// nothing from.
func TestRecoverFromCacheSurfacesATranscriptItCannotRead(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
	require.NoError(t, os.Remove(rawPath))
	require.NoError(t, os.Mkdir(rawPath, 0o700))

	err = recoverFromCache(&agentinstance.Instance{AgentID: agentID}, projectRoot, state, rawPath)
	require.ErrorContains(t, err, "failed to read cached session")
	require.ErrorContains(t, err, "session abort", "the error must say how to discard a transcript that stays unreadable")

	// The read can fail for reasons that pass. Clearing here would orphan the
	// transcript for good: the recording state is the only thing pointing at it.
	survivor, loadErr := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, loadErr)
	require.NotNil(t, survivor, "an unreadable transcript cost the recording that points at it")
	require.Equal(t, state.SessionID, survivor.SessionID)
}

// TestPublishingACachedRecordingFailsWhenItCannotBeCleared verifies a recovery
// that could not retire the recording is not reported as a recovery. A directory
// where the state file belongs makes the clear fail on every platform.
// Failure prevented: "session recovered" while the recording is still live, so a
// later hook appends to a published session and the next recover publishes it
// again.
func TestPublishingACachedRecordingFailsWhenItCannotBeCleared(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)
	state, rawPath := commitHookBatchLeavingJournal(t, projectRoot, agentID)
	statePath := filepath.Join(state.SessionPath, ".recording.json")
	require.NoError(t, os.Remove(statePath))
	require.NoError(t, os.Mkdir(statePath, 0o700))

	output, err := publishCachedRecording(&agentinstance.Instance{AgentID: agentID}, projectRoot, state, rawPath)
	require.ErrorContains(t, err, "clear recovered recording state")
	require.Nil(t, output, "a recovery that did not retire the recording must not produce a success payload")
}

// TestDiscardingACachedRecordingDiscardsOnlyTheOneThatWasOffered verifies the
// discard acts on the recording the coworker was asked about. The prompt is
// shown without the capture lock, so the recording can change underneath it.
// Failure prevented: "discard the orphaned session" deleting the state and the
// captured transcript of a NEW recording that started while the prompt was up.
func TestDiscardingACachedRecordingDiscardsOnlyTheOneThatWasOffered(t *testing.T) {
	t.Run("the offered recording is removed with its cache", func(t *testing.T) {
		projectRoot, agentID, _ := setupHandleAfterToolTest(t)
		offered, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
		require.NoError(t, err)

		require.NoError(t, discardCachedRecording(projectRoot, offered, filepath.Join(offered.SessionPath, "raw.jsonl")))

		gone, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
		require.NoError(t, err)
		require.Nil(t, gone)
		require.NoDirExists(t, offered.SessionPath)
	})

	t.Run("a transcript that cannot be removed is not reported as discarded", func(t *testing.T) {
		// os.Chmod does not deny deletion on Windows, and root ignores it.
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("needs a directory the current user cannot delete from")
		}
		projectRoot, agentID, _ := setupHandleAfterToolTest(t)
		offered, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
		require.NoError(t, err)
		pinned := filepath.Join(offered.SessionPath, "pinned")
		require.NoError(t, os.Mkdir(pinned, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(pinned, "transcript-fragment"), []byte("still here"), 0o600))
		require.NoError(t, os.Chmod(pinned, 0o500))
		t.Cleanup(func() { _ = os.Chmod(pinned, 0o700) })

		err = discardCachedRecording(projectRoot, offered, filepath.Join(offered.SessionPath, "raw.jsonl"))
		require.ErrorContains(t, err, "remove cached recording")
		require.FileExists(t, filepath.Join(pinned, "transcript-fragment"), "fixture must actually block the removal")
	})

	t.Run("a recording restarted during the prompt is left alone", func(t *testing.T) {
		projectRoot, agentID, _ := setupHandleAfterToolTest(t)
		offered, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
		require.NoError(t, err)
		rawPath := filepath.Join(offered.SessionPath, "raw.jsonl")
		restartedID := offered.SessionID + "-restarted"
		require.NoError(t, session.UpdateRecordingStateAt(offered.SessionPath, offered.SessionID, func(current *session.RecordingState) {
			current.SessionID = restartedID
		}))

		require.ErrorContains(t, discardCachedRecording(projectRoot, offered, rawPath), "recording changed")

		survivor, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
		require.NoError(t, err)
		require.NotNil(t, survivor)
		require.Equal(t, restartedID, survivor.SessionID)
		require.FileExists(t, rawPath, "the new recording's transcript was deleted")
	})
}

// --- Finalizers clear the recording BEFORE they release the capture lock ---

// TestFinalizersClearTheRecordingBeforeReleasingTheCaptureLock verifies stop
// and recover give up the capture lock only once the recording is gone. A hook
// queued on that lock re-reads the state when it gets in: if the recording is
// still live it appends a batch after the final drain.
// Failure prevented: the tail of a session captured into a finalized recording,
// never uploaded, with the state that pointed at it deleted a moment later.
func TestFinalizersClearTheRecordingBeforeReleasingTheCaptureLock(t *testing.T) {
	for _, tc := range []struct {
		name string
		// processed names a file the finalizer writes once its read of raw.jsonl
		// is over, so its appearance means only the clear is left.
		processed string
		finalize  func(inst *agentinstance.Instance, projectRoot string, state *session.RecordingState) error
	}{
		{"recover", "session.md", recoverViaNormalStop},
		{"stop", "session.md", func(inst *agentinstance.Instance, _ string, _ *session.RecordingState) error {
			return runAgentSessionStop(inst)
		}},
		{"recover from cache", ".needs-summary", func(inst *agentinstance.Instance, projectRoot string, state *session.RecordingState) error {
			return recoverFromCache(inst, projectRoot, state, filepath.Join(state.SessionPath, "raw.jsonl"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
			t.Setenv("SAGEOX_DAEMON", "false")
			projectRoot, agentID, _ := setupHandleAfterToolTest(t)
			t.Chdir(projectRoot)
			previousCfg := cfg
			cfg = &config.Config{Text: true}
			t.Cleanup(func() { cfg = previousCfg })

			state, rawPath := commitHookBatchLeavingJournal(t, projectRoot, agentID)
			statePath := filepath.Join(state.SessionPath, ".recording.json")

			// Park the finalizer at its clear: the clear needs the state lock.
			stateLockHeld := make(chan struct{})
			releaseStateLock := make(chan struct{})
			holderDone := make(chan error, 1)
			go func() {
				holderDone <- fileutil.WithFileLock(context.Background(), statePath, func() error {
					close(stateLockHeld)
					<-releaseStateLock
					return nil
				})
			}()
			<-stateLockHeld

			finalized := make(chan error, 1)
			go func() {
				finalized <- tc.finalize(&agentinstance.Instance{AgentID: agentID, AgentType: "claude-code"}, projectRoot, state)
			}()

			require.Eventually(t, func() bool {
				_, err := os.Stat(filepath.Join(state.SessionPath, tc.processed))
				return err == nil
			}, 30*time.Second, 10*time.Millisecond, "fixture never reached the end of processing")

			// The queued hook's view: whenever it can get the capture lock, the
			// recording must already be gone. Timing out means the finalizer
			// still holds the lock while it waits to clear, which is the point.
			hookErr := fileutil.WithFileLockTimeout(context.Background(), rawPath, 500*time.Millisecond, func() error {
				live, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
				require.NoError(t, err)
				require.Nil(t, live, "a queued hook got the capture lock while the finalized recording was still live")
				return nil
			})
			if hookErr == nil {
				t.Log("hook acquired the capture lock after the recording was cleared")
			}

			close(releaseStateLock)
			require.NoError(t, <-holderDone)
			require.NoError(t, <-finalized)
			gone, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
			require.NoError(t, err)
			require.Nil(t, gone)
		})
	}
}

// TestClearingAProcessedRecordingLeavesItsReplacementAlone verifies the clear
// names the recording that was processed. Session paths are minute-granular, so
// a recording restarted within the minute lives at the very same path.
// Failure prevented: a finalizer deleting the NEW recording an agent started in
// the gap, leaving a live session with no state and nothing capturing it.
func TestClearingAProcessedRecordingLeavesItsReplacementAlone(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)
	processed, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	replacementID := processed.SessionID + "-restarted"
	require.NoError(t, session.UpdateRecordingStateAt(processed.SessionPath, processed.SessionID, func(current *session.RecordingState) {
		current.SessionID = replacementID
	}))

	require.NoError(t, session.ClearRecordingStateAt(processed.SessionPath, processed.SessionID))
	survivor, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, survivor, "clearing the processed recording deleted the one that replaced it")
	require.Equal(t, replacementID, survivor.SessionID)

	// Negative control: the same call DOES clear the recording it names, and is
	// idempotent once it is gone.
	require.NoError(t, session.ClearRecordingStateAt(processed.SessionPath, replacementID))
	require.NoError(t, session.ClearRecordingStateAt(processed.SessionPath, replacementID))
	gone, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.Nil(t, gone)

	require.ErrorIs(t, session.ClearRecordingStateAt("", replacementID), session.ErrEmptyPath)
}

// --- Lifecycle bookkeeping never reverts a committed capture checkpoint ---

// TestRegistrationOutcomeDoesNotRevertACommittedCaptureCheckpoint verifies the
// registration outcome is persisted as a narrow update. The per-turn path holds
// a copy that is up to sessionSignalWait old by the time it saves.
// Failure prevented: a stale whole-state save regressing the capture cursor and
// forgetting a pending credential redaction, so the matching credential output
// is later written unredacted.
func TestRegistrationOutcomeDoesNotRevertACommittedCaptureCheckpoint(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)
	stale, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)

	// A hook commits while the registration signal is still in flight.
	committedOffset := stale.SourceOffset + 100
	require.NoError(t, session.UpdateRecordingStateAt(stale.SessionPath, stale.SessionID, func(current *session.RecordingState) {
		current.SourceOffset = committedOffset
		current.PendingCommandRedactions = map[string]string{"call_1": "aws-secret-key"}
	}))

	stale.LifecycleRegistrationState = "pending"
	stale.LifecycleRegistrationError = "server confirmation timed out"
	persistLifecycleRegistration(stale)

	latest, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.Equal(t, "pending", latest.LifecycleRegistrationState)
	require.Equal(t, "server confirmation timed out", latest.LifecycleRegistrationError)
	require.Equal(t, committedOffset, latest.SourceOffset, "registration bookkeeping regressed the capture cursor")
	require.Equal(t, map[string]string{"call_1": "aws-secret-key"}, latest.PendingCommandRedactions,
		"registration bookkeeping forgot a pending credential redaction")
}

// TestRegistrationOutcomeDoesNotLandOnAReplacementRecording verifies the outcome
// goes to the session the signal was sent for. If the agent stopped and restarted
// while the signal was in flight, the new recording sits at the same
// minute-granular path.
// Failure prevented: the new session inheriting "confirmed" from the old one, so
// its own registration never fires and its /c/ link never resolves.
func TestRegistrationOutcomeDoesNotLandOnAReplacementRecording(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)
	stale, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	replacementID := stale.SessionID + "-restarted"
	require.NoError(t, session.UpdateRecordingStateAt(stale.SessionPath, stale.SessionID, func(current *session.RecordingState) {
		current.SessionID = replacementID
		current.LifecycleRegistrationState = "deferred"
	}))

	stale.LifecycleRegistrationState = "confirmed"
	persistLifecycleRegistration(stale)

	replacement, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.Equal(t, replacementID, replacement.SessionID)
	require.Equal(t, "deferred", replacement.LifecycleRegistrationState,
		"the old session's registration outcome landed on the recording that replaced it")
}

// TestRegistrationOutcomeDoesNotResurrectAStoppedRecording covers the other way
// a stale copy bites: the recording stopped while the signal was in flight.
// Failure prevented: a ghost .recording.json that makes the agent look like it
// is still recording and blocks its next session start.
func TestRegistrationOutcomeDoesNotResurrectAStoppedRecording(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)
	stale, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NoError(t, session.ClearRecordingStateForAgent(projectRoot, agentID))

	stale.LifecycleRegistrationState = "confirmed"
	persistLifecycleRegistration(stale)

	_, statErr := os.Stat(filepath.Join(stale.SessionPath, ".recording.json"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

// TestPlanSlugAppendWaitsForTheStateLock verifies the plan reverse-link is a
// locked read-modify-write, not a reload-then-write beside the lock.
// Failure prevented: a plan save landing inside a capture batch's state commit
// and writing back the pre-commit cursor and redaction checkpoint.
func TestPlanSlugAppendWaitsForTheStateLock(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	statePath := filepath.Join(state.SessionPath, ".recording.json")

	held := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		// A capture batch mid-commit: it holds the lock and will advance the cursor.
		holderDone <- session.MutateRecordingStateFile(statePath, func(current *session.RecordingState) error {
			close(held)
			<-release
			current.SourceOffset += 100
			return nil
		})
	}()
	<-held

	appended := make(chan error, 1)
	go func() { appended <- session.AppendProducedPlan(projectRoot, state.SessionPath, "plan-slug") }()
	select {
	case <-appended:
		t.Fatal("plan slug was written while a capture batch held the state lock")
	case <-time.After(500 * time.Millisecond):
	}

	close(release)
	require.NoError(t, <-holderDone)
	require.NoError(t, <-appended)

	latest, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.Equal(t, state.SourceOffset+100, latest.SourceOffset, "plan save reverted the committed cursor")
	require.Equal(t, []string{"plan-slug"}, latest.ProducedPlans)
}

// TestLockedStateUpdatesSurfaceWhatTheyCannotApply verifies the locked writers
// report an unusable target instead of reading it as "nothing to do".
// Failure prevented: a corrupt .recording.json silently swallowing plan links,
// or an empty path mutating whatever happens to sit at the working directory.
func TestLockedStateUpdatesSurfaceWhatTheyCannotApply(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)

	require.ErrorIs(t, session.UpdateRecordingStateAt("", "ses_any", func(*session.RecordingState) {}), session.ErrEmptyPath)

	require.NoError(t, os.WriteFile(filepath.Join(state.SessionPath, ".recording.json"), []byte("{torn"), 0o600))
	require.ErrorContains(t, session.AppendProducedPlan(projectRoot, state.SessionPath, "plan-slug"), "update recording state")
}

// keep the journal shape honest: the fixtures above must produce a real journal,
// or the "committed" tests would pass without ever reaching reconciliation.
func TestRecoverFixturesLeaveARealJournal(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)
	_, rawPath := commitHookBatchLeavingJournal(t, projectRoot, agentID)
	data, err := os.ReadFile(rawPath + ".append.json")
	require.NoError(t, err)
	var journal map[string]any
	require.NoError(t, json.Unmarshal(data, &journal))
	require.NotEmpty(t, journal)
}
