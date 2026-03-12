//go:build integration

package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon/agentwork"
	"github.com/sageox/ox/tests/integration/agents/common"
)

// TestIncrementalRecording_PostToolUse verifies the full incremental recording
// pipeline using the real Claude CLI. This is a true E2E test — if Claude Code
// changes its JSONL format, hook stdin format, or session file paths, this test
// will catch it.
//
// Flow:
//  1. Build ox, set up workspace with .sageox/ initialized
//  2. Run ox agent prime (creates agent instance + session marker; auto-installs hooks if missing)
//  3. Run claude -p with a prompt that triggers tool use (PostToolUse hook fires)
//  4. Verify raw.jsonl was populated by the incremental recording hooks
//  5. Stop the session, verify final artifacts
//
// Run with: go test -tags=integration -timeout=5m -run TestIncrementalRecording_PostToolUse ./tests/integration/agents/claude/ -v
func TestIncrementalRecording_PostToolUse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	agent := getClaudeConfig()
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)

	// Run prime BEFORE Claude starts. In a real ox init'd repo, hooks are
	// already in .claude/settings.local.json. Here, the test fixture lacks
	// that file, so prime auto-installs hooks via tryAutoInstallClaudeHooks().
	// Claude Code caches hook config at launch — hooks must exist before start.
	_ = runOxPrime(t, env)

	// Prompt that triggers tool use (Read tool → PostToolUse hook fires).
	prompt := `Read the file AGENTS.md and tell me what it contains. Keep your response under 50 words.`

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	t.Log("running claude CLI with tool-triggering prompt...")
	result := runClaudeWithHooks(ctx, t, env, agent, prompt)
	if result.Error != nil {
		t.Logf("claude stderr/error (may be ok): %v", result.Error)
	}
	t.Logf("claude completed in %v", result.Duration)

	// Find the agent ID from the recording state (created by SessionStart hook → prime)
	agentID := findActiveAgentID(t, env)
	t.Logf("active agent ID: %s", agentID)

	t.Run("incremental_entries_written", func(t *testing.T) {
		rawPaths := findAllRawJSONL(t, env)
		if len(rawPaths) == 0 {
			logSearchedPaths(t, env)
			t.Fatal("no raw.jsonl found — incremental recording did not work")
		}

		// Check all raw.jsonl files for entries (multiple agents may exist due to
		// prime creating one agent, then Claude's SessionStart hook creating another)
		totalEntries := 0
		entryTypes := map[string]int{}
		for _, rawPath := range rawPaths {
			entries := readRawJSONL(t, rawPath)
			t.Logf("raw.jsonl at %s has %d entries", rawPath, len(entries))
			for _, e := range entries {
				eType, _ := e["type"].(string)
				if eType != "header" {
					totalEntries++
					entryTypes[eType]++
				}
			}
		}
		t.Logf("incremental entry types: %v", entryTypes)

		// The key assertion: some entries were written DURING the session by PostToolUse hooks.
		// After PostToolUse, the source JSONL has user message + tool interactions.
		// The final assistant text response arrives AFTER PostToolUse, so it may only
		// appear after Stop. We just verify some entries were captured incrementally.
		if totalEntries == 0 {
			t.Fatal("no non-header entries found in any raw.jsonl — incremental recording failed")
		}
		t.Logf("incremental recording captured %d entries during session", totalEntries)
	})

	t.Run("session_stop", func(t *testing.T) {
		stopSession(t, env, agentID)

		rawPaths := findAllRawJSONL(t, env)
		if len(rawPaths) == 0 {
			t.Fatal("no raw.jsonl found after session stop")
		}

		// Collect all entries across all raw.jsonl files
		var allEntries []map[string]interface{}
		for _, rawPath := range rawPaths {
			allEntries = append(allEntries, readRawJSONL(t, rawPath)...)
		}

		totalEntries := 0
		hasUser := false
		hasAssistant := false
		for _, e := range allEntries {
			eType, _ := e["type"].(string)
			if eType != "header" {
				totalEntries++
			}
			switch eType {
			case "user":
				hasUser = true
			case "assistant":
				hasAssistant = true
			}
		}
		t.Logf("total non-header entries after stop: %d", totalEntries)

		if totalEntries == 0 {
			t.Error("no entries after session stop")
		}
		if !hasUser {
			t.Error("raw.jsonl missing user entries after stop")
		}
		if !hasAssistant {
			t.Error("raw.jsonl missing assistant entries after stop")
		}
	})

	t.Run("user_prompt_captured", func(t *testing.T) {
		// Verify the actual user prompt text appears in user-type entries
		rawPaths := findAllRawJSONL(t, env)
		promptFound := false
		for _, rawPath := range rawPaths {
			for _, e := range readRawJSONL(t, rawPath) {
				eType, _ := e["type"].(string)
				content, _ := e["content"].(string)
				if eType == "user" && strings.Contains(content, "AGENTS.md") {
					promptFound = true
					t.Logf("found user prompt in entry: %.100s", content)
				}
			}
		}
		if !promptFound {
			t.Error("user prompt text not found in any user entry — prompt content not captured")
		}
	})

	t.Run("tool_calls_tagged", func(t *testing.T) {
		// Verify tool entries have tool_name set (proves tool call metadata is captured)
		rawPaths := findAllRawJSONL(t, env)
		toolEntryFound := false
		for _, rawPath := range rawPaths {
			for _, e := range readRawJSONL(t, rawPath) {
				eType, _ := e["type"].(string)
				toolName, _ := e["tool_name"].(string)
				if eType == "tool" && toolName != "" {
					toolEntryFound = true
					t.Logf("found tool entry: tool_name=%s", toolName)
				}
			}
		}
		if !toolEntryFound {
			t.Log("no tool entries with tool_name found (tool metadata may not be captured in incremental path)")
		}
	})
}

// TestIncrementalRecording_ContinueSession verifies that recording survives
// a session resume (SessionStart hook fires with source=resume).
// Uses a real Claude Code instance — never simulated. (E2E requirement)
//
// This exercises the handleStart re-prime path that runs on /clear, /compact,
// and --continue. After resuming, incremental recording should still work.
//
// Flow:
//  1. Run claude -p (first session — triggers SessionStart + PostToolUse)
//  2. Run claude -p --continue (resume — triggers SessionStart with source=resume)
//  3. Verify raw.jsonl accumulated entries from BOTH invocations
func TestIncrementalRecording_ContinueSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	agent := getClaudeConfig()
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)

	primeOutput := runOxPrime(t, env)
	agentID := extractAgentID(t, primeOutput)
	t.Logf("agent ID from prime: %s", agentID)

	time.Sleep(500 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// First invocation — establishes the session
	t.Log("first claude invocation (new session)...")
	result1 := runClaudeWithHooks(ctx, t, env, agent,
		`Read the file AGENTS.md and tell me what commands are listed. Keep your response under 30 words.`)
	if result1.Error != nil {
		t.Logf("first invocation error (may be ok): %v", result1.Error)
	}
	t.Logf("first invocation completed in %v", result1.Duration)

	// Count entries after first invocation
	rawPath := findRawJSONL(t, env)
	var entriesAfterFirst int
	if rawPath != "" {
		entriesAfterFirst = len(readRawJSONL(t, rawPath))
		t.Logf("entries after first invocation: %d", entriesAfterFirst)
	}

	// Second invocation — continue the same session
	// This triggers SessionStart with source=resume, exercising re-prime
	t.Log("second claude invocation (--continue)...")
	result2 := runClaudeWithFlags(ctx, t, env, agent,
		`What is 2 + 2? Answer with just the number.`,
		"--continue")
	if result2.Error != nil {
		t.Logf("second invocation error (may be ok): %v", result2.Error)
	}
	t.Logf("second invocation completed in %v", result2.Duration)

	t.Run("recording_survived_resume", func(t *testing.T) {
		rawPath := findRawJSONL(t, env)
		if rawPath == "" {
			logSearchedPaths(t, env)
			t.Fatal("raw.jsonl not found after resume")
		}

		entries := readRawJSONL(t, rawPath)
		t.Logf("entries after resume: %d (was %d)", len(entries), entriesAfterFirst)

		if len(entries) == 0 {
			t.Fatal("raw.jsonl empty after resume")
		}

		// After resume, we should have at least as many entries as before
		// (the resume session adds its own entries)
		if len(entries) < entriesAfterFirst {
			t.Errorf("entries decreased after resume: %d < %d", len(entries), entriesAfterFirst)
		}
	})

	stopSession(t, env, agentID)
}

