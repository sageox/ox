//go:build integration

package common

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/version"
)

// updateResults controls whether test results are written to disk.
// Off by default; enable with -update-results flag.
var updateResults = flag.Bool("update-results", false, "write test results JSON to test-results/runs/")

// TestRunResult captures results from a single test run for one agent.
// Written as a JSON file to test-results/runs/ when --update-results is set.
type TestRunResult struct {
	RunID       string          `json:"run_id"`
	Timestamp   time.Time       `json:"timestamp"`
	OxVersion   string          `json:"ox_version"`
	OxGitCommit string          `json:"ox_git_commit"`
	Agent       string          `json:"agent"`
	AgentVer    string          `json:"agent_version"`
	Environment string          `json:"environment"`
	Results     []FeatureResult `json:"results"`
}

// FeatureResult captures aggregated results for one feature group.
type FeatureResult struct {
	FeatureGroup string `json:"feature_group"`
	TestCount    int    `json:"test_count"`
	Passed       int    `json:"passed"`
	Failed       int    `json:"failed"`
	Skipped      int    `json:"skipped"`
	DurationMs   int64  `json:"duration_ms"`
}

// featureAccumulator tracks results for a single feature group.
type featureAccumulator struct {
	passed   int
	failed   int
	skipped  int
	duration time.Duration
}

// ResultCollector aggregates test results during a test run.
// Safe for concurrent use from parallel subtests.
type ResultCollector struct {
	mu       sync.Mutex
	agent    AgentType
	agentVer string
	features map[FeatureGroup]*featureAccumulator
}

// NewResultCollector creates a collector for the given agent.
// Detects agent version via agentx at creation time.
func NewResultCollector(agent AgentType) *ResultCollector {
	return &ResultCollector{
		agent:    agent,
		agentVer: DetectAgentVersion(agent),
		features: make(map[FeatureGroup]*featureAccumulator),
	}
}

// Record records a single test outcome for a feature group.
func (rc *ResultCollector) Record(feature FeatureGroup, passed bool, duration time.Duration) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	acc, ok := rc.features[feature]
	if !ok {
		acc = &featureAccumulator{}
		rc.features[feature] = acc
	}

	if passed {
		acc.passed++
	} else {
		acc.failed++
	}
	acc.duration += duration
}

// RecordSkip records a skipped test for a feature group.
func (rc *ResultCollector) RecordSkip(feature FeatureGroup) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	acc, ok := rc.features[feature]
	if !ok {
		acc = &featureAccumulator{}
		rc.features[feature] = acc
	}
	acc.skipped++
}

// WriteResults writes collected results to test-results/runs/.
// No-op if --update-results was not passed.
// File naming: <date>-<agent>-<version>.json
func (rc *ResultCollector) WriteResults() error {
	if updateResults == nil || !*updateResults {
		return nil
	}

	rc.mu.Lock()
	defer rc.mu.Unlock()

	run := TestRunResult{
		RunID:       uuid.Must(uuid.NewV7()).String(),
		Timestamp:   time.Now().UTC(),
		OxVersion:   version.Version,
		OxGitCommit: resultsGitCommitShort(),
		Agent:       string(rc.agent),
		AgentVer:    rc.agentVer,
		Environment: detectEnvironment(),
		Results:     rc.buildResults(),
	}

	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal results: %w", err)
	}

	runsDir := resultsRunsDir()
	if err := os.MkdirAll(runsDir, 0755); err != nil {
		return fmt.Errorf("create runs dir: %w", err)
	}

	safeVersion := sanitizeFilename(rc.agentVer)
	date := time.Now().Format("2006-01-02")
	filename := fmt.Sprintf("%s-%s-%s.json", date, rc.agent, safeVersion)

	path := filepath.Join(runsDir, filename)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write results to %s: %w", path, err)
	}

	return nil
}

// Summary returns a human-readable summary of collected results.
func (rc *ResultCollector) Summary() string {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	results := rc.buildResults()

	totalPassed, totalFailed, totalSkipped := 0, 0, 0
	for _, r := range results {
		totalPassed += r.Passed
		totalFailed += r.Failed
		totalSkipped += r.Skipped
	}

	return fmt.Sprintf("agent=%s version=%s features=%d passed=%d failed=%d skipped=%d",
		rc.agent, rc.agentVer, len(results), totalPassed, totalFailed, totalSkipped)
}

func (rc *ResultCollector) buildResults() []FeatureResult {
	results := make([]FeatureResult, 0, len(rc.features))
	for fg, acc := range rc.features {
		results = append(results, FeatureResult{
			FeatureGroup: string(fg),
			TestCount:    acc.passed + acc.failed + acc.skipped,
			Passed:       acc.passed,
			Failed:       acc.failed,
			Skipped:      acc.skipped,
			DurationMs:   acc.duration.Milliseconds(),
		})
	}
	return results
}

// resultsRunsDir returns the absolute path to test-results/runs/
// relative to the repo root.
func resultsRunsDir() string {
	return filepath.Join(findRepoRootFromCaller(), "test-results", "runs")
}

// findRepoRootFromCaller walks up from this source file to find go.mod.
func findRepoRootFromCaller() string {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}

	dir := filepath.Dir(filename)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

// resultsGitCommitShort returns the short git commit hash.
// Named with "results" prefix to avoid collision with benchmark.go's gitCommitShort.
func resultsGitCommitShort() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func detectEnvironment() string {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "docker"
	}
	return "local"
}

func sanitizeFilename(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '.':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "unknown"
	}
	return string(out)
}
