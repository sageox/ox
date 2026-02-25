//go:build integration

package benchmark

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/tests/integration/agents/common"
)

const (
	iterationsPerQuery = 5 // covers all prompt variants per query
	queryTimeout       = 3 * time.Minute
	maxRetries         = 2
)

// TestPrimeEfficiencyBenchmark runs all benchmark queries against installed agents
// and measures tool call efficiency.
//
// Cost: ~40 API calls per agent (~$1.10-2.20, ~80 min wall time for Claude).
// Each of 8 queries runs 5 iterations, cycling through prompt variants.
func TestPrimeEfficiencyBenchmark(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping benchmark in short mode")
	}

	agent := common.DefaultAgentConfigs()[common.AgentClaude]
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)
	queries := DefaultQueries()
	run := NewBenchmarkRun("claude")

	for _, query := range queries {
		query := query
		t.Run(query.ID, func(t *testing.T) {
			for iter := 0; iter < iterationsPerQuery; iter++ {
				variantIdx := iter % len(query.Texts)
				prompt := query.Texts[variantIdx]

				result := runWithRetry(t, env, agent, prompt, maxRetries)
				if result == nil {
					continue
				}

				calls, err := parseClaudeToolCalls(result.RawOutput)
				if err != nil {
					t.Logf("iter=%d parse error: %v", iter, err)
					continue
				}

				qr := scoreQuery(calls, query)
				qr.Iteration = iter
				qr.PromptVariant = variantIdx
				qr.PromptText = prompt
				qr.Duration = result.Duration

				// handle 0-tool-call case via response validation
				if len(calls) == 0 && query.ValidateResponse != nil {
					if query.ValidateResponse(result.RawOutput) {
						qr.FoundCorrectSource = true
						qr.ToolCallsUntilFound = 0
					}
				}

				run.Queries = append(run.Queries, qr)

				t.Logf("  iter=%d variant=%d found=%v calls_until=%d total=%d extraneous=%d duration=%v",
					iter, variantIdx, qr.FoundCorrectSource, qr.ToolCallsUntilFound,
					qr.ToolCallsTotal, qr.ExtraneousCalls, qr.Duration.Round(time.Second))
			}
		})
	}

	// save and report
	if err := appendResult(run); err != nil {
		t.Errorf("save results: %v", err)
	}

	report := generateReport(run)
	t.Logf("\n%s", report)

	previous := lastRunForAgent("claude")
	checkRegression(t, &run, previous)
}

// runWithRetry executes a prompt with retries on transient failures.
func runWithRetry(t *testing.T, env *common.TestEnvironment, agent *common.AgentConfig, prompt string, retries int) *common.AgentTestResult {
	t.Helper()

	for attempt := 0; attempt <= retries; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		result := env.RunAgentPrompt(ctx, agent, prompt)
		cancel()

		if result.Error == nil {
			return result
		}

		if attempt < retries {
			t.Logf("  attempt %d failed, retrying: %v", attempt+1, result.Error)
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			continue
		}

		t.Logf("  all %d attempts failed: %v", retries+1, result.Error)
	}
	return nil
}

// --- Unit tests for parser and scorer (no agent CLI required) ---

func TestParseClaudeToolCalls(t *testing.T) {
	// sample Claude Code verbose JSON output with tool_use blocks
	sampleOutput := `[
		{
			"type": "assistant",
			"message": {
				"content": [
					{
						"type": "text",
						"text": "Let me check the team discussions."
					},
					{
						"type": "tool_use",
						"id": "toolu_01",
						"name": "Bash",
						"input": {"command": "ox agent team-ctx"}
					}
				]
			}
		},
		{
			"type": "tool_result",
			"tool_use_id": "toolu_01",
			"content": "Team discussions content here..."
		},
		{
			"type": "assistant",
			"message": {
				"content": [
					{
						"type": "tool_use",
						"id": "toolu_02",
						"name": "Read",
						"input": {"file_path": "/tmp/team-context/distilled-discussions.md"}
					}
				]
			}
		},
		{
			"type": "assistant",
			"message": {
				"content": [
					{
						"type": "text",
						"text": "Based on the discussions, you should implement X."
					}
				]
			}
		}
	]`

	calls, err := parseClaudeToolCalls(sampleOutput)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(calls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(calls))
	}

	if calls[0].Name != "Bash" {
		t.Errorf("call[0] name: want Bash, got %s", calls[0].Name)
	}
	if calls[1].Name != "Read" {
		t.Errorf("call[1] name: want Read, got %s", calls[1].Name)
	}
	if calls[0].Index != 0 || calls[1].Index != 1 {
		t.Errorf("call indices: want 0,1 got %d,%d", calls[0].Index, calls[1].Index)
	}
}