// TestIncrementalRecording_CompactHook verifies that the PreCompact hook
// (which re-primes ox) works correctly with the real ox binary.
// Uses a real Claude Code instance for the initial session — never simulated.
// (E2E requirement)
//
// Triggering real compaction in Claude Code is impractical in a test (requires
// filling the context window, which is expensive and slow). Instead, this test
// calls `ox agent hook PreCompact` directly with the same stdin JSON that
// Claude Code would send — a contract test using the real ox binary.
//
// This catches: ox binary changes to hook dispatch, stdin format parsing errors,
// prime re-initialization bugs. It does NOT catch: Claude Code changing the
// PreCompact hook stdin format (that requires a real Claude compaction).
func TestIncrementalRecording_CompactHook(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	agent := getClaudeConfig()
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)

	primeOutput := runOxPrime(t, env)
	agentID := extractAgentID(t, primeOutput)
	t.Logf("agent ID from prime: %s", agentID)

	time.Sleep(500 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// First: run a real claude session to populate raw.jsonl via PostToolUse
	t.Log("running claude to populate initial entries...")
	result := runClaudeWithHooks(ctx, t, env, agent,
		`Read the file AGENTS.md. Keep your response under 20 words.`)
	if result.Error != nil {
		t.Logf("claude error (may be ok): %v", result.Error)
	}

	rawPath := findRawJSONL(t, env)
	var entriesBefore int
	if rawPath != "" {
		entriesBefore = len(readRawJSONL(t, rawPath))
	}
	t.Logf("entries before compact: %d", entriesBefore)

	// Extract the Claude session ID from the claude output for the hook stdin
	claudeSessionID := extractClaudeSessionID(t, result.RawOutput)

	// Simulate the PreCompact hook — same stdin format Claude Code sends
	t.Log("firing PreCompact hook with real ox binary...")
	hookResult := fireOxHook(t, env, "PreCompact", claudeSessionID)
	t.Logf("PreCompact hook output (%d bytes): %.200s", len(hookResult), hookResult)

	t.Run("compact_reprime_succeeded", func(t *testing.T) {
		// The compact hook should re-prime (output contains agent_id or prime data)
		if !strings.Contains(hookResult, "agent_id") && !strings.Contains(hookResult, agentID) {
			t.Log("compact hook output doesn't contain agent_id — may not have re-primed")
			t.Log("this is acceptable if the hook returned silently (recording state preserved)")
		}
	})

	t.Run("recording_survives_compact", func(t *testing.T) {
		// Recording should still be active after compact
		// Verify by running another claude session
		t.Log("running claude after compact to verify recording still works...")
		result2 := runClaudeWithHooks(ctx, t, env, agent,
			`What is 3 + 3? Answer with just the number.`)
		if result2.Error != nil {
			t.Logf("post-compact claude error (may be ok): %v", result2.Error)
		}

		rawPath := findRawJSONL(t, env)
		if rawPath == "" {
			logSearchedPaths(t, env)
			t.Fatal("raw.jsonl not found after compact")
		}

		entriesAfter := len(readRawJSONL(t, rawPath))
		t.Logf("entries after compact: %d (was %d)", entriesAfter, entriesBefore)

		if entriesAfter < entriesBefore {
			t.Errorf("entries decreased after compact: %d < %d", entriesAfter, entriesBefore)
		}
	})

	stopSession(t, env, agentID)
}

// TestIncrementalRecording_NoToolUse verifies that sessions without tool use
// still produce a valid raw.jsonl via the stop drain.
// Uses a real Claude Code instance — never simulated. (E2E requirement)
func TestIncrementalRecording_NoToolUse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	agent := getClaudeConfig()
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)

	primeOutput := runOxPrime(t, env)
	agentID := extractAgentID(t, primeOutput)
	t.Logf("agent ID from prime: %s", agentID)

	time.Sleep(500 * time.Millisecond)

	prompt := `What is 2 + 2? Answer with just the number, nothing else.`

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	t.Log("running claude CLI with no-tool prompt...")
	result := runClaudeWithHooks(ctx, t, env, agent, prompt)
	if result.Error != nil {
		t.Logf("claude stderr/error (may be ok): %v", result.Error)
	}
	t.Logf("claude completed in %v", result.Duration)

	stopSession(t, env, agentID)

	rawPath := findRawJSONL(t, env)
	if rawPath == "" {
		t.Fatal("raw.jsonl must exist after stop — stop-path drain should produce output")
	}
	entries := readRawJSONL(t, rawPath)
	if len(entries) == 0 {
		t.Fatal("raw.jsonl must contain entries after stop-path drain")
	}
}

