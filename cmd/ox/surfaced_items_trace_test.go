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
			first:    providedHit{SurfacedRef: refOf("cnv_a", "discussion", "Standup", "u/cnv_a")},
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
		{"pln id wins", plan.PlanInfo{PlanID: "pln_1", Slug: "2026-10-01-cache", Topic: "Add cache"}, providedHit{SurfacedRef: refOf("pln_1", "plan", "Add cache", "https://sageox.ai/plan/pln_1")}},
		{"legacy plan uses slug, no link", plan.PlanInfo{Slug: "2026-10-01-cache", Topic: "Add cache"}, providedHit{SurfacedRef: refOf("2026-10-01-cache", "plan", "Add cache", "")}},
		{"slug titles an untitled plan", plan.PlanInfo{PlanID: "pln_2", Slug: "s"}, providedHit{SurfacedRef: refOf("pln_2", "plan", "s", "https://sageox.ai/plan/pln_2")}},
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
