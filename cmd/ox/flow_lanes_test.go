package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sageox/agentx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/session/contexttrace"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// Flow lanes outside the Claude Code plugin: Codex's notice under each ox
// call, and `ox statusline --watch`. These tests drive the real hook handlers (pre-tool,
// post-tool, Stop) the way Codex fires them and read what the coworker sees.

const searchJSON = `{"results":[{"id":"a"},{"id":"b"},{"id":"c"}],"pad":"` + "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx" + `"}`

// codexToolInput is one Codex shell tool call as its hooks deliver it.
func codexToolInput(t *testing.T, command, stdout string) *agentx.HookInput {
	t.Helper()
	in := &agentx.HookInput{SessionID: codexDigestSessionID, ToolName: "exec_command", ToolInput: shellInput(t, "cmd", command)}
	if stdout != "" {
		resp, err := json.Marshal(map[string]string{"stdout": stdout})
		require.NoError(t, err)
		in.ToolResponse = resp
	}
	return in
}

// runCodexCall runs one Codex tool call through the real hooks: the pre-tool
// start `took` before the post-tool handler. It returns the notice Codex
// shows under the call, line by line; nil when PostToolUse printed none.
func runCodexCall(t *testing.T, root, command, stdout string, took time.Duration) []string {
	t.Helper()
	recordFlowStart(root, codexToolInput(t, command, ""), time.Now().Add(-took))
	return afterToolNotice(t, root, codexToolInput(t, command, stdout))
}

// afterToolNotice runs the real PostToolUse handler and reads its notice.
func afterToolNotice(t *testing.T, root string, in *agentx.HookInput) []string {
	t.Helper()
	var hookErr error
	out := string(captureRealStdout(t, func() {
		hookErr = handleAfterTool(codexDigestCtx(root, "codex", phaseAfterTool, in))
	}))
	require.NoError(t, hookErr)
	if out == "" {
		return nil
	}
	doc := decodeCodexHookDoc(t, out)
	require.NotContains(t, out, "\x1b", "hook protocol text carries no escape bytes")
	require.Nil(t, doc.Continue, "a lane must never steer Codex's control flow")
	require.Nil(t, doc.Decision)
	if doc.SystemMessage == "" {
		return nil
	}
	return strings.Split(doc.SystemMessage, "\n")
}

func stopNotice(t *testing.T, root string) []string {
	t.Helper()
	out := runStopHook(t, root, "codex")
	if out == "" {
		return nil
	}
	return strings.Split(decodeCodexHookDoc(t, out).SystemMessage, "\n")
}

// --- A. The notice under each ox call ---

// TestCodexAfterTool_ShowsTheCallsFlowLane is the customer promise: in Codex,
// an ox call that moved team data shows, right under that call, what moved,
// which way, how much and how long it took, once.
// Failure prevented: Codex coworkers seeing nothing of what SageOx gave the
// session, the lanes arriving only in a batch after the turn, the status
// line riding along, or a lane repeated at the end of the turn.
func TestCodexAfterTool_ShowsTheCallsFlowLane(t *testing.T) {
	root := codexDigestEnv(t, nil)

	notice := runCodexCall(t, root, `ox query "auth middleware"`, searchJSON, 800*time.Millisecond)
	require.Len(t, notice, 1, "one lane, no status line: %q", notice)
	assert.Regexp(t, regexp.MustCompile(`^SAGEOX ●─{16}● SESSION  searched team memory · ~1\d\d from 3 discussions · 0\.[89]s$`), notice[0])

	// A call that counts an item on the status line still shows only its lane.
	notice = runCodexCall(t, root, "ox plan view x", "# plan", 300*time.Millisecond)
	require.Len(t, notice, 1, "%q", notice)
	assert.Contains(t, notice[0], "pulled plan")
	assert.NotContains(t, strings.Join(notice, ""), "● ox")

	// Outbound: ox cannot see a CLI murmur's size, and says so rather than
	// reporting the acknowledgement's size as what was sent.
	notice = runCodexCall(t, root, `ox murmur --topic=wip "editing the lane"`, "Murmur published: 01abc", 400*time.Millisecond)
	require.Len(t, notice, 1)
	assert.Regexp(t, regexp.MustCompile(`^SAGEOX ●─{16}● SESSION  posted murmur · size unknown · 0\.[45]s$`), notice[0])
	assert.NotContains(t, notice[0], "editing the lane", "a murmur's body never reaches the notice")

	assert.Nil(t, stopNotice(t, root), "every lane was shown under its call: the turn's end adds nothing")
}

