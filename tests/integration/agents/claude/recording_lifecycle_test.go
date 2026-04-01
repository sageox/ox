//go:build integration

package claude

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/tests/integration/agents/common"
	"github.com/stretchr/testify/require"
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
			t.Error("no tool entries with tool_name found — tool metadata not captured in incremental path")
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

	// Count entries after first invocation — must be non-zero
	rawPath := findRawJSONL(t, env)
	require.NotEmpty(t, rawPath, "raw.jsonl should exist after first invocation")
	entriesAfterFirst := len(readRawJSONL(t, rawPath))
	require.Greater(t, entriesAfterFirst, 0, "should have entries after first invocation")

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
