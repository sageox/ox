package usage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/session/contextuse"
)

func used(evidence, ref, refType string, seq int) contexttrace.Event {
	ev := contexttrace.Event{Type: contexttrace.EventUsed, Evidence: evidence, Ref: ref, RefType: refType, Seq: seq}
	switch evidence {
	case contexttrace.EvidenceReported, contexttrace.EvidenceNone:
		ev.Mechanism = contexttrace.MechanismSelfReport
	case contexttrace.EvidenceJudged:
		ev.Mechanism = contexttrace.MechanismJudge
	case contexttrace.EvidenceCited, contexttrace.EvidenceCitedCommit:
		ev.Mechanism = contexttrace.MechanismCitation
	default:
		ev.Mechanism = contexttrace.MechanismRetrieval
	}
	return ev
}

var allBuckets = []string{contextuse.BucketRead, contextuse.BucketCited, contextuse.BucketReported, contextuse.BucketJudged}

// TestBuild_Relevant covers the surfaced-vs-used rollup the report carries.
// Failure prevented: a use is shown without its evidence grade, a judged
// guess silently raises the derived category, or by_evidence drops a bucket
// so the display session's table has holes.
func TestBuild_Relevant(t *testing.T) {
	base := func(extra ...contexttrace.Event) Input {
		evs := []contexttrace.Event{primeEvent(), memoryProvided(), whisper()}
		return Input{TraceSource: TraceSourceSession, AgentID: "OxAb12", Recording: true, Events: append(evs, extra...)}
	}
	tests := []struct {
		name   string
		in     Input
		assert func(t *testing.T, r Report)
	}{
		{
			name: "nothing used",
			in:   base(),
			assert: func(t *testing.T, r Report) {
				assert.Zero(t, r.Relevant.Count)
				assert.Equal(t, 2, r.Relevant.SurfacedCount)
				assert.NotNil(t, r.Relevant.Items, "empty array, never null")
				assert.Equal(t, "none", r.Relevant.Derived.Category)
				for _, s := range r.Surfaced {
					assert.Nil(t, s.Used)
				}
				assert.False(t, r.Relevant.ReportedNone)
			},
		},
		{
			name: "a read of a surfaced doc is provable and marks the surfaced item",
			in:   base(used(contexttrace.EvidenceRead, "/ledger/MEMORY.md", "doc", 14)),
			assert: func(t *testing.T, r Report) {
				assert.Equal(t, 1, r.Relevant.Count)
				assert.Equal(t, 1, r.Relevant.ByEvidence["read"])
				require.Len(t, r.Relevant.Items, 1)
				assert.Equal(t, RelevantItem{Ref: "/ledger/MEMORY.md", RefType: "doc", Evidence: "read", Grade: "provable"}, r.Relevant.Items[0])
				assert.Equal(t, "moderate", r.Relevant.Derived.Category)
				require.NotNil(t, r.Surfaced[0].Used, "basename match joins the use to MEMORY.md")
				assert.Equal(t, ItemUsed{Evidence: "read", Seq: 14, Grade: "provable"}, *r.Surfaced[0].Used)
				assert.Nil(t, r.Surfaced[1].Used)
				assert.False(t, r.Relevant.ReportedNone, "a use is not a none report")
				assert.Empty(t, r.Used.SelfReported, "a provable read is not a self-report")
			},
		},
		{
			name: "a moment-of-use self-report is reported grade and still a self-report",
			in: base(func() contexttrace.Event {
				ev := used(contexttrace.EvidenceReported, "mur_x", "murmur", 20)
				ev.Decision = "picked the retry budget"
				return ev
			}()),
			assert: func(t *testing.T, r Report) {
				assert.Equal(t, 1, r.Relevant.ByEvidence["reported"])
				assert.Equal(t, "reported", r.Relevant.Items[0].Grade)
				assert.Equal(t, "moderate", r.Relevant.Derived.Category, "a why that matches a surfaced item")
				require.Len(t, r.Used.SelfReported, 1)
				assert.Equal(t, "self-assessed", r.Used.SelfReported[0].Grade)
				assert.True(t, r.Used.SelfReported[0].MatchedSurfaced)
				require.NotNil(t, r.Surfaced[1].Used)
				assert.Equal(t, "reported", r.Surfaced[1].Used.Evidence)
			},
		},
		{
			name: "mixed evidence dedups per ref at the strongest grade",
			in: base(
				used(contexttrace.EvidenceReported, "MEMORY.md", "doc", 5),
				used(contexttrace.EvidenceCitedCommit, "memory.md", "doc", 30),
				used(contexttrace.EvidenceRead, "mur_x", "murmur", 8),
				selfReport("unsurfaced.md"),
			),
			assert: func(t *testing.T, r Report) {
				assert.Equal(t, 3, r.Relevant.Count)
				assert.Equal(t, map[string]int{"read": 1, "cited": 1, "reported": 1, "judged": 0}, r.Relevant.ByEvidence)
				assert.Equal(t, "cited", r.Surfaced[0].Used.Evidence)
				assert.Equal(t, 30, r.Surfaced[0].Used.Seq)
				assert.Equal(t, "significant", r.Relevant.Derived.Category)
				assert.Contains(t, r.Relevant.Derived.Reason, "cited memory.md")
			},
		},
		{
			name: "judged evidence is listed but gated out of the derived category",
			in:   base(used(contexttrace.EvidenceJudged, "mur_x", "murmur", 9)),
			assert: func(t *testing.T, r Report) {
				assert.Equal(t, 1, r.Relevant.Count)
				assert.Equal(t, 1, r.Relevant.ByEvidence["judged"])
				assert.Equal(t, "judged", r.Relevant.Items[0].Grade)
				assert.Equal(t, "none", r.Relevant.Derived.Category)
				assert.Equal(t, "judged", r.Surfaced[1].Used.Evidence)
			},
		},
		{
			name: "judged evidence counts when allowed",
			in: func() Input {
				in := base(used(contexttrace.EvidenceJudged, "mur_x", "murmur", 9))
				in.AllowJudged = true
				return in
			}(),
			assert: func(t *testing.T, r Report) {
				assert.Equal(t, "minor", r.Relevant.Derived.Category)
			},
		},
		{
			name: "an explicit none report is reported_none",
			in:   base(used(contexttrace.EvidenceNone, "", "", 12)),
			assert: func(t *testing.T, r Report) {
				assert.Zero(t, r.Relevant.Count)
				assert.True(t, r.Relevant.ReportedNone)
				assert.Empty(t, r.NextActions, "nothing left to report")
			},
		},
		{
			name: "a use after a none report overrides it",
			in:   base(used(contexttrace.EvidenceNone, "", "", 12), used(contexttrace.EvidenceRead, "MEMORY.md", "doc", 15)),
			assert: func(t *testing.T, r Report) {
				assert.Equal(t, 1, r.Relevant.Count)
				assert.False(t, r.Relevant.ReportedNone, "a later use overrides the none report")
			},
		},
		{
			name: "titles and urls come from the surfaced item",
			in: Input{TraceSource: TraceSourceSession, Events: []contexttrace.Event{
				{Type: contexttrace.EventProvided, Source: contexttrace.SourceLocalRecall, Ref: "2026-10-01-ryan-Ox1", RefType: "session",
					Title: "Retry budget", URL: "https://sageox.ai/s/1", Seq: 2},
				used(contexttrace.EvidenceRequery, "2026-10-01-ryan-Ox1", "", 6),
			}},
			assert: func(t *testing.T, r Report) {
				require.Len(t, r.Surfaced, 1)
				assert.Equal(t, "recall", r.Surfaced[0].Via)
				assert.Equal(t, "Retry budget", r.Surfaced[0].Title)
				require.Len(t, r.Relevant.Items, 1)
				assert.Equal(t, RelevantItem{Ref: "2026-10-01-ryan-Ox1", RefType: "session", Title: "Retry budget",
					URL: "https://sageox.ai/s/1", Evidence: "read", Grade: "provable"}, r.Relevant.Items[0])
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Build(tt.in)
			require.NotNil(t, r.Relevant, "an available trace always carries relevant")
			for _, b := range allBuckets {
				_, ok := r.Relevant.ByEvidence[b]
				assert.True(t, ok, "by_evidence always carries %q", b)
			}
			tt.assert(t, r)
		})
	}
}

