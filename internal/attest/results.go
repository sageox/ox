package attest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FailedScenario is the diagnostics-free identity of one failed BDD scenario.
// The raw run report remains local because it can reference screenshots,
// machine paths, response envelopes, and other high-volume diagnostics.
type FailedScenario struct {
	ScenarioInstanceID string  `json:"scenarioInstanceId"`
	Feature            string  `json:"feature"`
	Rule               *string `json:"rule"`
	Scenario           string  `json:"scenario"`
	Outcome            string  `json:"outcome"`
	SessionStatus      string  `json:"sessionStatus"`
}

// RunResult is the deliberately small, publishable projection of a BDD run.
// Full reports can contain diagnostics and screenshots; the Attest bundle only
// carries run lifecycle, aggregate counts, and failed BDD identities.
type RunResult struct {
	RunID           string           `json:"runId"`
	Source          string           `json:"source"`
	Status          string           `json:"status"`
	FinalizeStatus  string           `json:"finalizeStatus,omitempty"`
	CreatedAt       string           `json:"createdAt,omitempty"`
	EndedAt         string           `json:"endedAt,omitempty"`
	ScenarioTotal   int              `json:"scenarioTotal,omitempty"`
	ScenarioFailed  int              `json:"scenarioFailed,omitempty"`
	FailedScenarios []FailedScenario `json:"failedScenarios,omitempty"`
}

type runJSON struct {
	SchemaVersion int `json:"schemaVersion"`
	Run           struct {
		RunID          string   `json:"runId"`
		Status         string   `json:"status"`
		FinalizeStatus string   `json:"finalizeStatus"`
		CreatedAt      string   `json:"createdAt"`
		EndedAt        string   `json:"endedAt"`
		SessionIDs     []string `json:"sessionIds"`
		Summary        struct {
			Scenarios struct {
				Total  int `json:"total"`
				Failed int `json:"failed"`
			} `json:"scenarios"`
		} `json:"summary"`
	} `json:"run"`
}

type orchestratorResultsJSON struct {
	SchemaVersion int `json:"schemaVersion"`
	Run           struct {
		RunID          string `json:"runId"`
		Status         string `json:"status"`
		FinalizeStatus string `json:"finalizeStatus"`
	} `json:"run"`
	Scenarios []orchestratorScenarioResult `json:"scenarios"`
}

type orchestratorScenarioResult struct {
	SessionID          string `json:"sessionId"`
	Feature            string `json:"feature"`
	ScenarioName       string `json:"scenarioName"`
	ScenarioInstanceID string `json:"scenarioInstanceId"`
	Status             string `json:"status"`
	Outcome            string `json:"outcome"`
	RelArtifactsDir    string `json:"relArtifactsDir"`
}

type scenarioRunReport struct {
	Feature            string `json:"feature"`
	Rule               string `json:"rule"`
	Scenario           string `json:"scenario"`
	SessionID          string `json:"sessionId"`
	Status             string `json:"status"`
	ScenarioInstanceID string `json:"scenarioInstanceId"`
}

type standaloneRunReport struct {
	Version        int    `json:"version"`
	RunID          string `json:"runId"`
	CreatedAt      string `json:"createdAt"`
	FinalizeStatus string `json:"finalizeStatus"`
	Scenarios      []struct {
		ScenarioInstanceID string  `json:"scenarioInstanceId"`
		Feature            string  `json:"feature"`
		Rule               *string `json:"rule"`
		Report             struct {
			Scenario      string `json:"scenario"`
			Outcome       string `json:"outcome"`
			SessionStatus string `json:"sessionStatus"`
		} `json:"report"`
	} `json:"scenarios"`
	// Early standalone writers emitted only these fields. They remain readable,
	// but cannot provide a BDD identity and therefore stay a shallow summary.
	Status string `json:"status"`
	Passed *bool  `json:"passed"`
}

// LoadRunResults reads finalized orchestrator run.json + report/results.json
// artifacts and joins failed rows to their scenario run-report.json evidence.
// A root run-report.json remains a read-only adapter for standalone runners.
func LoadRunResults(repoRoot, runsRoot string) ([]RunResult, error) {
	return loadRunResults(repoRoot, runsRoot, nil)
}

