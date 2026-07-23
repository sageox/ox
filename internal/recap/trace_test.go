package recap

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- readTrace ---
//
// Failure prevented: hydrating an LFS stub on the recap read path (recap is
// supposed to be offline/fast) or silently dropping evidence instead of
// counting it as dehydrated.

func TestReadTrace_CacheFirst(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	cacheEvents := []contexttrace.Event{
		{Type: contexttrace.EventProvided, Timestamp: "2026-06-01T00:00:00Z", Source: contexttrace.SourceTeamDocs, Doc: "principles.md"},
	}
	f.WriteTraceCache("s1", cacheEvents...)
	// in-place is a pointer — cache must still win without ever consulting it.
	f.WritePointerTrace("s1")

	events, dehydrated := readTrace(f.Ledger, "s1")
	require.False(t, dehydrated)
	require.Len(t, events, 1)
	assert.Equal(t, "principles.md", events[0].Doc)
}

func TestReadTrace_PointerInPlaceRejectedAsDehydrated(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	f.WritePointerTrace("s1") // no cache copy at all

	events, dehydrated := readTrace(f.Ledger, "s1")
	assert.True(t, dehydrated, "an LFS pointer with no cache copy must be reported dehydrated, never hydrated on the read path")
	assert.Nil(t, events)
}

func TestReadTrace_RealInPlaceContent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	f.WriteTrace("s1", contexttrace.Event{
		Type: contexttrace.EventProvided, Timestamp: "2026-06-01T00:00:00Z",
		Source: contexttrace.SourceTeamDocs, Doc: "glossary.md",
	})

	events, dehydrated := readTrace(f.Ledger, "s1")
	require.False(t, dehydrated)
	require.Len(t, events, 1)
	assert.Equal(t, "glossary.md", events[0].Doc)
}

func TestReadTrace_NoTraceArtifactAtAll(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.WriteSession("s1") // meta.json only, no context-trace.jsonl

	events, dehydrated := readTrace(f.Ledger, "s1")
	assert.False(t, dehydrated, "a session that simply has no trace file is not the same as a dehydrated one")
	assert.Nil(t, events)
}

// --- scanTraces ---

func TestScanTraces_AggregatesAcrossSessionsAndCapsSamples(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	// maxSampleWork is 3 — a 4th distinct session touching the same doc must
	// still increment Sessions but not grow Samples past the cap.
	sessions := []SessionFacts{}
	for i, name := range []string{"s1", "s2", "s3", "s4"} {
		ts := time.Date(2026, 6, 1, 0, i, 0, 0, time.UTC).Format(time.RFC3339)
		f.WriteTrace(name, contexttrace.Event{
			Type: contexttrace.EventProvided, Timestamp: ts,
			Source: contexttrace.SourceTeamDocs, Doc: "principles.md",
		})
		sessions = append(sessions, SessionFacts{Name: name, Title: "Title " + name})
	}

	scan := scanTraces(f.Ledger, sessions)
	require.Contains(t, scan.docs, "principles.md")
	dr := scan.docs["principles.md"]
	assert.Equal(t, 4, len(dr.SessionSet), "all 4 sessions should be counted toward reach")
	assert.Len(t, dr.Samples, maxSampleWork, "sample titles must be capped at maxSampleWork")
	assert.Equal(t, 4, scan.withTraces)
}

func TestScanTraces_DocEventShapeVsDocsEventShape(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	f.WriteTrace("singular", contexttrace.Event{
		Type: contexttrace.EventProvided, Timestamp: "2026-06-01T00:00:00Z",
		Source: contexttrace.SourceTeamDocs, Doc: "principles.md",
	})
	f.WriteTrace("plural", contexttrace.Event{
		Type: contexttrace.EventProvided, Timestamp: "2026-06-01T00:00:00Z",
		Source: contexttrace.SourceTeamDocs, Docs: []string{"glossary.md", "onboarding.md"},
	})

	sessions := []SessionFacts{{Name: "singular"}, {Name: "plural"}}
	scan := scanTraces(f.Ledger, sessions)

	assert.Contains(t, scan.docs, "principles.md")
	assert.Contains(t, scan.docs, "glossary.md")
	assert.Contains(t, scan.docs, "onboarding.md")
}

