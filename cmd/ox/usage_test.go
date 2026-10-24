package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/usage"
)

// resetUsageFlags returns usageCmd's flags to their defaults around a test,
// so one run's --json or --session never leaks into the next.
func resetUsageFlags(t *testing.T) {
	t.Helper()
	reset := func() {
		usageCmd.Flags().VisitAll(func(f *pflag.Flag) {
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		})
	}
	reset()
	t.Cleanup(reset)
}

// runOxUsageJSON runs the REAL `ox usage --json` through the root command as
// the calling AI coworker and decodes the report.
func runOxUsageJSON(t *testing.T, args ...string) usage.Report {
	t.Helper()
	resetUsageFlags(t)
	t.Setenv("SAGEOX_AGENT_ID", usageAgentID)
	out, err := executeRootCLI(t, append([]string{"usage", "--json"}, args...)...)
	require.NoError(t, err)
	t.Logf("ox usage --json:\n%s", out)
	var r usage.Report
	require.NoError(t, json.Unmarshal([]byte(out), &r), "stdout must be one JSON document: %s", out)
	return r
}

// --- A. The customer claim ---

// TestUsage_ListsOxCallAfterHookRecordsIt is the customer claim: after an AI
// coworker runs `ox murmur list`, `ox usage` lists it with what it returned.
// It drives the REAL PostToolUse hook and the REAL `ox usage --json` command.
// Failure prevented: a coworker asks "what did ox do?" and the report shows
// nothing for calls that ran, or the wrong volume.
func TestUsage_ListsOxCallAfterHookRecordsIt(t *testing.T) {
	root := usageTraceEnv(t, nil)
	startUsageRecording(t, root)
	const n = 4000
	runAfterTool(t, root, "claude-code", usageAgentID, &agentx.HookInput{
		ToolName:     "Bash",
		ToolInput:    shellInput(t, "command", "ox agent OxAb12 murmur list --json"),
		ToolResponse: bashStdout(t, strings.Repeat("m", n)),
	})

	r := runOxUsageJSON(t)

	assert.Equal(t, usage.ViewSession, r.View)
	assert.True(t, r.Trace.Available)
	assert.Equal(t, usage.TraceSourceSession, r.Trace.Source)
	assert.True(t, r.Session.Recording)
	var found *usage.CommandStat
	for i := range r.Calls.ByCommand {
		if r.Calls.ByCommand[i].Label == "checked murmurs" {
			found = &r.Calls.ByCommand[i]
		}
	}
	require.NotNil(t, found, "ox usage must list the murmur list call; got %+v", r.Calls.ByCommand)
	assert.Equal(t, "murmur list", found.Command)
	assert.Equal(t, 1, found.Count)
	assert.InDelta(t, n/4, found.Tokens, 1, "tokens ≈ bytes/4")
	assert.Contains(t, r.Display, "checked murmurs ×1")
	assert.Equal(t, usage.UsedNotYetReported, r.Used.Status)
	require.NotNil(t, r.Used.SelfReport)
	assert.Equal(t, "ox agent OxAb12 session context-trace", r.Used.SelfReport.Command)
}

// TestUsage_NotRecordingReadsAgentCache: an AI coworker that is not recording
// still sees its ox calls, from the per-agent cache. Failure prevented: the
// report says "no usage trace" for calls the hook did record.
func TestUsage_NotRecordingReadsAgentCache(t *testing.T) {
	root := usageTraceEnv(t, nil)
	runAfterTool(t, root, "claude-code", usageAgentID, &agentx.HookInput{
		ToolName: "Bash", ToolInput: shellInput(t, "command", "ox murmur list"), ToolResponse: bashStdout(t, "[]"),
	})

	r := runOxUsageJSON(t)

	assert.True(t, r.Trace.Available)
	assert.Equal(t, usage.TraceSourceAgentCache, r.Trace.Source)
	assert.False(t, r.Session.Recording)
	assert.Equal(t, 1, r.Calls.Total)
	assert.Equal(t, usage.UsedNotRecording, r.Used.Status)
	assert.Nil(t, r.Used.SelfReport)
}

// TestUsage_NoTraceSaysWhy: a coworker with nothing recorded gets an honest
// "no trace" with a reason, never an error or an empty success.
func TestUsage_NoTraceSaysWhy(t *testing.T) {
	usageTraceEnv(t, nil)

	r := runOxUsageJSON(t)

	assert.False(t, r.Trace.Available)
	assert.Equal(t, usage.TraceSourceNone, r.Trace.Source)
	assert.NotEmpty(t, r.Trace.Reason)
	assert.Contains(t, r.Display, "no usage trace for this session")
}

// --- B. Self-report: JSONL batches on stdin ---

func runContextTraceStdin(t *testing.T, stdin string) (string, error) {
	t.Helper()
	pipeStdin(t, stdin)
	var err error
	out := captureRealStdout(t, func() {
		err = runAgentSessionContextTrace(&agentinstance.Instance{AgentID: usageAgentID}, nil)
	})
	return string(out), err
}

func influencedLine(ref string) string {
	return `{"type":"influenced","source":"team-context","mechanism":"self-report","ref":"` + ref + `","ref_type":"doc","decision":"followed it"}`
}

func influencedEvents(t *testing.T, path string) []contexttrace.Event {
	t.Helper()
	events, err := contexttrace.ReadEvents(path)
	require.NoError(t, err)
	var out []contexttrace.Event
	for _, ev := range events {
		if ev.Type == contexttrace.EventInfluenced {
			out = append(out, ev)
		}
	}
	return out
}

