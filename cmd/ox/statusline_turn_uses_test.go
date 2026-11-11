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
