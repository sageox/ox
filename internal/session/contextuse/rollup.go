package contextuse

import (
	"path"
	"strings"

	"github.com/sageox/ox/internal/session/contexttrace"
)

// Grades of evidence behind a use.
const (
	GradeProvable = "provable" // retrieval or citation: observed, not claimed
	GradeReported = "reported" // the AI coworker's own self-report
	GradeJudged   = "judged"   // an LLM judge's inference
)

// Display buckets for ByEvidence.
const (
	BucketRead     = "read"     // read + requery
	BucketCited    = "cited"    // cited + cited_commit
	BucketReported = "reported" // self-report
	BucketJudged   = "judged"   // LLM judge
)

// MaxRefs caps Rollup.Refs. Counts are computed before the cap.
const MaxRefs = 20

// UseRef is one used ref after dedup: the strongest evidence for that ref.
type UseRef struct {
	Ref             string                 `json:"ref"`
	RefType         string                 `json:"ref_type,omitempty"`
	Evidence        string                 `json:"evidence"`
	Grade           string                 `json:"grade"`
	Mechanism       contexttrace.Mechanism `json:"mechanism,omitempty"`
	Seq             int                    `json:"seq,omitempty"`
	Commit          string                 `json:"commit,omitempty"`
	Decision        string                 `json:"decision,omitempty"`
	MatchedSurfaced bool                   `json:"matched_surfaced"`
}

// Rollup is the compact surfaced-vs-used summary of one session.
type Rollup struct {
	Surfaced        int            `json:"surfaced"`
	UsedProvable    int            `json:"used_provable"`
	UsedReported    int            `json:"used_reported"`
	UsedJudged      int            `json:"used_judged,omitempty"`
	ReportedNone    bool           `json:"reported_none,omitempty"`
	ByEvidence      map[string]int `json:"by_evidence"`
	Refs            []UseRef       `json:"refs,omitempty"`
	DerivedCategory string         `json:"derived_category"`
	DerivedReason   string         `json:"derived_reason,omitempty"`
}

// evidenceRank orders evidence strongest first; a higher value wins dedup.
var evidenceRank = map[string]int{
	contexttrace.EvidenceCitedCommit: 6,
	contexttrace.EvidenceCited:       5,
	contexttrace.EvidenceRead:        4,
	contexttrace.EvidenceRequery:     3,
	contexttrace.EvidenceReported:    2,
	contexttrace.EvidenceJudged:      1,
}

// GradeOf maps an evidence value to its grade ("" for unknown or none).
func GradeOf(evidence string) string {
	switch evidence {
	case contexttrace.EvidenceRead, contexttrace.EvidenceRequery,
		contexttrace.EvidenceCited, contexttrace.EvidenceCitedCommit:
		return GradeProvable
	case contexttrace.EvidenceReported:
		return GradeReported
	case contexttrace.EvidenceJudged:
		return GradeJudged
	}
	return ""
}

// BucketOf maps an evidence value to its ByEvidence display bucket.
func BucketOf(evidence string) string {
	switch evidence {
	case contexttrace.EvidenceRead, contexttrace.EvidenceRequery:
		return BucketRead
	case contexttrace.EvidenceCited, contexttrace.EvidenceCitedCommit:
		return BucketCited
	case contexttrace.EvidenceReported:
		return BucketReported
	case contexttrace.EvidenceJudged:
		return BucketJudged
	}
	return ""
}

// evidenceOf returns the evidence a use-bearing event carries, or "" when the
// event is not a use. Legacy influenced events written by the self-report
// path (mechanism self-report or empty) count as reported. A used event with
// no evidence falls back to its mechanism.
func evidenceOf(ev contexttrace.Event) string {
	switch ev.Type {
	case contexttrace.EventUsed:
		if ev.Evidence != "" {
			return ev.Evidence
		}
		switch ev.Mechanism {
		case contexttrace.MechanismJudge:
			return contexttrace.EvidenceJudged
		case contexttrace.MechanismCitation:
			return contexttrace.EvidenceCited
		case contexttrace.MechanismRetrieval:
			return contexttrace.EvidenceRead
		}
		return contexttrace.EvidenceReported
	case contexttrace.EventInfluenced:
		if ev.Mechanism == contexttrace.MechanismSelfReport || ev.Mechanism == "" {
			return contexttrace.EvidenceReported
		}
	}
	return ""
}