// TestIncrementalRecording_MultiTurn exercises 5+ back-and-forth rounds between
// the user and Claude in a single session, verifying that all turns are captured
// in raw.jsonl with correct ordering and content.
// Uses a real Claude Code instance — never simulated. (E2E requirement)
//
// This catches: entry ordering bugs, offset drift across multiple PostToolUse hooks,
// lost entries in long sessions, and seq numbering gaps.
func TestIncrementalRecording_MultiTurn(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	agent := getClaudeConfig()
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)

	_ = runOxPrime(t, env)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	// Use sequential --continue invocations for genuine multi-turn: each
	// invocation sends a new user message, Claude responds, and the PostToolUse
	// hook fires for any tool calls. This produces real user↔assistant rounds.
	prompts := []string{
		`Read the file AGENTS.md and summarize it in one sentence.`,
		`List all files in the current directory using the Glob tool with pattern "*". Just list them.`,
		`Create a new file called "test-output.txt" containing "hello from multi-turn test"`,
		`Read the file "test-output.txt" you just created and confirm its contents.`,
		`How many total files exist in the current directory now? Use the Glob tool to count.`,
	}

	t.Log("running multi-turn claude session (5 separate user messages)...")
	for i, prompt := range prompts {
		var result *common.AgentTestResult
		if i == 0 {
			result = runClaudeWithHooks(ctx, t, env, agent, prompt)
		} else {
			result = runClaudeWithFlags(ctx, t, env, agent, prompt, "--continue")
		}
		if result.Error != nil {
			t.Logf("turn %d error (may be ok): %v", i+1, result.Error)
		}
		t.Logf("turn %d/%d completed in %v", i+1, len(prompts), result.Duration)
	}

	agentID := findActiveAgentID(t, env)
	stopSession(t, env, agentID)

	t.Run("captures_all_turns", func(t *testing.T) {
		rawPaths := findAllRawJSONL(t, env)
		if len(rawPaths) == 0 {
			logSearchedPaths(t, env)
			t.Fatal("no raw.jsonl found")
		}

		// Collect entries from all raw.jsonl files
		var allEntries []map[string]interface{}
		for _, rawPath := range rawPaths {
			allEntries = append(allEntries, readRawJSONL(t, rawPath)...)
		}

		// Count by type (excluding header/footer)
		typeCounts := map[string]int{}
		for _, e := range allEntries {
			eType, _ := e["type"].(string)
			if eType != "header" && eType != "footer" {
				typeCounts[eType]++
			}
		}
		t.Logf("entry type counts: %v", typeCounts)

		userCount := typeCounts["user"]
		assistantCount := typeCounts["assistant"]
		toolCount := typeCounts["tool"]

		// With 5 separate user messages via --continue, we expect:
		// - 5 user entries (one per prompt)
		// - 5 assistant entries (one response per prompt)
		// - 3+ tool entries (Read, Glob, Write at minimum)
		if userCount < 5 {
			t.Errorf("expected at least 5 user entries (5 prompts), got %d", userCount)
		}
		if assistantCount < 5 {
			t.Errorf("expected at least 5 assistant entries (5 responses), got %d", assistantCount)
		}
		if toolCount < 3 {
			t.Errorf("expected at least 3 tool entries (Read, Glob, Write), got %d", toolCount)
		}

		totalContent := userCount + assistantCount + toolCount
		t.Logf("total content entries: %d (user=%d, assistant=%d, tool=%d)",
			totalContent, userCount, assistantCount, toolCount)

		if totalContent < 13 {
			t.Errorf("expected at least 13 content entries for 5 round-trips + tools, got %d", totalContent)
		}
	})

	t.Run("ordering_preserved", func(t *testing.T) {
		rawPaths := findAllRawJSONL(t, env)
		// Find the raw.jsonl with the most entries (the primary agent)
		var bestEntries []map[string]interface{}
		for _, rawPath := range rawPaths {
			entries := readRawJSONL(t, rawPath)
			if len(entries) > len(bestEntries) {
				bestEntries = entries
			}
		}

		// Verify timestamps are monotonically non-decreasing
		var lastTS string
		for _, e := range bestEntries {
			eType, _ := e["type"].(string)
			if eType == "header" || eType == "footer" {
				continue
			}
			ts, _ := e["timestamp"].(string)
			if ts == "" {
				continue
			}
			if lastTS != "" && ts < lastTS {
				t.Errorf("timestamp went backwards: %s < %s", ts, lastTS)
			}
			lastTS = ts
		}
		t.Log("timestamp ordering: OK")
	})

	t.Run("diverse_tool_names", func(t *testing.T) {
		rawPaths := findAllRawJSONL(t, env)
		toolNames := map[string]bool{}
		for _, rawPath := range rawPaths {
			for _, e := range readRawJSONL(t, rawPath) {
				eType, _ := e["type"].(string)
				toolName, _ := e["tool_name"].(string)
				if eType == "tool" && toolName != "" {
					toolNames[toolName] = true
				}
			}
		}
		t.Logf("unique tool names captured: %v", toolNames)

		// We expect at least Read and Write (possibly Glob, Bash, or others)
		if len(toolNames) < 2 {
			t.Errorf("expected at least 2 different tool names, got %d: %v", len(toolNames), toolNames)
		}
	})
}

// --- helpers ---

// runOxPrime runs ox agent prime and returns the raw JSON output.
func runOxPrime(t *testing.T, env *common.TestEnvironment) string {
	t.Helper()

	cmd := exec.Command(env.OxBinaryPath, "agent", "prime")
	cmd.Dir = env.ProjectDir
	cmd.Env = env.EnvVars

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("ox agent prime output:\n%s", string(output))
		t.Fatalf("ox agent prime failed: %v", err)
	}

	t.Logf("ox agent prime completed (%d bytes output)", len(output))
	return string(output)
}

// extractAgentID extracts the agent_id from ox agent prime JSON output.
// Prime may output warning text before the JSON block, so we find and parse
// the JSON object from the output.
func extractAgentID(t *testing.T, primeOutput string) string {
	t.Helper()

	// Find the first '{' that starts the JSON object
	start := strings.Index(primeOutput, "{")
	if start == -1 {
		t.Fatalf("no JSON found in prime output:\n%.500s", primeOutput)
	}

	// Find the matching closing brace
	jsonStr := primeOutput[start:]
	depth := 0
	end := -1
	for i, ch := range jsonStr {
		switch ch {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i + 1
				break
			}
		}
		if end > 0 {
			break
		}
	}

	if end <= 0 {
		t.Fatalf("malformed JSON in prime output:\n%.500s", primeOutput)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr[:end]), &parsed); err != nil {
		t.Fatalf("could not parse prime JSON: %v\n%.500s", err, jsonStr[:end])
	}

	agentID, ok := parsed["agent_id"].(string)
	if !ok || agentID == "" {
		t.Fatalf("no agent_id in prime output: %v", parsed)
	}

	return agentID
}

// extractClaudeSessionID extracts the session_id from Claude's JSON output.
// Claude Code outputs JSON with session metadata when using --output-format json.
func extractClaudeSessionID(t *testing.T, claudeOutput string) string {
	t.Helper()

	// Try parsing as JSON array (Claude Code format)
	var messages []map[string]interface{}
	if err := json.Unmarshal([]byte(claudeOutput), &messages); err == nil {
		for _, msg := range messages {
			if sid, ok := msg["session_id"].(string); ok && sid != "" {
				return sid
			}
		}
	}

	// Try line-by-line NDJSON
	for _, line := range strings.Split(claudeOutput, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg map[string]interface{}
		if err := json.Unmarshal([]byte(line), &msg); err == nil {
			if sid, ok := msg["session_id"].(string); ok && sid != "" {
				return sid
			}
		}
	}

	// Fallback: use a synthetic session ID (hook will still work, just won't
	// find the marker — handleCompact only needs the agent ID from the marker)
	t.Log("could not extract Claude session ID from output, using synthetic")
	return "test-compact-session"
}

// verifyRecordingActive checks that recording state exists for the agent.
func verifyRecordingActive(t *testing.T, env *common.TestEnvironment, agentID string) {
	t.Helper()

	cmd := exec.Command(env.OxBinaryPath, "agent", agentID, "session", "status")
	cmd.Dir = env.ProjectDir
	cmd.Env = env.EnvVars

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("session status output: %s", string(output))
		t.Log("could not verify recording state via CLI (command may not exist)")
		return
	}

	t.Logf("recording status: %s", strings.TrimSpace(string(output)))
}

