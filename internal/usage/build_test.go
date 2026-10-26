package usage

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/session/contexttrace"
)

func invoked(label, command string, tok, seq int) contexttrace.Event {
	return contexttrace.Event{Type: contexttrace.EventInvoked, Source: contexttrace.SourceOxCLI,
		Label: label, Command: command, InlineTokens: tok, OutputBytes: tok * 4, Seq: seq}
}

func primeEvent() contexttrace.Event {
	return contexttrace.Event{Type: contexttrace.EventInvoked, Source: contexttrace.SourceOxCLI,
		Label: "loaded team context", Command: "agent prime", OutputBytes: 8100, InlineTokens: 4000,
		TokensBySource: map[string]int{"team": 3100, "sageox": 900}}
}

func memoryProvided() contexttrace.Event {
	return contexttrace.Event{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamMemory, Doc: "MEMORY.md", InlineTokens: 2000}
}

func whisper() contexttrace.Event {
	return contexttrace.Event{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamWhisper,
		From: "OxR9a1", Topic: "wip", Ref: "mur_x", RefType: "murmur", InlineTokens: 220}
}

func selfReport(ref string) contexttrace.Event {
	return contexttrace.Event{Type: contexttrace.EventInfluenced, Source: contexttrace.SourceTeamContext,
		Mechanism: contexttrace.MechanismSelfReport, Ref: ref, RefType: "doc", Decision: "kept token TTL"}
}

func surfacedRefs(r Report) []string {
	var out []string
	for _, s := range r.Surfaced {
		out = append(out, s.Via+":"+s.Ref)
	}
	return out
}