// Build rolls the trace's use evidence up against what was surfaced. Judged
// uses are always counted but reach the derived category only when
// allowJudged is true.
func Build(events []contexttrace.Event, surfaced []SurfacedRef, producedCommits int, allowJudged bool) Rollup {
	r := Rollup{
		Surfaced: len(surfaced),
		ByEvidence: map[string]int{
			BucketRead: 0, BucketCited: 0, BucketReported: 0, BucketJudged: 0,
		},
	}

	var refs []UseRef
	index := map[string]int{}
	for _, ev := range events {
		evidence := evidenceOf(ev)
		if evidence == "" {
			continue
		}
		if evidence == contexttrace.EvidenceNone {
			r.ReportedNone = true
			continue
		}
		grade := GradeOf(evidence)
		if grade == "" {
			continue // unknown evidence from a newer writer
		}
		ref := ev.Ref
		if ref == "" {
			ref = ev.Doc
		}
		if ref == "" {
			continue
		}
		u := UseRef{
			Ref: CanonicalRef(ev.RefType, ref), RefType: ev.RefType, Evidence: evidence, Grade: grade,
			Mechanism: ev.Mechanism, Seq: ev.Seq, Commit: ev.Commit, Decision: ev.Decision,
		}
		if u.Ref == "" {
			continue
		}
		if s, ok := matchSurfaced(ref, ev.Aliases, surfaced); ok {
			u.MatchedSurfaced = true
			if u.RefType == "" {
				u.RefType = s.RefType
			}
			if c := CanonicalRef(s.RefType, s.Ref); s.byName && !strings.EqualFold(c, u.Ref) {
				// Recorded under another of the item's names (rec_ for cnv_,
				// a slug for pln_): fold the use into the surfaced item so it
				// is one item with its link. A case-only difference keeps the
				// use's own spelling, as dedup always has.
				u.Ref = c
			}
		}
		key := strings.ToLower(u.Ref)
		i, ok := index[key]
		if !ok {
			index[key] = len(refs)
			refs = append(refs, u)
			continue
		}
		refs[i] = merge(refs[i], u)
	}

	var in DeriveInput
	in.ProducedCommits = producedCommits
	for _, u := range refs {
		r.ByEvidence[BucketOf(u.Evidence)]++
		switch u.Grade {
		case GradeProvable:
			r.UsedProvable++
			in.Provable = append(in.Provable, u)
		case GradeReported:
			r.UsedReported++
			in.Reported = append(in.Reported, u)
		case GradeJudged:
			r.UsedJudged++
			if allowJudged {
				in.Judged = append(in.Judged, u)
			}
		}
	}
	r.DerivedCategory, r.DerivedReason = Derive(in)

	if len(refs) > MaxRefs {
		refs = refs[:MaxRefs]
	}
	r.Refs = refs
	return r
}

// merge keeps the stronger of two uses of the same ref, carrying over a
// ref type or a why the stronger one lacks.
func merge(cur, next UseRef) UseRef {
	win, lose := cur, next
	if evidenceRank[next.Evidence] > evidenceRank[cur.Evidence] {
		win, lose = next, cur
	}
	if win.RefType == "" {
		win.RefType = lose.RefType
	}
	if win.Decision == "" {
		win.Decision = lose.Decision
	}
	win.MatchedSurfaced = win.MatchedSurfaced || lose.MatchedSurfaced
	return win
}

// surfacedMatch is the surfaced entry a use matched, and whether it matched
// by identity (one of the item's names) rather than by file basename.
type surfacedMatch struct {
	SurfacedRef
	byName bool
}

// matchSurfaced finds the surfaced entry for a used ref and its aliases:
// first by identity — the ref or an alias names the item (SurfacedRef.Matches:
// canonical, case-insensitive, aliases included) — else by a case-insensitive
// basename (a use of "/ledger/MEMORY.md" matches surfaced "MEMORY.md").
func matchSurfaced(ref string, aliases []string, surfaced []SurfacedRef) (surfacedMatch, bool) {
	for _, name := range append([]string{ref}, aliases...) {
		for _, s := range surfaced {
			if s.Matches(name) {
				return surfacedMatch{SurfacedRef: s, byName: true}, true
			}
		}
	}
	base := path.Base(strings.ReplaceAll(ref, "\\", "/"))
	for _, s := range surfaced {
		if strings.EqualFold(path.Base(strings.ReplaceAll(s.Ref, "\\", "/")), base) {
			return surfacedMatch{SurfacedRef: s}, true
		}
	}
	return surfacedMatch{}, false
}
