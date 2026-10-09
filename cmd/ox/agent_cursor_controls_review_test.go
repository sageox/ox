package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/daemon/agentwork"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cursorReviewTurn(label string) string {
	return strings.ReplaceAll(cursorHostUser+cursorHostAnswer+cursorHostTerminal, "first prompt", label+" prompt")
}

func finalizeCursorHostRecording(t *testing.T, f *cursorHostFixture, state *session.RecordingState) error {
	t.Helper()
	return fileutil.WithFileLock(context.Background(), filepath.Join(state.SessionPath, "raw.jsonl"), func() error {
		_, err := session.FinalizeCursorCapture(context.Background(), f.root, state.SessionPath, f.home, f.reader)
		return err
	})
}

// Native rows exported during pause must be excluded even before the watcher polls.
func TestCursorResumeDrainsPausedRowsBeforeClosingExclusion(t *testing.T) {
	f := newCursorHostFixture(t)
	state := f.record(t)
	inst := &agentinstance.Instance{AgentID: state.AgentID}
	public := cursorReviewTurn("PUBLIC_BEFORE")
	private := cursorReviewTurn("PRIVATE_PAUSED")
	resumed := cursorReviewTurn("PUBLIC_AFTER")
	f.write(t, public)
	require.NoError(t, requestCursorCapture(f.root, state.AgentID, f.home))
	// A live watcher owns raw.jsonl for its lifetime. Controls must share its
	// short append transaction without waiting for that lifetime lock.
	owned, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- fileutil.WithFileLock(context.Background(), filepath.Join(state.SessionPath, "raw.jsonl"), func() error {
			close(owned)
			<-release
			return nil
		})
	}()
	<-owned
	var once sync.Once
	releaseOwner := func() { once.Do(func() { close(release); require.NoError(t, <-done) }) }
	t.Cleanup(releaseOwner)
	captureStdoutForPlanCLI(t, func() { require.NoError(t, runAgentSessionPause(inst, nil)) })
	f.write(t, public+private)
	captureStdoutForPlanCLI(t, func() { require.NoError(t, runAgentSessionResume(inst, nil)) })
	releaseOwner()
	f.write(t, public+private+resumed)
	require.NoError(t, finalizeCursorHostRecording(t, f, state))
	raw, err := os.ReadFile(filepath.Join(state.SessionPath, "raw.jsonl"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "PRIVATE_PAUSED")
	assert.Contains(t, string(raw), "PUBLIC_BEFORE")
	assert.Contains(t, string(raw), "PUBLIC_AFTER")
}

// Recovery cannot publish an unmasked cache or discard a pending native boundary.
func TestCursorRecoverPreservesPendingAndMissingNativeSource(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "missing with paused rows"}[missing], func(t *testing.T) {
			f := newCursorHostFixture(t)
			state := f.record(t)
			inst := &agentinstance.Instance{AgentID: state.AgentID}
			if missing {
				captureStdoutForPlanCLI(t, func() { require.NoError(t, runAgentSessionPause(inst, nil)) })
				f.write(t, cursorReviewTurn("PRIVATE_PAUSED"))
				require.NoError(t, requestCursorCapture(f.root, state.AgentID, f.home))
				require.NoError(t, os.Remove(f.source))
			}
			before, err := session.LoadRecordingStateForAgent(f.root, state.AgentID)
			require.NoError(t, err)
			var recoveryErr error
			captureStdoutForPlanCLI(t, func() { recoveryErr = runAgentSessionRecover(inst) })
			require.ErrorIs(t, recoveryErr, session.ErrCursorSourcePending)
			after, err := session.LoadRecordingStateForAgent(f.root, state.AgentID)
			require.NoError(t, err)
			require.NotNil(t, after)
			assert.Equal(t, before.SourceOffset, after.SourceOffset)
			assert.Equal(t, before.SourcePrefixSHA256, after.SourcePrefixSHA256)
			assert.Equal(t, before.Lifecycle, after.Lifecycle)
		})
	}
}