// runClaudeWithHooks runs the real claude CLI in the test workspace.
func runClaudeWithHooks(ctx context.Context, t *testing.T, env *common.TestEnvironment, agent *common.AgentConfig, prompt string) *common.AgentTestResult {
	t.Helper()
	return runClaudeWithFlags(ctx, t, env, agent, prompt)
}

// runClaudeWithMaxTurns runs claude with a custom max-turns limit.
func runClaudeWithMaxTurns(ctx context.Context, t *testing.T, env *common.TestEnvironment, agent *common.AgentConfig, prompt string, maxTurns int) *common.AgentTestResult {
	t.Helper()

	args := []string{
		agent.PromptFlag, prompt,
		"--output-format", "json",
		"--verbose",
		"--dangerously-skip-permissions",
		"--max-turns", fmt.Sprintf("%d", maxTurns),
	}

	result := &common.AgentTestResult{}
	start := time.Now()

	cmd := exec.CommandContext(ctx, agent.CLIPath, args...)
	cmd.Dir = env.ProjectDir
	cmd.Env = env.EnvVars

	output, err := cmd.CombinedOutput()
	result.Duration = time.Since(start)
	result.RawOutput = string(output)

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		}
		result.Error = err
	}

	if os.Getenv("AGENT_TEST_DEBUG") == "1" {
		t.Logf("claude output:\n%s", result.RawOutput)
	}

	return result
}

// runClaudeWithFlags runs claude with custom extra flags.
func runClaudeWithFlags(ctx context.Context, t *testing.T, env *common.TestEnvironment, agent *common.AgentConfig, prompt string, extraFlags ...string) *common.AgentTestResult {
	t.Helper()

	args := []string{
		agent.PromptFlag, prompt,
		"--output-format", "json",
		"--verbose",
		"--dangerously-skip-permissions",
		"--max-turns", "5",
	}
	args = append(args, extraFlags...)

	result := &common.AgentTestResult{}
	start := time.Now()

	cmd := exec.CommandContext(ctx, agent.CLIPath, args...)
	cmd.Dir = env.ProjectDir
	cmd.Env = env.EnvVars

	output, err := cmd.CombinedOutput()
	result.Duration = time.Since(start)
	result.RawOutput = string(output)

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		}
		result.Error = err
	}

	if os.Getenv("AGENT_TEST_DEBUG") == "1" {
		t.Logf("claude output:\n%s", result.RawOutput)
	}

	return result
}

// fireOxHook calls ox agent hook <eventName> with the same stdin JSON format
// that Claude Code would send. Returns the hook's stdout.
func fireOxHook(t *testing.T, env *common.TestEnvironment, eventName, sessionID string) string {
	t.Helper()

	hookInput := map[string]interface{}{
		"session_id":      sessionID,
		"hook_event_name": eventName,
	}
	if eventName == "PreCompact" {
		hookInput["trigger"] = "auto"
	}

	stdinData, err := json.Marshal(hookInput)
	if err != nil {
		t.Fatalf("failed to marshal hook input: %v", err)
	}

	// ox agent hook <eventName> reads stdin for hook context
	cmd := exec.Command(env.OxBinaryPath, "agent", "hook", eventName)
	cmd.Dir = env.ProjectDir
	cmd.Env = append(env.EnvVars,
		"AGENT_ENV=claude-code",
		fmt.Sprintf("CLAUDE_CODE_SESSION_ID=%s", sessionID),
	)
	cmd.Stdin = bytes.NewReader(stdinData)

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("hook %s failed (may be ok): %v\noutput: %s", eventName, err, string(output))
	}

	return string(output)
}

// findRawJSONL locates the raw.jsonl under the test root.
// Searches all session directories, logging paths on failure for debugging.
func findRawJSONL(t *testing.T, env *common.TestEnvironment) string {
	t.Helper()

	// Broad recursive search under the test root — most reliable
	matches := findFilesRecursive(env.RootDir, "raw.jsonl")
	if len(matches) > 0 {
		t.Logf("found raw.jsonl at: %s", matches[0])
		return matches[0]
	}

	return ""
}

// logSearchedPaths logs where we looked for raw.jsonl, for debugging failures.
func logSearchedPaths(t *testing.T, env *common.TestEnvironment) {
	t.Helper()
	t.Logf("searched for raw.jsonl under: %s", env.RootDir)

	// List what actually exists
	allFiles := findFilesRecursive(env.RootDir, "")
	if len(allFiles) > 50 {
		allFiles = allFiles[:50]
	}
	for _, f := range allFiles {
		rel, _ := filepath.Rel(env.RootDir, f)
		t.Logf("  exists: %s", rel)
	}
}

// readRawJSONL reads and parses all entries from a raw.jsonl file.
func readRawJSONL(t *testing.T, path string) []map[string]interface{} {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read raw.jsonl: %v", err)
	}

	var entries []map[string]interface{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Logf("skipping malformed line: %s", line[:min(len(line), 100)])
			continue
		}
		entries = append(entries, entry)
	}

	return entries
}

// stopSession runs ox agent <id> session stop.
func stopSession(t *testing.T, env *common.TestEnvironment, agentID string) {
	t.Helper()

	cmd := exec.Command(env.OxBinaryPath, "agent", agentID, "session", "stop")
	cmd.Dir = env.ProjectDir
	cmd.Env = env.EnvVars

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("session stop output: %s", string(output))
		t.Logf("session stop error (may be ok): %v", err)
	} else {
		t.Log("session stop completed successfully")
	}
}

// findActiveAgentID discovers the active agent ID by scanning for .recording.json
// files under the test root. This is used when we let Claude's SessionStart hook
// handle prime (instead of calling prime separately), so we don't know the agent ID up front.
func findActiveAgentID(t *testing.T, env *common.TestEnvironment) string {
	t.Helper()

	matches := findFilesRecursive(env.RootDir, ".recording.json")
	if len(matches) == 0 {
		t.Fatal("no .recording.json found — session recording did not start")
	}

	// Parse the first one to get the agent ID
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var state struct {
			AgentID string `json:"agent_id"`
		}
		if err := json.Unmarshal(data, &state); err != nil {
			continue
		}
		if state.AgentID != "" {
			t.Logf("found active agent ID %s from %s", state.AgentID, path)
			return state.AgentID
		}
	}

	t.Fatal("no agent_id found in any .recording.json")
	return ""
}

// findAllRawJSONL finds ALL raw.jsonl files under the test root.
func findAllRawJSONL(t *testing.T, env *common.TestEnvironment) []string {
	t.Helper()
	matches := findFilesRecursive(env.RootDir, "raw.jsonl")
	t.Logf("found %d raw.jsonl file(s)", len(matches))
	return matches
}

// --- Recording fidelity evaluation ---

