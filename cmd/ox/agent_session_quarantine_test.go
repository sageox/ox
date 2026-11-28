package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/session/claudesource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// heldRecording returns the one recording the agent has quarantined.
func heldRecording(t *testing.T, projectRoot, agentID string) *session.RecordingState {
	t.Helper()
	held, err := session.LoadQuarantinedRecordingsForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.Len(t, held, 1, "the quarantined recording must survive")
	return held[0]
}

// moveToEarlierMinute re-homes a recording's folder under a name from a minute
// ago, standing in for the time that passes between a quarantine and the next
// recording (folder names are minute-granular).
func moveToEarlierMinute(t *testing.T, state *session.RecordingState) *session.RecordingState {
	t.Helper()
	earlier := filepath.Join(filepath.Dir(state.SessionPath), "2001-02-03T04-05-testuser-"+state.AgentID)
	require.NoError(t, os.Rename(state.SessionPath, earlier))
	require.NoError(t, session.MutateRecordingStateFile(filepath.Join(earlier, ".recording.json"), func(s *session.RecordingState) error {
		s.SessionPath = earlier
		return nil
	}))
	moved, err := session.ReadRecordingStateFile(earlier)
	require.NoError(t, err)
	return moved
}

// A quarantined recording is held for review, not recording. After /clear the
// agent starts a new native file; if the held recording kept the agent's slot,
// nothing would ever capture it and its content would never reach the Ledger.
func TestQuarantinedRecordingDoesNotBlockTheNextRecording(t *testing.T) {
	// Both recording starts must use the same minute-granular folder name.
	synctest.Test(t, func(t *testing.T) {
		projectRoot, agentID, source := setupHandleAfterToolTest(t)
		held, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
		require.NoError(t, err)
		first := held.StartedAt.Add(time.Second)
		appendClaudeEntries(t, source, first,
			`{"type":"user","timestamp":"`+first.Format(time.RFC3339Nano)+`","message":{"role":"user","content":"captured before the quarantine"}}`,
		)
		require.NoError(t, handleAfterTool(&HookContext{Phase: phaseAfterTool, AgentType: "claude-code", ProjectRoot: projectRoot, Marker: &SessionMarker{AgentID: agentID}}))
		require.NoError(t, session.MarkSourceRejected(projectRoot, agentID))

		rawPath := filepath.Join(held.SessionPath, "raw.jsonl")
		rawBefore, err := os.ReadFile(rawPath)
		require.NoError(t, err)
		markerBefore, err := os.ReadFile(filepath.Join(held.SessionPath, ".recording.json"))
		require.NoError(t, err)

		opts := session.StartRecordingOptions{AgentID: agentID, AdapterName: "claude-code", Username: "testuser"}
		_, err = session.StartRecording(projectRoot, opts)
		require.ErrorIs(t, err, session.ErrQuarantinedSessionPath, "a new recording must never be written over the quarantined one's folder")
		markerAfter, err := os.ReadFile(filepath.Join(held.SessionPath, ".recording.json"))
		require.NoError(t, err)
		assert.Equal(t, string(markerBefore), string(markerAfter), "the refused start must leave the quarantine untouched")

		// /clear later: nothing active to stop, the held recording stays put
		moved := moveToEarlierMinute(t, held)
		stopSessionForClear(&HookContext{Phase: phaseStart, AgentType: "claude-code", ProjectRoot: projectRoot, Marker: &SessionMarker{AgentID: agentID}}, agentID)
		stillHeld, err := session.LoadQuarantinedRecordingsForAgent(projectRoot, agentID)
		require.NoError(t, err)
		require.Len(t, stillHeld, 1, "/clear must not finalize or clear a quarantined recording")

		fresh, err := session.StartRecording(projectRoot, opts)
		require.NoError(t, err, "the agent must be free to record again while the old recording waits for review")
		assert.NotEqual(t, moved.SessionID, fresh.SessionID)
		active, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
		require.NoError(t, err)
		require.NotNil(t, active)
		assert.Equal(t, fresh.SessionID, active.SessionID, "the agent's active recording is the new one")

		// the agent exiting finalizes the new recording and only that one
		require.NoError(t, handleEnd(&HookContext{Phase: phaseEnd, AgentType: "claude-code", ProjectRoot: projectRoot, Marker: &SessionMarker{AgentID: agentID}}))
		stillHeld, err = session.LoadQuarantinedRecordingsForAgent(projectRoot, agentID)
		require.NoError(t, err)
		require.Len(t, stillHeld, 1, "SessionEnd must not take the held recording with it")
		assert.True(t, stillHeld[0].SourceRejected)
		rawAfter, err := os.ReadFile(filepath.Join(moved.SessionPath, "raw.jsonl"))
		require.NoError(t, err)
		assert.Equal(t, string(rawBefore), string(rawAfter), "the held recording's captured data must be untouched")
	})
}

// Prime is where a coworker learns whether they are being recorded. It must
// start the new recording and say the earlier one is held back.
func TestPrimeStartsAFreshRecordingBesideAQuarantinedOne(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("OX_XDG_DISABLE", "")
	f := newDraftLedgerFixture(t)
	t.Chdir(f.projectRoot)
	defaultLedger, err := ledger.DefaultPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(defaultLedger), 0o755))
	runGit(t, f.projectRoot, "clone", "--quiet", f.barePath, defaultLedger)
	oldCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = oldCfg })
	oxConfigSetRepo(t, "session_recording", "auto")

	const agentID = "OxPrimeHeld"
	first := startSessionRecording(f.projectRoot, agentID, "claude-code", inheritedParent{}, "", "")
	require.NotNil(t, first)
	require.True(t, first.Recording)
	require.NotContains(t, first.UserNotification, "held back")
	held, err := session.LoadRecordingStateForAgent(f.projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, held)
	require.NoError(t, session.MarkSourceRejected(f.projectRoot, agentID))
	moved := moveToEarlierMinute(t, held)

	status := startSessionRecording(f.projectRoot, agentID, "claude-code", inheritedParent{}, "", "")
	require.NotNil(t, status)
	assert.True(t, status.Recording, "prime must report the new recording")
	assert.True(t, status.AutoStarted, "a quarantined recording must not make prime think it is already recording")
	assert.Contains(t, status.UserNotification, "held back", "prime must say the earlier recording is being held")
	active, err := session.LoadRecordingStateForAgent(f.projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, active)
	assert.NotEqual(t, moved.SessionID, active.SessionID)
	stillHeld, err := session.LoadQuarantinedRecordingsForAgent(f.projectRoot, agentID)
	require.NoError(t, err)
	require.Len(t, stillHeld, 1)
}