// Recovery must take ownership from a live PIDless watcher even without IPC.
func TestCursorRecoverHandsOffLiveWatcher(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the real watcher's polling handoff")
	}
	for _, source := range []string{"complete", "pending", "missing"} {
		t.Run(source, func(t *testing.T) {
			f := newCursorHostFixture(t)
			t.Setenv("SAGEOX_DAEMON", "false")
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			require.Nil(t, daemon.TryConnect())
			runGit(t, f.root, "init")
			state := f.record(t)
			inst := &agentinstance.Instance{AgentID: state.AgentID}
			public, private := cursorReviewTurn("PUBLIC_RECOVERY"), cursorReviewTurn("PRIVATE_RECOVERY")
			if source != "pending" {
				f.write(t, public)
			}
			captureStdoutForPlanCLI(t, func() { require.NoError(t, runAgentSessionPause(inst, nil)) })
			if source != "pending" {
				f.write(t, public+private)
			}
			manager := agentwork.NewSessionWatcherManager(slog.Default())
			manager.SetHomeDirForTest(f.home)
			t.Cleanup(manager.StopAll)
			rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
			require.NoError(t, manager.StartWatch(filepath.Base(state.SessionPath), f.source, "cursor", deriveLedgerPath(state.SessionPath), state.SessionPath))
			require.Eventually(t, func() bool {
				return fileutil.WithFileLockTimeout(context.Background(), rawPath, 20*time.Millisecond, func() error { return nil }) != nil
			}, 3*time.Second, 10*time.Millisecond, "the real watcher must own raw.jsonl before recovery")
			if source != "pending" {
				require.Eventually(t, func() bool {
					current, err := session.ReadRecordingStateFile(state.SessionPath)
					return err == nil && current.EntryCount == 4
				}, 3*time.Second, 10*time.Millisecond)
			}
			if source == "missing" {
				require.NoError(t, os.Remove(f.source))
			}
			before, err := session.ReadRecordingStateFile(state.SessionPath)
			require.NoError(t, err)
			var recoveryErr error
			captureStdoutForPlanCLI(t, func() {
				done := make(chan error, 1)
				go func() { done <- runAgentSessionRecover(inst) }()
				select {
				case recoveryErr = <-done:
				case <-time.After(5 * time.Second):
					manager.StopAll()
					<-done
					t.Fatal("recovery did not hand off the live watcher without IPC")
				}
			})
			require.Empty(t, manager.ActiveSessions())
			assert.True(t, session.HasExplicitStop(f.root, state.AgentID))
			if source == "complete" {
				require.NoError(t, recoveryErr)
				require.NoFileExists(t, filepath.Join(state.SessionPath, ".recording.json"))
				raw, err := os.ReadFile(rawPath)
				require.NoError(t, err)
				assert.Contains(t, string(raw), "PUBLIC_RECOVERY")
				assert.NotContains(t, string(raw), "PRIVATE_RECOVERY")
			} else {
				require.ErrorIs(t, recoveryErr, session.ErrCursorSourcePending)
				after, err := session.ReadRecordingStateFile(state.SessionPath)
				require.NoError(t, err)
				require.NotNil(t, after)
				assert.NotNil(t, after.StoppedAt)
				assert.True(t, after.CursorFinalDrainPending)
				assert.Equal(t, before.SourceOffset, after.SourceOffset)
				assert.Equal(t, before.SourcePrefixSHA256, after.SourcePrefixSHA256)
				assert.Equal(t, before.Lifecycle, after.Lifecycle)
				require.ErrorContains(t, manager.StartWatch(filepath.Base(state.SessionPath), f.source, "cursor", deriveLedgerPath(state.SessionPath), state.SessionPath), "stopped")
			}
		})
	}
}

func TestCursorRecoverDoesNotStopReplacedRecording(t *testing.T) {
	f := newCursorHostFixture(t)
	state := f.record(t)
	stale := *state
	stale.SessionID = "replaced-session"
	err := recoverRecording(&agentinstance.Instance{AgentID: state.AgentID}, f.root, &stale)
	require.ErrorIs(t, err, session.ErrRecordingChanged)
	current, err := session.ReadRecordingStateFile(state.SessionPath)
	require.NoError(t, err)
	assert.Nil(t, current.StoppedAt)
	assert.False(t, current.CursorFinalDrainPending)
	assert.False(t, session.HasExplicitStop(f.root, state.AgentID))
}

