//go:build integration

// Tests for session URL disambiguation in commit trailers and subagent prime output.
//
// These verify that when multiple agents record sessions concurrently:
//   - Git commit hooks link commits to the CORRECT session (not first-found)
//   - Subagent prime output contains the PARENT session URL (not the subagent's own)
//
// Regression tests for: wrong session recording linked to PRs and commits.
//
// Scenario: Conductor runs multiple agents in worktrees. Each agent has its own
// SAGEOX_AGENT_ID and session recording. Commits from agent A must link to
// agent A's session, not agent B's session that happens to sort first.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/tests/integration/agents/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCommitTrailer_ConcurrentAgents verifies that when two agents record
// sessions concurrently, a commit from one agent gets THAT agent's session URL
// in the trailer, not the other agent's.
//
// This is the core regression test for the "wrong session in PR" bug.
// Does NOT require real Claude — exercises the ox CLI hook chain directly.
func TestCommitTrailer_ConcurrentAgents(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := common.SetupTestEnvironment(t)

	// Step 1: Prime two agents (simulating Conductor's parallel agent model).
	// Each gets a unique agent ID and starts its own session recording.
	primeOutputA := runOxPrimeWithEnv(t, env, nil)
	agentIDA := extractAgentID(t, primeOutputA)
	require.NotEmpty(t, agentIDA, "agent A should get an ID")

	// Fire SessionStart for agent A (creates recording state)
	fireOxHookWithAgent(t, env, "SessionStart", "session-a-"+agentIDA, agentIDA)

	// Find agent A's recording to get its session name
	recordingA := findRecordingForAgent(t, env, agentIDA)
	require.NotNil(t, recordingA, "agent A should have a .recording.json")
	sessionNameA := filepath.Base(recordingA.SessionPath)
	t.Logf("agent A: id=%s session=%s", agentIDA, sessionNameA)

	// Prime agent B (different agent ID, concurrent recording)
	primeOutputB := runOxPrimeWithEnv(t, env, map[string]string{
		// clear SAGEOX_AGENT_ID so prime generates a new one
		"SAGEOX_AGENT_ID": "",
	})
	agentIDB := extractAgentID(t, primeOutputB)
	require.NotEmpty(t, agentIDB, "agent B should get an ID")
	require.NotEqual(t, agentIDA, agentIDB, "agents must have different IDs")

	fireOxHookWithAgent(t, env, "SessionStart", "session-b-"+agentIDB, agentIDB)

	recordingB := findRecordingForAgent(t, env, agentIDB)
	require.NotNil(t, recordingB, "agent B should have a .recording.json")
	sessionNameB := filepath.Base(recordingB.SessionPath)
	t.Logf("agent B: id=%s session=%s", agentIDB, sessionNameB)

	// Verify we have two concurrent recordings
	require.NotEqual(t, sessionNameA, sessionNameB, "sessions must be different")

	// Step 2: Agent A makes a commit. The prepare-commit-msg hook should
	// link to agent A's session, not agent B's.
	commitMsg := createCommitWithAgent(t, env, agentIDA, "test commit from agent A")

	// Step 3: Verify the commit trailer has agent A's session URL
	assert.Contains(t, commitMsg, "SageOx-Session:",
		"commit should have a SageOx-Session trailer")
	assert.Contains(t, commitMsg, sessionNameA,
		"commit trailer must contain agent A's session name, not agent B's")
	assert.NotContains(t, commitMsg, sessionNameB,
		"commit trailer must NOT contain agent B's session name")

	// Step 4: Agent B makes a commit. Should link to agent B's session.
	commitMsgB := createCommitWithAgent(t, env, agentIDB, "test commit from agent B")

	assert.Contains(t, commitMsgB, "SageOx-Session:",
		"agent B's commit should have a SageOx-Session trailer")
	assert.Contains(t, commitMsgB, sessionNameB,
		"agent B's commit trailer must contain agent B's session name")
	assert.NotContains(t, commitMsgB, sessionNameA,
		"agent B's commit trailer must NOT contain agent A's session name")
}