// A quarantined recording is held for review, so abort can discard it by name,
// and plain abort points at that name instead of claiming nothing exists.
func TestAbortDiscardsAQuarantinedRecordingByName(t *testing.T) {
	projectRoot, state := setupAbortTest(t)
	require.NoError(t, session.MarkSourceRejected(projectRoot, "OxAbrt"))
	setForceFlag(t, true)
	inst := &agentinstance.Instance{AgentID: "OxAbrt"}

	err := runAgentSessionAbort(inst, agentCmd, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "quarantined recording is kept for review")
	assert.Contains(t, err.Error(), session.GetSessionName(state.SessionPath), "the refusal must name what to discard")
	_, statErr := os.Stat(state.SessionPath)
	require.NoError(t, statErr, "plain abort must not touch the held recording")

	require.NoError(t, runAgentSessionAbort(inst, agentCmd, []string{session.GetSessionName(state.SessionPath)}))
	_, statErr = os.Stat(state.SessionPath)
	assert.True(t, os.IsNotExist(statErr), "aborting by name discards the held recording, which a live agent must not make look active")
}

// Release re-runs the ownership check and recovers only when it passes. It must
// never fall through to publishing the cache when the check cannot run.
func TestReleaseQuarantine_RechecksBeforeAnythingPublishes(t *testing.T) {
	for _, tt := range []struct {
		name        string
		setup       func(t *testing.T, projectRoot, source string)
		wantErr     string
		wantHeld    bool
		wantCleared bool
	}{
		{name: "source that now passes", wantCleared: true},
		{
			name: "source that still crosses repositories",
			setup: func(t *testing.T, projectRoot, source string) {
				f, err := os.OpenFile(source, os.O_APPEND|os.O_WRONLY, 0)
				require.NoError(t, err)
				_, err = f.WriteString(`{"type":"assistant","sessionId":"session","cwd":` + quote(filepath.Dir(projectRoot)) + `}` + "\n")
				require.NoError(t, err)
				require.NoError(t, f.Close())
			},
			wantErr:  "still crosses repositories",
			wantHeld: true,
		},
		{
			name: "a visited directory cannot be placed",
			setup: func(t *testing.T, projectRoot, source string) {
				dangling := filepath.Join(projectRoot, "shortcut")
				require.NoError(t, os.Symlink(filepath.Join(filepath.Dir(projectRoot), "removed-elsewhere"), dangling))
				f, err := os.OpenFile(source, os.O_APPEND|os.O_WRONLY, 0)
				require.NoError(t, err)
				_, err = f.WriteString(`{"type":"user","sessionId":"session","cwd":` + quote(filepath.Join(dangling, "pkg")) + `}` + "\n")
				require.NoError(t, err)
				require.NoError(t, f.Close())
			},
			wantErr:  "cannot re-check ownership right now",
			wantHeld: true,
		},
		{
			name:     "native file is gone",
			setup:    func(t *testing.T, _, source string) { require.NoError(t, os.Remove(source)) },
			wantErr:  "cannot re-check",
			wantHeld: true,
		},
		{
			name: "no native file recorded",
			setup: func(t *testing.T, projectRoot, _ string) {
				require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, "OxHook1", func(s *session.RecordingState) { s.SessionFile = "" }))
			},
			wantErr:  "names no native session file",
			wantHeld: true,
		},
		{
			name: "recovery cannot finish",
			setup: func(t *testing.T, _, _ string) {
				// the source passes the recheck, but the pipeline has no adapter
				adapters.Unregister("claude-code")
			},
			wantErr:  "adapter not found",
			wantHeld: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			projectRoot, agentID, source := setupHandleAfterToolTest(t)
			t.Chdir(projectRoot)
			inst := &agentinstance.Instance{AgentID: agentID}
			held, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
			require.NoError(t, err)
			if tt.setup != nil {
				tt.setup(t, projectRoot, source)
			}
			require.NoError(t, session.MarkSourceRejected(projectRoot, agentID))
			rawPath := filepath.Join(held.SessionPath, "raw.jsonl")
			rawBefore, err := os.ReadFile(rawPath)
			require.NoError(t, err)

			err = runAgentSessionRecover(inst)
			require.Error(t, err, "recover without a release must refuse a quarantined recording")
			assert.Contains(t, err.Error(), "--release-quarantine", "the refusal must say how to get out")

			err = recoverAgentSession(inst, true)
			remaining, loadErr := session.LoadQuarantinedRecordingsForAgent(projectRoot, agentID)
			require.NoError(t, loadErr)
			if tt.wantHeld {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				require.Len(t, remaining, 1, "the recording must stay quarantined")
				rawAfter, readErr := os.ReadFile(rawPath)
				require.NoError(t, readErr)
				assert.Equal(t, string(rawBefore), string(rawAfter), "a refused release must leave the captured data untouched")
				return
			}
			require.NoError(t, err)
			assert.Empty(t, remaining)
			active, loadErr := session.LoadRecordingStateForAgent(projectRoot, agentID)
			require.NoError(t, loadErr)
			assert.Nil(t, active, "a source that passes the recheck recovers and clears the recording")
		})
	}
}

func TestReleaseQuarantine_WithNothingHeldSaysSo(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)
	t.Chdir(projectRoot)
	err := recoverAgentSession(&agentinstance.Instance{AgentID: agentID}, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no quarantined recording")
	active, loadErr := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, loadErr)
	require.NotNil(t, active, "release must not touch an ordinary recording")
}

// Released recording and the agent's new active one coexist. Recovery must work
// on the one it was asked to, not whichever an agent lookup returns first.
func TestReleaseQuarantine_RecoversTheHeldRecordingNotTheActiveOne(t *testing.T) {
	projectRoot, agentID, source := setupHandleAfterToolTest(t)
	t.Chdir(projectRoot)
	held, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NoError(t, session.MarkSourceRejected(projectRoot, agentID))
	moved := moveToEarlierMinute(t, held)
	require.NoError(t, session.UpdateRecordingStateAt(moved.SessionPath, moved.SessionID, func(state *session.RecordingState) {
		state.StartedAt = state.StartedAt.Add(time.Hour)
		state.WorkspacePath = projectRoot
	}))

	fresh, err := session.StartRecording(projectRoot, session.StartRecordingOptions{AgentID: agentID, AdapterName: "claude-code", Username: "testuser", SessionFile: source, WorkspacePath: projectRoot})
	require.NoError(t, err)

	// Simulate a clock rollback, then check both active lookup routes while
	// the held recording is being handed to the recovery pipeline.
	originalRecover := recoverForRelease
	t.Cleanup(func() { recoverForRelease = originalRecover })
	recoverForRelease = func(inst *agentinstance.Instance, root string, state *session.RecordingState) error {
		require.Equal(t, moved.SessionID, state.SessionID)
		active, err := session.LoadRecordingStateForAgent(root, inst.AgentID)
		require.NoError(t, err)
		require.NotNil(t, active)
		require.Equal(t, fresh.SessionID, active.SessionID)
		workspace, err := session.LoadRecordingStateForWorkspace(root, fresh.WorkspacePath)
		require.NoError(t, err)
		require.NotNil(t, workspace)
		require.Equal(t, fresh.SessionID, workspace.SessionID)
		assert.True(t, heldRecording(t, root, inst.AgentID).SourceRejected)
		return originalRecover(inst, root, state)
	}
	require.NoError(t, recoverAgentSession(&agentinstance.Instance{AgentID: agentID}, true))

	_, statErr := os.Stat(filepath.Join(moved.SessionPath, ".recording.json"))
	assert.True(t, os.IsNotExist(statErr), "the released recording is the one recovered and cleared")
	active, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, active, "the agent's new recording must survive the release of the old one")
	assert.Equal(t, fresh.SessionID, active.SessionID)
}

