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

// runOxUsageJSON runs the REAL `ox brief --json` through the root command as
// the calling AI coworker and decodes the report.
func runOxUsageJSON(t *testing.T, args ...string) usage.Report {
	t.Helper()
	resetUsageFlags(t)
	t.Setenv("SAGEOX_AGENT_ID", usageAgentID)
	out, err := executeRootCLI(t, append([]string{"brief", "--json"}, args...)...)
	require.NoError(t, err)
	t.Logf("ox brief --json:\n%s", out)
	var r usage.Report
	require.NoError(t, json.Unmarshal([]byte(out), &r), "stdout must be one JSON document: %s", out)
	return r
}

// --- A. The customer claim ---

// TestUsage_ListsOxCallAfterHookRecordsIt is the customer claim: after an AI
// coworker runs `ox murmur list`, `ox brief` lists it with what it returned.
// It drives the REAL PostToolUse hook and the REAL `ox brief --json` command.
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
	require.NotNil(t, found, "ox brief must list the murmur list call; got %+v", r.Calls.ByCommand)
	assert.Equal(t, "murmur list", found.Command)
	assert.Equal(t, 1, found.Count)
	assert.InDelta(t, n/4, found.Tokens, 1, "tokens ≈ bytes/4")
	assert.Contains(t, r.Display, "checked murmurs ×1")
	require.NotNil(t, r.Used.SelfReport)
	assert.Equal(t, "ox agent OxAb12 session context-trace", r.Used.SelfReport.BatchCommand)
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
	assert.Nil(t, r.Used.SelfReport)
}

// TestBrief_EndpointFromProject: a discussion the AI coworker pulled is linked
// on the project's own SageOx endpoint, through the real `ox brief --json`.
// Failure prevented: the brief shows a discussion with no link, or a link on
// the wrong environment's host.
func TestBrief_EndpointFromProject(t *testing.T) {
	root := usageTraceEnv(t, nil)
	t.Setenv("SAGEOX_ENDPOINT", "")
	require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox", "config.json"),
		[]byte(`{"config_version":"2","repo_id":"repo_usage_trace","endpoint":"https://test.sageox.ai"}`), 0o644))
	state := startUsageRecording(t, root)
	const cnv = "cnv_0192a3b4-5c6d-7e8f-9a0b-1c2d3e4f5a6b"
	w := contexttrace.NewWriter(state.SessionPath)
	require.NoError(t, w.Append(contexttrace.Event{
		Type: contexttrace.EventInvoked, Source: contexttrace.SourceOxCLI, Label: "searched team memory",
		Command: "query", Query: cnv, RefType: "discussion", InlineTokens: 40, Seq: 2,
	}))

	r := runOxUsageJSON(t)

	var found *usage.Surfaced
	for i := range r.Surfaced {
		if r.Surfaced[i].Ref == cnv {
			found = &r.Surfaced[i]
		}
	}
	require.NotNil(t, found, "the discussion must be surfaced; got %+v", r.Surfaced)
	assert.Equal(t, "https://test.sageox.ai/c/"+cnv, found.URL)
	assert.Contains(t, r.Display, "https://test.sageox.ai/c/"+cnv)
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
// used in ONE call, as ox brief's guidance tells it to. Failure prevented: a
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
// the command ox brief names, re-run ox brief, and see it reported.
func TestUsage_SelfReportRoundTrip(t *testing.T) {
	root := usageTraceEnv(t, nil)
	state := startUsageRecording(t, root)
	w := contexttrace.NewWriter(state.SessionPath)
	require.NoError(t, w.Append(contexttrace.Event{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamMemory, Doc: "MEMORY.md", InlineTokens: 50}))

	before := runOxUsageJSON(t)
	require.NotNil(t, before.Used.SelfReport)
	_, err := runContextTraceStdin(t, before.Used.SelfReport.BatchExampleLine)
	require.NoError(t, err)

	after := runOxUsageJSON(t)
	require.Len(t, after.Used.SelfReported, 1)
	assert.Equal(t, "MEMORY.md", after.Used.SelfReported[0].Ref)
	assert.True(t, after.Used.SelfReported[0].MatchedSurfaced)
}

