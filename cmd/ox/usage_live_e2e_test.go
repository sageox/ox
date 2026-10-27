package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/oxactivity"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/consultscan"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/usage"
)

// Live use evidence: a recorded session that re-opened or named surfaced
// discussions shows them as used in `ox brief` while it is still recording,
// with no `context used` self-report and no session stop.

const (
	liveEndpoint     = "https://test.sageox.ai"
	liveHarnessSess  = "claude-session-brief-live"
	liveUUIDA        = "01a17990-4cb6-7bf9-9584-4728f14b54f1"
	liveUUIDB        = "0192a3b4-5c6d-7e8f-9a0b-1c2d3e4f5a6b"
	liveCnvA         = "cnv_" + liveUUIDA
	liveCnvB         = "cnv_" + liveUUIDB
	liveTitleA       = "Pricing page rollout review"
	liveTitleB       = "Field team onboarding retro"
	liveThirdPlanRef = "pln_7c41e0"
)

// liveBriefEnv is a project with an endpoint (so the recording lives in the
// ledger's local cache, as in production) and a live recording for
// usageAgentID bound to a Claude Code harness session.
func liveBriefEnv(t *testing.T) (string, *session.RecordingState) {
	t.Helper()
	root := usageTraceEnv(t, nil)
	t.Setenv("SAGEOX_ENDPOINT", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir()) // a fresh ledger per test
	require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox", "config.json"),
		[]byte(`{"config_version":"2","repo_id":"repo_brief_live","endpoint":"`+liveEndpoint+`"}`), 0o644))
	state, err := session.StartRecording(root, session.StartRecordingOptions{
		AgentID: usageAgentID, AdapterName: "claude-code", AgentSessionID: liveHarnessSess,
	})
	require.NoError(t, err)
	return root, state
}

