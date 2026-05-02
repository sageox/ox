package summaryeval

import (
	"math"
	"strings"
	"testing"
)

func floatEq(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestScoreTitle_Perfect(t *testing.T) {
	d := scoreTitle("Fix 19 lint errors", "Fix 19 lint errors")
	if d.Score != 1.0 {
		t.Errorf("exact title match: got %.3f, want 1.0", d.Score)
	}
}

func TestScoreTitle_Paraphrase(t *testing.T) {
	// Good paraphrase shares key tokens; score should be high but <1.
	d := scoreTitle("Fix 19 golangci-lint issues across 10 files", "Fix lint issues in 10 files")
	if d.Score < 0.3 || d.Score > 0.8 {
		t.Errorf("paraphrase: got %.3f, want in [0.3, 0.8]", d.Score)
	}
}

func TestScoreTitle_Unrelated(t *testing.T) {
	d := scoreTitle("Fix 19 lint errors", "Deploy new payment API")
	if d.Score > 0.1 {
		t.Errorf("unrelated titles: got %.3f, want near 0", d.Score)
	}
}

func TestScoreTitle_EmptyBoth(t *testing.T) {
	d := scoreTitle("", "")
	if d.Score != 1.0 {
		t.Errorf("both empty: got %.3f, want 1.0", d.Score)
	}
}

func TestScoreTitle_OneEmpty(t *testing.T) {
	d := scoreTitle("Something real", "")
	if d.Score != 0 {
		t.Errorf("one empty: got %.3f, want 0", d.Score)
	}
}

func TestScoreOutcome_Exact(t *testing.T) {
	if s := scoreOutcome("success", "success").Score; s != 1.0 {
		t.Errorf("exact match: %f", s)
	}
}

func TestScoreOutcome_Mismatch(t *testing.T) {
	if s := scoreOutcome("success", "failed").Score; s != 0 {
		t.Errorf("mismatch: %f", s)
	}
}

func TestScoreOutcome_CaseInsensitive(t *testing.T) {
	if s := scoreOutcome("Success", "success").Score; s != 1.0 {
		t.Errorf("case insensitive: %f", s)
	}
}

func TestScoreKeyActions_Perfect(t *testing.T) {
	ref := []string{"Ran make lint", "Fixed 19 issues", "Opened PR #317"}
	d := scoreKeyActions(ref, ref)
	if d.Score != 1.0 {
		t.Errorf("identical actions: got %.3f, want 1.0", d.Score)
	}
}

func TestScoreKeyActions_Paraphrase(t *testing.T) {
	ref := []string{"Ran make lint", "Fixed 19 issues", "Opened PR #317"}
	cand := []string{"Ran lint", "Fixed lint issues", "Opened pull request 317"}
	d := scoreKeyActions(ref, cand)
	if d.Score < 0.5 {
		t.Errorf("paraphrase: got %.3f, want >=0.5", d.Score)
	}
}

func TestScoreKeyActions_Missing(t *testing.T) {
	ref := []string{"A", "B", "C", "D"}
	cand := []string{"A"}
	d := scoreKeyActions(ref, cand)
	if d.Score > 0.5 {
		t.Errorf("1-of-4 recall: got %.3f, want <=0.5", d.Score)
	}
}

func TestScoreAhaMoments_CountAndType(t *testing.T) {
	ref := []AhaMoment{{Seq: 1, Type: "question"}, {Seq: 3, Type: "decision"}}
	cand := []AhaMoment{{Seq: 2, Type: "question"}, {Seq: 4, Type: "decision"}}
	d := scoreAhaMoments(ref, cand)
	if d.Score < 0.8 {
		t.Errorf("same count + types: got %.3f, want >=0.8", d.Score)
	}
}

func TestScoreAhaMoments_MissedInsights(t *testing.T) {
	ref := []AhaMoment{{Seq: 1, Type: "question"}, {Seq: 3, Type: "decision"}}
	var cand []AhaMoment
	d := scoreAhaMoments(ref, cand)
	if d.Score != 0 {
		t.Errorf("missed all: got %.3f, want 0", d.Score)
	}
}

func TestScore_CompositeWeightedCorrectly(t *testing.T) {
	ref := Summary{
		Title:      "Fix lint errors",
		Summary:    "Fixed all the lint errors by adding nolint comments",
		KeyActions: []string{"Ran lint", "Added nolint", "Opened PR"},
		Outcome:    "success",
	}
	s := Score("test", ref, ref, DefaultWeights())
	if !floatEq(s.Overall, 1.0) {
		t.Errorf("identical ref/cand should score 1.0, got %.3f", s.Overall)
	}
}

func TestScoreCorpus_AggregatesAndAverages(t *testing.T) {
	corpus := []GoldenSession{
		{Name: "s1", Reference: Summary{Title: "a", Summary: "a", KeyActions: []string{"x"}, Outcome: "success"}},
		{Name: "s2", Reference: Summary{Title: "b", Summary: "b", KeyActions: []string{"y"}, Outcome: "failed"}},
	}
	candidates := map[string]Summary{
		"s1": corpus[0].Reference,                                                            // perfect
		"s2": {Title: "zzz", Summary: "zzz", KeyActions: []string{"zz"}, Outcome: "success"}, // mismatched
	}
	r := ScoreCorpus(corpus, candidates, DefaultWeights(), nil)
	if r.CorpusSize != 2 {
		t.Errorf("corpus size: %d", r.CorpusSize)
	}
	if r.OverallMax != 1.0 {
		t.Errorf("one session should score 1.0, got max=%.3f", r.OverallMax)
	}
	if r.OverallMin >= r.OverallMax {
		t.Errorf("expected min < max; got min=%.3f max=%.3f", r.OverallMin, r.OverallMax)
	}
	if r.OverallMean <= 0 || r.OverallMean >= 1 {
		t.Errorf("mean should be between 0 and 1, got %.3f", r.OverallMean)
	}
}

func TestScoreCorpus_MissingCandidateIsZero(t *testing.T) {
	corpus := []GoldenSession{
		{Name: "s1", Reference: Summary{Title: "a", Outcome: "success"}},
	}
	r := ScoreCorpus(corpus, map[string]Summary{}, DefaultWeights(), nil)
	if r.OverallMean != 0 {
		t.Errorf("missing candidate: mean should be 0, got %.3f", r.OverallMean)
	}
	if r.Sessions[0].Dimensions[0].Reason != "no candidate" {
		t.Errorf("expected no-candidate reason, got %q", r.Sessions[0].Dimensions[0].Reason)
	}
}

// TestScoreCorpus_AllZeroMinMaxNotInverted guards against a bug where
// initial OverallMin=1.0 / OverallMax=0.0 left min > max when every
// session scored zero (e.g., all candidates missing). Fix initializes
// min/max from the first observed score.
func TestScoreCorpus_AllZeroMinMaxNotInverted(t *testing.T) {
	corpus := []GoldenSession{
		{Name: "a", Reference: Summary{Title: "a", Outcome: "success"}},
		{Name: "b", Reference: Summary{Title: "b", Outcome: "success"}},
		{Name: "c", Reference: Summary{Title: "c", Outcome: "success"}},
	}
	// Empty candidates map → all scores 0.
	r := ScoreCorpus(corpus, map[string]Summary{}, DefaultWeights(), nil)
	if r.OverallMin != 0 {
		t.Errorf("OverallMin: got %f want 0", r.OverallMin)
	}
	if r.OverallMax != 0 {
		t.Errorf("OverallMax: got %f want 0", r.OverallMax)
	}
	if r.OverallMin > r.OverallMax {
		t.Errorf("min > max: got min=%f max=%f", r.OverallMin, r.OverallMax)
	}
}

// TestScoreCorpus_UnknownGateKeyFailsLoudly prevents a config typo from
// silently letting CI pass. If someone sets a MinDimensions entry under
// a misspelled dimension name, the gate must fail explicitly rather
// than be ignored.
func TestScoreCorpus_UnknownGateKeyFailsLoudly(t *testing.T) {
	corpus := []GoldenSession{
		{Name: "s1", Reference: Summary{Title: "perfect", Summary: "everything is fine and matches exactly", KeyActions: []string{"a", "b", "c"}, Outcome: "success"}},
	}
	candidates := map[string]Summary{
		"s1": corpus[0].Reference, // perfect match — no score would fail a legit gate
	}
	r := ScoreCorpus(corpus, candidates, DefaultWeights(), &Gates{
		MinDimensions: map[string]float64{
			"titel": 0.9, // typo: should be "title"
		},
	})
	if len(r.GatesFailed) == 0 {
		t.Fatal("typo in MinDimensions key should produce a gate failure")
	}
	foundUnknown := false
	for _, f := range r.GatesFailed {
		if strings.Contains(f, "unknown gate dimension") && strings.Contains(f, "titel") {
			foundUnknown = true
		}
	}
	if !foundUnknown {
		t.Errorf("expected gate failure naming the unknown key, got: %v", r.GatesFailed)
	}
}

func TestScoreCorpus_GatesFailAppropriately(t *testing.T) {
	corpus := []GoldenSession{
		{Name: "s1", Reference: Summary{Title: "a", Outcome: "success"}},
	}
	r := ScoreCorpus(corpus, map[string]Summary{}, DefaultWeights(), &Gates{MinOverall: 0.5})
	if len(r.GatesFailed) != 1 {
		t.Errorf("expected 1 gate failure, got %v", r.GatesFailed)
	}
}

func TestTokenSet_Basic(t *testing.T) {
	got := tokenSet("Hello, World! 123 test-case")
	want := []string{"hello", "world", "123", "test", "case"}
	if len(got) != len(want) {
		t.Errorf("token count: got %d want %d: %v", len(got), len(want), got)
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing token %q", w)
		}
	}
}

func TestJaccard_DisjointAndOverlap(t *testing.T) {
	a := map[string]bool{"a": true, "b": true, "c": true}
	b := map[string]bool{"b": true, "c": true, "d": true}
	if j := jaccard(a, b); !floatEq(j, 2.0/4.0) {
		t.Errorf("partial overlap: got %.3f want 0.5", j)
	}
	if j := jaccard(a, a); j != 1.0 {
		t.Errorf("identical: got %.3f want 1.0", j)
	}
	disjoint := map[string]bool{"z": true}
	if j := jaccard(a, disjoint); j != 0 {
		t.Errorf("disjoint: got %.3f want 0", j)
	}
}

func TestDefaultWeights_SumToOne(t *testing.T) {
	w := DefaultWeights()
	sum := w.Title + w.Summary + w.KeyActions + w.Outcome + w.AhaMoments
	if !floatEq(sum, 1.0) {
		t.Errorf("weights must sum to 1.0, got %.3f", sum)
	}
}