// TestUsage_RecentFlagListsEveryCall drives `ox brief --json --recent N`
// through the root command over a recorded trace of 25 ox calls. Failure
// prevented: a coworker asks for the full call list and still gets the newest
// 10, or the flag changes the human display.
func TestUsage_RecentFlagListsEveryCall(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"default lists the newest 10", nil, 10},
		{"--recent 0 lists every call", []string{"--recent", "0"}, 25},
		{"--recent 5 lists the newest 5", []string{"--recent", "5"}, 5},
	}
	var display string
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := usageTraceEnv(t, nil)
			state := startUsageRecording(t, root)
			events := make([]contexttrace.Event, 0, 25)
			for i := range 25 {
				events = append(events, contexttrace.Event{Type: contexttrace.EventInvoked, Source: contexttrace.SourceOxCLI,
					Label: "checked murmurs", Command: "murmur list", InlineTokens: 10, Seq: i + 1})
			}
			require.NoError(t, contexttrace.NewWriter(state.SessionPath).AppendAll(events))

			r := runOxUsageJSON(t, tt.args...)

			assert.Equal(t, 25, r.Calls.Total)
			require.Len(t, r.Calls.Recent, tt.want)
			assert.Equal(t, 25, r.Calls.Recent[0].Seq, "newest first")
			if display == "" {
				display = r.Display
			}
			assert.Equal(t, display, r.Display, "--recent never changes the display")
		})
	}
}

// TestUsage_RecentFlagRejectsNegative: a negative --recent is a usage error.
func TestUsage_RecentFlagRejectsNegative(t *testing.T) {
	usageTraceEnv(t, nil)
	resetUsageFlags(t)
	t.Setenv("SAGEOX_AGENT_ID", usageAgentID)
	_, err := executeRootCLI(t, "brief", "--json", "--recent", "-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--recent")
}

// --- C. --team and --me through the real command ---

// TestUsage_TeamViewThroughRealCommand drives `ox brief --team --json` over a
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
	out, err := executeRootCLI(t, "brief", "--team", "--json")
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
	_, err := executeRootCLI(t, "brief", "--me", "--team")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "none of the others can be")
}

// TestUsage_DeprecatedAliasStillRuns drives the hidden `ox usage` alias of
// `ox brief` through the root command. Failure prevented: a coworker's script
// or muscle memory hits "unknown command" the release `ox usage` became
// `ox brief`, or keeps using the old name without being told the new one.
func TestUsage_DeprecatedAliasStillRuns(t *testing.T) {
	root := usageTraceEnv(t, nil)
	runAfterTool(t, root, "claude-code", usageAgentID, &agentx.HookInput{
		ToolName: "Bash", ToolInput: shellInput(t, "command", "ox murmur list"), ToolResponse: bashStdout(t, "[]"),
	})
	reset := func() {
		usageDeprecatedCmd.Flags().VisitAll(func(f *pflag.Flag) {
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		})
	}
	reset()
	t.Cleanup(reset)
	t.Setenv("SAGEOX_AGENT_ID", usageAgentID)

	out, err := executeRootCLI(t, "usage", "--json")
	require.NoError(t, err)

	// cobra prints the notice on the command's output writer, which the
	// real CLI leaves as stderr; the test harness routes it to out.
	notice, body, ok := strings.Cut(out, "\n")
	require.True(t, ok, out)
	assert.Contains(t, notice, `Command "usage" is deprecated, use 'ox brief' instead`)
	var r usage.Report
	require.NoError(t, json.Unmarshal([]byte(body), &r), "the alias must print the same JSON report: %s", body)
	assert.Equal(t, 1, r.Calls.Total)

	assert.True(t, usageDeprecatedCmd.Hidden, "only `ox brief` shows in help and docs")
	usageCmd.Flags().VisitAll(func(f *pflag.Flag) {
		assert.NotNil(t, usageDeprecatedCmd.Flags().Lookup(f.Name), "alias lacks --%s", f.Name)
	})
}
