package attest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writePlan drops a compiled plan next to a corpus so the verdict path can be
// exercised end to end through LoadPlans rather than by poking internals.
func writePlan(t *testing.T, corpusRoot string, plan CompiledPlan) {
	t.Helper()
	dir := filepath.Join(corpusRoot, compiledSubdir, "d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	name := filepath.Base(plan.Feature)
	name = name[:len(name)-len(".feature")] + ".plan.json"
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
		t.Fatalf("write plan: %v", err)
	}
}

const ladderFeature = `Feature: Ladder

  Rule: Dispatches and is stamped
    @validated
    # validated: 2026-08-12 · Tilt · run_aaaa1111-2222
    Scenario: Stamped one
      Given a thing

  Rule: Dispatches but was never stamped
    Scenario: Unstamped one
      Given a thing

  Rule: Exists but is switched off
    @pending
    Scenario: Excluded one
      Given a thing
`

func TestAssess_LadderRungs(t *testing.T) {
	root := writeCorpus(t, map[string]string{"d/ladder.feature": ladderFeature})
	writePlan(t, root, CompiledPlan{
		SchemaVersion: 1,
		Feature:       "features/d/ladder.feature",
		Scenarios: []PlanScenario{
			{Name: "Stamped one", Index: 0, Rule: "Dispatches and is stamped"},
			{Name: "Unstamped one", Index: 1, Rule: "Dispatches but was never stamped"},
		},
		Excluded: []PlanExcluded{{Name: "Excluded one", Tags: []string{"@pending"}, Reason: "tag"}},
	})

	corpus, err := ScanCorpus(root, root)
	if err != nil {
		t.Fatalf("ScanCorpus: %v", err)
	}
	plans, err := LoadPlans(root)
	if err != nil {
		t.Fatalf("LoadPlans: %v", err)
	}
	if plans.Count != 1 {
		t.Fatalf("plans loaded = %d, want 1", plans.Count)
	}

	want := map[string]Verdict{
		"d/ladder#dispatches-and-is-stamped":        VerdictStamped,
		"d/ladder#dispatches-but-was-never-stamped": VerdictUnproven,
		"d/ladder#exists-but-is-switched-off":       VerdictSkipped,
	}
	for _, cap := range corpus.Capabilities {
		a := Assess(cap, plans, &Records{})
		if got := a.Verdict; got != want[cap.ID] {
			t.Errorf("%s verdict = %q, want %q", cap.ID, got, want[cap.ID])
		}
		if a.NoPlan {
			t.Errorf("%s reported NoPlan although a compiled plan exists", cap.ID)
		}
	}
}

// A feature with NO compiled plan cannot dispatch, whatever its tags say. That
// is a materially different cause of `skipped` than "tagged off", and the two
// must stay distinguishable so a reader knows which fix applies.
func TestAssess_NoCompiledPlanIsSkippedAndSaysSo(t *testing.T) {
	root := writeCorpus(t, map[string]string{"d/ladder.feature": ladderFeature})
	corpus, err := ScanCorpus(root, root)
	if err != nil {
		t.Fatalf("ScanCorpus: %v", err)
	}
	plans, err := LoadPlans(root) // no compiled/ directory at all
	if err != nil {
		t.Fatalf("LoadPlans must tolerate a missing compiled dir: %v", err)
	}

	for _, cap := range corpus.Capabilities {
		a := Assess(cap, plans, &Records{})
		if a.Verdict != VerdictSkipped {
			t.Errorf("%s verdict = %q, want %q with no compiled plan", cap.ID, a.Verdict, VerdictSkipped)
		}
		if !a.NoPlan {
			t.Errorf("%s must report NoPlan so the cause is legible", cap.ID)
		}
		if a.Dispatching != 0 {
			t.Errorf("%s dispatching = %d, want 0", cap.ID, a.Dispatching)
		}
	}
}

func TestAssess_CapabilityWithNoScenariosIsUntested(t *testing.T) {
	root := writeCorpus(t, map[string]string{
		"d/empty.feature": "Feature: Empty\n\n  Rule: Nobody wrote a scenario for this\n",
	})
	corpus, err := ScanCorpus(root, root)
	if err != nil {
		t.Fatalf("ScanCorpus: %v", err)
	}
	if len(corpus.Capabilities) != 1 {
		t.Fatalf("capabilities = %d, want 1", len(corpus.Capabilities))
	}
	if a := Assess(corpus.Capabilities[0], &Plans{}, &Records{}); a.Verdict != VerdictUntested {
		t.Errorf("verdict = %q, want %q", a.Verdict, VerdictUntested)
	}
}