// loadRunResults applies the optional directory-name filter before opening or
// parsing any artifact. Run directories are canonically named by run id; this
// lets publication ignore an unrelated corrupt run instead of failing before
// it reaches the proof links it actually needs.
func loadRunResults(repoRoot, runsRoot string, needed map[string]struct{}) ([]RunResult, error) {
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
		if needed != nil {
			if _, ok := needed[entry.Name()]; !ok {
				continue
			}
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
	runPath := filepath.Join(dir, "run.json")
	raw, err := os.ReadFile(runPath)
	if err == nil {
		var parsed runJSON
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return RunResult{}, false, fmt.Errorf("parse %s: %w", runPath, err)
		}
		if parsed.SchemaVersion != 1 || strings.TrimSpace(parsed.Run.RunID) == "" || parsed.Run.RunID != fallbackID {
			return RunResult{}, false, fmt.Errorf("parse %s: unsupported or incomplete run.json", runPath)
		}
		if parsed.Run.Status != "finalized" && parsed.Run.Status != "aborted" {
			return RunResult{}, false, fmt.Errorf("parse %s: run is not terminal", runPath)
		}
		if !validFinalizeStatus(parsed.Run.FinalizeStatus) || !validRFC3339(parsed.Run.CreatedAt) ||
			!validRFC3339(parsed.Run.EndedAt) {
			return RunResult{}, false, fmt.Errorf("parse %s: terminal run is incomplete", runPath)
		}
		if (parsed.Run.Status == "aborted") != (parsed.Run.FinalizeStatus == "aborted") {
			return RunResult{}, false, fmt.Errorf("parse %s: terminal status and disposition conflict", runPath)
		}
		result := RunResult{
			RunID: parsed.Run.RunID, Source: "orchestrator", Status: parsed.Run.Status,
			FinalizeStatus: parsed.Run.FinalizeStatus, CreatedAt: parsed.Run.CreatedAt, EndedAt: parsed.Run.EndedAt,
		}
		if parsed.Run.Status == "aborted" {
			return result, true, nil
		}
		if err := addOrchestratorScenarioProjection(dir, parsed, &result); err != nil {
			return RunResult{}, false, err
		}
		return result, true, nil
	}
	if !os.IsNotExist(err) {
		return RunResult{}, false, fmt.Errorf("read %s: %w", runPath, err)
	}

	return readStandaloneRunReport(dir, fallbackID)
}

func addOrchestratorScenarioProjection(dir string, lifecycle runJSON, result *RunResult) error {
	resultsPath := filepath.Join(dir, "report", "results.json")
	raw, err := os.ReadFile(resultsPath)
	if err != nil {
		return fmt.Errorf("read %s: finalized run is incomplete: %w", resultsPath, err)
	}
	var parsed orchestratorResultsJSON
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("parse %s: %w", resultsPath, err)
	}
	if parsed.SchemaVersion != 1 || parsed.Run.RunID != lifecycle.Run.RunID || parsed.Run.Status != "finalized" ||
		parsed.Run.FinalizeStatus != lifecycle.Run.FinalizeStatus {
		return fmt.Errorf("parse %s: results do not match finalized run.json", resultsPath)
	}
	if len(parsed.Scenarios) != len(lifecycle.Run.SessionIDs) ||
		(lifecycle.Run.Summary.Scenarios.Total != 0 && len(parsed.Scenarios) != lifecycle.Run.Summary.Scenarios.Total) {
		return fmt.Errorf("parse %s: scenario rows do not match run.json", resultsPath)
	}
	expectedSessions := make(map[string]struct{}, len(lifecycle.Run.SessionIDs))
	for _, sessionID := range lifecycle.Run.SessionIDs {
		if sessionID == "" {
			return fmt.Errorf("parse %s: run.json contains an empty session id", resultsPath)
		}
		expectedSessions[sessionID] = struct{}{}
	}

	type scenarioGroup struct {
		id   string
		rows []orchestratorScenarioResult
	}
	groups := make([]scenarioGroup, 0, len(parsed.Scenarios))
	groupIndex := make(map[string]int, len(parsed.Scenarios))
	for _, scenario := range parsed.Scenarios {
		if strings.TrimSpace(scenario.SessionID) == "" || !validScenarioStatus(scenario.Status) ||
			!validScenarioOutcome(scenario.Outcome) {
			return fmt.Errorf("parse %s: scenario row is incomplete", resultsPath)
		}
		if _, ok := expectedSessions[scenario.SessionID]; !ok {
			return fmt.Errorf("parse %s: scenario sessionId is not declared by run.json", resultsPath)
		}
		delete(expectedSessions, scenario.SessionID)
		if scenario.RelArtifactsDir != filepath.ToSlash(filepath.Join("scenarios", scenario.SessionID)) {
			return fmt.Errorf("parse %s: scenario artifacts path does not match sessionId", resultsPath)
		}
		groupID := scenario.ScenarioInstanceID
		if groupID == "" {
			groupID = scenario.SessionID
		}
		idx, found := groupIndex[groupID]
		if !found {
			idx = len(groups)
			groupIndex[groupID] = idx
			groups = append(groups, scenarioGroup{id: groupID})
		}
		groups[idx].rows = append(groups[idx].rows, scenario)
	}

	failed := make([]FailedScenario, 0)
	for _, group := range groups {
		businessFailed := false
		for _, row := range group.rows {
			businessFailed = businessFailed || row.Outcome == "failed"
		}
		if !businessFailed {
			continue
		}
		report, row, err := readGroupRunReport(dir, group.rows)
		if err != nil {
			return fmt.Errorf("read failed scenario %s: %w", group.id, err)
		}
		instanceID := group.id
		if report.ScenarioInstanceID != "" {
			instanceID = report.ScenarioInstanceID
		}
		feature := report.Feature
		if feature == "" {
			feature = row.Feature
		}
		scenarioName := report.Scenario
		if scenarioName == "" {
			scenarioName = row.ScenarioName
		}
		if strings.TrimSpace(feature) == "" || strings.TrimSpace(scenarioName) == "" {
			return fmt.Errorf("scenario evidence is missing feature or scenario identity")
		}
		var rule *string
		if report.Rule != "" {
			value := report.Rule
			rule = &value
		}
		// The monorepo orchestrator calls clean teardown "ended"; the portable
		// @sageox/attest summary contract calls the same lifecycle "completed".
		sessionStatus := "completed"
		for _, candidate := range group.rows {
			if candidate.Status == "failed" {
				sessionStatus = "failed"
				break
			}
		}
		failed = append(failed, FailedScenario{
			ScenarioInstanceID: instanceID, Feature: feature, Rule: rule,
			Scenario: scenarioName, Outcome: "failed", SessionStatus: sessionStatus,
		})
	}
	sortFailedScenarios(failed)
	result.ScenarioTotal = len(groups)
	result.ScenarioFailed = len(failed)
	result.FailedScenarios = failed
	return nil
}

