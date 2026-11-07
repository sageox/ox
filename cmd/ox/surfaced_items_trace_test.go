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

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/conversation/read"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/plan"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/session/contextuse"
)

// Commands that put one specific team item in front of the AI coworker trace
// it as a surfaced item — ref, type, title, link — so it can be cited later.
// None of these carries content: no summary, transcript, or document text.

const leakMarker = "CONTENT-MUST-NOT-REACH-THE-TRACE"

func assertNoContent(t *testing.T, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	assert.NotContains(t, string(b), leakMarker)
}

func TestDiscussionURL(t *testing.T) {
	tests := []struct{ id, want string }{
		{"cnv_01a1", "https://sageox.ai/c/cnv_01a1"},
		{"rec_01a1", "https://sageox.ai/c/rec_01a1"},
		{"pln_01a1", ""},
		{"2026-10-01-standup", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			assert.Equal(t, tt.want, discussionURL("https://sageox.ai", tt.id))
		})
	}
}

func TestConversationProvidedHits(t *testing.T) {
	urlFor := func(id string) string { return "u/" + id }
	var many []read.SearchResult
	for i := range 7 {
		many = append(many, read.SearchResult{ConversationID: fmt.Sprintf("cnv_%d", i), Title: fmt.Sprintf("t%d", i), Hits: []read.SearchHit{{Text: leakMarker}}})
	}
	tests := []struct {
		name     string
		env      *read.Envelope
		wantRefs []string
		first    providedHit
	}{
		{
			name:     "show",
			env:      &read.Envelope{Data: &read.ShowData{ConversationID: "cnv_a", RecordingID: "rec_a", Title: "Standup", Summary: read.ShowSummary{Available: true, HumanSummary: leakMarker}}},
			wantRefs: []string{"cnv_a"},
			first:    providedHit{SurfacedRef: withAliases(refOf("cnv_a", "discussion", "Standup", "u/cnv_a"), "rec_a")},
		},
		{
			name:     "a recording id alone canonicalises to its cnv_ twin",
			env:      &read.Envelope{Data: &read.SearchData{Results: []read.SearchResult{{RecordingID: convTestFullRec, Title: "Full"}}}},
			wantRefs: []string{convTestFullCnv},
			first:    providedHit{SurfacedRef: withAliases(refOf(convTestFullCnv, "discussion", "Full", "u/"+convTestFullCnv), convTestFullRec)},
		},
		{
			name:     "show falls back to the recording id",
			env:      &read.Envelope{Data: &read.ShowData{RecordingID: "rec_b", Title: "Retro"}},
			wantRefs: []string{"rec_b"},
			first:    providedHit{SurfacedRef: refOf("rec_b", "discussion", "Retro", "u/rec_b")},
		},
		{
			name:     "search capped at five",
			env:      &read.Envelope{Data: &read.SearchData{Results: many}},
			wantRefs: []string{"cnv_0", "cnv_1", "cnv_2", "cnv_3", "cnv_4"},
			first:    providedHit{SurfacedRef: refOf("cnv_0", "discussion", "t0", "u/cnv_0")},
		},
		{name: "other payloads trace nothing", env: &read.Envelope{Data: &read.TranscriptData{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hits := conversationProvidedHits(tt.env, urlFor)
			var refs []string
			for _, h := range hits {
				refs = append(refs, h.Ref)
			}
			assert.Equal(t, tt.wantRefs, refs)
			if len(hits) > 0 {
				assert.Equal(t, tt.first, hits[0])
			}
			assertNoContent(t, hits)
		})
	}
}

// TestConversationShow_TracesShownConversation drives the real show command:
// the conversation lands in the live session trace with its /c/ link, and the
// human summary it printed does not.
func TestConversationShow_TracesShownConversation(t *testing.T) {
	useConversationTestReader(t) // isolates auth dirs; the recording env goes on top
	root := usageTraceEnv(t, nil)
	state := startUsageRecording(t, root)
	t.Setenv("SAGEOX_AGENT_ID", usageAgentID)

	stdout, _, err := runConversationInProc(t, "show", convTestFullCnv)
	require.NoError(t, err, stdout)
	var shown struct {
		Data read.ShowData `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &shown))
	require.True(t, shown.Data.Summary.Available, "fixture must print a summary for the no-content check to mean anything")

	evs := providedEvents(t, sessionTracePath(state))
	require.Len(t, evs, 1)
	ev := evs[0]
	assert.Equal(t, convTestFullCnv, ev.Ref)
	assert.Equal(t, []string{convTestFullRec}, ev.Aliases)
	assert.Equal(t, "discussion", ev.RefType)
	assert.Equal(t, shown.Data.Title, ev.Title)
	assert.True(t, strings.HasSuffix(ev.URL, "/c/"+convTestFullCnv), ev.URL)
	assert.Equal(t, contexttrace.MechanismRetrieval, ev.Mechanism)
	raw, err := os.ReadFile(sessionTracePath(state))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), shown.Data.Summary.HumanSummary)
}

func TestPlanProvidedHit(t *testing.T) {
	share := func(_, id string) string { return "https://sageox.ai/plan/" + id }
	tests := []struct {
		name string
		info plan.PlanInfo
		want providedHit
	}{
		{"pln id wins, slug is an alias", plan.PlanInfo{PlanID: "pln_1", Slug: "2026-10-01-cache", Topic: "Add cache"}, providedHit{SurfacedRef: withAliases(refOf("pln_1", "plan", "Add cache", "https://sageox.ai/plan/pln_1"), "2026-10-01-cache")}},
		{"legacy plan uses slug, no link, no self-alias", plan.PlanInfo{Slug: "2026-10-01-cache", Topic: "Add cache"}, providedHit{SurfacedRef: refOf("2026-10-01-cache", "plan", "Add cache", "")}},
		{"slug titles an untitled plan", plan.PlanInfo{PlanID: "pln_2", Slug: "s"}, providedHit{SurfacedRef: withAliases(refOf("pln_2", "plan", "s", "https://sageox.ai/plan/pln_2"), "s")}},
		{"dated directory is an alias too", plan.PlanInfo{PlanID: "pln_3", Slug: "cache", Dir: "/repo/plans/2026-10-01-cache", Topic: "Add cache"}, providedHit{SurfacedRef: withAliases(refOf("pln_3", "plan", "Add cache", "https://sageox.ai/plan/pln_3"), "cache", "2026-10-01-cache")}},
		{"directory equal to slug is not repeated", plan.PlanInfo{PlanID: "pln_4", Slug: "2026-10-01-cache", Dir: "/repo/plans/2026-10-01-cache"}, providedHit{SurfacedRef: withAliases(refOf("pln_4", "plan", "2026-10-01-cache", "https://sageox.ai/plan/pln_4"), "2026-10-01-cache")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, planProvidedHit("/repo", tt.info, share))
		})
	}
}

func TestSessionProvidedHit(t *testing.T) {
	cfg := &config.ProjectConfig{RepoID: "repo_1", Endpoint: "https://sageox.ai"}
	st := &session.StoredSession{Info: session.SessionInfo{SessionName: "2026-10-01-dev-abc", Filename: "raw.jsonl", Title: "info title", Summary: leakMarker}}
	sesID := "ses_019ff2f5-2079-7be1-b05e-8caad2772e61"
	tests := []struct {
		name string
		meta *lfs.SessionMeta
		want providedHit
	}{
		{
			name: "ses id and meta title, /c/ link",
			meta: &lfs.SessionMeta{SessionID: sesID, SessionName: "2026-10-01-dev-abc", Title: "Fix login", Summary: leakMarker},
			want: providedHit{SurfacedRef: refOf(sesID, "session", "Fix login", "https://sageox.ai/c/"+sesID)},
		},
		{
			name: "no meta: folder name and view link",
			want: providedHit{SurfacedRef: refOf("2026-10-01-dev-abc", "session", "info title", "https://sageox.ai/repo/repo_1/sessions/2026-10-01-dev-abc/view")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sessionProvidedHit(cfg, st, tt.meta)
			assert.Equal(t, tt.want, got)
			assertNoContent(t, got)
		})
	}
}

func TestTeamCtxProvidedHits(t *testing.T) {
	dir := t.TempDir()
	for name, meta := range map[string]string{
		"2026-09-09-standup": `{"recording_id":"rec_9","title":"Standup"}`,
		"2026-09-08-notitle": ``,
	} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, name), 0o755))
		if meta != "" {
			require.NoError(t, os.WriteFile(filepath.Join(dir, name, "metadata.json"), []byte(meta), 0o644))
		}
		require.NoError(t, os.WriteFile(filepath.Join(dir, name, "transcript.vtt"), []byte("WEBVTT\n\n00:00.000 --> 00:01.000\n<v Dana>"+leakMarker+"\n"), 0o644))
	}
	var out bytes.Buffer
	listed := writeRecentDiscussions(&out, dir)
	require.Len(t, listed, 2)

	urlFor := func(id string) string { return discussionURL("https://sageox.ai", id) }
	tc := &config.TeamContext{TeamID: "team_1", TeamName: "Platform", Slug: "platform"}
	tests := []struct {
		name         string
		hasDistilled bool
		want         []providedHit
	}{
		{
			name:         "distilled first, then discussions newest first",
			hasDistilled: true,
			want: []providedHit{
				{SurfacedRef: refOf("team-ctx/platform", "team-context", "Platform distilled team context", "")},
				{SurfacedRef: refOf("rec_9", "discussion", "Standup", "https://sageox.ai/c/rec_9")},
				{SurfacedRef: refOf("2026-09-08-notitle", "discussion", "2026-09-08-notitle", "")},
			},
		},
		{
			name: "no distilled context",
			want: []providedHit{
				{SurfacedRef: refOf("rec_9", "discussion", "Standup", "https://sageox.ai/c/rec_9")},
				{SurfacedRef: refOf("2026-09-08-notitle", "discussion", "2026-09-08-notitle", "")},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := teamCtxProvidedHits(tc, tt.hasDistilled, listed, urlFor)
			assert.Equal(t, tt.want, got)
			assertNoContent(t, got)
		})
	}
}

// TestConversationReads_TraceTheConversationRead drives the real transcript,
// topics, topic and walkthrough commands: each read puts the conversation in
// the live session trace once — canonical cnv_ ref, its rec_ twin as an alias,
// the conversation title, the /c/ link — and none of what it printed.
// Failure prevented: a session that read a transcript gets no credit in
// `ox session report` or the status line because the conversation was never surfaced.
func TestConversationReads_TraceTheConversationRead(t *testing.T) {
	const fullTitle = "2026-08-11-22-32-full" // fixture: index + metadata titles empty → folder name
	tests := []struct {
		name      string
		sub       string
		args      []string
		desktop   bool // walkthrough fixture corpus
		wantRef   string
		wantAlias string
		wantTitle string
	}{
		{"transcript by cnv id", "transcript", []string{convTestFullCnv, "--full"}, false, convTestFullCnv, convTestFullRec, fullTitle},
		{"transcript by rec id", "transcript", []string{convTestFullRec, "--full"}, false, convTestFullCnv, convTestFullRec, fullTitle},
		{"transcript by citation URI", "transcript", []string{convTestCueURI}, false, convTestFullCnv, convTestFullRec, fullTitle},
		{"topics", "topics", []string{convTestFullCnv}, false, convTestFullCnv, convTestFullRec, fullTitle},
		{"topic", "topic", []string{convTestFullCnv, convTestTopicID}, false, convTestFullCnv, convTestFullRec, fullTitle},
		{"walkthrough", "walkthrough", []string{convTestWalkthroughCnv}, true, convTestWalkthroughCnv, "rec_" + strings.TrimPrefix(convTestWalkthroughCnv, "cnv_"), "Saved page walkthrough"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.desktop {
				useDesktopWalkthroughReader(t)
			} else {
				useConversationTestReader(t)
			}
			root := usageTraceEnv(t, nil)
			state := startUsageRecording(t, root)
			t.Setenv("SAGEOX_AGENT_ID", usageAgentID)

			stdout, _, err := runConversationInProc(t, tt.sub, tt.args...)
			require.NoError(t, err, stdout)
			env := decodeConvEnvelope(t, stdout)
			require.True(t, env.Success, stdout)

			evs := providedEvents(t, sessionTracePath(state))
			require.Len(t, evs, 1)
			ev := evs[0]
			assert.Equal(t, tt.wantRef, ev.Ref)
			assert.Equal(t, []string{tt.wantAlias}, ev.Aliases)
			assert.Equal(t, "discussion", ev.RefType)
			assert.Equal(t, tt.wantTitle, ev.Title)
			assert.True(t, strings.HasSuffix(ev.URL, "/c/"+tt.wantRef), ev.URL)
			assert.Equal(t, contexttrace.MechanismRetrieval, ev.Mechanism)

			raw, err := os.ReadFile(sessionTracePath(state))
			require.NoError(t, err)
			content := printedContent(t, env.Data, tt.wantTitle)
			require.NotEmpty(t, content, "fixture must print content for the no-content check to mean anything")
			for _, s := range content {
				assert.NotContains(t, string(raw), s)
			}
		})
	}
}

// TestConversationReads_FailedReadTracesNothing: a read that errors surfaced
// nothing, so it must not credit the conversation.
func TestConversationReads_FailedReadTracesNothing(t *testing.T) {
	useConversationTestReader(t)
	root := usageTraceEnv(t, nil)
	state := startUsageRecording(t, root)
	t.Setenv("SAGEOX_AGENT_ID", usageAgentID)

	for _, sub := range []string{"transcript", "topics", "walkthrough"} {
		stdout, _, _ := runConversationInProc(t, sub, convTestUnknownCnv)
		require.False(t, decodeConvEnvelope(t, stdout).Success, stdout)
	}
	assert.Empty(t, providedEvents(t, sessionTracePath(state)))
}

// withAliases returns r carrying aliases.
func withAliases(r contextuse.SurfacedRef, aliases ...string) contextuse.SurfacedRef {
	r.Aliases = aliases
	return r
}

// printedContent collects the long human-readable strings a command printed
// (cue text, topic summaries, atom bodies) — everything but ids, links and
// the conversation title, which a provided event is allowed to carry.
func printedContent(t *testing.T, data json.RawMessage, title string) []string {
	t.Helper()
	var v any
	require.NoError(t, json.Unmarshal(data, &v))
	var out []string
	var walk func(any)
	walk = func(x any) {
		switch x := x.(type) {
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		case string:
			if len(x) < 24 || x == title || strings.Contains(x, "://") || strings.HasPrefix(x, "cnv_") || strings.HasPrefix(x, "rec_") {
				return
			}
			out = append(out, x)
		}
	}
	walk(v)
	return out
}
