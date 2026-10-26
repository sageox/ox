package contextuse

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ct "github.com/sageox/ox/internal/session/contexttrace"
)

func used(ref, refType, evidence string, mech ct.Mechanism, seq int) ct.Event {
	return ct.Event{Type: ct.EventUsed, Ref: ref, RefType: refType, Evidence: evidence, Mechanism: mech, Seq: seq}
}

var (
	whisperOx = ct.Event{Type: ct.EventProvided, Source: ct.SourceTeamWhisper, Ref: "Ox7f2a", Seq: 3}
	primeMem  = ct.Event{Type: ct.EventProvided, Source: ct.SourceTeamMemory, Doc: "MEMORY.md"}
	primeSoul = ct.Event{Type: ct.EventProvided, Source: ct.SourceTeamDocs, Doc: "SOUL.md"}
	recallSes = ct.Event{Type: ct.EventProvided, Source: ct.SourceLocalRecall, Ref: "2026-10-01-dana-auth", RefType: "session"}
)

func TestBuild_DerivedCategory(t *testing.T) {
	tests := []struct {
		name        string
		events      []ct.Event
		commits     int
		allowJudged bool
		wantCat     string
	}{
		{name: "empty -> none", wantCat: CategoryNone},
		{name: "surfaced but unused -> none", events: []ct.Event{whisperOx, primeMem}, wantCat: CategoryNone},
		{
			name:    "one read -> moderate",
			events:  []ct.Event{primeMem, used("MEMORY.md", "doc", ct.EvidenceRead, ct.MechanismRetrieval, 14)},
			wantCat: CategoryModerate,
		},
		{
			name: "reported with why and matched -> moderate",
			events: []ct.Event{whisperOx, {
				Type: ct.EventUsed, Ref: "Ox7f2a", Evidence: ct.EvidenceReported, Mechanism: ct.MechanismSelfReport, Decision: "used retry budget",
			}},
			wantCat: CategoryModerate,
		},
		{
			name:    "reported without why -> minor",
			events:  []ct.Event{whisperOx, used("Ox7f2a", "", ct.EvidenceReported, ct.MechanismSelfReport, 4)},
			wantCat: CategoryMinor,
		},
		{
			name: "reported with why but unmatched -> minor",
			events: []ct.Event{whisperOx, {
				Type: ct.EventUsed, Ref: "OxNOPE1", Evidence: ct.EvidenceReported, Mechanism: ct.MechanismSelfReport, Decision: "why",
			}},
			wantCat: CategoryMinor,
		},
		{
			name: "two distinct reads -> significant",
			events: []ct.Event{
				primeMem, primeSoul,
				used("MEMORY.md", "doc", ct.EvidenceRead, ct.MechanismRetrieval, 5),
				used("SOUL.md", "doc", ct.EvidenceRead, ct.MechanismRetrieval, 6),
			},
			wantCat: CategorySignificant,
		},
		{
			name:    "session-ref read + commit -> significant",
			events:  []ct.Event{recallSes, used("2026-10-01-dana-auth", "session", ct.EvidenceRead, ct.MechanismRetrieval, 7)},
			commits: 1, wantCat: CategorySignificant,
		},
		{
			name:    "session-ref read without commit -> moderate",
			events:  []ct.Event{recallSes, used("2026-10-01-dana-auth", "session", ct.EvidenceRead, ct.MechanismRetrieval, 7)},
			wantCat: CategoryModerate,
		},
		{
			name:    "doc read + commit -> moderate",
			events:  []ct.Event{primeMem, used("MEMORY.md", "doc", ct.EvidenceRead, ct.MechanismRetrieval, 7)},
			commits: 2, wantCat: CategoryModerate,
		},
		{
			name: "cited_commit -> significant",
			events: []ct.Event{whisperOx, {
				Type: ct.EventUsed, Ref: "Ox7f2a", Evidence: ct.EvidenceCitedCommit, Mechanism: ct.MechanismCitation, Commit: "3e1c9d0aa",
			}},
			wantCat: CategorySignificant,
		},
		{
			name:        "judged only, allowed -> minor",
			events:      []ct.Event{whisperOx, used("Ox7f2a", "murmur", ct.EvidenceJudged, ct.MechanismJudge, 0)},
			allowJudged: true, wantCat: CategoryMinor,
		},
		{
			name:    "judged only, not allowed -> none",
			events:  []ct.Event{whisperOx, used("Ox7f2a", "murmur", ct.EvidenceJudged, ct.MechanismJudge, 0)},
			wantCat: CategoryNone,
		},
		{
			name:    "--none then read -> moderate",
			events:  []ct.Event{primeMem, {Type: ct.EventUsed, Evidence: ct.EvidenceNone, Mechanism: ct.MechanismSelfReport}, used("MEMORY.md", "doc", ct.EvidenceRead, ct.MechanismRetrieval, 9)},
			wantCat: CategoryModerate,
		},
		{
			name:    "--none only -> none",
			events:  []ct.Event{primeMem, {Type: ct.EventUsed, Evidence: ct.EvidenceNone, Mechanism: ct.MechanismSelfReport}},
			wantCat: CategoryNone,
		},
		{
			name: "same ref read + reported -> moderate",
			events: []ct.Event{
				primeMem,
				used("MEMORY.md", "doc", ct.EvidenceRead, ct.MechanismRetrieval, 3),
				used("MEMORY.md", "doc", ct.EvidenceReported, ct.MechanismSelfReport, 4),
			},
			wantCat: CategoryModerate,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			surfaced := Surfaced(tt.events)
			r := Build(tt.events, surfaced, tt.commits, tt.allowJudged)
			assert.Equal(t, tt.wantCat, r.DerivedCategory)
			assert.NotEmpty(t, r.DerivedReason)
			assert.Equal(t, len(surfaced), r.Surfaced)
		})
	}
}