func readGroupRunReport(runDir string, rows []orchestratorScenarioResult) (scenarioRunReport, orchestratorScenarioResult, error) {
	for _, row := range rows {
		if row.RelArtifactsDir == "" {
			continue
		}
		reportPath, err := runRelativePath(runDir, row.RelArtifactsDir, "run-report.json")
		if err != nil {
			return scenarioRunReport{}, orchestratorScenarioResult{}, err
		}
		raw, err := os.ReadFile(reportPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return scenarioRunReport{}, orchestratorScenarioResult{}, fmt.Errorf("read %s: %w", reportPath, err)
		}
		var report scenarioRunReport
		if err := json.Unmarshal(raw, &report); err != nil {
			return scenarioRunReport{}, orchestratorScenarioResult{}, fmt.Errorf("parse %s: %w", reportPath, err)
		}
		if !validRunReportStatus(report.Status) {
			return scenarioRunReport{}, orchestratorScenarioResult{}, fmt.Errorf("parse %s: invalid scenario status", reportPath)
		}
		if report.SessionID != "" && report.SessionID != row.SessionID {
			return scenarioRunReport{}, orchestratorScenarioResult{}, fmt.Errorf("parse %s: sessionId does not match results.json", reportPath)
		}
		if report.ScenarioInstanceID != "" && row.ScenarioInstanceID != "" &&
			report.ScenarioInstanceID != row.ScenarioInstanceID {
			return scenarioRunReport{}, orchestratorScenarioResult{}, fmt.Errorf("parse %s: scenarioInstanceId does not match results.json", reportPath)
		}
		if report.Feature != "" && row.Feature != "" && report.Feature != row.Feature {
			return scenarioRunReport{}, orchestratorScenarioResult{}, fmt.Errorf("parse %s: feature does not match results.json", reportPath)
		}
		if report.Scenario != "" && row.ScenarioName != "" && report.Scenario != row.ScenarioName {
			return scenarioRunReport{}, orchestratorScenarioResult{}, fmt.Errorf("parse %s: scenario does not match results.json", reportPath)
		}
		return report, row, nil
	}
	return scenarioRunReport{}, orchestratorScenarioResult{}, errors.New("run-report.json is missing")
}