// TestBuild_RelevantNilWithoutTrace keeps "no trace" distinct from "nothing
// used": without a trace there is no relevant section at all.
func TestBuild_RelevantNilWithoutTrace(t *testing.T) {
	r := Build(Input{TraceSource: TraceSourceNone, TraceReason: "not recording"})
	assert.Nil(t, r.Relevant)
}

// TestBuild_RelevantMatchesBeyondDisplayCap keeps use evidence honest when
// more was surfaced than the payload shows.
func TestBuild_RelevantMatchesBeyondDisplayCap(t *testing.T) {
	var evs []contexttrace.Event
	for i := 0; i < maxSurfaced+2; i++ {
		evs = append(evs, contexttrace.Event{Type: contexttrace.EventProvided, Source: contexttrace.SourceTeamDocs, Doc: "doc-" + string(rune('a'+i)) + ".md"})
	}
	last := evs[len(evs)-1].Doc
	evs = append(evs, used(contexttrace.EvidenceRead, last, "doc", 3))
	r := Build(Input{TraceSource: TraceSourceSession, Events: evs})
	assert.Len(t, r.Surfaced, maxSurfaced)
	assert.Equal(t, maxSurfaced+2, r.Relevant.SurfacedCount)
	assert.Equal(t, 1, r.Relevant.Count)
	assert.Equal(t, "moderate", r.Relevant.Derived.Category)
}