// TestCodexAfterTool_FlowLaneCases covers what must and must not draw a lane.
// Failure prevented: plumbing calls (doctor, status) or ordinary shell
// commands drawing a notice after every tool call, a failed call drawn as
// delivered, or lanes shown to a coworker who turned them off.
func TestCodexAfterTool_FlowLaneCases(t *testing.T) {
	tests := []struct {
		name string
		call func(t *testing.T, root string) []string
		want string // regexp for the one lane; "" = no notice at all
	}{
		{name: "an ox call that moves no team data", call: func(t *testing.T, root string) []string {
			return runCodexCall(t, root, "ox doctor", "all good", 300*time.Millisecond)
		}},
		{name: "the session report itself", call: func(t *testing.T, root string) []string {
			return runCodexCall(t, root, "ox session report", "{}", 300*time.Millisecond)
		}},
		{name: "a non-ox tool call", call: func(t *testing.T, root string) []string {
			return runCodexCall(t, root, "ls -la", "x", 100*time.Millisecond)
		}},
		{name: "flow lanes off", call: func(t *testing.T, root string) []string {
			require.NoError(t, SetConfigValue("flow_lanes", "off", ConfigLevelUser, ""))
			return runCodexCall(t, root, "ox plan view x", "# plan", 300*time.Millisecond)
		}},
		{name: "status line off turns the lanes off too", call: func(t *testing.T, root string) []string {
			setStatusline(t, false)
			return runCodexCall(t, root, `ox query "x"`, searchJSON, 300*time.Millisecond)
		}},
		{name: "a failed call draws the stub and says failed", call: func(t *testing.T, root string) []string {
			in := codexToolInput(t, `ox query "x"`, "")
			in.ToolError = "exit status 1"
			recordFlowStart(root, codexToolInput(t, `ox query "x"`, ""), time.Now().Add(-1200*time.Millisecond))
			return afterToolNotice(t, root, in)
		}, want: `^SAGEOX ●╶─ {14}● SESSION  failed while searching team discussions · 1\.[23]s$`},
		{name: "no pre-tool hook: the lane shows without a duration", call: func(t *testing.T, root string) []string {
			return afterToolNotice(t, root, codexToolInput(t, `ox query "x"`, searchJSON))
		}, want: `SESSION  searched team memory · ~1\d\d from 3 discussions$`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := codexDigestEnv(t, nil)
			notice := tt.call(t, root)
			if tt.want == "" {
				assert.Nil(t, notice)
			} else {
				require.Len(t, notice, 1, "notice: %q", notice)
				assert.Regexp(t, regexp.MustCompile(tt.want), notice[0])
			}
			assert.Nil(t, stopNotice(t, root), "Codex's turn end shows no status line and repeats no lane")
		})
	}
}

