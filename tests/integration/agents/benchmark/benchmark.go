//go:build integration

// Package benchmark measures how efficiently AI coworkers navigate to data
// sources after receiving ox agent prime output. The primary metric is
// "tool calls until correct source found" — tracked across ox versions,
// agent types, and agent versions.
package benchmark

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/version"
)

// BenchmarkRun captures one complete benchmark execution.
type BenchmarkRun struct {
	RunID        string        `json:"run_id"`
	Timestamp    time.Time     `json:"timestamp"`
	OxVersion    string        `json:"ox_version"`
	GitCommit    string        `json:"git_commit"`
	AgentType    string        `json:"agent_type"`
	AgentVersion string        `json:"agent_version"`
	AgentModel   string        `json:"agent_model,omitempty"`
	Queries      []QueryResult `json:"queries"`
}

// QueryResult captures the outcome of a single benchmark query iteration.
type QueryResult struct {
	QueryID             string        `json:"query_id"`
	Iteration           int           `json:"iteration"`
	FoundCorrectSource  bool          `json:"found_correct_source"`
	ToolCallsTotal      int           `json:"tool_calls_total"`
	ToolCallsUntilFound int           `json:"tool_calls_until_found"` // -1 if not found
	ExtraneousCalls     int           `json:"extraneous_calls"`
	Duration            time.Duration `json:"duration"`
	ToolCalls           []ToolCall    `json:"tool_calls"`
}

// ToolCall represents a parsed tool invocation from agent output.
type ToolCall struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	Input string `json:"input"`
	Error bool   `json:"error,omitempty"`
}

// NewBenchmarkRun creates a run with metadata populated from the current environment.
func NewBenchmarkRun(agentType string) BenchmarkRun {
	return BenchmarkRun{
		RunID:        uuid.Must(uuid.NewV7()).String(),
		Timestamp:    time.Now().UTC(),
		OxVersion:    version.Version,
		GitCommit:    gitCommitShort(),
		AgentType:    agentType,
		AgentVersion: detectAgentVersion(agentType),
	}
}

// parseClaudeToolCalls extracts tool calls from Claude Code verbose JSON output.
// Claude Code with --output-format json --verbose outputs a JSON array of message
// objects. Tool invocations appear as content blocks with type "tool_use" inside
// assistant messages.
func parseClaudeToolCalls(rawOutput string) ([]ToolCall, error) {
	var messages []map[string]any
	if err := json.Unmarshal([]byte(rawOutput), &messages); err != nil {
		return nil, fmt.Errorf("parse claude output: %w", err)
	}

	var calls []ToolCall
	idx := 0

	for _, msg := range messages {
		msgType, _ := msg["type"].(string)
		if msgType != "assistant" {
			continue
		}

		message, _ := msg["message"].(map[string]any)
		if message == nil {
			continue
		}

		content, _ := message["content"].([]any)
		for _, block := range content {
			blockMap, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if blockMap["type"] != "tool_use" {
				continue
			}

			name, _ := blockMap["name"].(string)
			input, _ := blockMap["input"].(map[string]any)
			inputJSON, _ := json.Marshal(input)

			calls = append(calls, ToolCall{
				Index: idx,
				Name:  name,
				Input: truncate(string(inputJSON), 500),
			})
			idx++
		}
	}

	return calls, nil
}

// scoreQuery counts tool calls until the validator returns true.
func scoreQuery(calls []ToolCall, query BenchmarkQuery) QueryResult {
	result := QueryResult{
		QueryID:             query.ID,
		ToolCallsTotal:      len(calls),
		ToolCallsUntilFound: -1,
		ToolCalls:           calls,
	}

	for i, call := range calls {
		if query.Validate(call) {
			result.FoundCorrectSource = true
			result.ToolCallsUntilFound = i + 1
			result.ExtraneousCalls = len(calls) - (i + 1)
			break
		}
	}

	return result
}

// detectAgentVersion returns the CLI version string for a given agent type.
func detectAgentVersion(agentType string) string {
	switch agentType {
	case "claude":
		out, err := exec.Command("claude", "--version").Output()
		if err != nil {
			return "unknown"
		}
		return strings.TrimSpace(string(out))
	}
	return "unknown"
}

// resultsPath returns the path to the results.jsonl file.
func resultsPath() string {
	return filepath.Join(benchmarkDir(), "results.jsonl")
}

// appendResult writes a benchmark run to the append-only JSONL results file.
func appendResult(run BenchmarkRun) error {
	f, err := os.OpenFile(resultsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open results file: %w", err)
	}
	defer f.Close()

	data, err := json.Marshal(run)
	if err != nil {
		return fmt.Errorf("marshal run: %w", err)
	}

	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write result: %w", err)
	}
	return nil
}

// lastRunForAgent returns the most recent benchmark run for a given agent type,
// or nil if no previous run exists.
func lastRunForAgent(agentType string) *BenchmarkRun {
	f, err := os.Open(resultsPath())
	if err != nil {
		return nil
	}
	defer f.Close()

	var last *BenchmarkRun
	scanner := bufio.NewScanner(f)
	// handle long lines from detailed tool call data
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var run BenchmarkRun
		if err := json.Unmarshal(scanner.Bytes(), &run); err != nil {
			continue
		}
		if run.AgentType == agentType {
			r := run
			last = &r
		}
	}
	return last
}

// medianToolCalls computes the median tool_calls_until_found across all query results.
// Results where the source was not found (value -1) are excluded.
func medianToolCalls(results []QueryResult) int {
	var vals []int
	for _, r := range results {
		if r.ToolCallsUntilFound >= 0 {
			vals = append(vals, r.ToolCallsUntilFound)
		}
	}
	if len(vals) == 0 {
		return -1
	}

	// simple sort for small slices
	for i := range vals {
		for j := i + 1; j < len(vals); j++ {
			if vals[j] < vals[i] {
				vals[i], vals[j] = vals[j], vals[i]
			}
		}
	}
	return vals[len(vals)/2]
}

func benchmarkDir() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Dir(filename)
}

func gitCommitShort() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