// Stop reloads the recording under the capture lock. With two recordings for one
// agent that reload must find the one being finalized.
func TestFinalDrainReloadFindsItsOwnRecordingBesideAnotherForTheAgent(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)
	held, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NoError(t, session.MarkSourceRejected(projectRoot, agentID))
	moved := moveToEarlierMinute(t, held)
	_, err = session.StartRecording(projectRoot, session.StartRecordingOptions{AgentID: agentID, AdapterName: "claude-code", Username: "testuser"})
	require.NoError(t, err)

	require.NoError(t, fileutil.WithFileLock(context.Background(), filepath.Join(moved.SessionPath, "raw.jsonl"), func() error {
		latest, err := reloadRecordingForFinalDrain(projectRoot, moved)
		require.NoError(t, err)
		assert.Equal(t, moved.SessionID, latest.SessionID)
		assert.Equal(t, moved.SessionPath, latest.SessionPath)
		return nil
	}))

	// and with no folder recorded it still resolves through the agent
	bare := *moved
	bare.SessionPath = ""
	_, err = reloadRecordingForFinalDrain(projectRoot, &bare)
	require.Error(t, err, "a recording that names no folder cannot be reloaded by identity")

	// a recording whose folder lost its state is reported as no longer recording
	gone := *moved
	gone.SessionPath = filepath.Join(t.TempDir(), "no-such-session")
	_, err = reloadRecordingForFinalDrain(projectRoot, &gone)
	require.ErrorIs(t, err, session.ErrNotRecording)
}