// TestSubagentPrime_UsesParentSessionURL verifies that when a subagent calls
// ox agent prime, the session URL in the output points to the PARENT session,
// not the subagent's own session. This ensures PRs created by subagents link
// to the parent session that humans care about.
func TestSubagentPrime_UsesParentSessionURL(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := common.SetupTestEnvironment(t)

	// Step 1: Prime the parent agent and start recording
	primeOutputParent := runOxPrimeWithEnv(t, env, nil)
	parentAgentID := extractAgentID(t, primeOutputParent)
	require.NotEmpty(t, parentAgentID)

	fireOxHookWithAgent(t, env, "SessionStart", "session-parent-"+parentAgentID, parentAgentID)

	parentRecording := findRecordingForAgent(t, env, parentAgentID)
	require.NotNil(t, parentRecording)
	parentSessionName := filepath.Base(parentRecording.SessionPath)
	t.Logf("parent: id=%s session=%s", parentAgentID, parentSessionName)

	// Step 2: Subagent primes with parent's SAGEOX_AGENT_ID in environment
	// (this is what happens when Claude Code spawns a subagent — the parent's
	// env vars are inherited, then prime detects the parent and generates a new ID)
	subagentPrimeOutput := runOxPrimeWithEnv(t, env, map[string]string{
		"SAGEOX_AGENT_ID": parentAgentID, // inherited from parent
	})
	subagentID := extractAgentID(t, subagentPrimeOutput)
	require.NotEmpty(t, subagentID)
	require.NotEqual(t, parentAgentID, subagentID, "subagent gets its own ID")
	t.Logf("subagent: id=%s", subagentID)

	// Step 3: Verify subagent's prime output contains the PARENT session URL
	assert.Contains(t, subagentPrimeOutput, parentSessionName,
		"subagent prime output should contain parent's session name in the session URL")

	// If subagent also starts recording, its own session name should NOT be the URL
	fireOxHookWithAgent(t, env, "SessionStart", "session-sub-"+subagentID, subagentID)
	subRecording := findRecordingForAgent(t, env, subagentID)
	if subRecording != nil {
		subSessionName := filepath.Base(subRecording.SessionPath)
		t.Logf("subagent session: %s", subSessionName)

		// Re-prime subagent to get updated output with both sessions active
		rePrimeOutput := runOxPrimeWithEnv(t, env, map[string]string{
			"SAGEOX_AGENT_ID": parentAgentID,
		})

		// Should still use parent's session, not subagent's
		assert.Contains(t, rePrimeOutput, parentSessionName,
			"re-primed subagent should still reference parent session")
	}
}

// TestCommitTrailer_NoAgentID_FallsBack verifies that commits made without
// SAGEOX_AGENT_ID (human commits) still get a session trailer via workspace
// matching. This is the "human doing git commit while an AI session is recording"
// scenario.
func TestCommitTrailer_NoAgentID_FallsBack(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	env := common.SetupTestEnvironment(t)

	// Prime starts a session recording automatically (auto-record).
	// Don't fire SessionStart separately — prime's auto-start is the only recording.
	_ = runOxPrimeWithEnv(t, env, nil)

	// Find whatever recording exists (prime auto-created it)
	allRecordings := findAllRecordings(t, env)
	require.NotEmpty(t, allRecordings, "prime should have auto-started a recording")
	sessionName := filepath.Base(allRecordings[0].SessionPath)
	t.Logf("active recording: agent=%s session=%s", allRecordings[0].AgentID, sessionName)

	// Commit WITHOUT SAGEOX_AGENT_ID (simulating human commit)
	commitMsg := createCommitWithAgent(t, env, "", "human commit without agent context")

	assert.Contains(t, commitMsg, "SageOx-Session:",
		"human commit should get a session trailer via workspace matching")
	assert.Contains(t, commitMsg, sessionName,
		"human commit should link to the workspace's active session")
}