// TestContextTrace_JSONLSelfReport: the AI coworker reports every item it
// used in ONE call, as ox usage's guidance tells it to. Failure prevented: a
// batch lands half-written, or the single-object form existing callers use
// stops working.
func TestContextTrace_JSONLSelfReport(t *testing.T) {
	tests := []struct {
		name      string
		stdin     string
		wantErr   string
		wantCount int
	}{
		{"three JSONL lines become three events",
			influencedLine("MEMORY.md") + "\n" + influencedLine("AGENTS.md") + "\n\n" + influencedLine("plan-x") + "\n", "", 3},
		{"single object still works", influencedLine("MEMORY.md"), "", 1},
		{"pretty-printed single object still works", "{\n  \"type\": \"influenced\",\n  \"source\": \"team-context\",\n  \"doc\": \"AGENTS.md\"\n}\n", "", 1},
		{"one malformed line rejects the whole batch",
			influencedLine("MEMORY.md") + "\n{not json\n" + influencedLine("AGENTS.md"), "line 2", 0},
		{"a line missing source rejects the whole batch",
			influencedLine("MEMORY.md") + "\n" + `{"type":"influenced","ref":"x"}`, "'source'", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := usageTraceEnv(t, nil)
			state := startUsageRecording(t, root)

			out, err := runContextTraceStdin(t, tt.stdin)

			got := influencedEvents(t, sessionTracePath(state))
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Empty(t, got, "no partial write")
				return
			}
			require.NoError(t, err)
			assert.Len(t, got, tt.wantCount)
			var resp sessionContextTraceOutput
			require.NoError(t, json.Unmarshal([]byte(out), &resp))
			assert.Equal(t, tt.wantCount, resp.Appended)
		})
	}
}

// TestUsage_SelfReportRoundTrip follows guidance step 1 end to end: report via
// the command ox usage names, re-run ox usage, and see it reported.
func TestUsage_SelfReportRoundTrip(t *testing.T) {
	root := usageTraceEnv(t, nil)
	state := startUsageRecording(t, root)
	w := contexttrace.NewWriter(state.SessionPath)
	require.NoError(t, w.Append(contexttrace.Event{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamMemory, Doc: "MEMORY.md", InlineTokens: 50}))

	before := runOxUsageJSON(t)
	require.NotNil(t, before.Used.SelfReport)
	_, err := runContextTraceStdin(t, before.Used.SelfReport.ExampleLine)
	require.NoError(t, err)

	after := runOxUsageJSON(t)
	assert.Equal(t, usage.UsedReported, after.Used.Status)
	require.Len(t, after.Used.SelfReported, 1)
	assert.Equal(t, "MEMORY.md", after.Used.SelfReported[0].Ref)
	assert.True(t, after.Used.SelfReported[0].MatchedSurfaced)
}

// --- C. --team and --me through the real command ---

// TestUsage_TeamViewThroughRealCommand drives `ox usage --team --json` over a
// real Ledger checkout. Failure prevented: the team view is wired to the wrong
// ledger, or a teammate's identity reaches the payload.
func TestUsage_TeamViewThroughRealCommand(t *testing.T) {
	root := usageTraceEnv(t, nil)
	ledgerPath := t.TempDir()
	require.NoError(t, config.SaveLocalConfig(root, &config.LocalConfig{Ledger: &config.LedgerConfig{Path: ledgerPath}}))
	name := time.Now().Add(-time.Hour).UTC().Format("2006-01-02T15-04") + "-casey-teammate-OxCc33"
	dir := filepath.Join(ledgerPath, "sessions", name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, &lfs.SessionMeta{
		Version: "1.0", SessionName: name, Username: "Casey Teammate", UserID: "user_casey33", AgentID: "OxCc33",
		AgentType: "codex", SessionID: "ses_" + name, CreatedAt: time.Now().Add(-time.Hour), Files: map[string]lfs.FileRef{},
	}))
	require.NoError(t, contexttrace.NewWriter(dir).AppendAll([]contexttrace.Event{
		{Type: contexttrace.EventInvoked, Source: contexttrace.SourceOxCLI, Label: "checked murmurs", Command: "murmur list", InlineTokens: 250},
	}))

	resetUsageFlags(t)
	t.Setenv("SAGEOX_AGENT_ID", usageAgentID)
	out, err := executeRootCLI(t, "usage", "--team", "--json")
	require.NoError(t, err)

	var r usage.TeamReport
	require.NoError(t, json.Unmarshal([]byte(out), &r), out)
	assert.Equal(t, usage.ViewTeam, r.View)
	assert.Equal(t, 1, r.Traced)
	assert.Equal(t, []usage.LabelStat{{Label: "checked murmurs", Count: 1, Tokens: 250}}, r.Calls)
	for _, leak := range []string{"Casey", "casey", "user_casey33", "OxCc33", name} {
		assert.NotContains(t, out, leak)
	}
}

// TestUsage_ViewFlagsAreExclusive: --me, --team and --session pick one view.
func TestUsage_ViewFlagsAreExclusive(t *testing.T) {
	usageTraceEnv(t, nil)
	resetUsageFlags(t)
	_, err := executeRootCLI(t, "usage", "--me", "--team")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "none of the others can be")
}
