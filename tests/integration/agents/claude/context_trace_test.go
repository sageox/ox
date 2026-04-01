//go:build integration

// Tests for context-trace.jsonl generation during real Claude Code sessions.
// Verifies that ox agent prime writes "provided" events to context-trace.jsonl
// when team context is available, and that the file is present in the session cache.
//
// These tests use real Claude Code processes via `claude -p`.
package claude

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/tests/integration/agents/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContextTrace_PrimeWritesProvidedEvents verifies that ox agent prime
// writes context-trace.jsonl with "provided" events when team context is loaded.
//
// Flow:
//  1. Setup test environment with team context fixtures
//  2. Run ox agent prime (which auto-starts session recording)
//  3. Find context-trace.jsonl in the session cache dir
//  4. Verify it contains "provided" events for loaded context
func TestContextTrace_PrimeWritesProvidedEvents(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := common.SetupTestEnvironment(t)

	// run ox agent prime to bootstrap session + emit context trace
	primeOutput := runOxPrime(t, env)
	agentID := extractAgentID(t, primeOutput)
	require.NotEmpty(t, agentID, "prime should generate an agent_id")

	// find context-trace.jsonl in the session cache
	traceFiles := findFilesRecursive(env.RootDir, contexttrace.FileName)
	if len(traceFiles) == 0 {
		// context trace is best-effort — if team context wasn't loaded
		// (e.g., offline mode), the file won't exist. Log and skip.
		t.Skip("no context-trace.jsonl found — team context may not have loaded (offline mode)")
	}

	t.Logf("found %d context-trace.jsonl file(s)", len(traceFiles))

	for _, tracePath := range traceFiles {
		events, err := contexttrace.ReadEvents(tracePath)
		require.NoError(t, err, "should be able to read context-trace.jsonl")

		t.Logf("context-trace.jsonl at %s: %d events", tracePath, len(events))

		if len(events) == 0 {
			t.Error("context-trace.jsonl exists but has zero events")
			continue
		}

		// all events should be "provided" (prime only writes provided events)
		for i, ev := range events {
			assert.Equal(t, contexttrace.EventProvided, ev.Type,
				"event[%d] should be 'provided' (prime only writes provided events)", i)
			assert.NotEmpty(t, ev.Timestamp, "event[%d] should have a timestamp", i)
			assert.NotEmpty(t, ev.Source, "event[%d] should have a source", i)
		}

		// at least one event should be team-context source (from team instructions)
		hasTeamContext := false
		for _, ev := range events {
			if ev.Source == contexttrace.SourceTeamContext {
				hasTeamContext = true
				break
			}
		}
		if hasTeamContext {
			t.Log("confirmed: team-context events present")
		} else {
			t.Log("no team-context source events — team instructions may not have loaded")
		}
	}
}

// TestContextTrace_CLIAppendInfluencedEvent verifies that the
// `ox agent <id> session context-trace` CLI command appends an "influenced"
// event to an active session's context-trace.jsonl.
//
// Flow:
//  1. Setup + prime (starts session recording)
//  2. Pipe an influenced event JSON to `ox agent <id> session context-trace`
//  3. Verify the event appears in context-trace.jsonl
func TestContextTrace_CLIAppendInfluencedEvent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := common.SetupTestEnvironment(t)

	// prime to start session recording
	primeOutput := runOxPrime(t, env)
	agentID := extractAgentID(t, primeOutput)
	require.NotEmpty(t, agentID, "prime should generate an agent_id")

	// pipe an influenced event via CLI
	influencedEvent := map[string]interface{}{
		"type":     "influenced",
		"source":   "team-context",
		"doc":      "AGENTS.md",
		"section":  "Go Conventions",
		"decision": "used table-driven tests per team norm",
	}
	eventJSON, err := json.Marshal(influencedEvent)
	require.NoError(t, err)

	cmd := exec.Command(env.OxBinaryPath, "agent", agentID, "session", "context-trace")
	cmd.Dir = env.ProjectDir
	cmd.Env = env.EnvVars
	cmd.Stdin = strings.NewReader(string(eventJSON))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("context-trace CLI failed: %v\noutput: %s", err, output)
	}

	// parse CLI output
	var result struct {
		Success    bool   `json:"success"`
		Type       string `json:"type"`
		TracePath  string `json:"trace_path"`
		EventCount int    `json:"event_count"`
	}
	require.NoError(t, json.Unmarshal(output, &result), "CLI should return valid JSON\nraw: %s", output)
	assert.True(t, result.Success, "CLI should succeed")
	assert.Equal(t, "session_context_trace", result.Type)
	assert.Greater(t, result.EventCount, 0, "should have at least 1 event")

	// verify the event is readable
	events, err := contexttrace.ReadEvents(result.TracePath)
	require.NoError(t, err)

	// find our influenced event
	found := false
	for _, ev := range events {
		if ev.Type == contexttrace.EventInfluenced && ev.Decision == "used table-driven tests per team norm" {
			found = true
			assert.Equal(t, contexttrace.SourceTeamContext, ev.Source)
			assert.Equal(t, "AGENTS.md", ev.Doc)
			assert.Equal(t, "Go Conventions", ev.Section)
			break
		}
	}
	assert.True(t, found, "influenced event should be present in context-trace.jsonl\nevents: %+v", events)
}

// TestContextTrace_RealClaudeSession runs a real Claude Code session and
// verifies that context-trace.jsonl is populated with provided events after
// the session starts (via the SessionStart hook running ox agent prime).
func TestContextTrace_RealClaudeSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	agent := getClaudeConfig()
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)

	// run a simple prompt through real Claude — this triggers the full
	// hook → prime → session recording → context trace pipeline
	prompt := `What is 2 + 2? Answer with just the number.`

	ctx := t.Context()
	result := env.RunAgentPrompt(ctx, agent, prompt)
	if result.Error != nil {
		t.Fatalf("Claude execution failed: %v", result.Error)
	}

	t.Logf("Claude session completed in %v", result.Duration)

	// search for context-trace.jsonl anywhere in the test environment
	traceFiles := findFilesRecursive(env.RootDir, contexttrace.FileName)
	if len(traceFiles) == 0 {
		// known: SessionStart hook bug (#10373) may prevent prime from running
		// in new sessions, which means context-trace.jsonl won't be created
		t.Skip("no context-trace.jsonl found — SessionStart hook may not have fired (known: Claude Code bug #10373)")
	}

	for _, tracePath := range traceFiles {
		events, err := contexttrace.ReadEvents(tracePath)
		require.NoError(t, err)

		t.Logf("context-trace.jsonl: %d events at %s", len(events), tracePath)

		// verify all events are well-formed
		for i, ev := range events {
			assert.NotEmpty(t, ev.Type, "event[%d] should have a type", i)
			assert.NotEmpty(t, ev.Source, "event[%d] should have a source", i)
			assert.NotEmpty(t, ev.Timestamp, "event[%d] should have a timestamp", i)
		}
	}
}