func TestBuildReport_TotalsAndWeakestOrdering(t *testing.T) {
	root := writeCorpus(t, map[string]string{"d/ladder.feature": ladderFeature})
	writePlan(t, root, CompiledPlan{
		SchemaVersion: 1,
		Feature:       "features/d/ladder.feature",
		Scenarios: []PlanScenario{
			{Name: "Stamped one", Index: 0, Rule: "Dispatches and is stamped"},
			{Name: "Unstamped one", Index: 1, Rule: "Dispatches but was never stamped"},
		},
	})
	corpus, _ := ScanCorpus(root, root)
	plans, _ := LoadPlans(root)
	r := BuildReport(corpus, plans, &Records{})

	if r.Capabilities != 3 {
		t.Errorf("capabilities = %d, want 3", r.Capabilities)
	}
	if r.Scenarios.Authored != 3 {
		t.Errorf("authored = %d, want 3", r.Scenarios.Authored)
	}
	if r.Scenarios.Dispatching != 2 {
		t.Errorf("dispatching = %d, want 2", r.Scenarios.Dispatching)
	}
	if r.Scenarios.Stamped != 1 {
		t.Errorf("stamped = %d, want 1", r.Scenarios.Stamped)
	}
	if r.Counts[VerdictAttested] != 0 {
		t.Errorf("attested = %d, want 0 — unreachable until records exist", r.Counts[VerdictAttested])
	}

	// Worst first: the answer to "where does the next hour go?".
	weakest := r.Weakest(3)
	if len(weakest) != 3 {
		t.Fatalf("weakest = %d, want 3", len(weakest))
	}
	if weakest[0].Verdict != VerdictSkipped {
		t.Errorf("weakest[0] = %q, want the worst rung %q", weakest[0].Verdict, VerdictSkipped)
	}
	if weakest[len(weakest)-1].Verdict != VerdictStamped {
		t.Errorf("weakest[last] = %q, want the strongest present rung %q",
			weakest[len(weakest)-1].Verdict, VerdictStamped)
	}
}

func TestAssess_DuplicateScenarioNamesUseCompilerIdentity(t *testing.T) {
	root := writeCorpus(t, map[string]string{
		"d/duplicates.feature": `Feature: Duplicates

  Rule: First capability
    Scenario: Same display name
      Given the first thing

  Rule: Second capability
    Scenario: Same display name
      Given the second thing
`,
	})
	writePlan(t, root, CompiledPlan{
		SchemaVersion: 1,
		Feature:       "features/d/duplicates.feature",
		Scenarios: []PlanScenario{
			{Name: "Same display name", Index: 1, Rule: "Second capability"},
		},
		Excluded: []PlanExcluded{
			{Name: "Same display name", Index: 0, Reason: "tag"},
		},
	})

	corpus, err := ScanCorpus(root, root)
	if err != nil {
		t.Fatalf("ScanCorpus: %v", err)
	}
	plans, err := LoadPlans(root)
	if err != nil {
		t.Fatalf("LoadPlans: %v", err)
	}

	first := Assess(corpus.Capabilities[0], plans, &Records{})
	second := Assess(corpus.Capabilities[1], plans, &Records{})
	if first.Dispatching != 0 || first.Verdict != VerdictSkipped {
		t.Errorf("excluded duplicate assessment = %+v, want skipped", first)
	}
	if second.Dispatching != 1 || second.Verdict != VerdictUnproven {
		t.Errorf("selected duplicate assessment = %+v, want unproven", second)
	}
}

func TestReportApplyFreshness_StaleAndUnknownProofsAreNotAttested(t *testing.T) {
	tests := []struct {
		name      string
		freshness Freshness
	}{
		{name: "stale", freshness: Freshness{Reachable: true, SpecStale: true}},
		{name: "unknown", freshness: Freshness{Unknown: true, Reason: "current fingerprint unavailable"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := writeCorpus(t, map[string]string{
				"d/proof.feature": `Feature: Proof

  Rule: A clean proof exists
    Scenario: It works
      Given a thing
`,
			})
			writePlan(t, root, CompiledPlan{
				SchemaVersion: 1,
				Feature:       "features/d/proof.feature",
				Scenarios: []PlanScenario{
					{Name: "It works", Index: 0, Rule: "A clean proof exists"},
				},
			})
			corpus, _ := ScanCorpus(root, root)
			plans, _ := LoadPlans(root)
			record := validRecord()
			record.CapabilityID = corpus.Capabilities[0].ID
			records := &Records{
				byCapability: map[string]*Attestation{record.CapabilityID: record},
				Count:        1,
			}

			report := BuildReport(corpus, plans, records)
			if report.Counts[VerdictAttested] != 1 {
				t.Fatalf("precondition: attested count = %d, want 1", report.Counts[VerdictAttested])
			}
			report.ApplyFreshness(func(Capability, *Attestation) Freshness { return tc.freshness })

			assessment := report.Assessments[0]
			if assessment.Verdict != VerdictStale {
				t.Errorf("verdict = %q, want %q", assessment.Verdict, VerdictStale)
			}
			if assessment.Freshness == nil || assessment.Freshness.Current {
				t.Errorf("Freshness = %+v, want attached non-current verdict", assessment.Freshness)
			}
			if report.Counts[VerdictAttested] != 0 || report.Counts[VerdictStale] != 1 {
				t.Errorf("counts = %v, want stale=1 attested=0", report.Counts)
			}
			if got := report.ByDomain["d"][0].Verdict; got != VerdictStale {
				t.Errorf("ByDomain verdict = %q, want %q", got, VerdictStale)
			}
		})
	}
}

// A plan that cannot name its feature cannot be joined to a capability.
// Skipping it silently would inflate "skipped"; failing loudly is the honest
// behavior.
func TestLoadPlans_PlanWithoutFeaturePathIsAnError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, compiledSubdir, "d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.plan.json"), []byte(`{"schemaVersion":1}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadPlans(root); err == nil {
		t.Fatal("expected an error for a plan with no feature path, got nil")
	}
}
