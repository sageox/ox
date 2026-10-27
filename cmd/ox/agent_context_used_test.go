package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/oxactivity"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/session/contextuse"
)

var testSurfaced = []contextuse.SurfacedRef{
	{Ref: "Ox7f2a", RefType: "murmur", Via: contextuse.ViaWhisper, FromName: "Dana", Seq: 3},
	{Ref: "Ox7f9c", RefType: "murmur", Via: contextuse.ViaWhisper, Seq: 4},
	{Ref: "MEMORY.md", RefType: "doc", Via: contextuse.ViaPrime},
	{Ref: "2026-10-01-dana-auth", RefType: "session", Via: contextuse.ViaRecall, Title: "Auth middleware rewrite", Seq: 2},
}

func TestResolveSurfacedRef(t *testing.T) {
	tests := []struct {
		name     string
		ref      string
		refType  string
		wantRef  string
		wantCode string
	}{
		{name: "exact", ref: "Ox7f2a", wantRef: "Ox7f2a"},
		{name: "case-insensitive", ref: "ox7F2A", wantRef: "Ox7f2a"},
		{name: "basename of a path", ref: "/ledger/team/memory.md", wantRef: "MEMORY.md"},
		{name: "title", ref: "auth middleware rewrite", wantRef: "2026-10-01-dana-auth"},
		{name: "unique prefix", ref: "2026-10", wantRef: "2026-10-01-dana-auth"},
		{name: "ambiguous prefix", ref: "Ox7f", wantCode: contextErrAmbiguousRef},
		{name: "unknown", ref: "OxNOPE", wantCode: contextErrUnknownRef},
		{name: "ref type narrows", ref: "MEMORY.md", refType: "murmur", wantCode: contextErrUnknownRef},
		{name: "ref type matches", ref: "memory.md", refType: "DOC", wantRef: "MEMORY.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, code, msg := resolveSurfacedRef(tt.ref, tt.refType, testSurfaced)
			assert.Equal(t, tt.wantCode, code)
			if tt.wantCode != "" {
				assert.NotEmpty(t, msg)
				return
			}
			assert.Equal(t, tt.wantRef, got.Ref)
		})
	}
}

func TestParseContextUsedArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    contextUsedArgs
		wantErr bool
	}{
		{name: "ref", args: []string{"Ox7f2a"}, want: contextUsedArgs{ref: "Ox7f2a"}},
		{name: "ref with reinjected flags", args: []string{"Ox7f2a", "--why", "kept retries", "--ref-type", "murmur"}, want: contextUsedArgs{ref: "Ox7f2a", why: "kept retries", refType: "murmur"}},
		{name: "equals form", args: []string{"--why=w", "Ox7f2a"}, want: contextUsedArgs{ref: "Ox7f2a", why: "w"}},
		{name: "none", args: []string{"--none"}, want: contextUsedArgs{none: true}},
		{name: "none and ref", args: []string{"Ox7f2a", "--none"}, wantErr: true},
		{name: "missing ref", args: nil, wantErr: true},
		{name: "why without value", args: []string{"Ox7f2a", "--why"}, wantErr: true},
		{name: "two refs", args: []string{"a", "b"}, wantErr: true},
		{name: "unknown flag", args: []string{"a", "--bogus"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseContextUsedArgs(tt.args)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCapContextWhy(t *testing.T) {
	assert.Equal(t, "one line", capContextWhy("  one\n line "))
	long := strings.Repeat("é", 250)
	assert.Equal(t, maxContextWhyRunes, len([]rune(capContextWhy(long))))
}

// TestAgentContextUsed_Recording drives `context used` against a live
// recording. Failure prevented: an AI coworker's report of which murmur
// changed its work never reaches the session trace, so "relevant" stays empty.
func TestAgentContextUsed_Recording(t *testing.T) {
	projectRoot := setupSessionTestProject(t)
	const agentID = "OxCu01"
	state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{AgentID: agentID, AdapterName: "claude-code"})
	require.NoError(t, err)
	tracePath := filepath.Join(state.SessionPath, contexttrace.FileName)
	w := contexttrace.NewWriter(state.SessionPath)
	require.NoError(t, w.Append(contexttrace.Event{
		Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamWhisper,
		Ref: "Ox7f2a", RefType: "murmur", WhisperID: "Ox7f2a", FromName: "Dana", Title: "wip", Seq: 3,
	}))

	tests := []struct {
		name         string
		args         []string
		wantErr      bool
		wantCode     string
		wantEvidence string
		wantRef      string
	}{
		{name: "known ref", args: []string{"used", "ox7f2a", "--why", "kept the retry budget Dana set"}, wantEvidence: contexttrace.EvidenceReported, wantRef: "Ox7f2a"},
		{name: "unknown ref", args: []string{"used", "OxNOPE"}, wantErr: true, wantCode: contextErrUnknownRef},
		{name: "none", args: []string{"used", "--none"}, wantEvidence: contexttrace.EvidenceNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before, _ := contexttrace.ReadEvents(tracePath)
			var buf bytes.Buffer
			err := runAgentContextAt(&buf, projectRoot, agentID, tt.args)
			after, _ := contexttrace.ReadEvents(tracePath)

			var out map[string]any
			require.NoError(t, json.Unmarshal(buf.Bytes(), &out), buf.String())
			assert.Contains(t, out["guidance"], "context")
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, cli.ErrSilent)
				assert.Equal(t, false, out["success"])
				assert.Equal(t, tt.wantCode, out["error"].(map[string]any)["code"])
				assert.Len(t, out["surfaced"], 1)
				assert.Len(t, after, len(before), "an unknown ref writes nothing")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, true, out["recording"])
			require.Len(t, after, len(before)+1)
			ev := after[len(after)-1]
			assert.Equal(t, contexttrace.EventUsed, ev.Type)
			assert.Equal(t, contexttrace.MechanismSelfReport, ev.Mechanism)
			assert.Equal(t, contexttrace.SourceOnDemand, ev.Source)
			assert.Equal(t, tt.wantEvidence, ev.Evidence)
			assert.Equal(t, tt.wantRef, ev.Ref)
			if tt.wantRef != "" {
				assert.Equal(t, "murmur", ev.RefType)
				assert.Equal(t, "wip", ev.Title)
				assert.Equal(t, 3, ev.SurfacedSeq)
				assert.Equal(t, "kept the retry budget Dana set", ev.Decision)
			}
		})
	}

	// list shows the murmur with its reported evidence
	var buf bytes.Buffer
	require.NoError(t, runAgentContextAt(&buf, projectRoot, agentID, []string{"list"}))
	var list contextListOutput
	require.NoError(t, json.Unmarshal(buf.Bytes(), &list))
	require.Len(t, list.Surfaced, 1)
	assert.Equal(t, "Ox7f2a", list.Surfaced[0].Ref)
	assert.Equal(t, "Dana", list.Surfaced[0].FromName)
	assert.Equal(t, contexttrace.EvidenceReported, list.Surfaced[0].Evidence)
	assert.Equal(t, contextuse.GradeReported, list.Surfaced[0].Grade)
	assert.Equal(t, 1, list.UsedCount)
	assert.True(t, list.ReportedNone)
	assert.Contains(t, list.Guidance, "ox agent OxCu01 context used <ref>")
}

// TestAgentContextUsed_NotRecordingFallsBackToCache: with no live recording,
// the report lands in the per-agent cache file usage events use, validated
// against what prime surfaced into the shared cache trace.
func TestAgentContextUsed_NotRecordingFallsBackToCache(t *testing.T) {
	projectRoot := setupSessionTestProject(t)
	const agentID = "OxCu02"
	cacheDir := agentTraceCacheDir(projectRoot)
	require.NoError(t, contexttrace.NewWriter(mkdirAll(t, cacheDir)).Append(contexttrace.Event{
		Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamMemory, Doc: "MEMORY.md",
	}))

	var buf bytes.Buffer
	require.NoError(t, runAgentContextAt(&buf, projectRoot, agentID, []string{"used", "memory.md", "--why", "followed the release rule"}))
	var out contextUsedOutput
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
	assert.False(t, out.Recording)
	assert.Equal(t, "MEMORY.md", out.Ref)
	assert.Equal(t, filepath.Join(cacheDir, agentID+".jsonl"), out.TracePath)

	events, err := contexttrace.ReadEvents(out.TracePath)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, contexttrace.EvidenceReported, events[0].Evidence)
	assert.Equal(t, 0, events[0].Seq)
}

func TestAgentContext_UsageErrors(t *testing.T) {
	projectRoot := setupSessionTestProject(t)
	for _, args := range [][]string{nil, {"bogus"}, {"used"}} {
		var buf bytes.Buffer
		err := runAgentContextAt(&buf, projectRoot, "OxCu03", args)
		assert.Error(t, err, "%v", args)
		assert.Empty(t, buf.String())
	}
}