// The final drain must only hand back what it can vouch for: a dangling link in a
// visited cwd is "cannot check yet", which must fail the stop for retry instead
// of being mistaken for a vanished transcript and silently dropping the last turns.
func TestFinalizeIncrementalSession_UncheckableSourceFailsInsteadOfDroppingTheDrain(t *testing.T) {
	repo, agentID, source := setupHandleAfterToolTest(t)
	state, err := session.LoadRecordingStateForAgent(repo, agentID)
	require.NoError(t, err)
	first := state.StartedAt.Add(time.Second)
	appendClaudeEntries(t, source, first,
		`{"type":"user","timestamp":"`+first.Format(time.RFC3339Nano)+`","message":{"role":"user","content":"captured"}}`,
	)
	require.NoError(t, handleAfterTool(&HookContext{Phase: phaseAfterTool, AgentType: "claude-code", ProjectRoot: repo, Marker: &SessionMarker{AgentID: agentID}}))

	// the agent visits a directory it cannot be placed in, then answers
	dangling := filepath.Join(repo, "shortcut")
	require.NoError(t, os.Symlink(filepath.Join(filepath.Dir(repo), "removed-elsewhere"), dangling))
	second := state.StartedAt.Add(5 * time.Second)
	f, err := os.OpenFile(source, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString(`{"type":"assistant","sessionId":"session","cwd":` + quote(filepath.Join(dangling, "pkg")) + `,"timestamp":"` + second.Format(time.RFC3339Nano) + `","message":{"role":"assistant","content":[{"type":"text","text":"last reply"}]}}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	state, err = session.LoadRecordingStateForAgent(repo, agentID)
	require.NoError(t, err)
	adapter, err := adapters.GetAdapter(state.AdapterName)
	require.NoError(t, err)
	raw := filepath.Join(state.SessionPath, "raw.jsonl")
	before, err := os.ReadFile(raw)
	require.NoError(t, err)
	_, err = finalizeIncrementalSession(repo, state, raw, adapter, &agentSessionResult{})
	require.ErrorIs(t, err, claudesource.ErrUncheckable, "an unplaceable directory must fail the stop for retry")
	assert.NotErrorIs(t, err, claudesource.ErrSourceGone)
	after, err := os.ReadFile(raw)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "a failed stop must leave the captured data as it was")
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"` }

// Stop imports what it validated. A turn before the recording began is neither
// checked nor imported, even when it carries no timestamp for the age filter to
// catch.
func TestProcessAgentSession_ImportsOnlyWhatItValidated(t *testing.T) {
	projectRoot, agentID, source := setupHandleAfterToolTest(t)
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)

	nativeHeader := `{"type":"session","sessionId":"session","cwd":` + quote(projectRoot) + `}` + "\n"
	// before the recording: a turn from another repository, with no timestamp
	preSession := nativeHeader +
		`{"type":"user","sessionId":"session","cwd":` + quote(filepath.Dir(projectRoot)) + `,"message":{"role":"user","content":"from before the recording"}}` + "\n"
	recordedAt := state.StartedAt.Add(time.Minute)
	recorded := `{"type":"user","sessionId":"session","cwd":` + quote(projectRoot) + `,"timestamp":"` + recordedAt.Format(time.RFC3339Nano) + `","message":{"role":"user","content":"recorded turn"}}` + "\n"
	require.NoError(t, os.WriteFile(source, []byte(preSession+recorded), 0o644))
	state.StartOffset = int64(len(preSession))

	result, err := processAgentSession(projectRoot, state)
	require.NoError(t, err)
	assert.Equal(t, 1, result.EntryCount, "only the turn the recording could have captured is imported")
}

// A hook that rediscovers the transcript (Claude starts a new file after
// compaction) must start the new file from its beginning: the old file's start
// offset points nowhere in it.
func TestHandleAfterTool_RediscoveredTranscriptIsReadFromItsFirstByte(t *testing.T) {
	projectRoot, agentID, source := setupHandleAfterToolTest(t)
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)

	turn := func(text string, at time.Time) string {
		return `{"type":"user","sessionId":"rediscovered","cwd":` + quote(projectRoot) + `,"timestamp":"` + at.Format(time.RFC3339Nano) + `","message":{"role":"user","content":"` + text + `"}}` + "\n"
	}
	first := turn("first turn of the new transcript", state.StartedAt.Add(time.Minute))
	second := turn("second turn of the new transcript", state.StartedAt.Add(2*time.Minute))
	replacement := filepath.Join(t.TempDir(), "rediscovered.jsonl")
	require.NoError(t, os.WriteFile(replacement, []byte(first+second), 0o644))
	// an offset that is a record boundary in the new file by coincidence: the
	// bug is the old file's offset surviving rediscovery, skipping the first turn
	require.NoError(t, session.UpdateRecordingStateForAgent(projectRoot, agentID, func(s *session.RecordingState) {
		s.StartOffset = int64(len(first))
		s.SourceOffset = int64(len(first))
	}))
	require.NoError(t, os.Remove(source))
	adapters.Unregister("claude-code")
	adapters.Register(&rediscoveringClaudeAdapter{found: replacement})

	require.NoError(t, handleAfterTool(&HookContext{Phase: phaseAfterTool, AgentType: "claude-code", ProjectRoot: projectRoot, Marker: &SessionMarker{AgentID: agentID}}))

	after, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, after)
	assert.Equal(t, replacement, after.SessionFile)
	assert.Zero(t, after.StartOffset, "the old file's start offset must not follow the recording to the new file")
	contents, err := os.ReadFile(filepath.Join(after.SessionPath, "raw.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(contents), "first turn of the new transcript", "no byte of the new transcript may be skipped")
	assert.Contains(t, string(contents), "second turn of the new transcript")
}

// rediscoveringClaudeAdapter finds a different native file than the one the
// recording was started with.
type rediscoveringClaudeAdapter struct {
	testClaudeCodeAdapter
	found string
}

func (a *rediscoveringClaudeAdapter) FindSessionFile(_ adapters.SessionLookup) (string, error) {
	return a.found, nil
}

// Stop never finalizes a quarantined recording, and says why instead of the
// misleading "not currently recording".
func TestSessionStopRefusesToFinalizeAQuarantinedRecording(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)
	t.Chdir(projectRoot)
	held, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NoError(t, session.MarkSourceRejected(projectRoot, agentID))
	rawBefore, err := os.ReadFile(filepath.Join(held.SessionPath, "raw.jsonl"))
	require.NoError(t, err)

	err = runAgentSessionStop(&agentinstance.Instance{AgentID: agentID})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "held back for ownership review")
	assert.Contains(t, err.Error(), "ox doctor")

	assert.Equal(t, held.SessionID, heldRecording(t, projectRoot, agentID).SessionID)
	rawAfter, err := os.ReadFile(filepath.Join(held.SessionPath, "raw.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, string(rawBefore), string(rawAfter))
}

// Stop on a session that wandered into another repository must hold the
// recording back, not publish it, and say so.
func TestSessionStopQuarantinesASourceThatCrossedRepositories(t *testing.T) {
	t.Setenv("SAGEOX_DAEMON", "false")
	projectRoot, agentID, source := setupHandleAfterToolTest(t)
	t.Chdir(projectRoot)
	held, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	at := held.StartedAt.Add(time.Minute)
	f, err := os.OpenFile(source, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString(`{"type":"user","sessionId":"session","cwd":` + quote(filepath.Dir(projectRoot)) + `,"timestamp":"` + at.Format(time.RFC3339Nano) + `","message":{"role":"user","content":"worked next door"}}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	err = runAgentSessionStop(&agentinstance.Instance{AgentID: agentID})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "untrusted repository ownership")

	quarantined := heldRecording(t, projectRoot, agentID)
	assert.Equal(t, held.SessionID, quarantined.SessionID, "stop must hold the recording back, not clear it")
}

// An unplaceable directory is "cannot check yet", not "crossed repositories":
// the import fails for retry and the recording is not quarantined.
func TestProcessAgentSession_FailsForRetryWhenOwnershipCannotBeChecked(t *testing.T) {
	projectRoot, agentID, source := setupHandleAfterToolTest(t)
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	dangling := filepath.Join(projectRoot, "shortcut")
	require.NoError(t, os.Symlink(filepath.Join(filepath.Dir(projectRoot), "removed-elsewhere"), dangling))
	at := state.StartedAt.Add(time.Minute)
	f, err := os.OpenFile(source, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString(`{"type":"user","sessionId":"session","cwd":` + quote(filepath.Join(dangling, "pkg")) + `,"timestamp":"` + at.Format(time.RFC3339Nano) + `","message":{"role":"user","content":"somewhere unplaceable"}}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	_, err = processAgentSession(projectRoot, state)
	require.ErrorIs(t, err, claudesource.ErrUncheckable)
	assert.Contains(t, err.Error(), "cannot verify claude source ownership right now")
	assert.NotContains(t, err.Error(), "no longer belongs", "an unplaceable directory is not proof of a foreign turn")
	active, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, active, "the recording must stay active for retry")
}

// Recover on a recording with nothing left to recover clears its stale marker
// and says so; it must clear that recording, not whichever the agent has now.
func TestRecoverClearsOnlyTheEmptyRecordingItWasPointedAt(t *testing.T) {
	projectRoot, agentID, source := setupHandleAfterToolTest(t)
	t.Chdir(projectRoot)
	state, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NoError(t, os.Remove(source))
	require.NoError(t, os.Remove(filepath.Join(state.SessionPath, "raw.jsonl")))
	other, err := session.StartRecording(projectRoot, session.StartRecordingOptions{AgentID: "OxBystander", AdapterName: "claude-code", Username: "testuser"})
	require.NoError(t, err)

	require.NoError(t, runAgentSessionRecover(&agentinstance.Instance{AgentID: agentID}))

	cleared, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	assert.Nil(t, cleared, "a recording with nothing to recover is cleared")
	bystander, err := session.LoadRecordingStateForAgent(projectRoot, "OxBystander")
	require.NoError(t, err)
	require.NotNil(t, bystander, "another agent's recording is not recovered away")
	assert.Equal(t, other.SessionID, bystander.SessionID)
}

// The transcript can vanish between the final drain's read and its ownership
// check. What was read then cannot be vouched for and is not imported; the
// capture the hooks already validated is still finalized.
func TestFinalizeIncrementalSession_TranscriptVanishingMidDrainKeepsTheValidatedCapture(t *testing.T) {
	repo, agentID, source := setupHandleAfterToolTest(t)
	state, err := session.LoadRecordingStateForAgent(repo, agentID)
	require.NoError(t, err)
	first := state.StartedAt.Add(time.Second)
	appendClaudeEntries(t, source, first,
		`{"type":"user","timestamp":"`+first.Format(time.RFC3339Nano)+`","message":{"role":"user","content":"captured by the hooks"}}`,
	)
	require.NoError(t, handleAfterTool(&HookContext{Phase: phaseAfterTool, AgentType: "claude-code", ProjectRoot: repo, Marker: &SessionMarker{AgentID: agentID}}))
	// a turn the final drain will read, and a transcript that disappears right after
	second := state.StartedAt.Add(5 * time.Second)
	appendClaudeEntries(t, source, second,
		`{"type":"user","timestamp":"`+second.Format(time.RFC3339Nano)+`","message":{"role":"user","content":"read, then the file vanished"}}`,
	)
	adapters.Unregister("claude-code")
	adapters.Register(&vanishingClaudeAdapter{})

	state, err = session.LoadRecordingStateForAgent(repo, agentID)
	require.NoError(t, err)
	adapter, err := adapters.GetAdapter(state.AdapterName)
	require.NoError(t, err)
	raw := filepath.Join(state.SessionPath, "raw.jsonl")
	result, err := finalizeIncrementalSession(repo, state, raw, adapter, &agentSessionResult{})
	require.NoError(t, err, "a transcript that vanished mid-check must not fail the stop")
	assert.Equal(t, 1, result.EntryCount, "only the captured turn is finalized")
	contents, err := os.ReadFile(raw)
	require.NoError(t, err)
	assert.Contains(t, string(contents), "captured by the hooks")
	assert.NotContains(t, string(contents), "read, then the file vanished", "a turn that cannot be vouched for must not be imported")
}

// vanishingClaudeAdapter reads the transcript, then removes it, as Claude's
// cleanup can between a read and the check that follows it.
type vanishingClaudeAdapter struct{ testClaudeCodeAdapter }

func (a *vanishingClaudeAdapter) ReadFromOffset(path string, offset int64) ([]adapters.RawEntry, int64, error) {
	entries, next, err := a.testClaudeCodeAdapter.ReadFromOffset(path, offset)
	_ = os.Remove(path)
	return entries, next, err
}

// `ox doctor` runs this scan on every invocation, and the quarantine notice
// itself sends users to `ox doctor`. A stale or stop-incomplete recording is
// reclaimed here (marker deleted, transcript uploaded); a quarantined one must
// be left exactly as it is.
func TestFindOrphanedSessions_NeverReclaimsAQuarantinedRecording(t *testing.T) {
	for _, tt := range []struct {
		name     string
		recState string
		orphan   bool
	}{
		{"quarantined and stale", `{"agent_id":"OxHeld","source_rejected":true,"started_at":"2001-02-03T04:05:00Z"}`, false},
		{"quarantined and stop-incomplete", `{"agent_id":"OxHeld","source_rejected":true,"stop_incomplete":true}`, false},
		// the same recordings without the quarantine are the orphans the scan exists for
		{"stale", `{"agent_id":"OxHeld","started_at":"2001-02-03T04:05:00Z"}`, true},
		{"stop-incomplete", `{"agent_id":"OxHeld","stop_incomplete":true}`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			cacheDir := filepath.Join(tmpDir, "cache", "sessions")
			ledgerDir := filepath.Join(tmpDir, "ledger")
			require.NoError(t, os.MkdirAll(cacheDir, 0o755))
			require.NoError(t, os.MkdirAll(filepath.Join(ledgerDir, "sessions"), 0o755))
			dir := filepath.Join(cacheDir, "2026-01-15T10-30-ryan-OxHeld")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			writeTestRawJSONL(t, filepath.Join(dir, ledgerFileRaw))
			marker := filepath.Join(dir, ".recording.json")
			require.NoError(t, os.WriteFile(marker, []byte(tt.recState), 0o644))
			rawBefore, err := os.ReadFile(filepath.Join(dir, ledgerFileRaw))
			require.NoError(t, err)

			orphans := findTestOrphans(t, cacheDir, ledgerDir)

			if tt.orphan {
				assert.Len(t, orphans, 1)
				assert.NoFileExists(t, marker)
				return
			}
			assert.Empty(t, orphans, "a quarantined recording must never be queued for upload")
			assert.FileExists(t, marker, "the marker that records the quarantine must survive")
			rawAfter, err := os.ReadFile(filepath.Join(dir, ledgerFileRaw))
			require.NoError(t, err)
			assert.Equal(t, string(rawBefore), string(rawAfter))
		})
	}
}

// Upload, regenerate, migrate and doctor's retry all publish through one helper.
// It must refuse a session folder whose recording is quarantined, before it
// builds a client or touches the network.
func TestUploadSessionLFS_RefusesAQuarantinedSession(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "2026-01-15T10-30-ryan-OxHeld")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	writeTestRawJSONL(t, filepath.Join(dir, ledgerFileRaw))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".recording.json"), []byte(`{"agent_id":"OxHeld","source_rejected":true}`), 0o644))

	refs, err := uploadSessionLFS(t.TempDir(), dir)

	require.Error(t, err)
	assert.Nil(t, refs)
	assert.Contains(t, err.Error(), "held back for ownership review")
	assert.Contains(t, err.Error(), "ox agent OxHeld session recover --release-quarantine")
}

