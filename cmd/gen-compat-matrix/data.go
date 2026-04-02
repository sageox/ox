package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// CompatibilityData is the static declaration from compatibility.json.
// It defines which agents and feature groups exist, their tiers, and the
// hand-authored support matrix (used when no test run data is available).
type CompatibilityData struct {
	SchemaVersion int                        `json:"schema_version"`
	Agents        map[string]AgentInfo       `json:"agents"`
	FeatureGroups map[string]FeatureGroupInfo `json:"feature_groups"`
	// Support maps "agent/feature" keys to "supported"|"partial"|"planned"|"unsupported".
	Support map[string]string `json:"support"`
}

type AgentInfo struct {
	DisplayName string `json:"display_name"`
	Tier        string `json:"tier"`        // gold, silver, bronze
	MinVersion  string `json:"min_version"`
	CLIBinary   string `json:"cli_binary"`
	InstallCmd  string `json:"install_cmd"`
}

type FeatureGroupInfo struct {
	DisplayName string `json:"display_name"`
}

// TestRunResult is a per-run result file from test-results/runs/*.json.
type TestRunResult struct {
	RunID        string          `json:"run_id"`
	Timestamp    time.Time       `json:"timestamp"`
	OxVersion    string          `json:"ox_version"`
	Agent        string          `json:"agent"`
	AgentVersion string          `json:"agent_version"`
	Results      []FeatureResult `json:"results"`
}

type FeatureResult struct {
	FeatureGroup string `json:"feature_group"`
	TestCount    int    `json:"test_count"`
	Passed       int    `json:"passed"`
	Failed       int    `json:"failed"`
	Skipped      int    `json:"skipped"`
	DurationMs   int64  `json:"duration_ms"`
}

// LoadedData holds everything after merging static + run data.
type LoadedData struct {
	Compat CompatibilityData
	// Runs is the full list of all loaded test runs.
	Runs []TestRunResult
	// LatestRunByAgent maps agent key to the most recent run for overview display.
	LatestRunByAgent map[string]*TestRunResult
	// RunsByAgentVersion maps "agent/version" to ordered list of runs (oldest first).
	RunsByAgentVersion map[string][]TestRunResult
	// AgentOrder is agents sorted by tier then display name for consistent column order.
	AgentOrder []string
	// FeatureOrder is feature groups in declaration order.
	FeatureOrder []string
}

func loadData(inputDir string) (*LoadedData, error) {
	compatPath := filepath.Join(inputDir, "compatibility.json")
	raw, err := os.ReadFile(compatPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", compatPath, err)
	}

	var compat CompatibilityData
	if err := json.Unmarshal(raw, &compat); err != nil {
		return nil, fmt.Errorf("parse %s: %w", compatPath, err)
	}

	runs := loadRuns(filepath.Join(inputDir, "runs"))

	ld := &LoadedData{
		Compat:             compat,
		Runs:               runs,
		LatestRunByAgent:   make(map[string]*TestRunResult),
		RunsByAgentVersion: make(map[string][]TestRunResult),
	}

	// Index runs.
	for i := range runs {
		run := &runs[i]
		key := run.Agent + "/" + run.AgentVersion
		ld.RunsByAgentVersion[key] = append(ld.RunsByAgentVersion[key], *run)

		existing, ok := ld.LatestRunByAgent[run.Agent]
		if !ok || run.Timestamp.After(existing.Timestamp) {
			ld.LatestRunByAgent[run.Agent] = run
		}
	}

	// Sort runs within each version group oldest→newest so callers can take last.
	for key := range ld.RunsByAgentVersion {
		sort.Slice(ld.RunsByAgentVersion[key], func(i, j int) bool {
			return ld.RunsByAgentVersion[key][i].Timestamp.Before(ld.RunsByAgentVersion[key][j].Timestamp)
		})
	}

	ld.AgentOrder = sortedAgents(compat.Agents, compat.Support)
	ld.FeatureOrder = sortedFeatures(compat.FeatureGroups)

	return ld, nil
}

// loadRuns reads all *.json files from runsDir, skipping malformed ones with a warning.
func loadRuns(runsDir string) []TestRunResult {
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		log.Printf("warning: read runs dir %s: %v", runsDir, err)
		return nil
	}

	var runs []TestRunResult
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(runsDir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			log.Printf("warning: skip %s: %v", path, err)
			continue
		}
		var run TestRunResult
		if err := json.Unmarshal(raw, &run); err != nil {
			log.Printf("warning: skip malformed %s: %v", path, err)
			continue
		}
		runs = append(runs, run)
	}

	sort.Slice(runs, func(i, j int) bool {
		return runs[i].Timestamp.Before(runs[j].Timestamp)
	})
	return runs
}

// tierRank assigns a sort order so gold < silver < bronze.
func tierRank(tier string) int {
	switch tier {
	case "gold":
		return 0
	case "silver":
		return 1
	default:
		return 2
	}
}