// TestBuild covers the rules `ox brief` reports by. Failure prevented: the
// report double-counts prime, claims use the AI coworker never reported, or
// asks for a self-report it cannot write (not recording).
func TestBuild(t *testing.T) {
	tests := []struct {
		name   string
		in     Input
		assert func(t *testing.T, r Report)
	}{
		{
			name: "empty trace still reports cleanly",
			in:   Input{TraceSource: TraceSourceSession, AgentID: "OxAb12", Recording: true},
			assert: func(t *testing.T, r Report) {
				assert.True(t, r.Trace.Available)
				assert.Zero(t, r.Calls.Total)
				assert.NotNil(t, r.Calls.ByCommand, "empty arrays, never null")
				assert.NotNil(t, r.Surfaced)
				assert.Equal(t, Unmetered, r.Context.Unmetered)
				assert.True(t, r.Context.Estimated)
			},
		},
		{
			name: "no trace says so and points to session start",
			in:   Input{TraceSource: TraceSourceNone, TraceReason: "not recording"},
			assert: func(t *testing.T, r Report) {
				assert.False(t, r.Trace.Available)
				assert.Equal(t, "none", r.Trace.Source)
				require.NotEmpty(t, r.NextActions)
				assert.Equal(t, "ox session start", r.NextActions[0].Action)
				assert.Contains(t, r.Display, "no usage trace for this session: not recording")
			},
		},
		{
			name: "old trace without invoked events falls back to provided volume",
			in: Input{TraceSource: TraceSourceSession, Events: []contexttrace.Event{
				memoryProvided(), whisper(),
			}},
			assert: func(t *testing.T, r Report) {
				assert.Zero(t, r.Calls.Total)
				assert.Equal(t, []SourceStat{{Source: "team-memory", Tokens: 2000}, {Source: "whispers", Tokens: 220, Count: 1}}, r.Context.BySource)
				assert.Equal(t, 2220, r.Context.Tokens)
				assert.Equal(t, []string{"prime:MEMORY.md", "whisper:mur_x"}, surfacedRefs(r))
			},
		},
		{
			name: "prime budget replaces its provided events, never adds to them",
			in: Input{TraceSource: TraceSourceSession, Events: []contexttrace.Event{
				primeEvent(), memoryProvided(), invoked("checked murmurs", "murmur list", 300, 4),
				invoked("checked murmurs", "murmur list", 600, 9), whisper(),
			}},
			assert: func(t *testing.T, r Report) {
				assert.Equal(t, []SourceStat{
					{Source: "prime:team", Tokens: 3100},
					{Source: "ox calls", Tokens: 900, Count: 2},
					{Source: "prime:sageox", Tokens: 900},
					{Source: "whispers", Tokens: 220, Count: 1},
				}, r.Context.BySource)
				assert.Equal(t, 5120, r.Context.Tokens, "MEMORY.md's 2000 provided tokens are inside prime:team")
				assert.Equal(t, 3, r.Calls.Total, "prime is an ox call too")
				require.NotEmpty(t, r.Calls.ByCommand)
				assert.Equal(t, CommandStat{Label: "checked murmurs", Command: "murmur list", Count: 2, Tokens: 900, LastSeq: 9}, r.Calls.ByCommand[0])
				assert.Equal(t, 9, r.Calls.Recent[0].Seq, "recent is newest first")
			},
		},
		{
			name: "prime trimmed by a hook cap reports full and deferred",
			in: Input{TraceSource: TraceSourceSession, Events: []contexttrace.Event{func() contexttrace.Event {
				ev := primeEvent()
				ev.FullBytes, ev.Deferred = 14200, []string{"team-docs"}
				return ev
			}()}},
			assert: func(t *testing.T, r Report) {
				require.NotNil(t, r.Context.Prime)
				assert.Equal(t, PrimeBudget{EmittedBytes: 8100, FullBytes: 14200, Deferred: []string{"team-docs"}}, *r.Context.Prime)
				assert.Contains(t, r.Display, "prime trimmed 14.2k→8.1k B (deferred: team-docs)")
			},
		},
		{
			name: "failed call is counted",
			in: Input{TraceSource: TraceSourceSession, Events: []contexttrace.Event{func() contexttrace.Event {
				ev := invoked("searched team memory", "query", 0, 2)
				ev.Failed = true
				return ev
			}()}},
			assert: func(t *testing.T, r Report) {
				assert.Equal(t, 1, r.Calls.Failed)
				assert.Contains(t, r.Display, "1 failed")
			},
		},
		{
			name: "retrieval with a query is surfaced and counted as ran",
			in: Input{TraceSource: TraceSourceSession, Events: []contexttrace.Event{func() contexttrace.Event {
				ev := invoked("searched team memory", "query", 120, 3)
				ev.Query, ev.RefType = "auth refresh", "query"
				return ev
			}(), invoked("posted murmur", "murmur", 10, 4)}},
			assert: func(t *testing.T, r Report) {
				assert.Equal(t, []string{"retrieval:auth refresh"}, surfacedRefs(r))
				assert.Equal(t, []Retrieval{{Label: "searched team memory", Query: "auth refresh", Seq: 3, Grade: "retrieval"}}, r.Used.Retrieval,
					"a posted murmur is a write, never a retrieval")
			},
		},
		{
			name: "not recording: no self-report command",
			in:   Input{TraceSource: TraceSourceAgentCache, AgentID: "OxAb12", Events: []contexttrace.Event{invoked("checked murmurs", "murmur list", 10, 0)}},
			assert: func(t *testing.T, r Report) {
				assert.Nil(t, r.Used.SelfReport, "the context-trace command refuses without a recording")
				assert.Contains(t, r.Display, "not recording")
			},
		},
		{
			name: "recording, unreported: the self-report command names this AI coworker",
			in:   Input{TraceSource: TraceSourceSession, AgentID: "OxAb12", Recording: true, Events: []contexttrace.Event{memoryProvided()}},
			assert: func(t *testing.T, r Report) {
				require.NotNil(t, r.Used.SelfReport)
				assert.Equal(t, "ox agent OxAb12 session context-trace", r.Used.SelfReport.BatchCommand)
				assert.Contains(t, r.Used.SelfReport.Command, "ox agent OxAb12 context used ")
				var line contexttrace.Event
				require.NoError(t, json.Unmarshal([]byte(r.Used.SelfReport.BatchExampleLine), &line))
				assert.Equal(t, contexttrace.EventInfluenced, line.Type)
				assert.Equal(t, contexttrace.MechanismSelfReport, line.Mechanism)
				assert.Equal(t, "MEMORY.md", line.Ref, "example anchors to a real surfaced item")
				assert.NotEmpty(t, line.Source, "the context-trace command requires a source")
			},
		},
		{
			name: "self-report matched and unmatched",
			in: Input{TraceSource: TraceSourceSession, AgentID: "OxAb12", Recording: true, Events: []contexttrace.Event{
				memoryProvided(), selfReport("MEMORY.md"), selfReport("NOT-SURFACED.md"),
			}},
			assert: func(t *testing.T, r Report) {
				require.Len(t, r.Used.SelfReported, 2)
				assert.True(t, r.Used.SelfReported[0].MatchedSurfaced)
				assert.False(t, r.Used.SelfReported[1].MatchedSurfaced, "an unmatched ref is kept but flagged")
				assert.Equal(t, "self-assessed", r.Used.SelfReported[0].Grade)
			},
		},
		{
			name: "score set",
			in:   Input{TraceSource: TraceSourceSession, ScoreCategory: "moderate", ScoreReason: "r", ProducedCommits: 2},
			assert: func(t *testing.T, r Report) {
				require.NotNil(t, r.Used.SageoxScore)
				assert.Equal(t, Score{Category: "moderate", Reason: "r"}, *r.Used.SageoxScore)
				assert.Equal(t, 2, r.Used.ProducedCommits)
				assert.Contains(t, r.Display, "score      moderate · 2 commits")
			},
		},
		{
			name: "score unset",
			in:   Input{TraceSource: TraceSourceSession},
			assert: func(t *testing.T, r Report) {
				assert.Nil(t, r.Used.SageoxScore)
				assert.NotContains(t, r.Display, "score ")
			},
		},
		{
			name: "consulted events are carried with their mechanism",
			in: Input{TraceSource: TraceSourceSession, Events: []contexttrace.Event{{
				Type: contexttrace.EventConsulted, Source: contexttrace.SourceOnDemand,
				Mechanism: contexttrace.MechanismRetrieval, Ref: "docs/x.md", RefType: "doc", Seq: 5,
			}}},
			assert: func(t *testing.T, r Report) {
				assert.Equal(t, []Consulted{{Ref: "docs/x.md", RefType: "doc", Mechanism: "retrieval", Seq: 5}}, r.Used.Consulted)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Build(tt.in)
			assert.Equal(t, ViewSession, r.View)
			assert.Equal(t, guidanceText, r.Guidance)
			assert.NotContains(t, r.Display, "\x1b", "display is printed verbatim; it must carry no ANSI")
			tt.assert(t, r)
		})
	}
}