// TestCodexAfterTool_LaneAndWhisperShareOneDocument: Codex drops stdout that
// is not exactly one JSON document.
// Failure prevented: a lane and a pending whisper printed as two documents,
// losing both.
func TestCodexAfterTool_LaneAndWhisperShareOneDocument(t *testing.T) {
	withFakeRunner(t, &fakeRunner{})
	root := codexDigestEnv(t, []whisperstore.WhisperEntry{teammateMurmur()})
	var hookErr error
	out := string(captureRealStdout(t, func() {
		hookErr = handleAfterTool(codexDigestCtx(root, "codex", phaseAfterTool, codexToolInput(t, `ox query "x"`, searchJSON)))
	}))
	require.NoError(t, hookErr)
	doc := decodeCodexHookDoc(t, out)
	assert.Contains(t, doc.SystemMessage, "searched team memory")
	require.NotNil(t, doc.HookSpecificOutput, "the whisper still reaches the model")
	assert.Contains(t, doc.HookSpecificOutput.AdditionalContext, "Refactoring auth middleware")
}

// TestStop_ShowsLanesAnEarlierNoticeMissed: Conductor's Claude Code shows the
// turn's lanes at its end, under the status line, newest five.
// Failure prevented: a host with no per-call notice never showing the lanes,
// or a long turn flooding the transcript.
func TestStop_ShowsLanesAnEarlierNoticeMissed(t *testing.T) {
	root := codexDigestEnv(t, nil)
	stubOrchestrator(t, "conductor")
	for range 7 {
		captureRealStdout(t, func() {
			in := &agentx.HookInput{SessionID: codexDigestSessionID, ToolName: "Bash", ToolInput: shellInput(t, "command", "ox plan view x")}
			require.NoError(t, handleAfterTool(codexDigestCtx(root, "claude-code", phaseAfterTool, in)))
		})
	}
	notice := strings.Split(decodeCodexHookDoc(t, runStopHook(t, root, "claude-code")).SystemMessage, "\n")
	require.Len(t, notice, 7, "%q", notice)
	assert.Equal(t, "● ox  1 plan", notice[0])
	assert.Equal(t, "+2 earlier ox calls", notice[1])
	for _, row := range notice[2:] {
		assert.Contains(t, row, "pulled plan")
	}
	assert.Empty(t, runStopHook(t, root, "claude-code"), "shown once")
}

// TestCodexNotice_FirstSkipsAnEarlierSittingsCalls: the not-recording trace
// is kept per AI coworker for a week.
// Failure prevented: a session's first notice replaying ox calls from days ago.
func TestCodexNotice_FirstSkipsAnEarlierSittingsCalls(t *testing.T) {
	root := codexDigestEnv(t, nil)
	old := contexttrace.Event{
		Type: contexttrace.EventInvoked, Source: contexttrace.SourceOxCLI, Command: "query", Label: "searched team memory",
		InlineTokens: 900, Timestamp: time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339),
	}
	dir := agentTraceCacheDir(root)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, contexttrace.NewFileWriter(filepath.Join(dir, codexDigestAgentID+".jsonl")).Append(old))

	assert.Nil(t, stopNotice(t, root), "old calls alone are not news")

	notice := runCodexCall(t, root, "ox plan view x", "# plan", 300*time.Millisecond)
	require.Len(t, notice, 1, "only this call: %q", notice)
	assert.Contains(t, notice[0], "pulled plan")
}

// --- B. The running marker ---

