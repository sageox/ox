package summaryeval

import (
	"strings"
	"testing"
)

// This file is a regression guard: if the scorer ever stops catching
// the exact bug signatures we've seen in production, we want a test to
// fail loudly. Each bug has a named test that constructs a candidate
// matching the observed fingerprint and asserts the scorer gives it a
// low enough score that a CI gate would catch it.
//
// Filed bugs referenced:
//
//   - ox-g5zw: distiller ships summaries with empty meta.json.title.
//     91/153 sessions on the canonical ledger were affected before the
//     richness validator landed. The scorer's title dimension must
//     score near zero for empty-title candidates.
//
//   - ox-0pxt: galexy-account summaries were boilerplate 56-char
//     stats-only ("N user messages, N assistant responses"). ~15
//     sessions affected before ValidateSummaryRichness caught it.
//     The scorer's summary dimension must score near zero for
//     stats-only candidates against any substantive reference.

// richReference builds a reference summary with enough substance that
// legitimate candidates can plausibly score near 1.0.
func richReference() Summary {
	return Summary{
		Title:   "Fix 19 golangci-lint issues across 10 files",
		Summary: "CI flagged 19 lint issues across gosec, staticcheck, and misspell rules. Applied fixes in an isolated worktree, rebased onto main after it absorbed a test-coverage branch, verified make lint was clean and tests passed, opened PR #317.",
		KeyActions: []string{
			"Pulled the failing lint log from gh run 23602069696",
			"Dispatched worktree subagent to apply fixes across 10 files",
			"Rebased onto updated main after test-coverage merge",
			"Ran make lint (0 issues) and make test (9446 passing)",
			"Opened PR #317",
		},
		Outcome: "success",
		AhaMoments: []AhaMoment{
			{Seq: 1, Type: "question"},
			{Seq: 20, Type: "question"},
			{Seq: 50, Type: "breakthrough"},
		},
	}
}

// TestScorer_CatchesOxG5zw_EmptyTitleBug proves the scorer flags
// summaries with empty titles — the ox-g5zw distiller bug fingerprint.
// The title dimension must score 0 (one-side-empty case in scoreTitle).
func TestScorer_CatchesOxG5zw_EmptyTitleBug(t *testing.T) {
	ref := richReference()
	// Candidate mirrors the production bug: everything else is fine, only
	// the title is missing.
	buggy := ref
	buggy.Title = ""

	result := Score("ox-g5zw-fingerprint", ref, buggy, DefaultWeights())
	if result.Overall >= 0.85 {
		t.Errorf("empty-title candidate should score well below the gate; got overall=%.3f", result.Overall)
	}

	// The title dimension specifically must score 0 — that's the load-
	// bearing check. Any other dimension scoring poorly for unrelated
	// reasons would let the real bug hide behind noise.
	//
	// Fail fast if DimTitle is missing entirely from the result: a sentinel
	// score of -1 would otherwise silently pass the "< 0.15" style checks
	// and let a future Score() regression that stops emitting the title
	// dimension slip through this test.
	titleScore, found := dimensionScore(result.Dimensions, DimTitle)
	if !found {
		t.Fatalf("result missing %q dimension entirely; Score() contract broken", DimTitle)
	}
	if titleScore != 0 {
		t.Errorf("title dimension for empty-title candidate should be 0.0; got %.3f (reason: %q)",
			titleScore, findReason(result.Dimensions, DimTitle))
	}
}

// TestScorer_CatchesOxG5zw_EmptyTitleAtCorpusLevel proves a ledger with
// the ox-g5zw bug applied to every session would trip a MinDimensions
// gate on 'title'. This is the behavior CI needs in practice — not just
// a single-session score but an aggregate regression signal.
func TestScorer_CatchesOxG5zw_EmptyTitleAtCorpusLevel(t *testing.T) {
	corpus := []GoldenSession{
		{Name: "s1", Reference: richReference()},
		{Name: "s2", Reference: richReference()},
		{Name: "s3", Reference: richReference()},
	}
	// Every candidate has the ox-g5zw fingerprint — empty title.
	buggy := richReference()
	buggy.Title = ""
	candidates := map[string]Summary{
		"s1": buggy,
		"s2": buggy,
		"s3": buggy,
	}
	report := ScoreCorpus(corpus, candidates, DefaultWeights(), &Gates{
		MinDimensions: map[string]float64{DimTitle: 0.5},
	})
	if len(report.GatesFailed) == 0 {
		t.Fatalf("corpus-wide empty-title bug must trip the title gate; got GatesFailed=[] and DimensionMeans[title]=%.3f",
			report.DimensionMeans[DimTitle])
	}
	var titleGateFailed bool
	for _, g := range report.GatesFailed {
		if strings.Contains(g, "title") {
			titleGateFailed = true
			break
		}
	}
	if !titleGateFailed {
		t.Errorf("GatesFailed should name 'title': %v", report.GatesFailed)
	}
}