// TestBuild_CapsSurfacedAndRecent bounds the payload's token cost.
func TestBuild_CapsSurfacedAndRecent(t *testing.T) {
	var events []contexttrace.Event
	for i := range 30 {
		events = append(events,
			contexttrace.Event{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamDocs, Doc: string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".md"},
			invoked("checked murmurs", "murmur list", 1, i))
	}
	r := Build(Input{TraceSource: TraceSourceSession, Events: events})
	assert.Len(t, r.Surfaced, maxSurfaced)
	assert.Len(t, r.Calls.Recent, maxRecent)
	assert.Equal(t, 30, r.Calls.Total)
}

// TestBuild_RecentLimit: --recent widens calls.recent for a coworker who wants
// the full call list, without changing the default payload or the display.
// Failure prevented: `ox brief --recent 0` still truncates at 10, or the
// default report grows.
func TestBuild_RecentLimit(t *testing.T) {
	var events []contexttrace.Event
	for i := range 25 {
		events = append(events, invoked("checked murmurs", "murmur list", 1, i+1))
	}
	base := Build(Input{TraceSource: TraceSourceSession, Events: events})
	tests := []struct {
		name  string
		limit int
		want  int
	}{
		{"default keeps the last 10", 0, 10},
		{"all keeps every call", RecentAll, 25},
		{"any negative keeps every call", -7, 25},
		{"explicit 5", 5, 5},
		{"limit above total keeps every call", 100, 25},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Build(Input{TraceSource: TraceSourceSession, Events: events, RecentLimit: tt.limit})
			require.Len(t, r.Calls.Recent, tt.want)
			assert.Equal(t, 25, r.Calls.Recent[0].Seq, "newest first")
			assert.Equal(t, 25-tt.want+1, r.Calls.Recent[tt.want-1].Seq)
			assert.Equal(t, base.Display, r.Display, "display is for humans and ignores --recent")
		})
	}
}

const testCnv = "cnv_0192a3b4-5c6d-7e8f-9a0b-1c2d3e4f5a6b"

