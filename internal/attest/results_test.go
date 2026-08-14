package attest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRunArtifact(t *testing.T, root, runID, name, body string) {
	t.Helper()
	dir := filepath.Join(root, runID)
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatalf("create run artifact dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write run artifact: %v", err)
	}
}

func writeCanonicalLifecycle(t *testing.T, root, runID, finalizeStatus string, sessionIDs []string) {
	t.Helper()
	payload := map[string]any{
		"schemaVersion": 1,
		"run": map[string]any{
			"runId": runID, "status": "finalized", "finalizeStatus": finalizeStatus,
			"createdAt": "2026-08-13T00:00:00Z", "endedAt": "2026-08-13T00:01:00Z",
			"sessionIds": sessionIDs,
			"summary":    map[string]any{"scenarios": map[string]any{"total": len(sessionIDs), "cleanlyEnded": len(sessionIDs), "failed": 0}},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	writeRunArtifact(t, root, runID, "run.json", string(raw))
}

func TestLoadRunResults_JoinsCanonicalLifecycleAndFailedBDDEvidence(t *testing.T) {
	runsRoot := t.TempDir()
	writeCanonicalLifecycle(t, runsRoot, "run-canonical", "failed", []string{"sid_pass", "sid_tina", "sid_marcus"})
	writeRunArtifact(t, runsRoot, "run-canonical", "report/results.json", `{
  "schemaVersion":1,
  "run":{"runId":"run-canonical","status":"finalized","finalizeStatus":"failed"},
  "scenarios":[
    {"sessionId":"sid_pass","feature":"features/auth.feature","scenarioName":"Member signs in","scenarioInstanceId":"scen_pass","status":"ended","outcome":"passed","relArtifactsDir":"scenarios/sid_pass","artifactsDir":"/private/machine/path","steps":[]},
    {"sessionId":"sid_tina","feature":"features/auth.feature","scenarioName":"Guest is denied","scenarioInstanceId":"scen_fail","status":"ended","outcome":"failed","relArtifactsDir":"scenarios/sid_tina","failureReason":"do not publish this diagnostic","steps":[{"path":"web/auth","status":500}]},
    {"sessionId":"sid_marcus","feature":"features/auth.feature","scenarioName":"Guest is denied","scenarioInstanceId":"scen_fail","status":"ended","outcome":"failed","relArtifactsDir":"scenarios/sid_marcus","steps":[]}
  ]
}`)
	writeRunArtifact(t, runsRoot, "run-canonical", "scenarios/sid_tina/run-report.json", `{
  "feature":"features/auth.feature",
  "featureName":"Authentication",
  "rule":"Only members can enter",
  "scenario":"Guest is denied",
  "sessionId":"sid_tina",
  "scenarioInstanceId":"scen_fail",
  "status":"fail",
  "steps":[{"stepText":"Then access is denied","status":"fail","envelope":{"secret":"never publish"},"artifactsRelativeDir":"screenshots/001"}],
  "artifactsDir":"/private/machine/path"
}`)
	// A root report is the standalone adapter and must never override the
	// orchestrator's terminal commit marker plus results.json.
	writeRunArtifact(t, runsRoot, "run-canonical", "run-report.json", `{"passed":true}`)

	results, err := LoadRunResults(t.TempDir(), runsRoot)
	if err != nil {
		t.Fatalf("LoadRunResults: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	got := results[0]
	if got.Source != "orchestrator" || got.FinalizeStatus != "failed" || got.ScenarioTotal != 2 || got.ScenarioFailed != 1 {
		t.Fatalf("result = %#v, want canonical BDD projection", got)
	}
	if len(got.FailedScenarios) != 1 {
		t.Fatalf("failed scenarios = %#v", got.FailedScenarios)
	}
	failure := got.FailedScenarios[0]
	if failure.ScenarioInstanceID != "scen_fail" || failure.Feature != "features/auth.feature" ||
		failure.Rule == nil || *failure.Rule != "Only members can enter" || failure.Scenario != "Guest is denied" ||
		failure.Outcome != "failed" || failure.SessionStatus != "completed" {
		t.Errorf("failed scenario = %#v", failure)
	}

	published, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"do not publish", "never publish", "screenshots", "/private/machine", "steps", "envelope"} {
		if strings.Contains(string(published), forbidden) {
			t.Errorf("normalized result leaked %q: %s", forbidden, published)
		}
	}
}

func TestLoadRunResults_AdaptsStandaloneRunReportV1(t *testing.T) {
	runsRoot := t.TempDir()
	writeRunArtifact(t, runsRoot, "run-standalone", "run-report.json", `{
  "version":1,"runId":"run-standalone","createdAt":"2026-08-13T00:00:00Z","durationMs":25,"finalizeStatus":"mixed",
  "scenarios":[
    {"scenarioInstanceId":"features/auth.feature::0","feature":"features/auth.feature","rule":"Members only","report":{"version":1,"scenario":"Guest is denied","outcome":"failed","sessionStatus":"completed","steps":[],"diagnostics":[{"message":"raw diagnostic must not publish"}]}},
    {"scenarioInstanceId":"features/auth.feature::1","feature":"features/auth.feature","rule":null,"report":{"version":1,"scenario":"Member signs in","outcome":"passed","sessionStatus":"completed","steps":[],"diagnostics":[]}}
  ]
}`)

	results, err := LoadRunResults(t.TempDir(), runsRoot)
	if err != nil {
		t.Fatalf("LoadRunResults: %v", err)
	}
	if len(results) != 1 || results[0].RunID != "run-standalone" || results[0].Status != "mixed" ||
		results[0].Source != "legacy-run-report" || results[0].ScenarioTotal != 2 || results[0].ScenarioFailed != 1 {
		t.Fatalf("results = %#v, want standalone projection", results)
	}
	if len(results[0].FailedScenarios) != 1 || results[0].FailedScenarios[0].Scenario != "Guest is denied" {
		t.Fatalf("failed scenarios = %#v", results[0].FailedScenarios)
	}
	raw, _ := json.Marshal(results[0])
	if strings.Contains(string(raw), "raw diagnostic") {
		t.Fatalf("standalone normalized result leaked diagnostics: %s", raw)
	}
}

func TestLoadRunResults_AdaptsEarlyStandaloneReportReadOnly(t *testing.T) {
	runsRoot := t.TempDir()
	writeRunArtifact(t, runsRoot, "run-legacy", "run-report.json", `{"passed":false}`)

	results, err := LoadRunResults(t.TempDir(), runsRoot)
	if err != nil {
		t.Fatalf("LoadRunResults: %v", err)
	}
	if len(results) != 1 || results[0].RunID != "run-legacy" || results[0].Status != "failed" || results[0].Source != "legacy-run-report" {
		t.Errorf("results = %#v, want legacy failed projection", results)
	}
}

func TestLoadRunResults_RejectsIncompleteCanonicalArtifacts(t *testing.T) {
	tests := map[string]func(*testing.T, string){
		"running lifecycle": func(t *testing.T, root string) {
			writeRunArtifact(t, root, "run-bad", "run.json", `{"schemaVersion":1,"run":{"runId":"run-bad","status":"running"}}`)
		},
		"missing results": func(t *testing.T, root string) {
			writeCanonicalLifecycle(t, root, "run-bad", "failed", []string{"sid_one"})
		},
		"malformed results": func(t *testing.T, root string) {
			writeCanonicalLifecycle(t, root, "run-bad", "failed", []string{"sid_one"})
			writeRunArtifact(t, root, "run-bad", "report/results.json", `{not json`)
		},
		"mismatched results": func(t *testing.T, root string) {
			writeCanonicalLifecycle(t, root, "run-bad", "failed", []string{})
			writeRunArtifact(t, root, "run-bad", "report/results.json", `{"schemaVersion":1,"run":{"runId":"run-other","status":"finalized","finalizeStatus":"failed"},"scenarios":[]}`)
		},
		"failed scenario missing evidence": func(t *testing.T, root string) {
			writeCanonicalLifecycle(t, root, "run-bad", "failed", []string{"sid_one"})
			writeRunArtifact(t, root, "run-bad", "report/results.json", `{"schemaVersion":1,"run":{"runId":"run-bad","status":"finalized","finalizeStatus":"failed"},"scenarios":[{"sessionId":"sid_one","feature":"features/auth.feature","scenarioName":"Guest is denied","scenarioInstanceId":"scen_one","status":"ended","outcome":"failed","relArtifactsDir":"scenarios/sid_one"}]}`)
		},
		"failed scenario malformed evidence": func(t *testing.T, root string) {
			writeCanonicalLifecycle(t, root, "run-bad", "failed", []string{"sid_one"})
			writeRunArtifact(t, root, "run-bad", "report/results.json", `{"schemaVersion":1,"run":{"runId":"run-bad","status":"finalized","finalizeStatus":"failed"},"scenarios":[{"sessionId":"sid_one","feature":"features/auth.feature","scenarioName":"Guest is denied","scenarioInstanceId":"scen_one","status":"ended","outcome":"failed","relArtifactsDir":"scenarios/sid_one"}]}`)
			writeRunArtifact(t, root, "run-bad", "scenarios/sid_one/run-report.json", `{not json`)
		},
		"failed scenario path traversal": func(t *testing.T, root string) {
			writeCanonicalLifecycle(t, root, "run-bad", "failed", []string{"sid_one"})
			writeRunArtifact(t, root, "run-bad", "report/results.json", `{"schemaVersion":1,"run":{"runId":"run-bad","status":"finalized","finalizeStatus":"failed"},"scenarios":[{"sessionId":"sid_one","feature":"features/auth.feature","scenarioName":"Guest is denied","scenarioInstanceId":"scen_one","status":"ended","outcome":"failed","relArtifactsDir":"../../outside"}]}`)
		},
	}
	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			runsRoot := t.TempDir()
			setup(t, runsRoot)
			if _, err := LoadRunResults(t.TempDir(), runsRoot); err == nil {
				t.Fatal("LoadRunResults accepted incomplete canonical artifacts")
			}
		})
	}
}