func TestBuild_DedupStrongestWins(t *testing.T) {
	events := []ct.Event{
		whisperOx,
		{Type: ct.EventUsed, Ref: "Ox7f2a", Evidence: ct.EvidenceJudged, Mechanism: ct.MechanismJudge},
		{Type: ct.EventUsed, Ref: "ox7f2a", Evidence: ct.EvidenceReported, Mechanism: ct.MechanismSelfReport, Decision: "why"},
		{Type: ct.EventUsed, Ref: "Ox7f2a", Evidence: ct.EvidenceRequery, Mechanism: ct.MechanismRetrieval, Seq: 5},
		{Type: ct.EventUsed, Ref: "Ox7f2a", Evidence: ct.EvidenceRead, Mechanism: ct.MechanismRetrieval, Seq: 6},
		{Type: ct.EventUsed, Ref: "Ox7f2a", Evidence: ct.EvidenceCitedCommit, Mechanism: ct.MechanismCitation, Seq: 20, Commit: "3e1c9d0ffff"},
		{Type: ct.EventUsed, Ref: "Ox7f2a", Evidence: ct.EvidenceCited, Mechanism: ct.MechanismCitation, Seq: 8},
	}
	r := Build(events, Surfaced(events), 0, false)
	require.Len(t, r.Refs, 1)
	got := r.Refs[0]
	assert.Equal(t, ct.EvidenceCitedCommit, got.Evidence)
	assert.Equal(t, GradeProvable, got.Grade)
	assert.Equal(t, "murmur", got.RefType, "ref type filled from surfaced")
	assert.Equal(t, "why", got.Decision, "why carried over from weaker report")
	assert.Equal(t, 20, got.Seq)
	assert.True(t, got.MatchedSurfaced)
	assert.Equal(t, 1, r.UsedProvable)
	assert.Equal(t, 0, r.UsedReported)
	assert.Equal(t, map[string]int{BucketRead: 0, BucketCited: 1, BucketReported: 0, BucketJudged: 0}, r.ByEvidence)
	assert.Equal(t, "derived from context use: cited murmur Ox7f2a in commit 3e1c9d0 (turn 20)", r.DerivedReason)
}

func TestBuild_CountsAndBuckets(t *testing.T) {
	events := []ct.Event{
		primeMem, primeSoul, whisperOx, recallSes,
		used("MEMORY.md", "doc", ct.EvidenceRead, ct.MechanismRetrieval, 1),
		used("SOUL.md", "doc", ct.EvidenceRequery, ct.MechanismRetrieval, 2),
		used("Ox7f2a", "murmur", ct.EvidenceReported, ct.MechanismSelfReport, 3),
		used("2026-10-01-dana-auth", "session", ct.EvidenceJudged, ct.MechanismJudge, 4),
		{Type: ct.EventUsed, Evidence: ct.EvidenceNone},
		{Type: ct.EventUsed, Ref: "x", Evidence: "future-evidence"},
		{Type: ct.EventUsed, Evidence: ct.EvidenceRead}, // no ref
	}
	r := Build(events, Surfaced(events), 0, false)
	assert.Equal(t, 4, r.Surfaced)
	assert.Equal(t, 2, r.UsedProvable)
	assert.Equal(t, 1, r.UsedReported)
	assert.Equal(t, 1, r.UsedJudged)
	assert.True(t, r.ReportedNone)
	assert.Equal(t, map[string]int{BucketRead: 2, BucketCited: 0, BucketReported: 1, BucketJudged: 1}, r.ByEvidence)
	assert.Len(t, r.Refs, 4)
	assert.Equal(t, CategorySignificant, r.DerivedCategory)
	assert.Equal(t, "derived from context use: read MEMORY.md (turn 1); re-queried SOUL.md (turn 2); reported using murmur Ox7f2a (turn 3)", r.DerivedReason,
		"judged refs excluded from the reason when not allowed")
}