// TestBuild_URLFilledFromRefWithEndpoint: a surfaced discussion or plan with
// an opaque id gets its sageox.ai link, and nothing else is ever given a
// guessed one. Failure prevented: the brief prints a link that 404s (a slug,
// a doc path, a session id under discussion) or overwrites a link the trace
// already carried.
func TestBuild_URLFilledFromRefWithEndpoint(t *testing.T) {
	const ep = "https://sageox.ai"
	tests := []struct {
		name string
		in   Surfaced
		want string
	}{
		{"cnv discussion", Surfaced{Ref: testCnv, RefType: "discussion"}, ep + "/c/" + testCnv},
		{"rec discussion", Surfaced{Ref: "rec_abc", RefType: "discussion"}, ep + "/c/rec_abc"},
		{"pln plan", Surfaced{Ref: "pln_4d8e2f", RefType: "plan"}, ep + "/plan/pln_4d8e2f"},
		{"ses id under discussion is not guessed", Surfaced{Ref: "ses_abc", RefType: "discussion"}, ""},
		{"discussion slug is not guessed", Surfaced{Ref: "discussions/2026-10-23.md", RefType: "discussion"}, ""},
		{"plan slug is not guessed", Surfaced{Ref: "usage-receipts", RefType: "plan"}, ""},
		{"doc is never linked", Surfaced{Ref: "pln_4d8e2f", RefType: "doc"}, ""},
		{"existing url is kept", Surfaced{Ref: "pln_4d8e2f", RefType: "plan", URL: "https://test.sageox.ai/plan/pln_4d8e2f"}, "https://test.sageox.ai/plan/pln_4d8e2f"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := []Surfaced{tt.in}
			fillURLs(got, ep)
			assert.Equal(t, tt.want, got[0].URL)
		})
	}

	// end to end: Build fills the link from Input.Endpoint
	ev := invoked("searched team memory", "query", 10, 1)
	ev.Query, ev.RefType = testCnv, "discussion"
	r := Build(Input{TraceSource: TraceSourceSession, Endpoint: ep, Events: []contexttrace.Event{ev}})
	require.Len(t, r.Surfaced, 1)
	assert.Equal(t, ep+"/c/"+testCnv, r.Surfaced[0].URL)
	assert.Contains(t, r.Display, ep+"/c/"+testCnv)
}

// TestBuild_NoURLWithoutEndpoint: with no endpoint there is no trustworthy
// link, so none is invented. Failure prevented: a relative or default-host
// link for a self-hosted or unconfigured project.
func TestBuild_NoURLWithoutEndpoint(t *testing.T) {
	ev := invoked("searched team memory", "query", 10, 1)
	ev.Query, ev.RefType = testCnv, "discussion"
	r := Build(Input{TraceSource: TraceSourceSession, Events: []contexttrace.Event{ev}})
	require.Len(t, r.Surfaced, 1)
	assert.Empty(t, r.Surfaced[0].URL)
	assert.NotContains(t, r.Display, "https://")
}

// TestBuild_NextActions_NoReportNag: a recording with unreported items gets
// no "report which surfaced items" follow-up, and the self-report command is
// still available for the AI coworker to use quietly. Failure prevented: the
// brief nags the coworker about plumbing instead of showing value.
func TestBuild_NextActions_NoReportNag(t *testing.T) {
	r := Build(Input{TraceSource: TraceSourceSession, AgentID: "OxAb12", Recording: true, Events: []contexttrace.Event{memoryProvided()}})
	assert.Empty(t, r.NextActions)
	require.NotNil(t, r.Used.SelfReport)

	none := Build(Input{TraceSource: TraceSourceNone, TraceReason: "not recording"})
	require.Len(t, none.NextActions, 1)
	assert.Equal(t, "ox session start", none.NextActions[0].Action)

	b, err := json.Marshal(r)
	require.NoError(t, err)
	assert.NotContains(t, string(b), `"status"`, "used.status is gone from the contract")
	assert.NotContains(t, string(b), `"injected"`)
	assert.Contains(t, string(b), `"context":{"tokens":`)
	assert.Contains(t, string(b), `"relevant":{"count":0`, "an available trace always carries relevant")
	nb, err := json.Marshal(none)
	require.NoError(t, err)
	assert.NotContains(t, string(nb), `"relevant"`, "relevant is omitted when there is no trace")
}