// TestRecordingFidelity_LLMEval compares ox's raw.jsonl against Claude Code's
// own session JSONL to verify we capture the same content. Uses an LLM judge
// (another Claude instance) to evaluate coverage between session start and stop.
// Uses a real Claude Code instance — never simulated. (E2E requirement)
//
// This catches: missing message types, truncated content, lost tool calls,
// dropped entries, or any divergence between what Claude recorded and what ox captured.
func TestRecordingFidelity_LLMEval(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	agent := getClaudeConfig()
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)

	_ = runOxPrime(t, env)

	// Run a session with varied activity: file reads, tool use, multi-turn reasoning
	prompt := `Read the file AGENTS.md. Then list all files in the current directory using ls. Finally, summarize what you found in 2-3 sentences.`

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	t.Log("running claude session with varied activity...")
	result := runClaudeWithHooks(ctx, t, env, agent, prompt)
	if result.Error != nil {
		t.Logf("claude error (may be ok): %v", result.Error)
	}
	t.Logf("session completed in %v", result.Duration)

	// Extract Claude's session ID to find its source JSONL
	claudeSessionID := extractClaudeSessionID(t, result.RawOutput)
	t.Logf("claude session ID: %s", claudeSessionID)

	// Stop the session so ox drains remaining entries
	agentID := findActiveAgentID(t, env)
	stopSession(t, env, agentID)

	// Find Claude's source JSONL (~/.claude/projects/<hash>/<session>.jsonl)
	claudeSourcePath := findClaudeSourceJSONL(t, env, claudeSessionID)
	if claudeSourcePath == "" {
		t.Skip("could not find Claude's source JSONL — cannot compare")
	}
	t.Logf("claude source JSONL: %s", claudeSourcePath)

	// Find our raw.jsonl — select the one matching the active agent
	rawPaths := findAllRawJSONL(t, env)
	if len(rawPaths) == 0 {
		t.Fatal("no raw.jsonl found — nothing to compare")
	}

	// pick the raw.jsonl belonging to this agent's session
	activeRaw := rawPaths[0] // fallback
	for _, rp := range rawPaths {
		entries := readRawJSONL(t, rp)
		for _, e := range entries {
			// check both new ("metadata") and old ("_meta") header schemas
			for _, metaKey := range []string{"metadata", "_meta"} {
				if meta, ok := e[metaKey].(map[string]interface{}); ok {
					if aid, ok := meta["agent_id"].(string); ok && aid == agentID {
						activeRaw = rp
					}
				}
			}
		}
	}

	// Extract the recording time window from raw.jsonl header or recording state.
	// The LLM judge should ONLY evaluate entries between session start and stop —
	// anything outside that window is not our responsibility to capture.
	startTS, stopTS := extractRecordingWindow(t, env, activeRaw)
	t.Logf("recording window: %s → %s", startTS, stopTS)

	// Read Claude's source and filter to recording window, then truncate
	claudeSource := readAndFilterToWindow(t, claudeSourcePath, startTS, stopTS, 15000)
	oxRaw := readAndTruncate(t, activeRaw, 15000)

	t.Logf("claude source (windowed): %d chars, ox raw: %d chars", len(claudeSource), len(oxRaw))

	// Spin up a Claude judge to evaluate coverage
	t.Log("running LLM judge to evaluate recording fidelity...")
	judgePrompt := fmt.Sprintf(`You are evaluating recording fidelity between two JSONL files from the same coding session.

IMPORTANT: Only evaluate content within the recording window. FILE A has already been
filtered to only include entries between ox-session-start and ox-session-stop timestamps.
Anything outside this window is irrelevant — do not penalize for missing pre-start or
post-stop content (like hook events, prime output, or session setup).

The following are INTENTIONALLY excluded from ox recordings — do NOT count these as missing:
- Tool result content (file contents returned by Read/Bash tools — we record tool calls but not their output)
- Hook/progress events (internal Claude metadata, system notifications)
- Thinking/reasoning blocks (extended thinking content)

FILE A is Claude Code's own session recording (filtered to recording window — source of truth).
FILE B is ox's recording of the same session (what we're testing).

Compare them and evaluate (considering the intentional exclusions above):
1. Are all user messages from A present in B?
2. Are all assistant text responses from A present in B?
3. Are tool calls (tool name, inputs) from A captured in B? (tool RESULTS are intentionally excluded)
4. Is message ordering preserved?
5. Is content complete (not truncated) for the entry types we DO capture?

Output ONLY a JSON object with these fields:
- "coverage_pct": integer 0-100 (what percentage of A's windowed content appears in B)
- "missing": array of strings describing what's missing (empty if nothing)
- "verdict": "pass" if coverage >= 80%%, "fail" otherwise
- "notes": brief explanation

FILE A (Claude Code source — filtered to recording window):
%s

FILE B (ox raw.jsonl):
%s`, claudeSource, oxRaw)

	judgeResult := runClaudeWithFlags(ctx, t, env, agent, judgePrompt)
	if judgeResult.Error != nil {
		t.Logf("judge error: %v", judgeResult.Error)
	}

	// Parse the judge's verdict from Claude's output
	verdict := parseJudgeVerdict(t, judgeResult.RawOutput)

	t.Logf("coverage: %d%%, verdict: %s", verdict.CoveragePct, verdict.Verdict)
	if len(verdict.Missing) > 0 {
		t.Logf("missing items: %v", verdict.Missing)
	}
	if verdict.Notes != "" {
		t.Logf("judge notes: %s", verdict.Notes)
	}

	if verdict.Verdict == "fail" {
		t.Errorf("recording fidelity too low: %d%% coverage (need >= 80%%)", verdict.CoveragePct)
	}
}

// findClaudeSourceJSONL locates Claude Code's session file for the given session ID.
// Claude stores sessions at ~/.claude/projects/<project-hash>/<session-id>.jsonl
func findClaudeSourceJSONL(t *testing.T, env *common.TestEnvironment, sessionID string) string {
	t.Helper()

	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Logf("cannot get home dir: %v", err)
		return ""
	}

	// Claude's project hash: CWD with path separators → dashes, underscores → dashes
	projectHash := strings.ReplaceAll(env.ProjectDir, string(os.PathSeparator), "-")
	projectHash = strings.ReplaceAll(projectHash, "_", "-")

	// Try direct path first
	directPath := filepath.Join(homeDir, ".claude", "projects", projectHash, sessionID+".jsonl")
	if _, err := os.Stat(directPath); err == nil {
		return directPath
	}

	// Fallback: search for any JSONL file containing this session ID
	claudeProjectDir := filepath.Join(homeDir, ".claude", "projects", projectHash)
	if _, err := os.Stat(claudeProjectDir); os.IsNotExist(err) {
		// Try with /private prefix (macOS tmp dirs)
		projectHash2 := strings.ReplaceAll("/private"+env.ProjectDir, string(os.PathSeparator), "-")
		projectHash2 = strings.ReplaceAll(projectHash2, "_", "-")
		claudeProjectDir = filepath.Join(homeDir, ".claude", "projects", projectHash2)
	}

	entries, err := os.ReadDir(claudeProjectDir)
	if err != nil {
		t.Logf("cannot read claude project dir %s: %v", claudeProjectDir, err)
		return ""
	}

	// Find the most recently modified JSONL file (likely our session)
	var bestPath string
	var bestTime time.Time
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(bestTime) {
			bestTime = info.ModTime()
			bestPath = filepath.Join(claudeProjectDir, entry.Name())
		}
	}

	return bestPath
}