func TestScanTraces_WhisperEventsCarryNoDocAndAreExcluded(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	// Whisper delivery events are type=provided but carry From/Topic, not
	// Doc/Docs — they must never surface as an "artifact that reached you".
	f.WriteTrace("s1", contexttrace.Event{
		Type: contexttrace.EventProvided, Timestamp: "2026-06-01T00:00:00Z",
		Source: contexttrace.SourceTeamWhisper, From: "alice", Topic: "wip",
	})

	scan := scanTraces(f.Ledger, []SessionFacts{{Name: "s1"}})
	assert.Empty(t, scan.docs, "a whisper event with no Doc/Docs must not produce an artifact reach")
}

func TestScanTraces_InfluencedEventsIgnored(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	f.WriteTrace("s1", contexttrace.Event{
		Type: contexttrace.EventInfluenced, Timestamp: "2026-06-01T00:00:00Z",
		Source: contexttrace.SourceTeamDocs, Doc: "should-not-appear.md",
	})

	scan := scanTraces(f.Ledger, []SessionFacts{{Name: "s1"}})
	assert.Empty(t, scan.docs, "only `provided` events carry quotable doc content; `influenced` events must not surface as reaches")
}

func TestScanTraces_DehydratedSessionsCountedNotDropped(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	f.WritePointerTrace("dehydrated-1")
	f.WriteTrace("hydrated-1", contexttrace.Event{
		Type: contexttrace.EventProvided, Timestamp: "2026-06-01T00:00:00Z",
		Source: contexttrace.SourceTeamDocs, Doc: "principles.md",
	})

	sessions := []SessionFacts{{Name: "dehydrated-1"}, {Name: "hydrated-1"}}
	scan := scanTraces(f.Ledger, sessions)

	assert.Equal(t, 1, scan.tracesDehydrated)
	assert.Equal(t, 1, scan.withTraces)
}

func TestScanTraces_SortedByReachDescending(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	// "popular.md" reached 2 sessions, "rare.md" reached 1.
	f.WriteTrace("s1", contexttrace.Event{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamDocs, Doc: "rare.md"})
	f.WriteTrace("s2", contexttrace.Event{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamDocs, Doc: "popular.md"})
	f.WriteTrace("s3", contexttrace.Event{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamDocs, Doc: "popular.md"})

	sessions := []SessionFacts{{Name: "s1"}, {Name: "s2"}, {Name: "s3"}}
	scan := scanTraces(f.Ledger, sessions)

	sorted := scan.sorted()
	require.Len(t, sorted, 2)
	assert.Equal(t, "popular.md", sorted[0].Doc, "doc reaching more sessions must sort first")
	assert.Equal(t, "rare.md", sorted[1].Doc)
}

// --- docsOf ---

func TestDocsOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ev   contexttrace.Event
		want []string
	}{
		{"singular doc wins when both set", contexttrace.Event{Doc: "a.md", Docs: []string{"b.md"}}, []string{"a.md"}},
		{"plural docs used when singular empty", contexttrace.Event{Docs: []string{"b.md", "c.md"}}, []string{"b.md", "c.md"}},
		{"neither set returns empty", contexttrace.Event{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, docsOf(tt.ev))
		})
	}
}

// --- parseTS ---

func TestParseTS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		zero bool
	}{
		{"valid RFC3339", "2026-06-01T12:00:00Z", false},
		{"empty string", "", true},
		{"malformed", "not-a-timestamp", true},
		{"date only, not RFC3339", "2026-06-01", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseTS(tt.in)
			assert.Equal(t, tt.zero, got.IsZero(), "parseTS(%q).IsZero()", tt.in)
		})
	}
}

// --- SessionFacts.displayTitle ---

func TestSessionFacts_DisplayTitle(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "My Title", SessionFacts{Name: "folder-name", Title: "My Title"}.displayTitle())
	assert.Equal(t, "folder-name", SessionFacts{Name: "folder-name", Title: ""}.displayTitle(), "falls back to folder name when title is empty")
}