func TestParseClaudeToolCallsEmpty(t *testing.T) {
	// output with no tool calls (text-only response)
	sampleOutput := `[
		{
			"type": "assistant",
			"message": {
				"content": [
					{"type": "text", "text": "The answer is 42."}
				]
			}
		},
		{
			"type": "result",
			"result": "The answer is 42."
		}
	]`

	calls, err := parseClaudeToolCalls(sampleOutput)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("expected 0 tool calls, got %d", len(calls))
	}
}

func TestParseClaudeToolCallsInvalid(t *testing.T) {
	_, err := parseClaudeToolCalls("not json at all")
	if err == nil {
		t.Error("expected error on invalid JSON")
	}
}

func TestScoreQuery(t *testing.T) {
	query := BenchmarkQuery{
		ID:    "test",
		Texts: []string{"test query"},
		Validate: func(call ToolCall) bool {
			return call.Name == "Bash" && containsCI(call.Input, "ox doctor")
		},
	}

	tests := []struct {
		name        string
		calls       []ToolCall
		wantFound   bool
		wantUntil   int
		wantExtra   int
	}{
		{
			name:      "direct hit",
			calls:     []ToolCall{{Name: "Bash", Input: `{"command":"ox doctor"}`}},
			wantFound: true,
			wantUntil: 1,
			wantExtra: 0,
		},
		{
			name: "found after exploration",
			calls: []ToolCall{
				{Name: "Grep", Input: `{"pattern":"doctor"}`},
				{Name: "Read", Input: `{"file_path":"/some/file"}`},
				{Name: "Bash", Input: `{"command":"ox doctor"}`},
				{Name: "Read", Input: `{"file_path":"/other/file"}`},
			},
			wantFound: true,
			wantUntil: 3,
			wantExtra: 1,
		},
		{
			name: "not found",
			calls: []ToolCall{
				{Name: "Grep", Input: `{"pattern":"something"}`},
				{Name: "Read", Input: `{"file_path":"/some/file"}`},
			},
			wantFound: false,
			wantUntil: -1,
			wantExtra: 0,
		},
		{
			name:      "no tool calls",
			calls:     nil,
			wantFound: false,
			wantUntil: -1,
			wantExtra: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := scoreQuery(tt.calls, query)
			if result.FoundCorrectSource != tt.wantFound {
				t.Errorf("found: want %v, got %v", tt.wantFound, result.FoundCorrectSource)
			}
			if result.ToolCallsUntilFound != tt.wantUntil {
				t.Errorf("until_found: want %d, got %d", tt.wantUntil, result.ToolCallsUntilFound)
			}
			if result.ExtraneousCalls != tt.wantExtra {
				t.Errorf("extraneous: want %d, got %d", tt.wantExtra, result.ExtraneousCalls)
			}
		})
	}
}

func TestMedianToolCalls(t *testing.T) {
	tests := []struct {
		name    string
		results []QueryResult
		want    int
	}{
		{
			name: "normal",
			results: []QueryResult{
				{ToolCallsUntilFound: 1},
				{ToolCallsUntilFound: 3},
				{ToolCallsUntilFound: 2},
			},
			want: 2,
		},
		{
			name: "excludes not found",
			results: []QueryResult{
				{ToolCallsUntilFound: 1},
				{ToolCallsUntilFound: -1},
				{ToolCallsUntilFound: 5},
			},
			want: 5, // sorted [1, 5], index len/2=1 → 5
		},
		{
			name:    "empty",
			results: nil,
			want:    -1,
		},
		{
			name: "all not found",
			results: []QueryResult{
				{ToolCallsUntilFound: -1},
				{ToolCallsUntilFound: -1},
			},
			want: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := medianToolCalls(tt.results)
			if got != tt.want {
				t.Errorf("want %d, got %d", tt.want, got)
			}
		})
	}
}

func TestResultsJSONL(t *testing.T) {
	// verify BenchmarkRun round-trips through JSON correctly
	run := BenchmarkRun{
		RunID:        "test-id",
		OxVersion:    "0.17.0",
		AgentType:    "claude",
		AgentVersion: "2.1.49",
		Queries: []QueryResult{
			{QueryID: "q1", ToolCallsUntilFound: 2, FoundCorrectSource: true},
		},
	}

	data, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded BenchmarkRun
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.RunID != run.RunID || decoded.OxVersion != run.OxVersion {
		t.Error("round-trip mismatch")
	}
	if len(decoded.Queries) != 1 || decoded.Queries[0].ToolCallsUntilFound != 2 {
		t.Error("queries round-trip mismatch")
	}
}

// containsCI is a case-insensitive contains helper for tests.
func containsCI(s, substr string) bool {
	return len(s) >= len(substr) &&
		len(substr) > 0 &&
		(s == substr || len(s) > 0 && containsLower(s, substr))
}

func containsLower(s, substr string) bool {
	return len(s) >= len(substr) &&
		(strings.Contains(strings.ToLower(s), strings.ToLower(substr)))
}
