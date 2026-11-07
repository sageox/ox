package contextuse

import (
	"testing"

	"github.com/stretchr/testify/assert"

	ct "github.com/sageox/ox/internal/session/contexttrace"
)

func TestDerive(t *testing.T) {
	tests := []struct {
		name       string
		in         DeriveInput
		wantCat    string
		wantReason string
	}{
		{name: "empty", wantCat: CategoryNone, wantReason: "derived from context use: nothing used"},
		{
			name: "cited_commit plus read, spec example",
			in: DeriveInput{Provable: []UseRef{
				{Ref: "Ox7f2a", RefType: "murmur", Evidence: ct.EvidenceCitedCommit, Commit: "3e1c9d0123"},
				{Ref: "MEMORY.md", RefType: "doc", Evidence: ct.EvidenceRead, Seq: 14},
			}},
			wantCat:    CategorySignificant,
			wantReason: "derived from context use: cited murmur Ox7f2a in commit 3e1c9d0; read MEMORY.md (turn 14)",
		},
		{
			name:       "same ref twice is not two distinct provable refs",
			in:         DeriveInput{Provable: []UseRef{{Ref: "A.md", Evidence: ct.EvidenceRead}, {Ref: "a.md", Evidence: ct.EvidenceRequery}}},
			wantCat:    CategoryModerate,
			wantReason: "derived from context use: read A.md; re-queried a.md",
		},
		{
			name:       "cited without commit sha, short sha kept whole",
			in:         DeriveInput{Provable: []UseRef{{Ref: "pln_1", RefType: "plan", Evidence: ct.EvidenceCited, Seq: 2}, {Ref: "x", Evidence: ct.EvidenceCitedCommit, Commit: "abc"}}},
			wantCat:    CategorySignificant,
			wantReason: "derived from context use: cited plan pln_1 (turn 2); cited x in commit abc",
		},
		{
			name:       "judged only",
			in:         DeriveInput{Judged: []UseRef{{Ref: "ses_1", RefType: "session", Evidence: ct.EvidenceJudged}}},
			wantCat:    CategoryMinor,
			wantReason: "derived from context use: judged used session ses_1",
		},
		{
			name:    "reported matched with why beats nothing; never critical",
			in:      DeriveInput{Reported: []UseRef{{Ref: "Ox1", Evidence: ct.EvidenceReported, Decision: "w", MatchedSurfaced: true}}, ProducedCommits: 9},
			wantCat: CategoryModerate, wantReason: "derived from context use: reported using Ox1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cat, reason := Derive(tt.in)
			assert.Equal(t, tt.wantCat, cat)
			assert.Equal(t, tt.wantReason, reason)
			assert.NotEqual(t, "critical", cat)
		})
	}
}