// TestFlowStart_OnlyOxCallsThatMoveTeamData keeps the pre-tool hook a no-op
// for everything else. Failure prevented: every tool call of every Codex turn
// paying a file write, or a marker that never clears and leaves a lane
// "running" forever.
func TestFlowStart_OnlyOxCallsThatMoveTeamData(t *testing.T) {
	root := codexDigestEnv(t, nil)
	now := time.Now().Truncate(time.Millisecond) // the marker keeps milliseconds
	flowDir := filepath.Join(root, ".sageox", "cache", "flow")

	for _, cmd := range []string{"ls", "ox doctor", "ox agent " + codexDigestAgentID + " heartbeat", "git status"} {
		recordFlowStart(root, codexToolInput(t, cmd, ""), now)
	}
	assert.NoDirExists(t, flowDir, "nothing that moves team data: nothing written")

	// An unprimed session has no AI coworker to file a start under.
	unprimed := codexToolInput(t, `ox query "x"`, "")
	unprimed.SessionID = "never-primed"
	recordFlowStart(root, unprimed, now)
	assert.NoDirExists(t, flowDir)

	recordFlowStart(root, codexToolInput(t, `ox query "x"`, ""), now)
	running := runningFlowLanes(root, codexDigestAgentID, now.Add(time.Second))
	require.Len(t, running, 1)
	assert.True(t, running[0].lane.Running)
	assert.Equal(t, "searching team discussions", running[0].lane.Step)
	assert.Equal(t, time.Second, running[0].elapsed, "the marching dots are drawn from when the call started")

	// Another command's finish does not take it; its own does, once.
	_, ok := takeFlowStart(root, codexDigestAgentID, "plan view", "", now.Add(time.Second))
	assert.False(t, ok)
	took, ok := takeFlowStart(root, codexDigestAgentID, "query", "", now.Add(1500*time.Millisecond))
	require.True(t, ok)
	assert.Equal(t, 1500*time.Millisecond, took)
	_, ok = takeFlowStart(root, codexDigestAgentID, "query", "", now.Add(2*time.Second))
	assert.False(t, ok)
	assert.NoFileExists(t, flowMarkerPath(root, codexDigestAgentID), "an empty marker is removed")

	// A start whose finish never came (the call was denied, the harness died) ages out.
	recordFlowStart(root, codexToolInput(t, `ox query "x"`, ""), now)
	assert.Empty(t, runningFlowLanes(root, codexDigestAgentID, now.Add(flowRunningMaxAge+time.Second)))
	_, ok = takeFlowStart(root, codexDigestAgentID, "query", "", now.Add(flowRunningMaxAge+time.Second))
	assert.False(t, ok)

	// A corrupt marker reads as empty rather than failing the hook.
	require.NoError(t, os.MkdirAll(flowDir, 0o755))
	require.NoError(t, os.WriteFile(flowMarkerPath(root, codexDigestAgentID), []byte("{not json"), 0o600))
	assert.Empty(t, runningFlowLanes(root, codexDigestAgentID, now))
	recordFlowStart(root, codexToolInput(t, `ox query "x"`, ""), now)
	assert.Len(t, runningFlowLanes(root, codexDigestAgentID, now), 1)

	assert.Empty(t, flowMarkerPath(root, "../escape"), "an agent ID names a file: only a well-formed one may")
}

// TestFlowMarker_ALaterHooksStartSurvivesAnEarlierClock: each hook reads its
// clock before it takes the marker's lock. Failure prevented: the hook that
// read first but locked second finding the other's start "in the future" and
// deleting it, so that call's lane vanishes and its finish has no duration.
func TestFlowMarker_ALaterHooksStartSurvivesAnEarlierClock(t *testing.T) {
	root := codexDigestEnv(t, nil)
	early := time.Now().Truncate(time.Millisecond) // the marker keeps milliseconds
	later := early.Add(40 * time.Millisecond)

	recordFlowStart(root, codexToolInput(t, `ox query "x"`, ""), later) // locked first
	recordFlowStart(root, codexToolInput(t, "ox plan view x", ""), early)

	running := runningFlowLanes(root, codexDigestAgentID, early)
	require.Len(t, running, 2, "both calls are running")
	took, ok := takeFlowStart(root, codexDigestAgentID, "query", "", later.Add(time.Second))
	require.True(t, ok)
	assert.Equal(t, time.Second, took)

	// A start far ahead of the clock is not believed: a corrupt or foreign value.
	ahead := []flowRunning{{Command: "query", StartedAt: early.Add(flowRunningMaxAge + time.Minute).UnixMilli()}}
	assert.Empty(t, liveFlowRunning(ahead, early))
}