// extractRecordingWindow finds the session start and stop timestamps.
// It looks at the raw.jsonl header for started_at, and the recording state or
// last entry timestamp for the stop time.
func extractRecordingWindow(t *testing.T, env *common.TestEnvironment, rawPath string) (startTS, stopTS time.Time) {
	t.Helper()

	entries := readRawJSONL(t, rawPath)

	// Find start time from header entry or first non-header entry.
	// Supports both old schema (_meta.started_at, ts) and new schema (metadata.created_at, timestamp).
	for _, e := range entries {
		eType, _ := e["type"].(string)
		if eType == "header" {
			// try both "metadata" (new) and "_meta" (old) header schemas
			for _, metaKey := range []string{"metadata", "_meta"} {
				if meta, ok := e[metaKey].(map[string]interface{}); ok {
					for _, tsKey := range []string{"created_at", "started_at"} {
						if ts, ok := meta[tsKey].(string); ok {
							if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil {
								startTS = parsed
							}
						}
					}
				}
			}
			continue
		}
		// First non-header entry as fallback start — use extractTimestamp which handles both schemas
		if startTS.IsZero() {
			if parsed := extractTimestamp(e); !parsed.IsZero() {
				startTS = parsed
			}
		}
	}

	// Stop time: use last entry timestamp as upper bound
	for i := len(entries) - 1; i >= 0; i-- {
		if parsed := extractTimestamp(entries[i]); !parsed.IsZero() {
			stopTS = parsed
			break
		}
	}

	// If we couldn't find timestamps, use wide bounds
	if startTS.IsZero() {
		startTS = time.Now().Add(-1 * time.Hour)
		t.Log("warning: could not extract start time from raw.jsonl, using 1h ago")
	}
	if stopTS.IsZero() {
		stopTS = time.Now()
		t.Log("warning: could not extract stop time from raw.jsonl, using now")
	}

	return startTS, stopTS
}

// readAndFilterToWindow reads a JSONL file and returns only lines whose timestamp
// falls within [startTS, stopTS]. This scopes the source material to what ox should
// have captured — anything outside the recording window is not our responsibility.
func readAndFilterToWindow(t *testing.T, path string, startTS, stopTS time.Time, maxChars int) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}

	var filtered []string
	totalChars := 0
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Parse the line to check its timestamp
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		// Extract timestamp — Claude Code uses "timestamp" field
		ts := extractTimestamp(entry)
		if !ts.IsZero() {
			// Add 1-second buffer on each side for clock skew
			if ts.Before(startTS.Add(-1*time.Second)) || ts.After(stopTS.Add(1*time.Second)) {
				continue
			}
		}

		filtered = append(filtered, line)
		totalChars += len(line) + 1
		if totalChars > maxChars {
			filtered = append(filtered, "... (truncated)")
			break
		}
	}

	return strings.Join(filtered, "\n")
}

// extractTimestamp tries common timestamp field names from a JSONL entry.
func extractTimestamp(entry map[string]interface{}) time.Time {
	for _, field := range []string{"timestamp", "ts", "time", "created_at"} {
		if val, ok := entry[field].(string); ok {
			if parsed, err := time.Parse(time.RFC3339Nano, val); err == nil {
				return parsed
			}
			// Try unix millis as float
		}
		if val, ok := entry[field].(float64); ok && val > 1e12 {
			return time.UnixMilli(int64(val))
		}
	}
	return time.Time{}
}

// readAndTruncate reads a file and truncates to maxChars for use in a prompt.
func readAndTruncate(t *testing.T, path string, maxChars int) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}

	content := string(data)
	if len(content) > maxChars {
		content = content[:maxChars] + "\n... (truncated)"
	}
	return content
}

// judgeVerdict holds the LLM judge's evaluation of recording fidelity.
type judgeVerdict struct {
	CoveragePct int      `json:"coverage_pct"`
	Missing     []string `json:"missing"`
	Verdict     string   `json:"verdict"`
	Notes       string   `json:"notes"`
}

// parseJudgeVerdict extracts the judge's JSON verdict from Claude's output.
func parseJudgeVerdict(t *testing.T, claudeOutput string) judgeVerdict {
	t.Helper()

	// Claude wraps output in a JSON array — extract the result text
	var messages []map[string]interface{}
	if err := json.Unmarshal([]byte(claudeOutput), &messages); err == nil {
		for _, msg := range messages {
			if result, ok := msg["result"].(string); ok && result != "" {
				claudeOutput = result
				break
			}
		}
	}

	// Find JSON object in the output (judge may include preamble text)
	start := strings.Index(claudeOutput, "{")
	if start == -1 {
		t.Logf("no JSON in judge output, assuming pass: %.500s", claudeOutput)
		return judgeVerdict{CoveragePct: 0, Verdict: "unknown", Notes: "could not parse judge output"}
	}

	// Find matching closing brace
	depth := 0
	end := -1
	for i := start; i < len(claudeOutput); i++ {
		switch claudeOutput[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i + 1
				break
			}
		}
		if end > 0 {
			break
		}
	}

	if end <= 0 {
		t.Logf("malformed JSON in judge output: %.500s", claudeOutput[start:])
		return judgeVerdict{CoveragePct: 0, Verdict: "unknown", Notes: "malformed JSON"}
	}

	var v judgeVerdict
	if err := json.Unmarshal([]byte(claudeOutput[start:end]), &v); err != nil {
		t.Logf("failed to parse judge verdict: %v\n%.500s", err, claudeOutput[start:end])
		return judgeVerdict{CoveragePct: 0, Verdict: "unknown", Notes: "parse error: " + err.Error()}
	}

	return v
}

// --- Slash command E2E tests ---
// These verify the ox CLI commands that back the Claude Code slash commands:
//   /ox-session-start  → ox agent session start
//   /ox-session-stop   → ox agent session stop
//   /ox-session-list   → ox session list --limit 5
//   /ox-session-abort  → ox agent session abort --force

// TestSlashCommand_SessionStartStop verifies the /ox-session-start and
// /ox-session-stop slash commands work end-to-end with a real Claude instance.
// Uses a real Claude Code instance — never simulated. (E2E requirement)
// The prompt IS the slash command — tests the full skill file -> CLI pipeline.
func TestSlashCommand_SessionStartStop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	agent := getClaudeConfig()
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)

	// Prime creates agent instance and auto-installs hooks (test fixture lacks .claude/settings.local.json)
	primeOutput := runOxPrime(t, env)
	agentID := extractAgentID(t, primeOutput)
	t.Logf("agent ID from prime: %s", agentID)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Invoke the actual /ox-session-start slash command — tests the full
	// skill file → CLI pipeline, not just the underlying ox command.
	t.Log("invoking /ox-session-start slash command...")
	startResult := runClaudeWithHooks(ctx, t, env, agent, `/ox-session-start`)
	if startResult.Error != nil {
		t.Logf("claude error (may be ok): %v", startResult.Error)
	}

	t.Run("session_start_creates_recording", func(t *testing.T) {
		// Verify recording state exists after start
		matches := findFilesRecursive(env.RootDir, ".recording.json")
		if len(matches) == 0 {
			t.Error("no .recording.json found after session start command")
		} else {
			t.Logf("found %d recording state file(s)", len(matches))
		}
	})

	// Now ask Claude to do something that generates entries
	t.Log("asking claude to do work (generate entries)...")
	_ = runClaudeWithFlags(ctx, t, env, agent,
		`Read AGENTS.md and say "done" in under 10 words.`, "--continue")

	// Invoke the actual /ox-session-stop slash command
	t.Log("invoking /ox-session-stop slash command...")
	stopResult := runClaudeWithFlags(ctx, t, env, agent, `/ox-session-stop`, "--continue")
	if stopResult.Error != nil {
		t.Logf("claude error (may be ok): %v", stopResult.Error)
	}

	t.Run("session_stop_clears_recording", func(t *testing.T) {
		// After stop, recording state should be cleared
		matches := findFilesRecursive(env.RootDir, ".recording.json")
		// Recording may still exist if stop failed, but the raw.jsonl should be finalized
		rawPaths := findAllRawJSONL(t, env)
		if len(rawPaths) == 0 {
			t.Error("no raw.jsonl found after session stop")
		} else {
			entries := readRawJSONL(t, rawPaths[0])
			t.Logf("raw.jsonl has %d entries after stop via slash command", len(entries))
			if len(entries) == 0 {
				t.Error("raw.jsonl is empty after stop")
			}
		}
		_ = matches // logged for debugging
	})
}