// A release lifts the quarantine only for a recovery that publishes. When the
// coworker declines both the upload and the discard, recovery reports success
// without publishing anything; the recording must go back to being quarantined,
// not be left as an ordinary stale recording the next sweep would publish.
func TestReleaseQuarantine_SourceDisappearsBeforeFinalDrain(t *testing.T) {
	projectRoot, agentID, source := setupHandleAfterToolTest(t)
	t.Chdir(projectRoot)
	require.NoError(t, session.MarkSourceRejected(projectRoot, agentID))
	held := heldRecording(t, projectRoot, agentID)
	raw := filepath.Join(held.SessionPath, "raw.jsonl")
	before, err := os.ReadFile(raw)
	require.NoError(t, err)

	originalRecover := recoverForRelease
	t.Cleanup(func() { recoverForRelease = originalRecover })
	recoverForRelease = func(inst *agentinstance.Instance, root string, state *session.RecordingState) error {
		require.NoError(t, os.Remove(source))
		return originalRecover(inst, root, state)
	}

	err = recoverAgentSession(&agentinstance.Instance{AgentID: agentID}, true)
	require.ErrorContains(t, err, "cannot re-check")
	require.Equal(t, held.SessionID, heldRecording(t, projectRoot, agentID).SessionID)
	after, err := os.ReadFile(raw)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

// Failure prevented: `ox doctor` (every run) reclaims a crashed manual-mode
// recording and uploads it; or uploads a session already held (GH #1093).
func TestFindOrphanedSessions_HoldsManualRecording(t *testing.T) {
	for _, tt := range []struct {
		name, recState string
		alreadyHeld    bool
		orphan         bool
	}{
		{"stale, recorded manual", `{"agent_id":"OxHold","started_at":"2001-02-03T04:05:00Z","publishing_mode":"manual"}`, false, false},
		{"already held, no marker", "", true, false},
		{"control: stale, recorded auto", `{"agent_id":"OxHold","started_at":"2001-02-03T04:05:00Z","publishing_mode":"auto"}`, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			cacheDir := filepath.Join(tmpDir, "cache", "sessions")
			ledgerDir := filepath.Join(tmpDir, "ledger")
			require.NoError(t, os.MkdirAll(filepath.Join(ledgerDir, "sessions"), 0o755))
			dir := filepath.Join(cacheDir, "2026-01-15T10-30-ryan-OxHold")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			writeTestRawJSONL(t, filepath.Join(dir, ledgerFileRaw))
			if tt.recState != "" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".recording.json"), []byte(tt.recState), 0o644))
			}
			if tt.alreadyHeld {
				require.NoError(t, session.WriteHoldMarker(dir, session.HoldManualPublishing, "test"))
			}

			orphans := findTestOrphans(t, cacheDir, ledgerDir)

			if tt.orphan {
				assert.Len(t, orphans, 1)
				assert.False(t, session.IsHeld(dir))
				return
			}
			assert.Empty(t, orphans, "a held session must never be queued for upload")
			assert.True(t, session.IsHeld(dir))
			assert.NoFileExists(t, filepath.Join(dir, ".recording.json"), "the stale marker is still reclaimed")
		})
	}
}

