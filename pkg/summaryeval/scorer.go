package summaryeval

import (
	"fmt"
	"strings"
	"unicode"
)

// Score evaluates a candidate summary against a reference using the given
// weights. Each dimension produces a 0.0–1.0 score; the overall is the
// weighted sum. All scoring is deterministic and lexical — same input
// always produces the same score. No LLM calls.
func Score(name string, reference, candidate Summary, w Weights) SessionScore {
	dims := []DimensionScore{
		scoreTitle(reference.Title, candidate.Title),
		scoreSummary(reference.Summary, candidate.Summary),
		scoreKeyActions(reference.KeyActions, candidate.KeyActions),
		scoreOutcome(reference.Outcome, candidate.Outcome),
		scoreAhaMoments(reference.AhaMoments, candidate.AhaMoments),
	}

	overall := dims[0].Score*w.Title +
		dims[1].Score*w.Summary +
		dims[2].Score*w.KeyActions +
		dims[3].Score*w.Outcome +
		dims[4].Score*w.AhaMoments

	return SessionScore{
		Name:       name,
		Dimensions: dims,
		Overall:    clamp01(overall),
	}
}

// scoreTitle: Jaccard similarity on tokenized word sets + length sanity.
// Rationale: title paraphrases often share the key nouns ("lint", "fix",
// "refactor"); raw string match is too strict. A candidate that invents
// a wildly different title length also loses points.
func scoreTitle(ref, cand string) DimensionScore {
	refTokens := tokenSet(ref)
	candTokens := tokenSet(cand)
	if len(refTokens) == 0 && len(candTokens) == 0 {
		return DimensionScore{Dimension: DimTitle, Score: 1.0, Reason: "both empty"}
	}
	if len(refTokens) == 0 || len(candTokens) == 0 {
		return DimensionScore{Dimension: DimTitle, Score: 0.0, Reason: "one side empty"}
	}
	jac := jaccard(refTokens, candTokens)
	// Penalize gross length mismatch: candidate within 50%–200% of ref length keeps score
	lenRatio := float64(len(candTokens)) / float64(len(refTokens))
	if lenRatio < 0.5 || lenRatio > 2.0 {
		jac *= 0.7
	}
	return DimensionScore{
		Dimension: DimTitle,
		Score:     clamp01(jac),
		Reason:    fmt.Sprintf("jaccard=%.2f len_ratio=%.2f", jac, lenRatio),
	}
}

// scoreSummary: token-set Jaccard over the body, with a minimum-length
// penalty (a candidate that's 10% the length of the reference can't cover
// the same content even if its words overlap).
func scoreSummary(ref, cand string) DimensionScore {
	refTokens := tokenSet(ref)
	candTokens := tokenSet(cand)
	if len(refTokens) == 0 && len(candTokens) == 0 {
		return DimensionScore{Dimension: DimSummary, Score: 1.0, Reason: "both empty"}
	}
	if len(refTokens) == 0 || len(candTokens) == 0 {
		return DimensionScore{Dimension: DimSummary, Score: 0.0, Reason: "one side empty"}
	}
	jac := jaccard(refTokens, candTokens)
	lenRatio := float64(len(candTokens)) / float64(len(refTokens))
	if lenRatio < 0.3 {
		jac *= 0.5
	} else if lenRatio > 3.0 {
		jac *= 0.8
	}
	return DimensionScore{
		Dimension: DimSummary,
		Score:     clamp01(jac),
		Reason:    fmt.Sprintf("jaccard=%.2f len_ratio=%.2f", jac, lenRatio),
	}
}