// writeLiveRaw writes raw.jsonl as the recorder does: a header line, then one
// line per captured entry.
func writeLiveRaw(t *testing.T, state *session.RecordingState, entries ...map[string]any) string {
	t.Helper()
	lines := []string{`{"type":"header","metadata":{"version":"1.0","agent_id":"` + usageAgentID + `"}}`}
	for _, e := range entries {
		b, err := json.Marshal(e)
		require.NoError(t, err)
		lines = append(lines, string(b))
	}
	path := filepath.Join(state.SessionPath, "raw.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
	return path
}

func appendLiveRaw(t *testing.T, rawPath string, e map[string]any) {
	t.Helper()
	b, err := json.Marshal(e)
	require.NoError(t, err)
	f, err := os.OpenFile(rawPath, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.Write(append(b, '\n'))
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

func liveUser(text string) map[string]any { return map[string]any{"type": "user", "content": text} }
func liveAssistant(text string) map[string]any {
	return map[string]any{"type": "assistant", "content": text}
}
func liveBash(cmd, output string) map[string]any {
	return map[string]any{"type": "tool", "tool_name": "Bash", "tool_input": map[string]string{"command": cmd}, "tool_output": output}
}

// traceConversations writes what `ox conversation list|search` traces for the
// discussions it surfaced, through the command's own builders.
func traceConversations(t *testing.T, state *session.RecordingState, seq int, convs ...[2]string) {
	t.Helper()
	var hits []providedHit
	for _, c := range convs {
		hits = append(hits, conversationHit(c[0], "", c[1], func(id string) string { return discussionURL(liveEndpoint, id) }))
	}
	events := providedHitEvents(nil, contexttrace.SourceOnDemand, contexttrace.MechanismRetrieval, seq, hits)
	require.Len(t, events, len(convs))
	require.NoError(t, contexttrace.NewWriter(state.SessionPath).AppendAll(events))
}

func readTraceBytes(t *testing.T, state *session.RecordingState) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(state.SessionPath, contexttrace.FileName))
	require.NoError(t, err)
	return b
}

func relevantDetails(r usage.Report) map[string]string {
	out := map[string]string{}
	if r.Relevant == nil {
		return out
	}
	for _, it := range r.Relevant.Items {
		out[it.Ref] = it.Detail
	}
	return out
}

// TestBrief_LiveUses_AshbyShape is the customer claim of the live rule in
// tests/acceptance/features/brief/relevant-context.feature: Ashby's AI
// coworker searched discussions, re-opened the first by its sageox.ai
// recording link, and wrote its reply naming the second by title. While the
// session is still recording, with no self-report, `ox brief` lists both as
// used — one re-opened, one cited — each with its /c/ link. Failure
// prevented: the brief says "used 0" for discussions the reply was built
// from, until the session stops.
func TestBrief_LiveUses_AshbyShape(t *testing.T) {
	root, state := liveBriefEnv(t)
	traceConversations(t, state, 1, [2]string{liveCnvA, liveTitleA}, [2]string{liveCnvB, liveTitleB})
	writeLiveRaw(t, state,
		liveUser("what did we decide about the pricing rollout?"),
		liveBash(`ox conversation search "pricing rollout"`, "2 results"),
		liveBash("ox conversation transcript https://sageox.ai/c/rec_"+liveUUIDA, "…transcript…"),
		liveAssistant("We agreed to stage it behind a flag; the "+liveTitleB+" raised the same concern."),
	)
	traceBefore := readTraceBytes(t, state)

	r := runOxUsageJSON(t)

	require.NotNil(t, r.Relevant)
	assert.Equal(t, 2, r.Relevant.Count, "both discussions the reply was built from are used")
	details := relevantDetails(r)
	assert.Equal(t, "requery", details[liveCnvA], "re-opened by its recording link")
	assert.Equal(t, "cited", details[liveCnvB], "named by title in the reply")
	for _, it := range r.Relevant.Items {
		assert.Equal(t, liveEndpoint+"/c/"+it.Ref, it.URL, "used discussion %s carries its /c/ link", it.Ref)
	}
	assert.Equal(t, traceBefore, readTraceBytes(t, state), "ox brief writes nothing to the trace")

	st := oxactivity.LoadState(root, oxactivity.StateKey(usageAgentID, liveHarnessSess))
	assert.Equal(t, 2, st.Used, "the status line's used count agrees with the brief")
}

// TestBrief_LiveUses_FieldTeamShape: prime surfaced 4 docs, 11 murmurs and 5
// plans; `ox conversation list` surfaced two discussions; the AI coworker ran
// `ox conversation show` on each and named both in its reply. The brief counts
// exactly those two as used out of 22 surfaced. A plan id that appears only
// in thinking, a tool call, tool output and the user's own message is never
// counted: only the AI coworker's own reply text is a citation.
func TestBrief_LiveUses_FieldTeamShape(t *testing.T) {
	_, state := liveBriefEnv(t)
	w := contexttrace.NewWriter(state.SessionPath)
	var prime []contexttrace.Event
	for i := range 4 {
		prime = append(prime, contexttrace.Event{
			Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamDocs, Doc: fmt.Sprintf("docs/field-%d.md", i),
			Title: fmt.Sprintf("field-%d.md", i), ReadOnDemand: true, Seq: 1,
		})
	}
	for i := range 11 {
		id := fmt.Sprintf("OxM%03d", i)
		prime = append(prime, contexttrace.Event{
			Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamWhisper, WhisperID: id, Ref: id,
			Title: fmt.Sprintf("murmur %d", i), Seq: 1,
		})
	}
	for i := range 5 {
		ref := fmt.Sprintf("pln_7c41e%d", i)
		prime = append(prime, contexttrace.Event{
			Type: contexttrace.EventProvided, Source: contexttrace.SourceOnDemand, Mechanism: contexttrace.MechanismRetrieval,
			Ref: ref, RefType: "plan", Title: fmt.Sprintf("Plan number %d for the field", i), Seq: 1,
		})
	}
	require.NoError(t, w.AppendAll(prime))
	traceConversations(t, state, 2, [2]string{liveCnvA, liveTitleA}, [2]string{liveCnvB, liveTitleB})
	writeLiveRaw(t, state,
		liveUser("catch me up on the field team"),
		liveBash("ox agent prime", "…"),
		liveBash("ox conversation list", liveCnvA+"\n"+liveCnvB),
		liveBash("ox conversation show "+liveCnvA, "…"),
		liveBash("ox conversation show rec_"+liveUUIDB, "…"),
		map[string]any{"type": "thinking", "content": "maybe " + liveThirdPlanRef + " matters"},
		liveBash("echo "+liveThirdPlanRef, liveThirdPlanRef),
		liveUser("is "+liveThirdPlanRef+" related?"),
		liveAssistant("From "+liveTitleA+" ("+liveCnvA+") and "+liveTitleB+": onboarding moves to week one."),
	)

	r := runOxUsageJSON(t)

	require.NotNil(t, r.Relevant)
	assert.Equal(t, 2, r.Relevant.Count)
	assert.Equal(t, 22, r.Relevant.SurfacedCount)
	details := relevantDetails(r)
	assert.Contains(t, details, liveCnvA)
	assert.Contains(t, details, liveCnvB)
	assert.NotContains(t, details, liveThirdPlanRef, "a plan named only outside the reply text is not cited")
	used := map[string]bool{}
	for _, s := range r.Surfaced {
		if s.Used != nil {
			used[s.Ref] = true
		}
	}
	assert.True(t, used[liveCnvA], "surfaced discussion A is marked used")
	assert.True(t, used[liveCnvB], "surfaced discussion B is marked used")
	assert.Len(t, used, 2)
}

// TestBrief_LiveUses_OptOutShowsNone: with recap.trace_consultations off
// (ADR-027), the brief adds no live evidence.
func TestBrief_LiveUses_OptOutShowsNone(t *testing.T) {
	_, state := liveBriefEnv(t)
	traceConversations(t, state, 1, [2]string{liveCnvA, liveTitleA})
	writeLiveRaw(t, state, liveBash("ox conversation show "+liveCnvA, "…"))
	off := false
	require.NoError(t, config.SaveUserConfig(&config.UserConfig{Recap: &config.RecapConfig{TraceConsultations: &off}}))

	r := runOxUsageJSON(t)

	require.NotNil(t, r.Relevant)
	assert.Equal(t, 0, r.Relevant.Count)
}

// TestBrief_LiveUses_IncrementalCache: the 15 s refresh reads only what is
// new. A second brief resumes at the first one's offset; new entries are
// scanned and found; a truncated raw.jsonl or a corrupt cache rescans from
// zero; the trace is never written.
func TestBrief_LiveUses_IncrementalCache(t *testing.T) {
	root, state := liveBriefEnv(t)
	traceConversations(t, state, 1, [2]string{liveCnvA, liveTitleA}, [2]string{liveCnvB, liveTitleB})
	rawPath := writeLiveRaw(t, state,
		liveUser("hi"),
		liveBash("ox conversation show "+liveCnvA, "…"),
	)
	traceBefore := readTraceBytes(t, state)

	var offsets []int64
	orig := loadRawEntriesFrom
	loadRawEntriesFrom = func(p string, off int64, idx int) ([]consultscan.Entry, int64, int, error) {
		offsets = append(offsets, off)
		return orig(p, off, idx)
	}
	t.Cleanup(func() { loadRawEntriesFrom = orig })

	ctx, err := config.LoadProjectContext(root)
	require.NoError(t, err)
	cachePath := filepath.Join(ctx.DefaultLedgerPath(), ".sageox", "cache", "brief-uses", usageAgentID+".json")
	size := func() int64 {
		info, err := os.Stat(rawPath)
		require.NoError(t, err)
		return info.Size()
	}

	r := runOxUsageJSON(t)
	assert.Equal(t, 1, r.Relevant.Count)
	require.FileExists(t, cachePath, "the scan cache lives in the ledger's local cache")
	firstEnd := size()

	r = runOxUsageJSON(t)
	assert.Equal(t, 1, r.Relevant.Count, "cached uses are kept")
	appendLiveRaw(t, rawPath, liveAssistant("see "+liveTitleB))
	r = runOxUsageJSON(t)
	assert.Equal(t, 2, r.Relevant.Count, "a new entry is scanned")
	require.Equal(t, []int64{0, firstEnd, firstEnd}, offsets, "each brief resumes where the last stopped")

	// truncated (rewritten) raw.jsonl: rescan from zero
	offsets = nil
	writeLiveRaw(t, state, liveUser("hi"))
	r = runOxUsageJSON(t)
	assert.Equal(t, []int64{0}, offsets)
	assert.Equal(t, 0, r.Relevant.Count, "uses from the old file are gone")

	// rewritten to a longer file: the bytes before the cached offset
	// changed, so rescan from zero rather than resume mid-line
	offsets = nil
	writeLiveRaw(t, state, liveUser("hey"), liveBash("ox conversation show "+liveCnvA, "…"))
	r = runOxUsageJSON(t)
	assert.Equal(t, []int64{0}, offsets)
	assert.Equal(t, 1, r.Relevant.Count)

	// corrupt cache: rescan from zero
	offsets = nil
	require.NoError(t, os.WriteFile(cachePath, []byte("{not json"), 0o644))
	r = runOxUsageJSON(t)
	assert.Equal(t, []int64{0}, offsets)
	assert.Equal(t, 1, r.Relevant.Count)

	assert.Equal(t, traceBefore, readTraceBytes(t, state), "ox brief never writes the trace")
}