// TestAgentContextUsed_Dispatch runs the real `ox agent <id> context used`
// path so --why/--none survive cobra's flag parse and reach the handler.
func TestAgentContextUsed_Dispatch(t *testing.T) {
	root := dispatchTestEnv(t)
	startDispatchRecording(t, root, dispatchAgentID)
	state, err := session.LoadRecordingStateForAgent(root, dispatchAgentID)
	require.NoError(t, err)
	require.NoError(t, contexttrace.NewWriter(state.SessionPath).Append(contexttrace.Event{
		Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamWhisper, Ref: "Ox7f2a", RefType: "murmur",
	}))

	out, err := executeRootCLI(t, "agent", dispatchAgentID, "context", "used", "Ox7f2a", "--why", "reused Dana's fix")
	require.NoError(t, err, out)
	assert.Contains(t, out, `"evidence": "reported"`)

	out, err = executeRootCLI(t, "agent", dispatchAgentID, "context", "used", "--none")
	require.NoError(t, err, out)

	events, err := contexttrace.ReadEvents(filepath.Join(state.SessionPath, contexttrace.FileName))
	require.NoError(t, err)
	require.Len(t, events, 3)
	assert.Equal(t, "reused Dana's fix", events[1].Decision)
	assert.Equal(t, contexttrace.EvidenceNone, events[2].Evidence)
}

// TestAgentContextUsed_CountsOnStatusline: a reported use shows on the status
// line of the harness session that is recording, once per item. Failure
// prevented: the bar says how much context was fetched but never how much
// mattered, a re-report reads "2 used", or a --none / unrecorded run invents
// a use.
func TestAgentContextUsed_CountsOnStatusline(t *testing.T) {
	const harnessSession = "c1a2b3c4-0000-4000-8000-000000000001"
	newProject := func(t *testing.T, agentID string) (string, string) {
		t.Helper()
		projectRoot := setupSessionTestProject(t)
		state, err := session.StartRecording(projectRoot, session.StartRecordingOptions{
			AgentID: agentID, AgentSessionID: harnessSession, AdapterName: "claude-code",
		})
		require.NoError(t, err)
		require.NoError(t, contexttrace.NewWriter(state.SessionPath).AppendAll([]contexttrace.Event{
			{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamWhisper, Ref: "Ox7f2a", RefType: "murmur", Seq: 1},
			{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamMemory, Doc: "MEMORY.md"},
		}))
		return projectRoot, oxactivity.StateKey(agentID, harnessSession)
	}

	tests := []struct {
		name string
		runs [][]string
		want oxactivity.Counts
	}{
		{"one reported use", [][]string{{"used", "Ox7f2a", "--why", "kept Dana's retry budget"}}, oxactivity.Counts{Murmurs: 1, Used: 1}},
		{"re-reported use counts once", [][]string{{"used", "Ox7f2a"}, {"used", "ox7f2a"}}, oxactivity.Counts{Murmurs: 1, Used: 1}},
		{"two items", [][]string{{"used", "Ox7f2a"}, {"used", "memory.md"}}, oxactivity.Counts{Murmurs: 1, Docs: 1, Used: 2}},
		{"--none uses nothing", [][]string{{"used", "--none"}}, oxactivity.Counts{}},
		{"unknown ref uses nothing", [][]string{{"used", "OxNOPE"}}, oxactivity.Counts{}},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectRoot, key := newProject(t, fmt.Sprintf("OxSu%02d", i))
			for _, args := range tt.runs {
				var buf bytes.Buffer
				_ = runAgentContextAt(&buf, projectRoot, fmt.Sprintf("OxSu%02d", i), args)
			}
			got := oxactivity.LoadState(projectRoot, key)
			assert.Equal(t, tt.want, got.Counts())
			assert.Equal(t, tt.want.Used, got.Used)
		})
	}

	t.Run("not recording writes no status-line state", func(t *testing.T) {
		projectRoot := setupSessionTestProject(t)
		cacheDir := agentTraceCacheDir(projectRoot)
		require.NoError(t, contexttrace.NewWriter(mkdirAll(t, cacheDir)).Append(contexttrace.Event{
			Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamMemory, Doc: "MEMORY.md",
		}))
		var buf bytes.Buffer
		require.NoError(t, runAgentContextAt(&buf, projectRoot, "OxSuNR", []string{"used", "memory.md"}))
		assert.NoDirExists(t, filepath.Join(projectRoot, ".sageox", "cache", "statusline"))
	})
}

func mkdirAll(t *testing.T, dir string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return dir
}