// TestRealClaude_CommitDuringSession_CorrectTrailer verifies the full E2E flow:
// real Claude Code session → agent makes a commit → commit trailer has the
// correct session URL. This is the ultimate verification that the fix works
// in production.
//
// Key: Claude's SessionStart hook creates its own agent via `ox agent prime`.
// The pre-prime (needed to install hooks) creates a different agent. The commit
// trailer must link to Claude's OWN agent's session, which is the one whose
// SAGEOX_AGENT_ID is in Claude's environment when `git commit` runs.
func TestRealClaude_CommitDuringSession_CorrectTrailer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	agent := getClaudeConfig()
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)

	// Install the prepare-commit-msg git hook (normally done by `ox init`)
	installGitHook(t, env)

	// Prime before Claude starts. This installs Claude Code hooks in
	// .claude/settings and creates agent A. When Claude starts, its
	// SessionStart hook fires and creates agent B (Claude's actual agent).
	_ = runOxPrime(t, env)

	prompt := `Do these steps using the Bash tool:
1. echo "session link test" > test-session-link.txt
2. git add test-session-link.txt
3. git commit -m "add session link test file"
4. git log -1 --format=%B
Output ONLY the git log result.`

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()

	t.Log("running real Claude session with commit...")
	result := runClaudeWithFlags(ctx, t, env, agent, prompt)
	if result.Error != nil {
		t.Logf("claude error (may be ok): %v", result.Error)
	}

	// Check git log for the commit and its trailer
	gitLog := exec.Command("git", "log", "--all", "-1", "--format=%B")
	gitLog.Dir = env.ProjectDir
	gitLog.Env = env.EnvVars
	logOutput, err := gitLog.CombinedOutput()
	commitBody := string(logOutput)
	t.Logf("commit message:\n%s", commitBody)

	if err != nil || strings.TrimSpace(commitBody) == "Initial commit" {
		t.Fatal("Claude did not create a commit — E2E test cannot verify trailer")
	}

	// The commit MUST have the session trailer (hook is installed)
	require.Contains(t, commitBody, "SageOx-Session:",
		"commit must have SageOx-Session trailer — prepare-commit-msg hook is installed")

	// Extract the session name from the trailer URL
	trailerSessionName := extractSessionNameFromTrailer(t, commitBody)
	require.NotEmpty(t, trailerSessionName, "should be able to extract session name from trailer")

	// The trailer's session must belong to an ACTIVE recording — not a stale/random one
	allRecordings := findAllRecordings(t, env)
	t.Logf("found %d recording(s):", len(allRecordings))
	found := false
	for _, rec := range allRecordings {
		sessionName := filepath.Base(rec.SessionPath)
		t.Logf("  agent=%s session=%s", rec.AgentID, sessionName)
		if sessionName == trailerSessionName {
			found = true
			t.Logf("  ^ MATCHED trailer")
		}
	}
	require.True(t, found,
		"commit trailer session %q must match an active recording", trailerSessionName)
}

// TestRealClaude_SubagentCommit_LinksParentSession simulates Conductor's model:
// a parent agent starts recording, then a subagent (real Claude) makes a commit.
// The commit trailer must link to the PARENT session, not the subagent's.
//
// This is the Conductor scenario: multiple agents in parallel, each with their
// own worktree/workspace, but sharing the same project root for session state.
func TestRealClaude_SubagentCommit_LinksParentSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	agent := getClaudeConfig()
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)

	// Install git hook
	installGitHook(t, env)

	// Step 1: Prime the "parent" agent and start its session recording.
	// This simulates the orchestrator agent in Conductor.
	parentPrimeOutput := runOxPrimeWithEnv(t, env, nil)
	parentAgentID := extractAgentID(t, parentPrimeOutput)
	t.Logf("parent agent: %s", parentAgentID)

	fireOxHookWithAgent(t, env, "SessionStart", "session-parent-"+parentAgentID, parentAgentID)
	parentRecording := findRecordingForAgent(t, env, parentAgentID)
	require.NotNil(t, parentRecording, "parent recording must exist")
	parentSessionName := filepath.Base(parentRecording.SessionPath)
	t.Logf("parent session: %s", parentSessionName)

	// Step 2: Launch real Claude as a "subagent".
	// Pass the parent's SAGEOX_AGENT_ID so the subagent's prime detects it as parent.
	// This is exactly what happens in Conductor — the parent's env is inherited.
	subagentEnv := append([]string{}, env.EnvVars...)
	subagentEnv = setEnvInSlice(subagentEnv, "SAGEOX_AGENT_ID", parentAgentID)

	prompt := `Do these steps using the Bash tool:
1. echo "subagent work" > subagent-output.txt
2. git add subagent-output.txt
3. git commit -m "subagent commit"
4. git log -1 --format=%B
Output ONLY the git log result.`

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()

	t.Log("running real Claude as subagent with commit...")
	args := []string{
		agent.PromptFlag, prompt,
		"--output-format", "json",
		"--verbose",
		"--dangerously-skip-permissions",
		"--max-turns", "5",
	}

	cmd := exec.CommandContext(ctx, agent.CLIPath, args...)
	cmd.Dir = env.ProjectDir
	cmd.Env = subagentEnv

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("subagent claude error (may be ok): %v", err)
	}
	t.Logf("subagent claude output: %.500s", string(output))

	// Step 3: Check the commit trailer
	gitLog := exec.Command("git", "log", "--all", "-1", "--format=%B")
	gitLog.Dir = env.ProjectDir
	gitLog.Env = env.EnvVars
	logOutput, logErr := gitLog.CombinedOutput()
	commitBody := string(logOutput)
	t.Logf("subagent commit message:\n%s", commitBody)

	if logErr != nil || strings.TrimSpace(commitBody) == "Initial commit" {
		t.Fatal("subagent Claude did not create a commit — E2E test cannot verify trailer")
	}

	// The commit must have a session trailer
	require.Contains(t, commitBody, "SageOx-Session:",
		"subagent commit must have SageOx-Session trailer")

	// The trailer must reference the PARENT session, not the subagent's own
	assert.Contains(t, commitBody, parentSessionName,
		"subagent's commit trailer must link to PARENT session, not its own")
}

