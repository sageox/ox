package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/ledger"
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
	first := startSessionRecording(f.projectRoot, agentID, "claude-code", "", "", "")
	require.NotNil(t, first)
	require.True(t, first.Recording)
	require.NotContains(t, first.UserNotification, "held back")
	held, err := session.LoadRecordingStateForAgent(f.projectRoot, agentID)
	require.NoError(t, err)
	require.NotNil(t, held)
	require.NoError(t, session.MarkSourceRejected(f.projectRoot, agentID))
	moved := moveToEarlierMinute(t, held)

	status := startSessionRecording(f.projectRoot, agentID, "claude-code", "", "", "")
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

	fresh, err := session.StartRecording(projectRoot, session.StartRecordingOptions{AgentID: agentID, AdapterName: "claude-code", Username: "testuser", SessionFile: source})
	require.NoError(t, err)

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
func TestReleaseQuarantine_DecliningThePromptsPutsTheQuarantineBack(t *testing.T) {
	projectRoot, agentID, _ := setupHandleAfterToolTest(t)
	t.Chdir(projectRoot)
	held, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	require.NoError(t, session.MarkSourceRejected(projectRoot, agentID))
	raw := filepath.Join(held.SessionPath, "raw.jsonl")
	rawBefore, err := os.ReadFile(raw)
	require.NoError(t, err)

	// the cache-recovery path, with a coworker who answers no to every question
	prompted := 0
	oldEnabled, oldConfirm, oldRecover := recoverPromptsEnabled, recoverConfirm, recoverForRelease
	t.Cleanup(func() { recoverPromptsEnabled, recoverConfirm, recoverForRelease = oldEnabled, oldConfirm, oldRecover })
	recoverPromptsEnabled = func() bool { return true }
	recoverConfirm = func(string, bool) bool { prompted++; return false }
	recoverForRelease = func(inst *agentinstance.Instance, projectRoot string, state *session.RecordingState) error {
		return recoverFromCache(inst, projectRoot, state, filepath.Join(state.SessionPath, "raw.jsonl"))
	}

	err = recoverAgentSession(&agentinstance.Instance{AgentID: agentID}, true)
	require.NoError(t, err, "declining is not a failure")
	assert.Equal(t, 2, prompted, "the coworker was asked to upload and then to discard")

	again := heldRecording(t, projectRoot, agentID)
	assert.Equal(t, held.SessionID, again.SessionID)
	assert.True(t, again.SourceRejected, "a release that published nothing must put the quarantine back")
	rawAfter, err := os.ReadFile(raw)
	require.NoError(t, err)
	assert.Equal(t, string(rawBefore), string(rawAfter), "declined means untouched")
	active, err := session.LoadRecordingStateForAgent(projectRoot, agentID)
	require.NoError(t, err)
	assert.Nil(t, active, "the recording must not be left looking like an ordinary stale one")
}
