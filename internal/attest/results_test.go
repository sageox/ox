package attest

import (
	"os"
	"path/filepath"
	"testing"
)

func writeRunArtifact(t *testing.T, root, runID, name, body string) {
	t.Helper()
	dir := filepath.Join(root, runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create run dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write run artifact: %v", err)
	}
}

func TestLoadRunResults_PrefersFinalizedOrchestratorResult(t *testing.T) {
	runsRoot := t.TempDir()
	writeRunArtifact(t, runsRoot, "run-canonical", "run.json", `{
  "schemaVersion": 1,
  "run": {"runId":"run-canonical","status":"complete","finalizeStatus":"passed","createdAt":"2026-08-13T00:00:00Z","endedAt":"2026-08-13T00:01:00Z","summary":{"scenarios":{"total":2,"failed":0}}}
}`)
	writeRunArtifact(t, runsRoot, "run-canonical", "run-report.json", `{"runId":"legacy-should-not-win","status":"failed"}`)

	results, err := LoadRunResults(t.TempDir(), runsRoot)
	if err != nil {
		t.Fatalf("LoadRunResults: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	got := results[0]
	if got.Source != "orchestrator" || got.FinalizeStatus != "passed" || got.ScenarioTotal != 2 {
		t.Errorf("result = %#v, want finalized orchestrator projection", got)
	}
}

func TestLoadRunResults_AdaptsLegacyStandaloneReport(t *testing.T) {
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

func TestLoadRunResults_RejectsIncompleteCanonicalArtifact(t *testing.T) {
	runsRoot := t.TempDir()
	writeRunArtifact(t, runsRoot, "run-incomplete", "run.json", `{"schemaVersion":1,"run":{"status":"complete"}}`)

	if _, err := LoadRunResults(t.TempDir(), runsRoot); err == nil {
		t.Fatal("LoadRunResults succeeded for incomplete run.json")
	}
}