// TestScorer_CatchesOx0pxt_StatsOnlySummaryBug proves the scorer flags
// stats-only summaries — the exact 56-char "N user messages, N assistant
// responses" fingerprint seen in galexy-account sessions. The summary
// dimension must score near zero because that boilerplate shares
// almost no tokens with any substantive reference.
func TestScorer_CatchesOx0pxt_StatsOnlySummaryBug(t *testing.T) {
	ref := richReference()
	// The real bug produced literal text like this. Exact wording varies
	// slightly in the wild; the fingerprint is stats-only + no real
	// narrative.
	statsOnly := ref
	statsOnly.Summary = "42 user messages, 38 assistant responses"

	result := Score("ox-0pxt-fingerprint", ref, statsOnly, DefaultWeights())

	// Fail fast if DimSummary is missing. Without this, a sentinel score
	// of -1 would silently satisfy "-1 > 0.15" == false and let a
	// future Score() regression that stopped emitting the summary
	// dimension sneak past this bug-detection test.
	summaryScore, found := dimensionScore(result.Dimensions, DimSummary)
	if !found {
		t.Fatalf("result missing %q dimension entirely; Score() contract broken", DimSummary)
	}
	// A stats-only summary shares almost no vocabulary with a real
	// narrative reference — Jaccard should be < 0.1.
	if summaryScore > 0.15 {
		t.Errorf("stats-only summary candidate should score near zero on summary dim; got %.3f (reason: %q)",
			summaryScore, findReason(result.Dimensions, DimSummary))
	}
}

// TestScorer_CatchesOx0pxt_StatsOnlyAtCorpusLevel — corpus-wide version
// of the ox-0pxt check. A ledger where every candidate is stats-only
// boilerplate must fail a MinDimensions gate on summary.
func TestScorer_CatchesOx0pxt_StatsOnlyAtCorpusLevel(t *testing.T) {
	corpus := []GoldenSession{
		{Name: "s1", Reference: richReference()},
		{Name: "s2", Reference: richReference()},
		{Name: "s3", Reference: richReference()},
	}
	statsOnly := richReference()
	statsOnly.Summary = "42 user messages, 38 assistant responses"
	candidates := map[string]Summary{
		"s1": statsOnly,
		"s2": statsOnly,
		"s3": statsOnly,
	}
	report := ScoreCorpus(corpus, candidates, DefaultWeights(), &Gates{
		MinDimensions: map[string]float64{DimSummary: 0.5},
	})
	if len(report.GatesFailed) == 0 {
		t.Fatalf("corpus-wide stats-only bug must trip the summary gate; got GatesFailed=[] and DimensionMeans[summary]=%.3f",
			report.DimensionMeans[DimSummary])
	}
	var summaryGateFailed bool
	for _, g := range report.GatesFailed {
		if strings.Contains(g, "summary") {
			summaryGateFailed = true
			break
		}
	}
	if !summaryGateFailed {
		t.Errorf("GatesFailed should name 'summary': %v", report.GatesFailed)
	}
}

// TestScorer_LegitimateCandidateClearsGates is the positive control:
// a candidate that IS the reference (or paraphrases it closely) must
// clear the same gates the bug tests above fail. Prevents the gate
// thresholds from being accidentally set so loose that they'd let real
// bugs through, or so tight that legitimate work trips them.
func TestScorer_LegitimateCandidateClearsGates(t *testing.T) {
	corpus := []GoldenSession{
		{Name: "s1", Reference: richReference()},
		{Name: "s2", Reference: richReference()},
		{Name: "s3", Reference: richReference()},
	}
	// Candidates == references (perfect score).
	candidates := map[string]Summary{
		"s1": corpus[0].Reference,
		"s2": corpus[1].Reference,
		"s3": corpus[2].Reference,
	}
	report := ScoreCorpus(corpus, candidates, DefaultWeights(), &Gates{
		MinOverall: 0.9,
		MinDimensions: map[string]float64{
			DimTitle:   0.9,
			DimSummary: 0.9,
		},
	})
	if len(report.GatesFailed) != 0 {
		t.Errorf("legitimate candidates must clear gates; got GatesFailed=%v report=%+v",
			report.GatesFailed, report)
	}
}

func findReason(dims []DimensionScore, name string) string {
	for _, d := range dims {
		if d.Dimension == name {
			return d.Reason
		}
	}
	return ""
}

// dimensionScore returns (score, true) when the named dimension is
// present in the result, (0, false) when it's missing. Tests should
// Fatal on missing instead of allowing a sentinel value to vacuously
// satisfy a threshold comparison.
func dimensionScore(dims []DimensionScore, name string) (float64, bool) {
	for _, d := range dims {
		if d.Dimension == name {
			return d.Score, true
		}
	}
	return 0, false
}