// A terminal from the previous turn cannot seal newly submitted, unexported prompts.
func TestCursorFinalizationWaitsForEverySubmittedTurn(t *testing.T) {
	f := newCursorHostFixture(t)
	state := f.record(t)
	old := cursorReviewTurn("OLD")
	f.write(t, old)
	require.NoError(t, requestCursorCapture(f.root, state.AgentID, f.home))
	for _, generation := range []string{"new-a", "new-b", "new-a"} {
		input, err := normalizeCursorHookInput(f.input(t, "beforeSubmitPrompt", generation).RawBytes, f.root, f.home)
		require.NoError(t, err)
		_, err = prepareCursorHookBoundary(input, "beforeSubmitPrompt")
		require.NoError(t, err)
	}
	require.ErrorIs(t, finalizeCursorHostRecording(t, f, state), session.ErrCursorFinalDrainPending)
	f.write(t, old+cursorReviewTurn("NEW_A"))
	require.ErrorIs(t, finalizeCursorHostRecording(t, f, state), session.ErrCursorFinalDrainPending)
	f.write(t, old+cursorReviewTurn("NEW_A")+cursorReviewTurn("NEW_B"))
	require.NoError(t, finalizeCursorHostRecording(t, f, state))
	stored, err := session.ReadSessionFromPath(filepath.Join(state.SessionPath, "raw.jsonl"))
	require.NoError(t, err)
	require.Len(t, stored.Entries, 6)
	assert.Equal(t, "NEW_A prompt", stored.Entries[2]["content"])
	assert.Equal(t, "NEW_B prompt", stored.Entries[4]["content"])
}

// Resume must keep a pause open while its latest prompt has not finished export.
func TestCursorResumeWaitsForPausedTurnExport(t *testing.T) {
	f := newCursorHostFixture(t)
	state := f.record(t)
	inst := &agentinstance.Instance{AgentID: state.AgentID}
	captureStdoutForPlanCLI(t, func() { require.NoError(t, runAgentSessionPause(inst, nil)) })
	input, err := normalizeCursorHookInput(f.input(t, "beforeSubmitPrompt", "paused-generation").RawBytes, f.root, f.home)
	require.NoError(t, err)
	_, err = prepareCursorHookBoundary(input, "beforeSubmitPrompt")
	require.NoError(t, err)
	require.ErrorIs(t, runAgentSessionResume(inst, nil), session.ErrCursorSourcePending)
	partial := strings.ReplaceAll(cursorHostUser+cursorHostAnswer, "first prompt", "PRIVATE_DELAYED")
	f.write(t, partial)
	require.ErrorIs(t, runAgentSessionResume(inst, nil), session.ErrCursorFinalDrainPending)
	paused, err := session.LoadRecordingStateForAgent(f.root, state.AgentID)
	require.NoError(t, err)
	require.NotNil(t, paused.SuspendedAt)

	f.write(t, partial+cursorHostTerminal)
	// The generic marker API cannot bypass a drain using the stale checkpoint.
	err = session.UpdateRecordingStateForAgent(f.root, state.AgentID, func(current *session.RecordingState) {
		current.SuspendedAt = nil
		current.Lifecycle = append(current.Lifecycle, session.LifecycleEvent{Action: session.LifecycleActionResume, Seq: current.EntryCount})
	})
	require.ErrorIs(t, err, session.ErrCursorFinalDrainPending)
	captureStdoutForPlanCLI(t, func() { require.NoError(t, runAgentSessionResume(inst, nil)) })
	f.write(t, partial+cursorHostTerminal+cursorReviewTurn("PUBLIC_AFTER"))
	require.NoError(t, finalizeCursorHostRecording(t, f, state))
	raw, err := os.ReadFile(filepath.Join(state.SessionPath, "raw.jsonl"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "PRIVATE_DELAYED")
	assert.Contains(t, string(raw), "PUBLIC_AFTER")
}