// TestFlowMarker_AFinishTakesItsOwnStart: a harness names each tool call.
// Failure prevented: a call whose finish never came (denied, killed) lending
// its start to the next call of the same command, which then reports the
// wrong duration and leaves itself shown as running.
func TestFlowMarker_AFinishTakesItsOwnStart(t *testing.T) {
	root := codexDigestEnv(t, nil)
	now := time.Now().Truncate(time.Millisecond) // the marker keeps milliseconds
	call := func(id string) *agentx.HookInput {
		in := codexToolInput(t, `ox query "x"`, "")
		if id != "" {
			in.RawBytes = []byte(`{"tool_use_id":"` + id + `","unknown_field":1}`)
		}
		return in
	}
	require.Equal(t, "call_b", hookToolCallID(call("call_b")))
	require.Empty(t, hookToolCallID(call("")))

	recordFlowStart(root, call("call_a"), now)                    // abandoned: no finish ever comes
	recordFlowStart(root, call("call_b"), now.Add(5*time.Minute)) // the call that finishes

	took, ok := takeFlowStart(root, codexDigestAgentID, "query", "call_b", now.Add(5*time.Minute+2*time.Second))
	require.True(t, ok)
	assert.Equal(t, 2*time.Second, took, "its own start, not the abandoned one's five minutes")
	left := readFlowMarker(flowMarkerPath(root, codexDigestAgentID)).Running
	require.Len(t, left, 1)
	assert.Equal(t, "call_a", left[0].ID, "the abandoned start stays until it ages out")

	// A finish that names a call never takes another call's start...
	_, ok = takeFlowStart(root, codexDigestAgentID, "query", "call_c", now.Add(6*time.Minute))
	assert.False(t, ok)
	// ...and one that names none (a harness without ids) takes the oldest, as before.
	_, ok = takeFlowStart(root, codexDigestAgentID, "query", "", now.Add(6*time.Minute))
	assert.True(t, ok)
}

// --- C. Counting what came back ---

func TestFlowItems(t *testing.T) {
	tests := []struct {
		name, command, out string
		want               int
		noun               string
	}{
		{"results list", "query", `{"results":[1,2,3]}`, 3, "discussions"},
		{"one result is singular", "query", `{"results":[1]}`, 1, "discussion"},
		{"sessions list", "session list", `{"sessions":[1,2],"total":15}`, 2, "sessions"},
		{"murmurs list", "murmur list", `{"murmurs":[1,2,3,4]}`, 4, "murmurs"},
		{"code matches pluralize with es", "code search", `[1,2]`, 2, "matches"},
		{"team_context.results", "query", `{"team_context":{"results":[1,2]}}`, 2, "discussions"},
		{"a stated total when no list", "mcp Search", `{"total":7}`, 7, "discussions"},
		{"an empty list is no count", "query", `{"results":[]}`, 0, ""},
		{"plain text output", "query", `3 results found`, 0, ""},
		{"broken JSON", "query", `{"results":[1,2`, 0, ""},
		{"a command with no noun", "plan view", `{"results":[1,2]}`, 0, ""},
		{"results that is not a list", "query", `{"results":"none"}`, 0, ""},
		// An MCP reply wraps its JSON in content blocks: the results are
		// counted, never the blocks.
		{"mcp content array", "mcp ListMurmurs", `[{"type":"text","text":"{\"murmurs\":[1,2,3,4,5,6]}"}]`, 6, "murmurs"},
		{"mcp content array, empty result", "mcp ListMurmurs", `[{"type":"text","text":"{\"murmurs\":[]}"}]`, 0, ""},
		{"mcp content array, prose only", "mcp Search", `[{"type":"text","text":"no matches"}]`, 0, ""},
		{"mcp content object", "mcp Search", `{"content":[{"type":"text","text":"{\"results\":[1,2]}"}]}`, 2, "discussions"},
		{"mcp structuredContent", "mcp Search", `{"structuredContent":{"results":[1,2,3]},"content":[{"type":"text","text":"3 results"}]}`, 3, "discussions"},
		{"a result list of objects with text is still a list", "code search", `[{"text":"a","file":"x.go"},{"file":"y.go"}]`, 2, "matches"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, noun := flowItems(tt.command, tt.out)
			assert.Equal(t, tt.want, n)
			assert.Equal(t, tt.noun, noun)
		})
	}
}