// scoreKeyActions: F1-like over bullet content. For each reference bullet,
// the best candidate match contributes to recall; for each candidate bullet,
// best reference match contributes to precision. Bullets match by token-set
// Jaccard above a loose threshold (0.3). Captures paraphrasing.
func scoreKeyActions(ref, cand []string) DimensionScore {
	if len(ref) == 0 && len(cand) == 0 {
		return DimensionScore{Dimension: DimKeyActions, Score: 1.0, Reason: "both empty"}
	}
	if len(ref) == 0 || len(cand) == 0 {
		return DimensionScore{Dimension: DimKeyActions, Score: 0.0, Reason: "one side empty"}
	}
	const matchThreshold = 0.3

	// recall: fraction of reference bullets matched by any candidate
	var matchedRef int
	for _, r := range ref {
		best := 0.0
		for _, c := range cand {
			if s := jaccard(tokenSet(r), tokenSet(c)); s > best {
				best = s
			}
		}
		if best >= matchThreshold {
			matchedRef++
		}
	}
	recall := float64(matchedRef) / float64(len(ref))

	// precision: fraction of candidate bullets that match any reference
	var matchedCand int
	for _, c := range cand {
		best := 0.0
		for _, r := range ref {
			if s := jaccard(tokenSet(r), tokenSet(c)); s > best {
				best = s
			}
		}
		if best >= matchThreshold {
			matchedCand++
		}
	}
	precision := float64(matchedCand) / float64(len(cand))

	var f1 float64
	if recall+precision > 0 {
		f1 = 2 * recall * precision / (recall + precision)
	}
	return DimensionScore{
		Dimension: DimKeyActions,
		Score:     clamp01(f1),
		Reason:    fmt.Sprintf("precision=%.2f recall=%.2f f1=%.2f ref=%d cand=%d", precision, recall, f1, len(ref), len(cand)),
	}
}

// scoreOutcome: exact match on the outcome enum. Binary.
func scoreOutcome(ref, cand string) DimensionScore {
	ref = strings.TrimSpace(strings.ToLower(ref))
	cand = strings.TrimSpace(strings.ToLower(cand))
	if ref == cand {
		return DimensionScore{Dimension: DimOutcome, Score: 1.0, Reason: "exact match"}
	}
	return DimensionScore{Dimension: DimOutcome, Score: 0.0, Reason: fmt.Sprintf("ref=%q cand=%q", ref, cand)}
}

// scoreAhaMoments: blend of count ratio and type-distribution similarity.
// We don't score highlight text — that's too lexical-paraphrase-sensitive
// and better served by an LLM judge in a future version. What we can
// measure deterministically: "did the candidate identify roughly the
// right number of moments, of roughly the right types?"
func scoreAhaMoments(ref, cand []AhaMoment) DimensionScore {
	if len(ref) == 0 && len(cand) == 0 {
		return DimensionScore{Dimension: DimAhaMoments, Score: 1.0, Reason: "both empty"}
	}
	if len(ref) == 0 {
		return DimensionScore{Dimension: DimAhaMoments, Score: 0.5, Reason: "ref empty, cand non-empty — over-eager but not wrong"}
	}
	if len(cand) == 0 {
		return DimensionScore{Dimension: DimAhaMoments, Score: 0.0, Reason: "cand empty, ref non-empty — missed insights"}
	}

	// count ratio: 1.0 if counts match exactly, decays with delta
	countDelta := abs(len(ref) - len(cand))
	countScore := 1.0 - float64(countDelta)/float64(max(len(ref), len(cand)))

	// type-distribution Jaccard: set of distinct types present
	refTypes := make(map[string]bool, len(ref))
	candTypes := make(map[string]bool, len(cand))
	for _, m := range ref {
		refTypes[strings.ToLower(m.Type)] = true
	}
	for _, m := range cand {
		candTypes[strings.ToLower(m.Type)] = true
	}
	typeScore := jaccard(refTypes, candTypes)

	blended := 0.6*countScore + 0.4*typeScore
	return DimensionScore{
		Dimension: DimAhaMoments,
		Score:     clamp01(blended),
		Reason:    fmt.Sprintf("count_score=%.2f type_score=%.2f ref=%d cand=%d", countScore, typeScore, len(ref), len(cand)),
	}
}

// tokenSet returns a set of lowercased alphanumeric tokens from s.
// Punctuation is split on. Empty strings produce an empty set.
func tokenSet(s string) map[string]bool {
	out := make(map[string]bool)
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			out[b.String()] = true
			b.Reset()
		}
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		} else {
			flush()
		}
	}
	flush()
	return out
}

// jaccard returns |A ∩ B| / |A ∪ B| for two string sets. 0.0 when both
// empty is indeterminate; caller should handle that separately.
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	var inter int
	union := make(map[string]bool, len(a)+len(b))
	for k := range a {
		union[k] = true
		if b[k] {
			inter++
		}
	}
	for k := range b {
		union[k] = true
	}
	if len(union) == 0 {
		return 0
	}
	return float64(inter) / float64(len(union))
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