// Failure prevented: regenerate, migrate-lfs, recover, or push-summary
// publishes a held session; the explicit 'ox session upload' must still be
// able to (GH #1093).
func TestPublishPaths_RefuseAHeldSession(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "2026-01-15T10-30-ryan-OxHeLd")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	writeTestRawJSONL(t, filepath.Join(dir, ledgerFileRaw))
	require.NoError(t, session.WriteHoldMarker(dir, session.HoldManualPublishing, "test"))

	refs, err := uploadSessionLFS(t.TempDir(), dir)
	require.Error(t, err)
	assert.Nil(t, refs)
	assert.Contains(t, err.Error(), "held on this machine")
	assert.Contains(t, err.Error(), "ox session upload 2026-01-15T10-30-ryan-OxHeLd")

	_, err = publishSessionLFS(t.TempDir(), dir)
	if err != nil {
		assert.NotContains(t, err.Error(), "held on this machine", "the explicit publish is how a hold is released")
	}

	summary := filepath.Join(t.TempDir(), "summary.json")
	require.NoError(t, os.WriteFile(summary, []byte(`{"title":"t","summary":"s","quality_score":0.9}`), 0o644))
	out := pushSummaryToLedger(summary, dir)
	assert.False(t, out.Success)
	assert.Contains(t, out.Error, "held on this machine")
	assert.NoFileExists(t, filepath.Join(dir, "summary.json"), "nothing is written into a held session")
	assert.DirExists(t, dir, "and nothing is removed")
}

// setReleaseQuarantineFlag sets --release-quarantine as cobra would.
func setReleaseQuarantineFlag(t *testing.T) {
	t.Helper()
	require.NoError(t, agentCmd.ParseFlags(nil)) // merges persistent flags into Flags(), as cobra's dispatch does
	require.NoError(t, agentCmd.PersistentFlags().Set("release-quarantine", "true"))
	t.Cleanup(func() { _ = agentCmd.PersistentFlags().Set("release-quarantine", "false") })
}

// quarantinedWithoutInstance quarantines a recording whose source passes the
// re-check, for an AI coworker whose instance has expired out of the store.
func quarantinedWithoutInstance(t *testing.T) (projectRoot, agentID, sessionName string) {
	t.Helper()
	projectRoot, agentID, _ = setupHandleAfterToolTest(t)
	t.Setenv("SAGEOX_ENDPOINT", "http://127.0.0.1:9")
	t.Setenv("DO_NOT_TRACK", "1")
	t.Setenv("SAGEOX_TELEMETRY", "false")
	isolateSessionMarkerDir(t)
	t.Chdir(projectRoot)
	// the commands print through the process-wide config, which only a full
	// CLI start sets; without it these tests pass only after one that left it set
	prevCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = prevCfg })
	require.NoError(t, session.MarkSourceRejected(projectRoot, agentID))
	held := heldRecording(t, projectRoot, agentID)
	_, err := resolveInstance(agentID)
	require.ErrorContains(t, err, "instance not found", "the recording's AI coworker must be gone from the instance store")
	return projectRoot, agentID, session.GetSessionName(held.SessionPath)
}

// An instance expires a day after its AI coworker exits, while its quarantined
// recording waits indefinitely. The command 'ox doctor' prints names that
// recording's agent ID, so it must still work once the instance is gone.
// Failure prevented: every recording quarantined more than a day ago could only
// be discarded (sageox/ox#2029).
func TestReleaseQuarantine_WorksAfterTheInstanceExpires(t *testing.T) {
	for _, tt := range []struct {
		name string
		args func(sessionName string) []string
	}{
		{"without a session name", func(string) []string { return []string{"session", "recover"} }},
		{"with the session name", func(name string) []string { return []string{"session", "recover", name} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			projectRoot, agentID, sessionName := quarantinedWithoutInstance(t)
			setReleaseQuarantineFlag(t)

			held := heldRecording(t, projectRoot, agentID)
			released := captureRelease(t)

			require.NoError(t, runWithAgentID(agentCmd, agentID, tt.args(sessionName)))

			remaining, err := session.LoadQuarantinedRecordingsForAgent(projectRoot, agentID)
			require.NoError(t, err)
			assert.Empty(t, remaining, "a recording whose source passes the re-check is recovered")
			assertReleased(t, released, agentID, held)
		})
	}
}

// A coworker with a live AI coworker passes the session name 'ox doctor'
// listed. The release must act on that recording, whichever agent made it.
func TestReleaseQuarantine_ByNameFromAnotherAgent(t *testing.T) {
	projectRoot, agentID, sessionName := quarantinedWithoutInstance(t)
	registerTestInstance(t, projectRoot, "OxLive")
	setReleaseQuarantineFlag(t)

	err := runWithAgentID(agentCmd, "OxLive", []string{"session", "recover", "2001-02-03T04-05-testuser-OxNope"})
	require.ErrorContains(t, err, "no quarantined recording named 2001-02-03T04-05-testuser-OxNope")
	require.Equal(t, agentID, heldRecording(t, projectRoot, agentID).AgentID, "a name that matches nothing leaves the recording alone")

	held := heldRecording(t, projectRoot, agentID)
	released := captureRelease(t)
	require.NoError(t, runWithAgentID(agentCmd, "OxLive", []string{"session", "recover", sessionName}))
	remaining, err := session.LoadQuarantinedRecordingsForAgent(projectRoot, agentID)
	require.NoError(t, err)
	assert.Empty(t, remaining)
	assertReleased(t, released, agentID, held)
}