// TestCodexFlowNotice_NewRecordingShowsItsFirstCall: the lanes already shown
// are a position in one trace, and the trace changes when a recording starts.
// Failure prevented: the first ox call of a new recording shown nowhere,
// because one call made before recording had already been counted as shown.
func TestCodexFlowNotice_NewRecordingShowsItsFirstCall(t *testing.T) {
	root := codexDigestEnv(t, nil)
	cache := t.TempDir()
	t.Setenv("HOME", cache)
	t.Setenv("XDG_CACHE_HOME", cache)

	before := runCodexCall(t, root, "ox plan view x", "# plan", 300*time.Millisecond)
	require.Len(t, before, 1, "not recording yet: the call's lane is shown under it")

	startWorktreeRecording(t, root, codexDigestAgentID, codexDigestSessionID)
	after := runCodexCall(t, root, `ox query "x"`, searchJSON, 300*time.Millisecond)
	require.Len(t, after, 1, "the new recording's first call must show its lane")
	assert.Contains(t, after[0], "searched team memory")
}

// --- D. The watch pane ---

// TestStatuslineWatch_DrawsLanesLive is the customer promise for a terminal
// split beside Codex: the pane shows an ox call while it runs, animates it
// when it lands, then leaves it still.
// Failure prevented: a pane that only ever shows counts, a lane that never
// stops moving (the pane redrawing 12 times a second forever), or old calls
// replaying their animation each time the pane opens.
func TestStatuslineWatch_DrawsLanesLive(t *testing.T) {
	root := codexDigestEnv(t, nil)
	cache := t.TempDir()
	t.Setenv("HOME", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	startWorktreeRecording(t, root, codexDigestAgentID, codexDigestSessionID)
	opts := statuslineOptions{width: 100}
	t0 := time.Now()

	// A call that finished before the pane opened is drawn settled.
	runCodexCall(t, root, "ox plan view x", "# plan", 300*time.Millisecond)
	var w flowWatch
	f := buildWatchFrame(&w, root, opts, 6, t0)
	assert.Equal(t, "● ox  1 plan  ● rec", f.status)
	require.Len(t, f.lanes, 1)
	assert.Regexp(t, regexp.MustCompile(`^SAGEOX ●─{16}● SESSION  pulled plan · ~2 · 0\.[34]s$`), f.lanes[0])
	assert.False(t, f.moving)

	// A call in progress shows as the open, dotted lane, its dots marching
	// from SageOx toward the session for as long as it runs.
	recordFlowStart(root, codexToolInput(t, `ox query "x"`, ""), t0)
	f = buildWatchFrame(&w, root, opts, 6, t0.Add(100*time.Millisecond))
	require.Len(t, f.lanes, 2)
	assert.Equal(t, "SAGEOX ●•┄┄┄┄┄•┄┄┄┄┄•┄┄┄○ SESSION  searching team discussions", f.lanes[1])
	assert.True(t, f.moving, "a running lane keeps the pane at its motion pace")
	f = buildWatchFrame(&w, root, opts, 6, t0.Add(250*time.Millisecond))
	assert.Equal(t, "SAGEOX ●┄┄•┄┄┄┄┄•┄┄┄┄┄•┄○ SESSION  searching team discussions", f.lanes[1])

	// It lands: the lane moves for a while, then settles for good.
	captureRealStdout(t, func() {
		require.NoError(t, handleAfterTool(codexDigestCtx(root, "codex", phaseAfterTool, codexToolInput(t, `ox query "x"`, searchJSON))))
	})
	seen := t0.Add(time.Second)
	f = buildWatchFrame(&w, root, opts, 6, seen)
	require.Len(t, f.lanes, 2)
	assert.True(t, f.moving)
	mid := buildWatchFrame(&w, root, opts, 6, seen.Add(700*time.Millisecond))
	assert.True(t, mid.moving)
	assert.Contains(t, mid.lanes[1], "•", "dots travel toward the session")
	assert.NotEqual(t, f.lanes[1], mid.lanes[1])
	done := buildWatchFrame(&w, root, opts, 6, seen.Add(3*time.Second))
	assert.False(t, done.moving)
	assert.Regexp(t, regexp.MustCompile(`^SAGEOX ●─{16}● SESSION  searched team memory · ~1\d\d from 3 discussions`), done.lanes[1])

	// Only as many lanes as the pane has rows; the newest stay.
	one := buildWatchFrame(&w, root, opts, 1, seen.Add(3*time.Second))
	require.Len(t, one.lanes, 1)
	assert.Contains(t, one.lanes[0], "searched team memory")

	// A pane too narrow for the track draws no lane rather than a wrapped one.
	narrow := statuslineOptions{width: 17} // one short of the 18-cell track
	thin := buildWatchFrame(&w, root, narrow, 6, seen.Add(3*time.Second))
	assert.Empty(t, thin.lanes)
	assert.False(t, thin.moving)

	// Lanes off: the pane is the status line alone.
	require.NoError(t, SetConfigValue("flow_lanes", "off", ConfigLevelUser, ""))
	assert.Empty(t, buildWatchFrame(&w, root, opts, 6, seen.Add(4*time.Second)).lanes)
}

// TestWatchStatusline_RedrawsManyLinesInPlace: a frame of several lines must
// overwrite the last one where it stands.
// Failure prevented: the pane scrolling a new copy of itself on every change.
func TestWatchStatusline_RedrawsManyLinesInPlace(t *testing.T) {
	frames := []watchFrame{
		{status: "● ox"},
		{status: "● ox", lanes: []string{"lane-a"}},
		{status: "● ox", lanes: []string{"lane-a"}},
		{status: "● ox  1 plan", lanes: []string{"lane-a", "lane-b"}},
		{status: "● ox  1 plan"},
	}
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time)
	done := make(chan struct{})
	i := 0
	go func() {
		watchStatusline(ctx, &out, func() watchFrame { f := frames[i]; i++; return f }, tick, true)
		close(done)
	}()
	for range len(frames) - 1 {
		tick <- time.Time{}
	}
	cancel()
	<-done
	assert.Equal(t,
		"\r\x1b[2K● ox"+
			"\r\x1b[2K● ox\nlane-a"+
			"\x1b[A\r\x1b[2K\x1b[J● ox  1 plan\nlane-a\nlane-b"+
			"\x1b[2A\r\x1b[2K\x1b[J● ox  1 plan"+
			"\n",
		out.String())
}

// TestWatchStatusline_PipedLogsEachLaneOnce: a pipe cannot redraw.
// Failure prevented: a log filling with a repeated frame, or with the frames
// of an animation.
func TestWatchStatusline_PipedLogsEachLaneOnce(t *testing.T) {
	frames := []watchFrame{
		{status: "● ox"},
		{status: "● ox", lanes: []string{"moving-1"}, log: []string{"lane-a"}, moving: true},
		{status: "● ox", lanes: []string{"moving-2"}, log: []string{"lane-a"}, moving: true},
		{status: "● ox  1 plan", lanes: []string{"x", "y"}, log: []string{"lane-a", "lane-b"}},
	}
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time)
	done := make(chan struct{})
	i := 0
	go func() {
		watchStatusline(ctx, &out, func() watchFrame { f := frames[i]; i++; return f }, tick, false)
		close(done)
	}()
	for range len(frames) - 1 {
		tick <- time.Time{}
	}
	cancel()
	<-done
	assert.Equal(t, "● ox\nlane-a\n● ox  1 plan\nlane-b\n", out.String())
}