func TestBuild_LegacyInfluenced(t *testing.T) {
	tests := []struct {
		name     string
		ev       ct.Event
		wantRefs int
		wantEv   string
	}{
		{name: "empty mechanism counts as reported", ev: ct.Event{Type: ct.EventInfluenced, Ref: "MEMORY.md", Decision: "followed norm"}, wantRefs: 1, wantEv: ct.EvidenceReported},
		{name: "self-report counts as reported", ev: ct.Event{Type: ct.EventInfluenced, Mechanism: ct.MechanismSelfReport, Doc: "MEMORY.md"}, wantRefs: 1, wantEv: ct.EvidenceReported},
		{name: "injection influenced is not a use", ev: ct.Event{Type: ct.EventInfluenced, Mechanism: ct.MechanismInjection, Ref: "MEMORY.md"}},
		{name: "consulted is not a use", ev: ct.Event{Type: ct.EventConsulted, Mechanism: ct.MechanismRetrieval, Ref: "MEMORY.md"}},
		{name: "influenced without ref is skipped", ev: ct.Event{Type: ct.EventInfluenced}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := []ct.Event{primeMem, tt.ev}
			r := Build(events, Surfaced(events), 0, false)
			require.Len(t, r.Refs, tt.wantRefs)
			if tt.wantRefs > 0 {
				assert.Equal(t, tt.wantEv, r.Refs[0].Evidence)
				assert.Equal(t, GradeReported, r.Refs[0].Grade)
				assert.True(t, r.Refs[0].MatchedSurfaced)
			}
		})
	}
	// the legacy why+matched path reaches moderate like a new self-report
	events := []ct.Event{primeMem, {Type: ct.EventInfluenced, Ref: "MEMORY.md", Decision: "followed norm"}}
	assert.Equal(t, CategoryModerate, Build(events, Surfaced(events), 0, false).DerivedCategory)
}

func TestBuild_UsedWithoutEvidenceFallsBackToMechanism(t *testing.T) {
	tests := []struct {
		mech ct.Mechanism
		want string
	}{
		{ct.MechanismJudge, ct.EvidenceJudged},
		{ct.MechanismCitation, ct.EvidenceCited},
		{ct.MechanismRetrieval, ct.EvidenceRead},
		{ct.MechanismSelfReport, ct.EvidenceReported},
		{"", ct.EvidenceReported},
	}
	for _, tt := range tests {
		t.Run(string(tt.mech)+"->"+tt.want, func(t *testing.T) {
			r := Build([]ct.Event{{Type: ct.EventUsed, Ref: "a", Mechanism: tt.mech}}, nil, 0, true)
			require.Len(t, r.Refs, 1)
			assert.Equal(t, tt.want, r.Refs[0].Evidence)
			assert.False(t, r.Refs[0].MatchedSurfaced)
		})
	}
}

func TestBuild_MatchedSurfacedBasename(t *testing.T) {
	surfaced := []SurfacedRef{{Ref: "MEMORY.md", RefType: "doc"}, {Ref: "team/docs/Guide.md", RefType: "doc"}}
	tests := []struct {
		ref  string
		want bool
	}{
		{"MEMORY.md", true},
		{"memory.md", true},
		{"/home/x/.sageox/team/MEMORY.md", true},
		{`C:\team\memory.md`, true},
		{"guide.md", true},
		{"OTHER.md", false},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			r := Build([]ct.Event{used(tt.ref, "", ct.EvidenceRead, ct.MechanismRetrieval, 1)}, surfaced, 0, false)
			require.Len(t, r.Refs, 1)
			assert.Equal(t, tt.want, r.Refs[0].MatchedSurfaced)
			if tt.want {
				assert.Equal(t, "doc", r.Refs[0].RefType)
			}
		})
	}
}

func TestBuild_RefsCap(t *testing.T) {
	var events []ct.Event
	for i := range 30 {
		events = append(events, used(fmt.Sprintf("doc-%02d.md", i), "doc", ct.EvidenceRead, ct.MechanismRetrieval, i+1))
	}
	r := Build(events, nil, 0, false)
	assert.Len(t, r.Refs, MaxRefs)
	assert.Equal(t, 30, r.UsedProvable, "counts are computed before the cap")
	assert.Equal(t, 30, r.ByEvidence[BucketRead])
	assert.Equal(t, "doc-00.md", r.Refs[0].Ref)
	assert.Contains(t, r.DerivedReason, "+25 more")
}

func TestGradeAndBucketOf_Unknown(t *testing.T) {
	assert.Empty(t, GradeOf(ct.EvidenceNone))
	assert.Empty(t, GradeOf("bogus"))
	assert.Empty(t, BucketOf(ct.EvidenceNone))
}