// releaseCall is one recovery a release handed to recoverForRelease.
type releaseCall struct {
	agentID string
	state   *session.RecordingState
}

// captureRelease records every recovery a release runs, then runs it.
func captureRelease(t *testing.T) *[]releaseCall {
	t.Helper()
	var calls []releaseCall
	original := recoverForRelease
	t.Cleanup(func() { recoverForRelease = original })
	recoverForRelease = func(inst *agentinstance.Instance, root string, state *session.RecordingState) error {
		calls = append(calls, releaseCall{agentID: inst.AgentID, state: state})
		return original(inst, root, state)
	}
	return &calls
}

// assertReleased checks that exactly the held recording was recovered, under
// its own agent ID, and that recovery kept its captured data rather than
// deleting the folder.
func assertReleased(t *testing.T, calls *[]releaseCall, agentID string, held *session.RecordingState) {
	t.Helper()
	require.Len(t, *calls, 1, "the release must recover exactly one recording")
	call := (*calls)[0]
	assert.Equal(t, agentID, call.agentID, "recovery runs under the agent that made the recording")
	assert.Equal(t, held.SessionID, call.state.SessionID)
	assert.Equal(t, held.SessionPath, call.state.SessionPath)
	assert.FileExists(t, filepath.Join(held.SessionPath, "raw.jsonl"), "recovery keeps the captured data")
}

// The other way out 'ox doctor' offers, discarding by name, must also work
// under the recording's agent ID once its instance has expired.
func TestAbortQuarantinedByNameAfterTheInstanceExpires(t *testing.T) {
	projectRoot, agentID, sessionName := quarantinedWithoutInstance(t)
	held := heldRecording(t, projectRoot, agentID)
	setForceFlag(t, true)

	require.NoError(t, runWithAgentID(agentCmd, agentID, []string{"session", "abort", sessionName}))

	_, statErr := os.Stat(held.SessionPath)
	assert.True(t, os.IsNotExist(statErr), "the quarantined recording is discarded")
}

// A GUI process and a terminal can resolve different cache folders, so a
// recording can be quarantined in one this process does not write to. 'ox
// doctor' lists it there and prints a discard by name, so abort must look there
// too, with or without a Ledger. Failure prevented: a recording held back for
// crossing repositories that its owner could see but never discard.
func TestAbortFindsAQuarantinedRecordingInAnotherProcessCache(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the alternate caches are the macOS and XDG defaults")
	}
	projectRoot, agentID, sessionName := quarantinedWithoutInstance(t)
	held := heldRecording(t, projectRoot, agentID)
	repoID := getRepoIDOrDefault(projectRoot)

	// paths caches the home folder for the life of the process, so ask it where
	// the default cache is instead of trusting this test's HOME
	t.Setenv("XDG_CACHE_HOME", "")
	otherCache := filepath.Join(paths.SessionCacheDir(repoID), "sessions")
	t.Cleanup(func() { _ = os.RemoveAll(paths.SessionCacheDir(repoID)) })
	moved := filepath.Join(otherCache, sessionName)
	require.NoError(t, os.MkdirAll(otherCache, 0o755))
	require.NoError(t, os.Rename(held.SessionPath, moved))
	require.NoError(t, session.MutateRecordingStateFile(filepath.Join(moved, ".recording.json"), func(s *session.RecordingState) error {
		s.SessionPath = moved
		return nil
	}))
	// this process now writes somewhere else, as the terminal does
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	require.Contains(t, paths.AlternateSessionCacheDirs(repoID), filepath.Dir(otherCache), "the recording must sit in a cache this process does not write to")
	_, ledgerErr := resolveLedgerPath()
	require.Error(t, ledgerErr, "the case is the one with no Ledger to lead the search")
	require.Equal(t, moved, heldRecording(t, projectRoot, agentID).SessionPath, "'ox doctor' still lists it")

	require.NoError(t, abortResolvesTo(projectRoot, sessionName, moved))
	setForceFlag(t, true)
	require.NoError(t, runWithAgentID(agentCmd, agentID, []string{"session", "abort", sessionName}))
	assert.NoDirExists(t, moved, "the quarantined recording is discarded")
}

// The fallback stands only on a quarantined recording. An unknown agent ID, or
// any other command under an expired one, still fails at the instance lookup.
func TestExpiredInstanceFallbackIsLimitedToQuarantinedRecordings(t *testing.T) {
	projectRoot, agentID, _ := quarantinedWithoutInstance(t)

	err := runWithAgentID(agentCmd, agentID, []string{"session", "recover"})
	require.ErrorContains(t, err, "instance not found", "recover without a release has nothing to fall back on")
	err = runWithAgentID(agentCmd, agentID, []string{"session", "stop"})
	require.ErrorContains(t, err, "instance not found")

	other := filepath.Join(session.GetContextPath(getRepoIDOrDefault(projectRoot)), "sessions", "2026-03-15T10-00-testuser-OxOthr")
	require.NoError(t, os.MkdirAll(other, 0o755))
	setForceFlag(t, true)
	err = runWithAgentID(agentCmd, agentID, []string{"session", "abort", filepath.Base(other)})
	require.ErrorContains(t, err, "instance not found", "an expired agent may discard only its own quarantined recording")
	assert.DirExists(t, other)

	setReleaseQuarantineFlag(t)
	err = runWithAgentID(agentCmd, "OxNone", []string{"session", "recover"})
	require.ErrorContains(t, err, "instance not found: OxNone", "an agent with no quarantined recording is still unknown")
	err = runWithAgentID(agentCmd, agentID, []string{"session", "recover", filepath.Base(other)})
	require.ErrorContains(t, err, "instance not found", "nor release a session that is not its quarantined recording")
}