func runRelativePath(root string, segments ...string) (string, error) {
	for _, segment := range segments {
		// The orchestrator contract stores POSIX run-relative paths. Rejecting
		// both native absolute paths and backslashes keeps this check portable.
		if filepath.IsAbs(segment) || strings.Contains(segment, `\`) {
			return "", errors.New("scenario artifacts path escapes the run directory")
		}
	}
	parts := append([]string{root}, segments...)
	joined := filepath.Clean(filepath.Join(parts...))
	rel, err := filepath.Rel(root, joined)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("scenario artifacts path escapes the run directory")
	}
	return joined, nil
}

func validScenarioStatus(status string) bool {
	return status == "ended" || status == "failed"
}

func validScenarioOutcome(outcome string) bool {
	return outcome == "passed" || outcome == "failed" || outcome == "skipped" || outcome == "unknown"
}

func validRunReportStatus(status string) bool {
	return status == "pass" || status == "fail" || status == "blocked"
}

func validFinalizeStatus(status string) bool {
	return status == "passed" || status == "failed" || status == "mixed" || status == "aborted"
}

func validRFC3339(value string) bool {
	_, err := time.Parse(time.RFC3339, value)
	return err == nil
}

func readStandaloneRunReport(dir, fallbackID string) (RunResult, bool, error) {
	reportPath := filepath.Join(dir, "run-report.json")
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		if os.IsNotExist(err) {
			return RunResult{}, false, nil
		}
		return RunResult{}, false, fmt.Errorf("read %s: %w", reportPath, err)
	}
	var report standaloneRunReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return RunResult{}, false, fmt.Errorf("parse %s: %w", reportPath, err)
	}
	if report.Version == 1 {
		if report.RunID == "" || !validFinalizeStatus(report.FinalizeStatus) || !validRFC3339(report.CreatedAt) {
			return RunResult{}, false, fmt.Errorf("parse %s: incomplete standalone run report", reportPath)
		}
		failed := make([]FailedScenario, 0)
		for _, scenario := range report.Scenarios {
			if scenario.ScenarioInstanceID == "" || scenario.Feature == "" || scenario.Report.Scenario == "" ||
				!validScenarioOutcome(scenario.Report.Outcome) || !validStandaloneSessionStatus(scenario.Report.SessionStatus) ||
				(scenario.Rule != nil && *scenario.Rule == "") {
				return RunResult{}, false, fmt.Errorf("parse %s: incomplete scenario", reportPath)
			}
			if scenario.Report.Outcome != "failed" {
				continue
			}
			failed = append(failed, FailedScenario{
				ScenarioInstanceID: scenario.ScenarioInstanceID, Feature: scenario.Feature, Rule: scenario.Rule,
				Scenario: scenario.Report.Scenario, Outcome: scenario.Report.Outcome,
				SessionStatus: scenario.Report.SessionStatus,
			})
		}
		sortFailedScenarios(failed)
		return RunResult{
			RunID: report.RunID, Source: "legacy-run-report", Status: report.FinalizeStatus,
			FinalizeStatus: report.FinalizeStatus, CreatedAt: report.CreatedAt,
			ScenarioTotal: len(report.Scenarios), ScenarioFailed: len(failed), FailedScenarios: failed,
		}, true, nil
	}

	status := report.Status
	if status == "" && report.Passed != nil {
		if *report.Passed {
			status = "passed"
		} else {
			status = "failed"
		}
	}
	if status == "" {
		return RunResult{}, false, fmt.Errorf("parse %s: unsupported standalone run report", reportPath)
	}
	runID := report.RunID
	if runID == "" {
		runID = fallbackID
	}
	return RunResult{RunID: runID, Source: "legacy-run-report", Status: status}, true, nil
}

func validStandaloneSessionStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "timeout"
}

func sortFailedScenarios(failed []FailedScenario) {
	sort.Slice(failed, func(i, j int) bool {
		leftRule, rightRule := "", ""
		if failed[i].Rule != nil {
			leftRule = *failed[i].Rule
		}
		if failed[j].Rule != nil {
			rightRule = *failed[j].Rule
		}
		left := failed[i].Feature + "\x00" + leftRule + "\x00" + failed[i].Scenario + "\x00" + failed[i].ScenarioInstanceID
		right := failed[j].Feature + "\x00" + rightRule + "\x00" + failed[j].Scenario + "\x00" + failed[j].ScenarioInstanceID
		return left < right
	})
}

// ReferencedRunResults filters the local run corpus to durable proof links.
// A missing referenced run is intentionally omitted rather than synthesized:
// the UI must say evidence is unavailable, not quietly invent a result.
func ReferencedRunResults(repoRoot string, records *Records) ([]RunResult, error) {
	needed := make(map[string]struct{})
	for _, record := range records.All() {
		for _, id := range []string{record.Proof.RedRunID, record.Proof.GreenRunID} {
			if id != "" {
				needed[id] = struct{}{}
			}
		}
	}
	if len(needed) == 0 {
		return nil, nil
	}
	all, err := loadRunResults(repoRoot, "", needed)
	if err != nil {
		return nil, err
	}
	filtered := all[:0]
	for _, result := range all {
		if _, ok := needed[result.RunID]; ok {
			filtered = append(filtered, result)
		}
	}
	return filtered, nil
}
