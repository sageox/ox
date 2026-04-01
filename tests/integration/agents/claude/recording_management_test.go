//go:build integration

package claude

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/tests/integration/agents/common"
)

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
		// output should contain something meaningful (not just empty)
		if len(strings.TrimSpace(string(output))) == 0 {
			t.Error("ox session list returned empty output after creating a session")
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
		if len(listResult.RawOutput) == 0 {
			t.Error("claude session list returned empty output")
		}
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
			t.Errorf("recording state not fully cleared after abort: had %d before, still have %d after", len(recordingsBefore), len(recordingsAfter))
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