// installGitHook installs the ox prepare-commit-msg hook in the test project.
// This is normally done by `ox init` but the test fixture skips init.
func installGitHook(t *testing.T, env *common.TestEnvironment) {
	t.Helper()

	hooksDir := filepath.Join(env.ProjectDir, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0755))

	hookContent := fmt.Sprintf(`#!/bin/sh
# ox prepare-commit-msg hook (test)
if command -v %s >/dev/null 2>&1; then
  %s hooks commit-msg --msg-file "$1" --source "${2:-}" 2>/dev/null || true
fi
`, env.OxBinaryPath, env.OxBinaryPath)

	hookPath := filepath.Join(hooksDir, "prepare-commit-msg")
	require.NoError(t, os.WriteFile(hookPath, []byte(hookContent), 0755))
	t.Logf("installed prepare-commit-msg hook at %s", hookPath)
}

// --- Test helpers ---

// runOxPrimeWithEnv runs ox agent prime with optional extra/override env vars.
func runOxPrimeWithEnv(t *testing.T, env *common.TestEnvironment, extraEnv map[string]string) string {
	t.Helper()

	cmd := exec.Command(env.OxBinaryPath, "agent", "prime")
	cmd.Dir = env.ProjectDir
	cmd.Env = append([]string{}, env.EnvVars...) // copy to avoid mutation

	for k, v := range extraEnv {
		if v == "" {
			// remove the var
			prefix := k + "="
			filtered := cmd.Env[:0]
			for _, e := range cmd.Env {
				if !strings.HasPrefix(e, prefix) {
					filtered = append(filtered, e)
				}
			}
			cmd.Env = filtered
		} else {
			cmd.Env = setEnvInSlice(cmd.Env, k, v)
		}
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("ox agent prime output:\n%s", string(output))
		t.Fatalf("ox agent prime failed: %v", err)
	}

	return string(output)
}

// fireOxHookWithAgent fires an ox hook with a specific agent ID in the environment.
func fireOxHookWithAgent(t *testing.T, env *common.TestEnvironment, eventName, sessionID, agentID string) {
	t.Helper()

	hookInput := map[string]interface{}{
		"session_id":      sessionID,
		"hook_event_name": eventName,
	}
	stdinData, err := json.Marshal(hookInput)
	require.NoError(t, err)

	cmd := exec.Command(env.OxBinaryPath, "agent", "hook", eventName)
	cmd.Dir = env.ProjectDir
	envVars := append([]string{}, env.EnvVars...)
	envVars = setEnvInSlice(envVars, "AGENT_ENV", "claude-code")
	envVars = setEnvInSlice(envVars, "CLAUDE_CODE_SESSION_ID", sessionID)
	if agentID != "" {
		envVars = setEnvInSlice(envVars, "SAGEOX_AGENT_ID", agentID)
	}
	cmd.Env = envVars
	cmd.Stdin = bytes.NewReader(stdinData)

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("hook %s output: %s", eventName, string(output))
		t.Logf("hook %s error (may be ok): %v", eventName, err)
	}
}

