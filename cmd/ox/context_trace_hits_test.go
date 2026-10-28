package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/session/contextuse"
)

func hit(ref, refType, title, url string) providedHit {
	return providedHit{SurfacedRef: contextuse.SurfacedRef{Ref: ref, RefType: refType, Title: title, URL: url}}
}

func TestProvidedHitEvents(t *testing.T) {
	many := make([]providedHit, 0, 8)
	for _, r := range []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7"} {
		many = append(many, hit(r, "doc", r, ""))
	}
	tests := []struct {
		name     string
		existing []contexttrace.Event
		seq      int
		hits     []providedHit
		wantRefs []string
	}{
		{name: "one per hit", seq: 3, hits: []providedHit{hit("cnv_1", "discussion", "Auth", "https://x/c/cnv_1"), hit("pln_2", "plan", "Rollout", "")}, wantRefs: []string{"cnv_1", "pln_2"}},
		{name: "capped at five", hits: many, wantRefs: []string{"a1", "a2", "a3", "a4", "a5"}},
		{name: "refless hits dropped, cap not spent", hits: append([]providedHit{hit(" ", "doc", "t", "")}, many[:2]...), wantRefs: []string{"a1", "a2"}},
		{name: "duplicate within call", hits: []providedHit{hit("x", "doc", "", ""), hit("x", "doc", "", "")}, wantRefs: []string{"x"}},
		{
			name:     "already traced at same seq skipped",
			seq:      7,
			existing: []contexttrace.Event{{Type: contexttrace.EventProvided, Ref: "x", Seq: 7}},
			hits:     []providedHit{hit("x", "doc", "", ""), hit("y", "doc", "", "")},
			wantRefs: []string{"y"},
		},
		{
			name:     "same ref at an earlier seq is a new surfacing",
			seq:      9,
			existing: []contexttrace.Event{{Type: contexttrace.EventProvided, Ref: "x", Seq: 7}},
			hits:     []providedHit{hit("x", "doc", "", "")},
			wantRefs: []string{"x"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evs := providedHitEvents(tt.existing, contexttrace.SourceOnDemand, contexttrace.MechanismRetrieval, tt.seq, tt.hits)
			var refs []string
			for _, ev := range evs {
				refs = append(refs, ev.Ref)
				assert.Equal(t, contexttrace.EventProvided, ev.Type)
				assert.Equal(t, contexttrace.SourceOnDemand, ev.Source)
				assert.Equal(t, contexttrace.MechanismRetrieval, ev.Mechanism)
				assert.Equal(t, tt.seq, ev.Seq)
				assert.NotEmpty(t, ev.Timestamp)
			}
			assert.Equal(t, tt.wantRefs, refs)
		})
	}
}

func TestProvidedHitEvents_CarriesMetadataAndCapsTitle(t *testing.T) {
	h := hit("cnv_1", "discussion", strings.Repeat("t", 500), "https://sageox.ai/c/cnv_1")
	h.Score = 0.82
	h.FromName = "Dana"
	evs := providedHitEvents(nil, contexttrace.SourceOnDemand, contexttrace.MechanismRetrieval, 2, []providedHit{h})
	require.Len(t, evs, 1)
	ev := evs[0]
	assert.Equal(t, "discussion", ev.RefType)
	assert.Equal(t, "https://sageox.ai/c/cnv_1", ev.URL)
	assert.Equal(t, "Dana", ev.FromName)
	assert.InDelta(t, 0.82, ev.Score, 1e-9)
	assert.Equal(t, maxProvidedTitleRunes, len([]rune(ev.Title)))
}

// providedEvents returns the provided events in a session trace.
func providedEvents(t *testing.T, path string) []contexttrace.Event {
	t.Helper()
	events, err := contexttrace.ReadEvents(path)
	require.NoError(t, err)
	var out []contexttrace.Event
	for _, ev := range events {
		if ev.Type == contexttrace.EventProvided {
			out = append(out, ev)
		}
	}
	return out
}

// TestTraceProvidedHits_WritesToLiveRecording: the surfaced item lands in the
// recording's trace at the recording's turn, and becomes a surfaced ref.
func TestTraceProvidedHits_WritesToLiveRecording(t *testing.T) {
	root := usageTraceEnv(t, nil)
	state := startUsageRecording(t, root)

	h := hit("cnv_1", "discussion", "Auth rework", "https://sageox.ai/c/cnv_1")
	traceProvidedHits(root, usageAgentID, contexttrace.SourceOnDemand, contexttrace.MechanismRetrieval, []providedHit{h})
	// a second call at the same turn is idempotent
	traceProvidedHits(root, usageAgentID, contexttrace.SourceOnDemand, contexttrace.MechanismRetrieval, []providedHit{h})

	evs := providedEvents(t, sessionTracePath(state))
	require.Len(t, evs, 1)
	assert.Equal(t, state.EntryCount, evs[0].Seq)

	events, err := contexttrace.ReadEvents(sessionTracePath(state))
	require.NoError(t, err)
	surfaced := contextuse.Surfaced(events)
	require.Len(t, surfaced, 1)
	assert.Equal(t, contextuse.SurfacedRef{Ref: "cnv_1", RefType: "discussion", Via: contextuse.ViaRetrieval, Title: "Auth rework", URL: "https://sageox.ai/c/cnv_1", Seq: state.EntryCount}, surfaced[0])
}

func TestTraceProvidedHits_NoOps(t *testing.T) {
	root := usageTraceEnv(t, nil)
	h := []providedHit{hit("cnv_1", "discussion", "", "")}

	// not recording: nothing is written anywhere
	traceProvidedHits(root, usageAgentID, contexttrace.SourceOnDemand, contexttrace.MechanismRetrieval, h)
	assert.NoFileExists(t, agentTraceCacheDir(root)+"/"+usageAgentID+".jsonl")

	state := startUsageRecording(t, root)
	traceProvidedHits(root, "", contexttrace.SourceOnDemand, contexttrace.MechanismRetrieval, h)
	traceProvidedHits("", usageAgentID, contexttrace.SourceOnDemand, contexttrace.MechanismRetrieval, h)
	assert.Empty(t, providedEvents(t, sessionTracePath(state)))
}

func TestTraceAgentIDFromEnv(t *testing.T) {
	t.Setenv("SAGEOX_AGENT_ID", usageAgentID)
	assert.Equal(t, usageAgentID, traceAgentIDFromEnv())
	t.Setenv("SAGEOX_AGENT_ID", "../etc")
	assert.Empty(t, traceAgentIDFromEnv())
}

func refOf(ref, refType, title, url string) contextuse.SurfacedRef {
	return contextuse.SurfacedRef{Ref: ref, RefType: refType, Title: title, URL: url}
}
