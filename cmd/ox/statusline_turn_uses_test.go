package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/agentx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/oxactivity"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contexttrace"
)

// "N used" on the status line: the use evidence is in the AI coworker's
// reply, so the Stop hook scans the turn as it lands. Before this, only
// `ox session report` ran the scan and the line sat stale until the pane ran.

// turnUsesEnv is liveBriefEnv plus the session marker `ox statusline` needs
// to tie the harness session to the recording's AI coworker. The trace
// surfaces two conversations; the reply names the second by title.
func turnUsesEnv(t *testing.T) (string, *session.RecordingState) {
	t.Helper()
	isolateSessionMarkerDir(t)
	root, state := liveBriefEnv(t)
	require.NoError(t, WriteSessionMarker(&SessionMarker{
		AgentID: usageAgentID, AgentSessionID: liveHarnessSess, PrimedAt: time.Now(),
	}))
	traceConversations(t, state, 1, [2]string{liveCnvA, liveTitleA}, [2]string{liveCnvB, liveTitleB})
	writeLiveRaw(t, state,
		liveUser("hi"),
		liveAssistant("We agreed to stage it behind a flag; the "+liveTitleB+" raised the same concern."),
	)
	return root, state
}

func runStopFor(t *testing.T, root, agentID string) {
	t.Helper()
	ctx := &HookContext{
		Phase: phaseStop, AgentType: "claude-code", ProjectRoot: root,
		Input:  &agentx.HookInput{SessionID: liveHarnessSess},
		Marker: &SessionMarker{AgentID: agentID, AgentSessionID: liveHarnessSess},
	}
	var hookErr error
	captureRealStdout(t, func() { hookErr = handleStop(ctx) })
	require.NoError(t, hookErr)
}

// statuslineFor is what `ox statusline` prints for the recording's harness
// session, as Claude Code would run it.
func statuslineFor(t *testing.T, root string) string {
	t.Helper()
	stdin, err := json.Marshal(map[string]any{"session_id": liveHarnessSess, "cwd": root})
	require.NoError(t, err)
	var out bytes.Buffer
	runStatusline(bytes.NewReader(stdin), &out, testStatuslineOptions())
	return out.String()
}

func usedCount(root string) int {
	return oxactivity.LoadState(root, oxactivity.StateKey(usageAgentID, liveHarnessSess)).Used
}

// TestStop_CountsTurnUsesOnStatusline is the regression test: a reply that
// names a surfaced discussion moves "used" on the status line at the end of
// that turn, with nothing else running. Failure prevented: the line shows
// "0 used" for discussions the reply was built from until the coworker opens
// the session report pane (which runs the same scan).
func TestStop_CountsTurnUsesOnStatusline(t *testing.T) {
	root, state := turnUsesEnv(t)
	require.Equal(t, 0, usedCount(root))

	runStopFor(t, root, usageAgentID)
	assert.Equal(t, 1, usedCount(root))
	assert.Contains(t, statuslineFor(t, root), "1 used", "the line Claude Code draws")

	// A later turn with no new reply changes nothing.
	runStopFor(t, root, usageAgentID)
	assert.Equal(t, 1, usedCount(root))

	// `ox session report` finds nothing the hook has not already counted.
	events, err := contexttrace.ReadEvents(filepath.Join(state.SessionPath, contexttrace.FileName))
	require.NoError(t, err)
	_ = withLiveUses(root, state, events)
	assert.Equal(t, 1, usedCount(root), "report and hook agree")
}