// createCommitWithAgent creates a test file, stages it, and commits using the
// ox hooks commit-msg command to inject trailers. Returns the final commit message.
func createCommitWithAgent(t *testing.T, env *common.TestEnvironment, agentID, message string) string {
	t.Helper()

	// Create a unique test file
	filename := fmt.Sprintf("test-%s-%d.txt", agentID, time.Now().UnixNano())
	testFile := filepath.Join(env.ProjectDir, filename)
	require.NoError(t, os.WriteFile(testFile, []byte("test content\n"), 0644))

	// Stage the file
	gitAdd := exec.Command("git", "add", filename)
	gitAdd.Dir = env.ProjectDir
	gitAdd.Env = env.EnvVars
	addOut, err := gitAdd.CombinedOutput()
	require.NoError(t, err, "git add failed: %s", string(addOut))

	// Write a commit message file (what git passes to prepare-commit-msg)
	msgFile := filepath.Join(env.ProjectDir, "COMMIT_EDITMSG")
	require.NoError(t, os.WriteFile(msgFile, []byte(message+"\n"), 0644))

	// Run ox hooks commit-msg (the prepare-commit-msg hook handler)
	hookCmd := exec.Command(env.OxBinaryPath, "hooks", "commit-msg",
		"--msg-file", msgFile, "--source", "message")
	hookCmd.Dir = env.ProjectDir
	hookEnv := append([]string{}, env.EnvVars...)
	if agentID != "" {
		hookEnv = setEnvInSlice(hookEnv, "SAGEOX_AGENT_ID", agentID)
	} else {
		// remove SAGEOX_AGENT_ID to simulate human commit
		prefix := "SAGEOX_AGENT_ID="
		filtered := hookEnv[:0]
		for _, e := range hookEnv {
			if !strings.HasPrefix(e, prefix) {
				filtered = append(filtered, e)
			}
		}
		hookEnv = filtered
	}
	hookCmd.Env = hookEnv

	hookOut, err := hookCmd.CombinedOutput()
	if err != nil {
		t.Logf("hooks commit-msg output: %s", string(hookOut))
		t.Logf("hooks commit-msg error (may be ok): %v", err)
	}

	// Read the modified commit message
	modifiedMsg, err := os.ReadFile(msgFile)
	require.NoError(t, err)

	// Actually commit with the modified message
	// --no-verify skips prepare-commit-msg hook to avoid double-running ox hooks
	// (we already ran ox hooks commit-msg manually above)
	gitCommit := exec.Command("git", "commit", "--no-verify", "-F", msgFile)
	gitCommit.Dir = env.ProjectDir
	commitEnv := append([]string{}, env.EnvVars...)
	commitEnv = setEnvInSlice(commitEnv, "GIT_AUTHOR_NAME", "Test")
	commitEnv = setEnvInSlice(commitEnv, "GIT_AUTHOR_EMAIL", "test@example.com")
	commitEnv = setEnvInSlice(commitEnv, "GIT_COMMITTER_NAME", "Test")
	commitEnv = setEnvInSlice(commitEnv, "GIT_COMMITTER_EMAIL", "test@example.com")
	gitCommit.Env = commitEnv

	commitOut, err := gitCommit.CombinedOutput()
	if err != nil {
		t.Logf("git commit output: %s", string(commitOut))
		// don't fatal — the commit message is what we're testing
	}

	return string(modifiedMsg)
}

// extractSessionNameFromTrailer extracts the session name from a SageOx-Session trailer URL.
// URL format: https://sageox.ai/repo/{repo_id}/sessions/{session_name}/view
func extractSessionNameFromTrailer(t *testing.T, commitBody string) string {
	t.Helper()

	for _, line := range strings.Split(commitBody, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "SageOx-Session:") {
			continue
		}
		url := strings.TrimSpace(strings.TrimPrefix(line, "SageOx-Session:"))
		// extract session name from .../sessions/{name}/view
		parts := strings.Split(url, "/sessions/")
		if len(parts) < 2 {
			continue
		}
		rest := parts[1]
		if idx := strings.Index(rest, "/"); idx > 0 {
			return rest[:idx]
		}
		return rest
	}
	return ""
}

// findAllRecordings returns all active .recording.json files under the test root.
func findAllRecordings(t *testing.T, env *common.TestEnvironment) []*recordingState {
	t.Helper()

	var recordings []*recordingState
	matches := findFilesRecursive(env.RootDir, ".recording.json")
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var state recordingState
		if err := json.Unmarshal(data, &state); err != nil {
			continue
		}
		if state.AgentID != "" {
			recordings = append(recordings, &state)
		}
	}
	return recordings
}

// findRecordingForAgent finds the .recording.json for a specific agent ID.
func findRecordingForAgent(t *testing.T, env *common.TestEnvironment, agentID string) *recordingState {
	t.Helper()

	matches := findFilesRecursive(env.RootDir, ".recording.json")
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var state recordingState
		if err := json.Unmarshal(data, &state); err != nil {
			continue
		}
		if state.AgentID == agentID {
			return &state
		}
	}
	return nil
}

// recordingState is a minimal struct for parsing .recording.json in tests.
type recordingState struct {
	AgentID     string `json:"agent_id"`
	SessionPath string `json:"session_path"`
	ParentPID   int    `json:"parent_pid"`
}

// setEnvInSlice sets or updates an env var in a slice.
func setEnvInSlice(envVars []string, key, value string) []string {
	prefix := key + "="
	for i, v := range envVars {
		if strings.HasPrefix(v, prefix) {
			envVars[i] = prefix + value
			return envVars
		}
	}
	return append(envVars, prefix+value)
}
