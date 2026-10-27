package usage

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/session/contexttrace"
)

func startupDoc(i int) contexttrace.Event {
	return contexttrace.Event{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamDocs, Doc: fmt.Sprintf("doc-%02d.md", i)}
}

func murmurWhisper(i int) contexttrace.Event {
	return contexttrace.Event{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamWhisper,
		From: "OxR9a1", Topic: "wip", Ref: fmt.Sprintf("mur_%02d", i), RefType: "murmur"}
}

func fetched(ref, refType string, seq int) contexttrace.Event {
	return contexttrace.Event{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamContext,
		Mechanism: contexttrace.MechanismRetrieval, Ref: ref, RefType: refType, Seq: seq}
}

// TestBuild_SurfacedCapKeepsFetchedAndUsed: startup context (prime docs,
// whispers) arrives first; it must not crowd out of the brief what the AI
// coworker fetched or used. Failure prevented: a session that fetched 2
// discussions and 5 plans shows a brief listing only startup docs and
// murmurs, so the pane never mentions what was fetched.
func TestBuild_SurfacedCapKeepsFetchedAndUsed(t *testing.T) {
	var evs []contexttrace.Event
	for i := range 5 {
		evs = append(evs, startupDoc(i))
	}
	for i := range 11 {
		evs = append(evs, murmurWhisper(i))
	}
	evs = append(evs, fetched("cnv_a", "discussion", 20), fetched("cnv_b", "discussion", 21))
	for i := range 5 {
		evs = append(evs, fetched(fmt.Sprintf("pln_%d", i), "plan", 22+i))
	}
	evs = append(evs, used(contexttrace.EvidenceCited, "mur_10", "murmur", 30))

	r := Build(Input{TraceSource: TraceSourceSession, Events: evs})

	assert.Equal(t, []string{
		"prime:doc-00.md", "prime:doc-01.md", "prime:doc-02.md", "prime:doc-03.md", "prime:doc-04.md",
		"whisper:mur_00", "whisper:mur_01", "whisper:mur_10",
		"retrieval:cnv_a", "retrieval:cnv_b", "retrieval:pln_0", "retrieval:pln_1", "retrieval:pln_2", "retrieval:pln_3", "retrieval:pln_4",
	}, surfacedRefs(r), "used, then fetched, then startup fill the cap; arrival order kept")
	assert.Equal(t, map[string]int{"doc": 5, "murmur": 11, "discussion": 2, "plan": 5}, r.SurfacedByType)
	assert.Equal(t, 23, r.Relevant.SurfacedCount, "the rollup still counts everything surfaced")
}

// TestCapSurfaced covers the ordering rules on their own: under the cap
// nothing moves, and used items are never cut even past the cap.
func TestCapSurfaced(t *testing.T) {
	mark := &ItemUsed{Evidence: EvidenceRead}
	tests := []struct {
		name string
		in   []Surfaced
		n    int
		want []string
	}{
		{"under the cap: unchanged", []Surfaced{{Ref: "a", Via: "prime"}, {Ref: "b", Via: "retrieval"}}, 2, []string{"a", "b"}},
		{"fetched before startup", []Surfaced{{Ref: "a", Via: "prime"}, {Ref: "b", Via: "whisper"}, {Ref: "c", Via: "retrieval"}}, 2, []string{"a", "c"}},
		{"used before fetched", []Surfaced{{Ref: "a", Via: "retrieval"}, {Ref: "b", Via: "retrieval"}, {Ref: "c", Via: "prime", Used: mark}}, 2, []string{"a", "c"}},
		{"used never cut", []Surfaced{{Ref: "a", Used: mark}, {Ref: "b", Used: mark}, {Ref: "c", Used: mark}, {Ref: "d", Via: "retrieval"}}, 2, []string{"a", "b", "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, s := range capSurfaced(tt.in, tt.n) {
				got = append(got, s.Ref)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestBuild_SurfacedByTypeJSON pins the wire name and its absence without a trace.
func TestBuild_SurfacedByTypeJSON(t *testing.T) {
	b, err := json.Marshal(Build(Input{TraceSource: TraceSourceSession, Events: []contexttrace.Event{whisper(), memoryProvided()}}))
	require.NoError(t, err)
	assert.Contains(t, string(b), `"surfaced_by_type":{"doc":1,"murmur":1}`)

	b, err = json.Marshal(Build(Input{TraceSource: TraceSourceNone}))
	require.NoError(t, err)
	assert.NotContains(t, string(b), "surfaced_by_type")
}
