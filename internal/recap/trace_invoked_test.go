package recap

import (
	"testing"

	"github.com/sageox/ox/internal/knowledgeflow"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// legacyTraceEvents is a trace in the shape every session written before the
// `invoked` event type carries: prime docs, a whisper injection, a retrieval
// consult, and a self-report.
func legacyTraceEvents() []contexttrace.Event {
	return []contexttrace.Event{
		{Type: contexttrace.EventProvided, Timestamp: "2026-06-01T00:00:00Z", Source: contexttrace.SourceTeamDocs, Doc: "principles.md", InlineTokens: 400},
		{Type: contexttrace.EventProvided, Timestamp: "2026-06-01T00:01:00Z", Source: contexttrace.SourceTeamWhisper, From: "OxDmitri", Topic: "wip", Mechanism: contexttrace.MechanismInjection, RefType: "murmur", Ref: "m1", WhisperID: "m1", Seq: 3},
		{Type: contexttrace.EventConsulted, Timestamp: "2026-06-01T00:02:00Z", Source: contexttrace.SourceOnDemand, Mechanism: contexttrace.MechanismRetrieval, RefType: "query", Ref: "auth", Query: "auth", Seq: 5},
		{Type: contexttrace.EventInfluenced, Timestamp: "2026-06-01T00:03:00Z", Source: contexttrace.SourceTeamDocs, Doc: "principles.md", Mechanism: contexttrace.MechanismSelfReport, Ref: "principles.md", RefType: "doc", Decision: "kept TTL"},
	}
}

// invokedTraceEvents are the new ox-call events, carrying every additive field
// (including a doc-like Ref and a Query) to prove no reader keys off them.
func invokedTraceEvents() []contexttrace.Event {
	return []contexttrace.Event{
		{Type: contexttrace.EventInvoked, Timestamp: "2026-06-01T00:04:00Z", Source: contexttrace.SourceOxCLI, Command: "agent prime", Label: "loaded team context",
			InlineTokens: 2000, OutputBytes: 8000, FullBytes: 14000, TokensBySource: map[string]int{"team": 1500, "sageox": 500}, Deferred: []string{"team-docs"}},
		{Type: contexttrace.EventInvoked, Timestamp: "2026-06-01T00:05:00Z", Source: contexttrace.SourceOxCLI, Command: "query", Label: "searched team memory",
			RefType: "query", Query: "auth", InlineTokens: 300, OutputBytes: 1200, Seq: 6},
		{Type: contexttrace.EventInvoked, Timestamp: "2026-06-01T00:06:00Z", Source: contexttrace.SourceOxCLI, Command: "murmur", Label: "posted murmur", Failed: true},
	}
}

// TestTraceReaders_IgnoreInvokedEvents mixes old trace lines with new `invoked`
// lines and asserts every recap-side reader produces exactly what it produced
// from the old lines alone. Failure prevented: an ox call (even a posted
// murmur) surfaces in recap as team knowledge that reached the coworker, or as
// a consultation/knowledge flow it never was.
func TestTraceReaders_IgnoreInvokedEvents(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.WriteTrace("old", legacyTraceEvents()...)
	f.WriteTrace("mixed", append(legacyTraceEvents(), invokedTraceEvents()...)...)

	oldScan := scanTraces(f.Ledger, []SessionFacts{{Name: "old", Title: "T"}})
	mixedScan := scanTraces(f.Ledger, []SessionFacts{{Name: "mixed", Title: "T"}})
	assert.Equal(t, oldScan.order, mixedScan.order, "scanTraces doc reach")
	assert.Equal(t, oldScan.withTraces, mixedScan.withTraces)
	for doc, dr := range oldScan.docs {
		require.Contains(t, mixedScan.docs, doc)
		assert.Equal(t, dr.SessionSet, mixedScan.docs[doc].SessionSet)
	}

	oldEvents, _ := ReadTrace(f.Ledger, "old")
	mixedEvents, _ := ReadTrace(f.Ledger, "mixed")
	require.Len(t, mixedEvents, len(oldEvents)+len(invokedTraceEvents()), "new lines decode, nothing dropped")
	assert.Equal(t, influenceEvents(oldEvents), influenceEvents(mixedEvents), "influenceEvents")
	assert.Equal(t, knowledgeflow.Build("T", oldEvents), knowledgeflow.Build("T", mixedEvents), "knowledgeflow.Build")
	assert.Equal(t,
		gatherFlows(f.Ledger, []SessionFacts{{Name: "old", Title: "T"}}),
		gatherFlows(f.Ledger, []SessionFacts{{Name: "mixed", Title: "T"}}),
		"gatherFlows")
}
