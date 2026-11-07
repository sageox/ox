package contextuse

import (
	"fmt"
	"strings"

	"github.com/sageox/ox/internal/session/contexttrace"
)

// Derived categories. These are the session.ScoreCategory literals, kept as
// strings so this package never imports internal/session (which would cycle
// once session metadata embeds a Rollup). Critical is never derived.
const (
	CategoryNone        = "none"
	CategoryMinor       = "minor"
	CategoryModerate    = "moderate"
	CategorySignificant = "significant"
)

// reasonPrefix starts every derived reason.
const reasonPrefix = "derived from context use: "

// maxReasonRefs caps how many refs a reason lists before "+N more".
const maxReasonRefs = 5

// knowledgeRefTypes are ref types that carry team knowledge (as opposed to a
// generic doc); one provable use of one plus a commit is significant.
var knowledgeRefTypes = map[string]bool{
	"session": true, "discussion": true, "murmur": true, "plan": true, "kb": true,
}

// DeriveInput is the deduped use evidence Derive grades. Judged is empty
// unless judged evidence is allowed.
type DeriveInput struct {
	Provable        []UseRef
	Reported        []UseRef
	Judged          []UseRef
	ProducedCommits int
}

// Derive maps use evidence to a display-only attribution category and a
// deterministic, ref-listing reason. First match wins:
//
//  1. significant: >=2 distinct provable refs; or a provable knowledge ref
//     (session/discussion/murmur/plan/kb) and >=1 produced commit; or any
//     cited_commit.
//  2. moderate: >=1 provable ref; or a reported ref with a why that matches
//     something surfaced.
//  3. minor: only unmatched or why-less reported refs, or only judged refs.
//  4. none: otherwise.
func Derive(in DeriveInput) (category, reason string) {
	category = deriveCategory(in)
	if category == CategoryNone {
		return category, reasonPrefix + "nothing used"
	}
	return category, reasonFor(in)
}

func deriveCategory(in DeriveInput) string {
	distinct := map[string]bool{}
	for _, u := range in.Provable {
		distinct[strings.ToLower(u.Ref)] = true
		if u.Evidence == contexttrace.EvidenceCitedCommit {
			return CategorySignificant
		}
		if in.ProducedCommits >= 1 && knowledgeRefTypes[u.RefType] {
			return CategorySignificant
		}
	}
	if len(distinct) >= 2 {
		return CategorySignificant
	}
	if len(distinct) == 1 {
		return CategoryModerate
	}
	for _, u := range in.Reported {
		if u.Decision != "" && u.MatchedSurfaced {
			return CategoryModerate
		}
	}
	if len(in.Reported) > 0 || len(in.Judged) > 0 {
		return CategoryMinor
	}
	return CategoryNone
}

// reasonFor lists refs strongest grade first, in input order within a grade:
// "derived from context use: cited murmur Ox7f2a in commit 3e1c9d0; read MEMORY.md (turn 14)".
func reasonFor(in DeriveInput) string {
	var all []UseRef
	all = append(all, in.Provable...)
	all = append(all, in.Reported...)
	all = append(all, in.Judged...)
	parts := make([]string, 0, maxReasonRefs+1)
	for i, u := range all {
		if i == maxReasonRefs {
			parts = append(parts, fmt.Sprintf("+%d more", len(all)-maxReasonRefs))
			break
		}
		parts = append(parts, describe(u))
	}
	return reasonPrefix + strings.Join(parts, "; ")
}

func describe(u UseRef) string {
	var verb string
	switch u.Evidence {
	case contexttrace.EvidenceRequery:
		verb = "re-queried"
	case contexttrace.EvidenceCited, contexttrace.EvidenceCitedCommit:
		verb = "cited"
	case contexttrace.EvidenceReported:
		verb = "reported using"
	case contexttrace.EvidenceJudged:
		verb = "judged used"
	default:
		verb = "read"
	}
	var b strings.Builder
	b.WriteString(verb)
	b.WriteByte(' ')
	if knowledgeRefTypes[u.RefType] {
		b.WriteString(u.RefType)
		b.WriteByte(' ')
	}
	b.WriteString(u.Ref)
	if u.Evidence == contexttrace.EvidenceCitedCommit && u.Commit != "" {
		b.WriteString(" in commit ")
		b.WriteString(shortSHA(u.Commit))
	}
	if u.Seq > 0 {
		fmt.Fprintf(&b, " (turn %d)", u.Seq)
	}
	return b.String()
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