func TestReferencedRunResults_FiltersBeforeParsingUnrelatedRuns(t *testing.T) {
	repoRoot := t.TempDir()
	runsRoot := filepath.Join(repoRoot, "tests", "bdd", "runs")
	writeCanonicalLifecycle(t, runsRoot, "run_red", "failed", []string{"sid_red"})
	writeRunArtifact(t, runsRoot, "run_red", "report/results.json", `{"schemaVersion":1,"run":{"runId":"run_red","status":"finalized","finalizeStatus":"failed"},"scenarios":[{"sessionId":"sid_red","feature":"features/auth.feature","scenarioName":"Guest is denied","scenarioInstanceId":"scen_red","status":"ended","outcome":"failed","relArtifactsDir":"scenarios/sid_red"}]}`)
	writeRunArtifact(t, runsRoot, "run_red", "scenarios/sid_red/run-report.json", `{"feature":"features/auth.feature","rule":"Members only","scenario":"Guest is denied","sessionId":"sid_red","scenarioInstanceId":"scen_red","status":"fail","steps":[]}`)
	writeRunArtifact(t, runsRoot, "run_green", "run-report.json", `{"runId":"run_green","passed":true}`)
	writeRunArtifact(t, runsRoot, "unrelated-corrupt", "run.json", `{not json`)

	rec := validRecord()
	records := &Records{byCapability: map[string]*Attestation{rec.CapabilityID: rec}, Count: 1}
	results, err := ReferencedRunResults(repoRoot, records)
	if err != nil {
		t.Fatalf("ReferencedRunResults parsed unrelated corrupt run: %v", err)
	}
	if len(results) != 2 || results[0].RunID != "run_green" || results[1].RunID != "run_red" {
		t.Fatalf("results = %#v, want only the sorted referenced red/green runs", results)
	}
	if len(results[1].FailedScenarios) != 1 || results[1].FailedScenarios[0].Rule == nil {
		t.Fatalf("red result did not retain failed BDD identity: %#v", results[1])
	}
}

func TestReferencedRunResults_StillRejectsMalformedReferencedRun(t *testing.T) {
	repoRoot := t.TempDir()
	runsRoot := filepath.Join(repoRoot, "tests", "bdd", "runs")
	writeRunArtifact(t, runsRoot, "run_red", "run.json", `{not json`)

	rec := validRecord()
	records := &Records{byCapability: map[string]*Attestation{rec.CapabilityID: rec}, Count: 1}
	if _, err := ReferencedRunResults(repoRoot, records); err == nil {
		t.Fatal("ReferencedRunResults accepted malformed referenced run")
	}
}
