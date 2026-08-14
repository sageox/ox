package attest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// RunResult is the deliberately small, publishable projection of a BDD run.
// Full reports can contain diagnostics and screenshots; the Attest bundle only
// needs enough context to connect a proof's red/green run ids to their outcome.
type RunResult struct {
	RunID          string `json:"runId"`
	Source         string `json:"source"`
	Status         string `json:"status"`
	FinalizeStatus string `json:"finalizeStatus,omitempty"`
	CreatedAt      string `json:"createdAt,omitempty"`
	EndedAt        string `json:"endedAt,omitempty"`
	ScenarioTotal  int    `json:"scenarioTotal,omitempty"`
	ScenarioFailed int    `json:"scenarioFailed,omitempty"`
}

type runJSON struct {
	SchemaVersion int `json:"schemaVersion"`
	Run           struct {
		RunID          string `json:"runId"`
		Status         string `json:"status"`
		FinalizeStatus string `json:"finalizeStatus"`
		CreatedAt      string `json:"createdAt"`
		EndedAt        string `json:"endedAt"`
		Summary        struct {
			Scenarios struct {
				Total  int `json:"total"`
				Failed int `json:"failed"`
			} `json:"scenarios"`
		} `json:"summary"`
	} `json:"run"`
}

type legacyRunReport struct {
	RunID    string `json:"runId"`
	Status   string `json:"status"`
	Scenario string `json:"scenario"`
	Passed   *bool  `json:"passed"`
}

// LoadRunResults reads finalized orchestrator run.json files and, for older
// standalone runners, a run-report.json. The finalized run.json is canonical:
// it carries the lifecycle outcome, while run-report is evidence for one
// scenario and cannot safely stand in for a multi-scenario run.
func LoadRunResults(repoRoot, runsRoot string) ([]RunResult, error) {
	if runsRoot == "" {
		runsRoot = filepath.Join(repoRoot, "tests", "bdd", "runs")
	} else if !filepath.IsAbs(runsRoot) {
		runsRoot = filepath.Join(repoRoot, runsRoot)
	}

	entries, err := os.ReadDir(runsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read attest runs: %w", err)
	}

	results := make([]RunResult, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(runsRoot, entry.Name())
		result, ok, readErr := readRunResult(dir, entry.Name())
		if readErr != nil {
			return nil, readErr
		}
		if ok {
			results = append(results, result)
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].RunID < results[j].RunID })
	return results, nil
}

func readRunResult(dir, fallbackID string) (RunResult, bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "run.json"))
	if err == nil {
		var parsed runJSON
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return RunResult{}, false, fmt.Errorf("parse %s: %w", filepath.Join(dir, "run.json"), err)
		}
		if parsed.SchemaVersion != 1 || parsed.Run.RunID == "" {
			return RunResult{}, false, fmt.Errorf("parse %s: unsupported or incomplete run.json", filepath.Join(dir, "run.json"))
		}
		return RunResult{
			RunID: parsed.Run.RunID, Source: "orchestrator", Status: parsed.Run.Status,
			FinalizeStatus: parsed.Run.FinalizeStatus, CreatedAt: parsed.Run.CreatedAt, EndedAt: parsed.Run.EndedAt,
			ScenarioTotal: parsed.Run.Summary.Scenarios.Total, ScenarioFailed: parsed.Run.Summary.Scenarios.Failed,
		}, true, nil
	}
	if !os.IsNotExist(err) {
		return RunResult{}, false, fmt.Errorf("read %s: %w", filepath.Join(dir, "run.json"), err)
	}

	raw, err = os.ReadFile(filepath.Join(dir, "run-report.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return RunResult{}, false, nil
		}
		return RunResult{}, false, fmt.Errorf("read %s: %w", filepath.Join(dir, "run-report.json"), err)
	}
	var legacy legacyRunReport
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return RunResult{}, false, fmt.Errorf("parse %s: %w", filepath.Join(dir, "run-report.json"), err)
	}
	if legacy.RunID == "" {
		legacy.RunID = fallbackID
	}
	status := legacy.Status
	if status == "" && legacy.Passed != nil {
		if *legacy.Passed {
			status = "passed"
		} else {
			status = "failed"
		}
	}
	return RunResult{RunID: legacy.RunID, Source: "legacy-run-report", Status: status}, true, nil
}

// ReferencedRunResults filters the local run corpus to durable proof links.
// A missing referenced run is intentionally omitted rather than synthesized:
// the UI must say evidence is unavailable, not quietly invent a result.
func ReferencedRunResults(repoRoot string, records *Records) ([]RunResult, error) {
	all, err := LoadRunResults(repoRoot, "")
	if err != nil {
		return nil, err
	}
	needed := make(map[string]struct{})
	for _, record := range records.byCapability {
		for _, id := range []string{record.Proof.RedRunID, record.Proof.GreenRunID} {
			if id != "" {
				needed[id] = struct{}{}
			}
		}
	}
	filtered := make([]RunResult, 0, len(needed))
	for _, result := range all {
		if _, ok := needed[result.RunID]; ok {
			filtered = append(filtered, result)
		}
	}
	return filtered, nil
}