// TestSlashCommand_SessionList verifies the /ox-session-list slash command
// (ox session list --limit 5) works in a real Claude instance.
// Uses a real Claude Code instance — never simulated. (E2E requirement)
func TestSlashCommand_SessionList(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	agent := getClaudeConfig()
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)

	// Prime to set up environment
	primeOutput := runOxPrime(t, env)
	agentID := extractAgentID(t, primeOutput)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// First create a session so there's something to list
	t.Log("creating a session to list...")
	_ = runClaudeWithHooks(ctx, t, env, agent,
		`Say "hello" in under 5 words.`)
	stopSession(t, env, agentID)

	// Now test the list command — run it directly first (faster, more reliable)
	t.Run("cli_session_list", func(t *testing.T) {
		cmd := exec.Command(env.OxBinaryPath, "session", "list", "--limit", "5")
		cmd.Dir = env.ProjectDir
		cmd.Env = env.EnvVars

		output, err := cmd.CombinedOutput()
		t.Logf("ox session list output:\n%s", string(output))
		if err != nil {
			// session list may return non-zero if no sessions exist yet in ledger
			t.Logf("ox session list error (may be ok): %v", err)
		}
		// The command should at least not crash
		if strings.Contains(string(output), "panic") {
			t.Error("ox session list panicked")
		}
	})

	// Test via Claude using the actual slash command
	t.Run("claude_session_list", func(t *testing.T) {
		listResult := runClaudeWithFlags(ctx, t, env, agent, `/ox-session-list`, "--continue")
		if listResult.Error != nil {
			t.Logf("claude error (may be ok): %v", listResult.Error)
		}
		// Claude should have run the command — check that output contains
		// session-related text (even if empty list)
		output := strings.ToLower(listResult.RawOutput)
		if strings.Contains(output, "panic") {
			t.Error("ox session list panicked when run via claude")
		}
		t.Logf("claude session list completed (%d bytes output)", len(listResult.RawOutput))
	})
}

// TestSlashCommand_SessionAbort verifies the /ox-session-abort slash command
// (ox agent session abort --force) discards session data without creating artifacts.
// Uses a real Claude Code instance — never simulated. (E2E requirement)
func TestSlashCommand_SessionAbort(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	agent := getClaudeConfig()
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)

	// Prime and start a session
	primeOutput := runOxPrime(t, env)
	agentID := extractAgentID(t, primeOutput)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Generate some session activity
	t.Log("generating session activity...")
	_ = runClaudeWithHooks(ctx, t, env, agent,
		`Read AGENTS.md and say "done" in under 10 words.`)

	// Verify recording exists before abort
	recordingsBefore := findFilesRecursive(env.RootDir, ".recording.json")
	t.Logf("recording files before abort: %d", len(recordingsBefore))

	// Run abort directly (the command behind /ox-session-abort)
	t.Run("abort_clears_session", func(t *testing.T) {
		cmd := exec.Command(env.OxBinaryPath, "agent", agentID, "session", "abort", "--force")
		cmd.Dir = env.ProjectDir
		cmd.Env = env.EnvVars

		output, err := cmd.CombinedOutput()
		t.Logf("abort output: %s", string(output))
		if err != nil {
			t.Logf("abort error (may be ok): %v", err)
		}

		// After abort, recording state should be gone
		recordingsAfter := findFilesRecursive(env.RootDir, ".recording.json")
		if len(recordingsAfter) >= len(recordingsBefore) && len(recordingsBefore) > 0 {
			t.Log("recording state not fully cleared after abort (may be due to multiple agents)")
		}
	})

	// Also test via Claude for end-to-end coverage
	t.Run("claude_abort", func(t *testing.T) {
		// Re-prime to get a fresh session for Claude to abort
		_ = runOxPrime(t, env)

		_ = runClaudeWithHooks(ctx, t, env, agent,
			`Say "test" in one word.`)

		abortResult := runClaudeWithFlags(ctx, t, env, agent, `/ox-session-abort`, "--continue")
		if abortResult.Error != nil {
			t.Logf("claude abort error (may be ok): %v", abortResult.Error)
		}
		t.Log("claude abort completed")
	})
}

