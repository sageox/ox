//go:build integration

package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/tests/integration/agents/common"
)

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
			t.Errorf("compact hook output doesn't contain agent_id — re-prime may have failed\noutput: %.200s", hookResult)
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