// sortedAgents orders by tier (gold > silver > bronze), then by number of
// supported features descending (best-supported first within each tier).
func sortedAgents(agents map[string]AgentInfo, support map[string]string) []string {
	keys := make([]string, 0, len(agents))
	for k := range agents {
		keys = append(keys, k)
	}

	// count declared support entries per agent
	supportCount := make(map[string]int, len(keys))
	for key := range support {
		for i := len(key) - 1; i >= 0; i-- {
			if key[i] == '/' {
				supportCount[key[:i]]++
				break
			}
		}
	}

	sort.Slice(keys, func(i, j int) bool {
		ai, aj := agents[keys[i]], agents[keys[j]]
		ri, rj := tierRank(ai.Tier), tierRank(aj.Tier)
		if ri != rj {
			return ri < rj
		}
		// within same tier, more supported features first
		ci, cj := supportCount[keys[i]], supportCount[keys[j]]
		if ci != cj {
			return ci > cj
		}
		return ai.DisplayName < aj.DisplayName
	})
	return keys
}

func sortedFeatures(groups map[string]FeatureGroupInfo) []string {
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// CellState represents what to render in an overview matrix cell.
type CellState struct {
	Status     string // "pass", "warn", "fail", "untested", "planned", "unsupported", "na"
	Passed     int
	Total      int
	DurationMs int64
}

// cellState resolves the display state for a given agent+feature combination,
// preferring live test results over the static support matrix.
func cellState(ld *LoadedData, agent, feature string) CellState {
	run := ld.LatestRunByAgent[agent]
	if run != nil {
		for _, r := range run.Results {
			if r.FeatureGroup == feature {
				state := "pass"
				if r.Failed > 0 && r.Passed == 0 {
					state = "fail"
				} else if r.Failed > 0 {
					state = "warn"
				}
				return CellState{
					Status:     state,
					Passed:     r.Passed,
					Total:      r.TestCount,
					DurationMs: r.DurationMs,
				}
			}
		}
	}

	// Fall back to static support matrix.
	key := agent + "/" + feature
	switch ld.Compat.Support[key] {
	case "supported":
		return CellState{Status: "untested"}
	case "partial":
		return CellState{Status: "untested"}
	case "planned":
		return CellState{Status: "planned"}
	case "unsupported":
		return CellState{Status: "unsupported"}
	default:
		return CellState{Status: "na"}
	}
}

// AgentVersionRow summarizes one (agent, version) row for the drill-down view.
type AgentVersionRow struct {
	AgentVersion string
	Timestamp    time.Time
	OxVersion    string
	// Cells maps feature group key to CellState.
	Cells map[string]CellState
}

// agentVersionRows builds drill-down rows for one agent, newest version first.
func agentVersionRows(ld *LoadedData, agent string) []AgentVersionRow {
	// Collect all version keys for this agent.
	var versionKeys []string
	for key := range ld.RunsByAgentVersion {
		ag, _, found := splitAgentVersion(key)
		if found && ag == agent {
			versionKeys = append(versionKeys, key)
		}
	}

	// Sort versions descending (newest first) using timestamp of latest run.
	sort.Slice(versionKeys, func(i, j int) bool {
		ri := ld.RunsByAgentVersion[versionKeys[i]]
		rj := ld.RunsByAgentVersion[versionKeys[j]]
		latestI := ri[len(ri)-1].Timestamp
		latestJ := rj[len(rj)-1].Timestamp
		return latestI.After(latestJ)
	})

	var rows []AgentVersionRow
	for _, vk := range versionKeys {
		runs := ld.RunsByAgentVersion[vk]
		latest := runs[len(runs)-1]

		cells := make(map[string]CellState, len(ld.FeatureOrder))
		for _, feat := range ld.FeatureOrder {
			for _, r := range latest.Results {
				if r.FeatureGroup == feat {
					state := "pass"
					if r.Failed > 0 && r.Passed == 0 {
						state = "fail"
					} else if r.Failed > 0 {
						state = "warn"
					}
					cells[feat] = CellState{
						Status:     state,
						Passed:     r.Passed,
						Total:      r.TestCount,
						DurationMs: r.DurationMs,
					}
					break
				}
			}
			if _, ok := cells[feat]; !ok {
				cells[feat] = CellState{Status: "na"}
			}
		}

		rows = append(rows, AgentVersionRow{
			AgentVersion: latest.AgentVersion,
			Timestamp:    latest.Timestamp,
			OxVersion:    latest.OxVersion,
			Cells:        cells,
		})
	}
	return rows
}

func splitAgentVersion(key string) (agent, version string, ok bool) {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == '/' {
			return key[:i], key[i+1:], true
		}
	}
	return "", "", false
}

// latestOxVersion returns the ox version from the most recent run across all agents.
func latestOxVersion(ld *LoadedData) string {
	var latest time.Time
	version := "unknown"
	for _, run := range ld.Runs {
		if run.Timestamp.After(latest) {
			latest = run.Timestamp
			version = run.OxVersion
		}
	}
	return version
}