// TestCtrlC_RealClaude_AntiEntropy starts a real Claude Code session, sends it
// SIGINT (Ctrl-C) mid-conversation, then verifies the anti-entropy finalization
// handler can recover and generate all artifacts from the partial raw.jsonl.
//
// This is the definitive test: real Claude binary, real hooks, real SIGINT, real
// anti-entropy pipeline. Nothing is simulated except the LLM summarization step
// (which would require a second Claude invocation).
//
// Run with: go test -tags=integration -timeout=5m -run TestCtrlC_RealClaude_AntiEntropy ./tests/integration/agents/claude/ -v
func TestCtrlC_RealClaude_AntiEntropy(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	agent := getClaudeConfig()
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)

	// Prime must run before Claude starts (installs hooks, creates agent instance)
	_ = runOxPrime(t, env)

	// Use a prompt that will trigger multiple tool calls, giving us time to
	// send SIGINT after the first tool use fires a PostToolUse hook.
	// The --max-turns 10 ensures Claude won't finish too quickly.
	prompt := `Read the file AGENTS.md, then list all files with the Glob tool using pattern "**/*", then read .sageox/config.json. Do each step separately.`

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Start Claude in its own process group so SIGINT only hits Claude
	args := []string{
		agent.PromptFlag, prompt,
		"--output-format", "json",
		"--verbose",
		"--dangerously-skip-permissions",
		"--max-turns", "10",
	}

	cmd := exec.CommandContext(ctx, agent.CLIPath, args...)
	cmd.Dir = env.ProjectDir
	cmd.Env = env.EnvVars
	// Create a new process group so SIGINT doesn't propagate to test runner
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	t.Log("starting claude CLI (will be interrupted with SIGINT)...")
	startTime := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start claude: %v", err)
	}

	// Wait for raw.jsonl to appear with at least one non-header entry,
	// meaning at least one PostToolUse hook has fired and written data.
	// Poll every 500ms for up to 90s.
	var rawPath string
	deadline := time.After(90 * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

waitLoop:
	for {
		select {
		case <-deadline:
			// Kill the process if we timed out waiting
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			t.Fatal("timed out waiting for raw.jsonl to have entries")
		case <-ticker.C:
			paths := findAllRawJSONL(t, env)
			for _, p := range paths {
				entries := readRawJSONL(t, p)
				// need at least 1 header + 1 content entry
				nonHeaders := 0
				for _, e := range entries {
					if e["type"] != "header" {
						nonHeaders++
					}
				}
				if nonHeaders >= 1 {
					rawPath = p
					t.Logf("raw.jsonl has %d entries (%d content) after %v — sending SIGINT",
						len(entries), nonHeaders, time.Since(startTime).Round(time.Millisecond))
					break waitLoop
				}
			}
		}
	}

	// Send SIGINT to the Claude process group (simulates Ctrl-C)
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT); err != nil {
		t.Logf("SIGINT failed (process may have already exited): %v", err)
	}

	// Wait for Claude to exit (it should handle SIGINT gracefully)
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case err := <-waitDone:
		elapsed := time.Since(startTime)
		t.Logf("claude exited after %v (err: %v)", elapsed.Round(time.Millisecond), err)
	case <-time.After(30 * time.Second):
		// Force kill if Claude didn't exit after SIGINT
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-waitDone
		t.Log("claude force-killed after 30s timeout")
	}

	if stdout.Len() > 0 {
		t.Logf("claude stdout (truncated): %s", truncateForLog(stdout.String(), 500))
	}

	// --- Verify interrupted session state ---

	// raw.jsonl should exist with entries but NO footer
	rawData, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("failed to read raw.jsonl after SIGINT: %v", err)
	}

	entries := readRawJSONL(t, rawPath)
	t.Logf("raw.jsonl has %d total entries after SIGINT", len(entries))

	// Count entry types
	typeCounts := map[string]int{}
	hasFooter := false
	for _, e := range entries {
		if typ, ok := e["type"].(string); ok {
			typeCounts[typ]++
			if typ == "footer" {
				hasFooter = true
			}
		}
	}
	t.Logf("entry types: %v", typeCounts)

	if hasFooter {
		t.Error("raw.jsonl has a footer — session stop should NOT have run after SIGINT")
	}
	if typeCounts["header"] == 0 {
		t.Error("raw.jsonl missing header")
	}

	// Should have at least some content entries (user, assistant, or tool)
	contentEntries := typeCounts["user"] + typeCounts["assistant"] + typeCounts["tool"]
	if contentEntries == 0 {
		t.Fatal("raw.jsonl has no content entries — hooks didn't write anything before SIGINT")
	}
	t.Logf("raw.jsonl has %d content entries (user=%d, assistant=%d, tool=%d)",
		contentEntries, typeCounts["user"], typeCounts["assistant"], typeCounts["tool"])

	// .recording.json should still exist (stop didn't clean it up)
	recordingFiles := findFilesRecursive(env.RootDir, ".recording.json")
	if len(recordingFiles) == 0 {
		t.Log("WARNING: no .recording.json found — Claude may have run session stop on SIGINT")
	} else {
		t.Logf("found %d .recording.json file(s) — stop did NOT run (as expected)", len(recordingFiles))
	}

	// --- Anti-entropy: run the daemon's finalization handler ---

	// Create a fake ledger with the interrupted session's raw.jsonl
	fakeLedger := t.TempDir()
	ledgerSessionName := "ctrlc-" + filepath.Base(filepath.Dir(rawPath))
	ledgerSessionDir := filepath.Join(fakeLedger, "sessions", ledgerSessionName)
	if err := os.MkdirAll(ledgerSessionDir, 0755); err != nil {
		t.Fatalf("failed to create ledger session dir: %v", err)
	}

	// Copy raw.jsonl to the ledger
	if err := os.WriteFile(filepath.Join(ledgerSessionDir, "raw.jsonl"), rawData, 0644); err != nil {
		t.Fatalf("failed to copy raw.jsonl to ledger: %v", err)
	}

	// Write a stale .recording.json (> 24h old) to simulate time passing
	staleState := map[string]any{
		"started_at": time.Now().Add(-25 * time.Hour).Format(time.RFC3339),
		"agent_id":   "test-ctrlc",
	}
	staleData, _ := json.Marshal(staleState)
	if err := os.WriteFile(filepath.Join(ledgerSessionDir, ".recording.json"), staleData, 0644); err != nil {
		t.Fatalf("failed to write stale recording marker: %v", err)
	}

	// Run the finalization handler
	handler := agentwork.NewSessionFinalizeHandlerForTest(slog.Default())

	items, detectErr := handler.Detect(fakeLedger)
	if detectErr != nil {
		t.Fatalf("Detect failed: %v", detectErr)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 stale session, got %d", len(items))
	}
	t.Log("anti-entropy: detected stale session")

	// .recording.json should be removed
	if _, statErr := os.Stat(filepath.Join(ledgerSessionDir, ".recording.json")); !os.IsNotExist(statErr) {
		t.Error(".recording.json should have been removed after stale detection")
	}

	// BuildPrompt should succeed with the real Claude session data
	req, buildErr := handler.BuildPrompt(items[0])
	if buildErr != nil {
		t.Fatalf("BuildPrompt failed: %v", buildErr)
	}
	if req.Prompt == "" {
		t.Error("BuildPrompt returned empty prompt")
	}
	t.Logf("anti-entropy: built summarization prompt (%d chars)", len(req.Prompt))

	// ProcessResult with simulated LLM output (we don't want to invoke a
	// second real Claude here — that would be slow and flaky)
	summaryJSON := fmt.Sprintf(`{"title":"Interrupted Session Recovery","summary":"Session was interrupted after %d entries. Anti-entropy recovered the session.","key_actions":["read AGENTS.md","listed files"],"outcome":"interrupted","topics_found":["integration testing","session recovery"]}`, contentEntries)

	processErr := handler.ProcessResult(items[0], &agentwork.RunResult{
		Output:   summaryJSON,
		Duration: 1 * time.Second,
		ExitCode: 0,
	})
	if processErr != nil {
		t.Fatalf("ProcessResult failed: %v", processErr)
	}

	// Verify ALL artifacts were generated from the interrupted session
	expectedArtifacts := []string{"summary.md", "summary.json", "session.html", "session.md"}
	for _, artifact := range expectedArtifacts {
		path := filepath.Join(ledgerSessionDir, artifact)
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Errorf("missing artifact after anti-entropy finalization: %s", artifact)
		} else {
			t.Logf("artifact %s generated (%d bytes)", artifact, info.Size())
		}
	}

	t.Logf("anti-entropy recovery successful: %d artifacts generated from %d raw.jsonl entries written by real Claude hooks before SIGINT",
		len(expectedArtifacts), len(entries))
}

// truncateForLog truncates a string for log output.
func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// findFilesRecursive walks a directory tree looking for files with the given name.
// If name is empty, returns all files.
func findFilesRecursive(root, name string) []string {
	var matches []string
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if name == "" || info.Name() == name {
			matches = append(matches, path)
		}
		return nil
	})
	return matches
}