// TestBuild_Attribution pins the display-only attribution verdict. Failure
// prevented: a manual score is overridden by derivation, or the threshold
// comparison drifts from the one the commit-msg hook applies.
func TestBuild_Attribution(t *testing.T) {
	read := used(contexttrace.EvidenceRead, "MEMORY.md", "doc", 4)
	tests := []struct {
		name string
		in   Input
		want Attribution
	}{
		{
			name: "manual score wins over derived",
			in:   Input{TraceSource: TraceSourceSession, ScoreCategory: "none", ScoreReason: "nothing applied", Events: []contexttrace.Event{memoryProvided(), read}},
			want: Attribution{WouldAttribute: false, Category: "none", Reason: "nothing applied", Source: AttributionSelfReported, Threshold: 0.5},
		},
		{
			name: "manual score above threshold attributes",
			in:   Input{TraceSource: TraceSourceSession, ScoreCategory: "significant", ScoreThreshold: 0.7},
			want: Attribution{WouldAttribute: true, Category: "significant", Source: AttributionSelfReported, Threshold: 0.7},
		},
		{
			name: "derived moderate meets the default threshold exactly",
			in:   Input{TraceSource: TraceSourceSession, Events: []contexttrace.Event{memoryProvided(), read}},
			want: Attribution{WouldAttribute: true, Category: "moderate", Reason: "derived from context use: read MEMORY.md (turn 4)", Source: AttributionDerived, Threshold: 0.5},
		},
		{
			name: "derived moderate misses a stricter threshold",
			in:   Input{TraceSource: TraceSourceSession, ScoreThreshold: 0.7, Events: []contexttrace.Event{memoryProvided(), read}},
			want: Attribution{WouldAttribute: false, Category: "moderate", Reason: "derived from context use: read MEMORY.md (turn 4)", Source: AttributionDerived, Threshold: 0.7},
		},
		{
			name: "nothing used derives none",
			in:   Input{TraceSource: TraceSourceSession},
			want: Attribution{WouldAttribute: false, Category: "none", Reason: "derived from context use: nothing used", Source: AttributionDerived, Threshold: 0.5},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Build(tt.in).Attribution)
		})
	}
}

// TestBuild_AttributionThresholdMatchesHook pins the comparison to the same
// category values the commit-msg hook uses, so "would attribute" never lies.
func TestBuild_AttributionThresholdMatchesHook(t *testing.T) {
	for _, cat := range session.ValidScoreCategories() {
		in := Input{TraceSource: TraceSourceSession, ScoreCategory: string(cat), ScoreThreshold: 0.5}
		assert.Equal(t, session.CategoryValue(cat) >= 0.5, Build(in).Attribution.WouldAttribute, string(cat))
	}
}

// TestBuild_SelfReportCommand names the moment-of-use command first and keeps
// the batch path, anchored to a real surfaced ref.
func TestBuild_SelfReportCommand(t *testing.T) {
	r := Build(Input{TraceSource: TraceSourceSession, AgentID: "OxAb12", Recording: true, Events: []contexttrace.Event{whisper()}})
	require.NotNil(t, r.Used.SelfReport)
	assert.Equal(t, `ox agent OxAb12 context used mur_x --why "ONE LINE: what you did because of it"`, r.Used.SelfReport.Command)
	assert.Equal(t, "ox agent OxAb12 session context-trace", r.Used.SelfReport.BatchCommand)
	assert.Contains(t, r.Used.SelfReport.BatchExampleLine, `"ref":"mur_x"`)
	assert.Equal(t, "OxR9a1", r.Surfaced[0].From, "whisper sender id survives the contextuse join")
	assert.Equal(t, "wip", r.Surfaced[0].Topic)
	assert.NotContains(t, Unmetered, "recall preamble", "recall now writes to the trace")
}
