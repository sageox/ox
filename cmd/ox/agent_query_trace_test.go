package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/ledgersearch"
	"github.com/sageox/ox/internal/session/contexttrace"
)

// Each query hit becomes a citable surfaced item: ref, type, title, link and
// score in the session trace — never the hit's text.
func TestQueryProvidedHits(t *testing.T) {
	const secretText = "THE-HIT-BODY-MUST-NOT-LEAK"
	tests := []struct {
		name     string
		combined *combinedQueryResponse
		want     []providedHit
	}{
		{name: "no results", combined: &combinedQueryResponse{}},
		{
			name: "team hits carry title and url, doc_type wins over source_type",
			combined: &combinedQueryResponse{TeamContext: &api.QueryResponse{Results: []api.QueryResult{
				{SourceID: "cnv_1", DocType: "discussion", SourceType: "recording", Title: "Auth rework", URL: "https://sageox.ai/c/cnv_1", Score: 0.9, Text: secretText},
				{SourceID: "deploy-skill", SourceType: "skill", Title: "deploy", URL: "https://sageox.ai/skills/deploy", Score: 0.5, Text: secretText},
			}}},
			want: []providedHit{
				{SurfacedRef: refOf("cnv_1", "discussion", "Auth rework", "https://sageox.ai/c/cnv_1"), Score: 0.9},
				{SurfacedRef: refOf("deploy-skill", "skill", "deploy", "https://sageox.ai/skills/deploy"), Score: 0.5},
			},
		},
		{
			name: "local hits after team hits",
			combined: &combinedQueryResponse{
				TeamContext:  &api.QueryResponse{Results: []api.QueryResult{{SourceID: "pln_1", DocType: "plan", Title: "Plan"}}},
				LocalResults: []ledgersearch.Result{{SourceID: "2026-10-01-dev-abc", DocType: "session", Score: 0.4, Text: secretText}},
			},
			want: []providedHit{
				{SurfacedRef: refOf("pln_1", "plan", "Plan", "")},
				{SurfacedRef: refOf("2026-10-01-dev-abc", "session", "", ""), Score: 0.4},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := queryProvidedHits(tt.combined)
			assert.Equal(t, tt.want, got)
			b, _ := json.Marshal(got)
			assert.NotContains(t, string(b), secretText)
		})
	}
}

// TestQuery_TracesHitsAndGuidesReporting drives the write path: the hits land
// in the live session trace, and the JSON tells the coworker how to cite one.
func TestQuery_TracesHitsAndGuidesReporting(t *testing.T) {
	root := usageTraceEnv(t, nil)
	state := startUsageRecording(t, root)
	combined := &combinedQueryResponse{TeamContext: &api.QueryResponse{Results: []api.QueryResult{
		{SourceID: "cnv_1", DocType: "discussion", Title: "Auth rework", URL: "https://sageox.ai/c/cnv_1", Score: 0.9, Text: "body text"},
	}}}

	out := captureRealStdout(t, func() {
		_, err := writeAndTraceQueryResponse(combined, &queryArgs{limit: 5}, root, usageAgentID)
		require.NoError(t, err)
	})

	var resp compactQueryResponse
	require.NoError(t, json.Unmarshal(out, &resp))
	assert.Contains(t, resp.Guidance, "ox agent "+usageAgentID+" context used <ref> --why")

	evs := providedEvents(t, sessionTracePath(state))
	require.Len(t, evs, 1)
	ev := evs[0]
	assert.Equal(t, contexttrace.SourceOnDemand, ev.Source)
	assert.Equal(t, contexttrace.MechanismRetrieval, ev.Mechanism)
	assert.Equal(t, "cnv_1", ev.Ref)
	assert.Equal(t, "discussion", ev.RefType)
	assert.Equal(t, "Auth rework", ev.Title)
	assert.Equal(t, "https://sageox.ai/c/cnv_1", ev.URL)
	raw, err := os.ReadFile(sessionTracePath(state))
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(raw), "body text"), "hit text must never reach the trace")
}

func TestQueryUseGuidance_NoHitsNoSentence(t *testing.T) {
	out := captureRealStdout(t, func() {
		_, err := writeQueryResponse(&combinedQueryResponse{}, &queryArgs{limit: 5}, usageAgentID)
		require.NoError(t, err)
	})
	assert.NotContains(t, string(out), "context used")
	assert.Equal(t, "If a hit changes what you do, record it: ox agent <id> context used <ref> --why \"<one line>\".", queryUseGuidance(""))
}
