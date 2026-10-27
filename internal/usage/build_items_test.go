package usage

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/session/contexttrace"
)

// TestBuild_RelevantItemDetail keeps the raw evidence beside its display
// bucket. Failure prevented: the brief says "read" for a re-query or "cited"
// for a commit citation, losing the one fact a coworker can verify.
func TestBuild_RelevantItemDetail(t *testing.T) {
	tests := []struct {
		evidence   string
		wantBucket string
		wantGrade  string
	}{
		{contexttrace.EvidenceRead, "read", "provable"},
		{contexttrace.EvidenceRequery, "read", "provable"},
		{contexttrace.EvidenceCited, "cited", "provable"},
		{contexttrace.EvidenceCitedCommit, "cited", "provable"},
		{contexttrace.EvidenceReported, "reported", "reported"},
		{contexttrace.EvidenceJudged, "judged", "judged"},
	}
	for _, tt := range tests {
		t.Run(tt.evidence, func(t *testing.T) {
			r := Build(Input{TraceSource: TraceSourceSession, Events: []contexttrace.Event{used(tt.evidence, "mur_x", "murmur", 3)}})
			require.Len(t, r.Relevant.Items, 1)
			it := r.Relevant.Items[0]
			assert.Equal(t, tt.wantBucket, it.Evidence)
			assert.Equal(t, tt.evidence, it.Detail)
			assert.Equal(t, tt.wantGrade, it.Grade)
			assert.Equal(t, 3, it.Seq)
		})
	}
}

// TestBuild_RelevantItemsTimeline orders used items by the turn they were
// used in, ties in rollup order, so the brief reads as a timeline.
func TestBuild_RelevantItemsTimeline(t *testing.T) {
	r := Build(Input{TraceSource: TraceSourceSession, Events: []contexttrace.Event{
		used(contexttrace.EvidenceReported, "late", "doc", 30),
		used(contexttrace.EvidenceRead, "tie-a", "doc", 10),
		used(contexttrace.EvidenceRead, "early", "doc", 2),
		used(contexttrace.EvidenceRead, "tie-b", "doc", 10),
		used(contexttrace.EvidenceRead, "unsequenced", "doc", 0),
	}})
	var refs []string
	for _, it := range r.Relevant.Items {
		refs = append(refs, it.Ref)
	}
	assert.Equal(t, []string{"unsequenced", "early", "tie-a", "tie-b", "late"}, refs)
}

// TestBuild_RelevantItemCommitAndWhy carries the citing commit (short) and
// the reported --why onto the item. Failure prevented: "cited in commit"
// with no commit, or a runaway why bloating agent context.
func TestBuild_RelevantItemCommitAndWhy(t *testing.T) {
	cited := used(contexttrace.EvidenceCitedCommit, "cnv_abc", "discussion", 12)
	cited.Commit = "3e1c9d0f5a6b7c8d9e0f"
	plainCite := used(contexttrace.EvidenceCited, "pln_q", "plan", 13)
	plainCite.Commit = "ffffffffffff" // a non-commit citation never shows a commit
	short := used(contexttrace.EvidenceCitedCommit, "mur_s", "murmur", 14)
	short.Commit = "abc12"
	reported := used(contexttrace.EvidenceReported, "pln_r", "plan", 15)
	reported.Decision = "  kept the retry budget at 3  "
	long := used(contexttrace.EvidenceReported, "mur_long", "murmur", 16)
	long.Decision = strings.Repeat("é", maxWhyRunes+50)

	r := Build(Input{TraceSource: TraceSourceSession, Events: []contexttrace.Event{cited, plainCite, short, reported, long}})
	require.Len(t, r.Relevant.Items, 5)
	byRef := map[string]RelevantItem{}
	for _, it := range r.Relevant.Items {
		byRef[it.Ref] = it
	}
	assert.Equal(t, "3e1c9d0", byRef["cnv_abc"].Commit)
	assert.Equal(t, "cited_commit", byRef["cnv_abc"].Detail)
	assert.Empty(t, byRef["pln_q"].Commit)
	assert.Equal(t, "abc12", byRef["mur_s"].Commit)
	assert.Equal(t, "kept the retry budget at 3", byRef["pln_r"].Why)
	assert.Equal(t, maxWhyRunes, utf8.RuneCountInString(byRef["mur_long"].Why))
	assert.True(t, utf8.ValidString(byRef["mur_long"].Why))
}

// TestBuild_RelevantItemWhySurvivesStrongerEvidence: a provable read wins the
// dedup, but the why reported for the same ref is still shown.
func TestBuild_RelevantItemWhySurvivesStrongerEvidence(t *testing.T) {
	rep := used(contexttrace.EvidenceReported, "pln_x", "plan", 5)
	rep.Decision = "followed step 2"
	r := Build(Input{TraceSource: TraceSourceSession, Events: []contexttrace.Event{rep, used(contexttrace.EvidenceRead, "pln_x", "plan", 7)}})
	require.Len(t, r.Relevant.Items, 1)
	assert.Equal(t, "read", r.Relevant.Items[0].Detail)
	assert.Equal(t, "followed step 2", r.Relevant.Items[0].Why)
}