// Without a name the fallback cannot tell which of several quarantined
// recordings 'ox doctor' meant, so it asks for one instead of picking.
func TestExpiredInstanceFallbackNeedsANameForSeveralRecordings(t *testing.T) {
	projectRoot, agentID, _ := quarantinedWithoutInstance(t)
	first := moveToEarlierMinute(t, heldRecording(t, projectRoot, agentID))
	source := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(source, []byte(fmt.Sprintf("{\"type\":\"session\",\"sessionId\":\"second\",\"cwd\":%q}\n", projectRoot)), 0o644))
	_, err := session.StartRecording(projectRoot, session.StartRecordingOptions{AgentID: agentID, AdapterName: "claude-code", Username: "testuser", SessionFile: source, WorkspacePath: projectRoot})
	require.NoError(t, err)
	require.NoError(t, session.MarkSourceRejected(projectRoot, agentID))
	held, err := session.LoadQuarantinedRecordingsForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.Len(t, held, 2)
	setReleaseQuarantineFlag(t)
	released := captureRelease(t)

	err = runWithAgentID(agentCmd, agentID, []string{"session", "recover"})
	require.ErrorContains(t, err, "has 2 quarantined recordings; name the one to release")
	assert.Empty(t, *released, "nothing is released without a name")

	require.NoError(t, runWithAgentID(agentCmd, agentID, []string{"session", "recover", session.GetSessionName(first.SessionPath)}))
	assertReleased(t, released, agentID, first)
}

// A discard under an expired owner acts only on the quarantined recording it
// checked. A different folder the name resolves to first is left alone.
func TestAbortResolvesToRefusesAnotherFolderWithTheName(t *testing.T) {
	projectRoot, agentID, sessionName := quarantinedWithoutInstance(t)
	held := heldRecording(t, projectRoot, agentID)
	require.NoError(t, abortResolvesTo(projectRoot, sessionName, held.SessionPath))

	elsewhere := filepath.Join(t.TempDir(), sessionName)
	require.NoError(t, os.MkdirAll(elsewhere, 0o755))
	err := abortResolvesTo(projectRoot, sessionName, elsewhere)
	require.ErrorContains(t, err, "not the quarantined recording")
	assert.Contains(t, err.Error(), "nothing was removed")
	assert.DirExists(t, held.SessionPath)
}

// plantLegacyMarker writes a quarantined marker for agentID in the in-repo
// legacy sessions/ folder, as anyone who can commit to the repository could.
func plantLegacyMarker(t *testing.T, projectRoot, agentID string) string {
	t.Helper()
	dir := filepath.Join(projectRoot, "sessions", "2099-01-02T03-04-testuser-"+agentID)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	source := filepath.Join(t.TempDir(), "planted.jsonl")
	require.NoError(t, os.WriteFile(source, []byte(fmt.Sprintf("{\"type\":\"session\",\"sessionId\":\"planted\",\"cwd\":%q}\n", projectRoot)), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte("{}\n"), 0o644))
	data, err := json.Marshal(session.RecordingState{
		AgentID:        agentID,
		AdapterName:    "claude-code",
		SessionID:      "planted",
		SessionPath:    dir,
		SessionFile:    source,
		WorkspacePath:  projectRoot,
		StartedAt:      time.Now().Add(time.Hour),
		SourceRejected: true,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".recording.json"), data, 0o600))
	return dir
}

// The in-repo legacy sessions/ folder is controlled by anyone who can commit,
// and ox never quarantines there. An unnamed release or abort must not pick a
// marker planted there, for a live or an expired agent; the agent's own cache
// recording stays selectable.
func TestUnnamedReleaseAndAbortSkipLegacyMarkers(t *testing.T) {
	t.Run("live agent release", func(t *testing.T) {
		projectRoot, _, _ := quarantinedWithoutInstance(t)
		registerTestInstance(t, projectRoot, "OxLive")
		planted := plantLegacyMarker(t, projectRoot, "OxLive")
		setReleaseQuarantineFlag(t)
		released := captureRelease(t)

		err := runWithAgentID(agentCmd, "OxLive", []string{"session", "recover"})
		require.ErrorContains(t, err, "no quarantined recording to release")
		assert.Empty(t, *released)
		assert.FileExists(t, filepath.Join(planted, ".recording.json"))
	})
	t.Run("live agent abort", func(t *testing.T) {
		projectRoot, _, _ := quarantinedWithoutInstance(t)
		registerTestInstance(t, projectRoot, "OxLive")
		planted := plantLegacyMarker(t, projectRoot, "OxLive")
		setForceFlag(t, true)

		err := runWithAgentID(agentCmd, "OxLive", []string{"session", "abort"})
		require.ErrorContains(t, err, "no active session to abort")
		assert.NotContains(t, err.Error(), filepath.Base(planted), "the refusal must not point at a planted marker")
		assert.DirExists(t, planted)
	})
	t.Run("expired agent release", func(t *testing.T) {
		projectRoot, agentID, _ := quarantinedWithoutInstance(t)
		held := heldRecording(t, projectRoot, agentID)
		planted := plantLegacyMarker(t, projectRoot, agentID)
		setReleaseQuarantineFlag(t)
		released := captureRelease(t)

		require.NoError(t, runWithAgentID(agentCmd, agentID, []string{"session", "recover"}))
		assertReleased(t, released, agentID, held)
		assert.FileExists(t, filepath.Join(planted, ".recording.json"))
	})
}

// The expired-owner release checks the agent has exactly one quarantined
// recording, then releases that one. A recording quarantined after the check
// is not released in its place.
func TestExpiredOwnerReleaseIsBoundToTheCheckedRecording(t *testing.T) {
	projectRoot, agentID, _ := quarantinedWithoutInstance(t)
	first := moveToEarlierMinute(t, heldRecording(t, projectRoot, agentID))
	setReleaseQuarantineFlag(t)
	released := captureRelease(t)

	var newer *session.RecordingState
	original := loadQuarantinedForAgent
	t.Cleanup(func() { loadQuarantinedForAgent = original })
	loadQuarantinedForAgent = func(root, id string) ([]*session.RecordingState, error) {
		held, err := original(root, id)
		if newer == nil {
			source := filepath.Join(t.TempDir(), "session.jsonl")
			require.NoError(t, os.WriteFile(source, []byte(fmt.Sprintf("{\"type\":\"session\",\"sessionId\":\"newer\",\"cwd\":%q}\n", root)), 0o644))
			newer, err = session.StartRecording(root, session.StartRecordingOptions{AgentID: id, AdapterName: "claude-code", Username: "testuser", SessionFile: source, WorkspacePath: root})
			require.NoError(t, err)
			require.NoError(t, session.MarkSourceRejected(root, id))
		}
		return held, err
	}

	require.NoError(t, runWithAgentID(agentCmd, agentID, []string{"session", "recover"}))
	assertReleased(t, released, agentID, first)
	still := heldRecording(t, projectRoot, agentID)
	assert.Equal(t, newer.SessionID, still.SessionID, "the recording quarantined after the check stays held")
}