// TestStop_TurnUsesSilentCases: nothing is counted or cached when the status
// line is off, or there is no live recording for the AI coworker.
func TestStop_TurnUsesSilentCases(t *testing.T) {
	t.Run("status line off", func(t *testing.T) {
		root, state := turnUsesEnv(t)
		setStatusline(t, false)
		runStopFor(t, root, usageAgentID)
		assert.Equal(t, 0, usedCount(root))
		_, err := os.Stat(briefUsesCachePath(consultRoots(root).Ledger, state.AgentID))
		assert.True(t, os.IsNotExist(err), "no scan ran")
	})
	t.Run("recording not yet bound to a harness session", func(t *testing.T) {
		// A manual `ox session start` can record before ox knows the harness
		// session. Scanning then would cache the use as already seen while
		// counting nothing, so it must not scan at all.
		isolateSessionMarkerDir(t)
		root := usageTraceEnv(t, nil)
		t.Setenv("SAGEOX_ENDPOINT", "")
		t.Setenv("XDG_DATA_HOME", t.TempDir())
		require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox", "config.json"),
			[]byte(`{"config_version":"2","repo_id":"repo_brief_live","endpoint":"`+liveEndpoint+`"}`), 0o644))
		state, err := session.StartRecording(root, session.StartRecordingOptions{AgentID: usageAgentID, AdapterName: "claude-code"})
		require.NoError(t, err)
		require.Empty(t, state.AgentSessionID)
		traceConversations(t, state, 1, [2]string{liveCnvB, liveTitleB})
		writeLiveRaw(t, state, liveUser("hi"), liveAssistant("the "+liveTitleB+" raised it"))

		runStopFor(t, root, usageAgentID)
		_, err = os.Stat(briefUsesCachePath(consultRoots(root).Ledger, state.AgentID))
		assert.True(t, os.IsNotExist(err), "no scan may run before the binding")
	})
	t.Run("not recording", func(t *testing.T) {
		root, _ := turnUsesEnv(t)
		runStopFor(t, root, "OxOthr1") // an AI coworker with no recording
		assert.Equal(t, 0, oxactivity.LoadState(root, oxactivity.StateKey("OxOthr1", liveHarnessSess)).Used)
	})
}

// TestStop_CodexDigestShowsTurnUses pins the order inside Stop for Codex: the
// turn's uses are counted before the digest reads the counts, so the notice
// says "1 used" on the very turn that used it. Failure prevented: Codex,
// which has no pane to run the scan for it, never showing a use at all.
func TestStop_CodexDigestShowsTurnUses(t *testing.T) {
	root, _ := turnUsesEnv(t)
	ctx := &HookContext{
		Phase: phaseStop, AgentType: "codex", ProjectRoot: root,
		Input:  &agentx.HookInput{SessionID: liveHarnessSess},
		Marker: &SessionMarker{AgentID: usageAgentID, AgentSessionID: liveHarnessSess},
	}
	var hookErr error
	out := string(captureRealStdout(t, func() { hookErr = handleStop(ctx) }))
	require.NoError(t, hookErr)
	doc := decodeCodexHookDoc(t, out)
	assert.Equal(t, "● ox  1 discussion · 1 used  ● rec", doc.SystemMessage, "the cited discussion counts as used, and as a discussion")
}

// TestStatusline_NewRecordingStartsOver: one Claude Code session can span
// several ox recordings (a resume, a pause, an idle finalize), while `ox
// session report` counts only the current one. The bar must agree with it:
// when a new recording starts, the line goes back to zero and then counts
// the new recording's work only. Failure prevented: "4 discussions · 5 used"
// on the bar beside "used 3 of 13" in the pane, the two extra being a
// recording that closed yesterday.
func TestStatusline_NewRecordingStartsOver(t *testing.T) {
	root, first := turnUsesEnv(t)
	runStopFor(t, root, usageAgentID)
	require.Contains(t, statuslineFor(t, root), "1 used")
	key := oxactivity.StateKey(usageAgentID, liveHarnessSess)
	require.Equal(t, first.SessionID, oxactivity.LoadState(root, key).Recording)

	// The recording ends and a new one starts for the same harness session.
	_, err := session.StopRecording(root, usageAgentID)
	require.NoError(t, err)
	second, err := session.StartRecording(root, session.StartRecordingOptions{
		AgentID: usageAgentID, AdapterName: "claude-code", AgentSessionID: liveHarnessSess,
	})
	require.NoError(t, err)
	require.NotEqual(t, first.SessionID, second.SessionID)

	line := statuslineFor(t, root)
	assert.NotContains(t, line, "used", "a new recording starts at zero before anything is written: %q", line)

	// The first write of the new recording resets the file, then counts.
	traceConversations(t, second, 1, [2]string{liveCnvA, liveTitleA})
	writeLiveRaw(t, second, liveUser("again"), liveAssistant("as the "+liveTitleA+" put it"))
	runStopFor(t, root, usageAgentID)
	got := oxactivity.LoadState(root, key)
	assert.Equal(t, second.SessionID, got.Recording)
	assert.Equal(t, oxactivity.Counts{Discussions: 1, Used: 1}, got.Counts(), "only the new recording's use")
	assert.Contains(t, statuslineFor(t, root), "1 discussion · 1 used")
}
