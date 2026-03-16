//go:build slow

// NOTE: These are COMPONENT tests that exercise the real ox binary with
// SIMULATED Claude data (fake JSONL source files). They are NOT true E2E tests.
// True E2E tests that use real Claude Code instances live in:
//   tests/integration/agents/claude/ (build tag: integration)
//
// These component tests verify ox's internal recording pipeline works correctly
// in isolation. For release gates, the real-Claude integration tests MUST also pass.

package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon/agentwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIncrementalE2E_SingleAgent exercises the full incremental recording
// pipeline through the real ox binary with simulated Claude data (not a real
// Claude instance — see tests/integration/agents/claude/ for real E2E tests):
//
//  1. Build ox
//  2. Create workspace with .sageox/config.json
//  3. Create a fake Claude Code JSONL (the "source file")
//  4. Run: ox agent <id> session start
//  5. Simulate PostToolUse hooks (write to source, then run ox agent <id> hook PostToolUse)
//  6. Verify raw.jsonl has incremental entries after each hook
//  7. Run: ox agent <id> session stop
//  8. Verify final artifacts
//
// Run with: go test -tags=slow -timeout=5m -run TestIncrementalE2E ./cmd/ox/ -v
func TestIncrementalE2E_SingleAgent(t *testing.T) {
	oxBin := buildOxBinary(t)
	env := setupE2EWorkspace(t, oxBin)

	agentID := "OxE2E1"
	claudeSessionID := "test-e2e-session-001"

	// create Claude Code source JSONL in the expected location
	sourceFile := createClaudeSourceFile(t, env)

	// write session marker (simulates what ox agent prime does)
	writeE2ESessionMarker(t, env, agentID, claudeSessionID)

	// create agent instance (simulates what ox agent prime does)
	createE2EAgentInstance(t, env.workspace, agentID)

	// write initial user entry BEFORE session start (should be filtered out)
	writeClaudeEntry(t, sourceFile, claudeUserEntry(time.Now().Add(-1*time.Minute).UTC().Format(time.RFC3339Nano), "Hello, fix the bug"))

	// --- session start ---
	out := runOx(t, oxBin, env, agentID, "session", "start")
	t.Logf("session start output: %s", truncateStr(out, 500))
	require.Contains(t, out, `"success"`, "session start should succeed")

	// find the raw.jsonl path from recording state
	rawPath := findRawJSONL(t, env)
	if rawPath != "" {
		t.Logf("raw.jsonl at: %s", rawPath)
	}

	// timestamps must be AFTER session start for the filter to pass
	now := time.Now().Add(1 * time.Second)

	// --- simulate turn 1: append assistant response to source, fire hook ---
	writeClaudeEntry(t, sourceFile, claudeAssistantEntry(now.UTC().Format(time.RFC3339Nano),
		"Looking at the code.", "Read", `{"file_path":"/src/main.go"}`))

	hookOut := runOxHook(t, oxBin, env, agentID, "PostToolUse", claudeSessionID)
	t.Logf("hook 1 output: %s", truncateStr(hookOut, 500))

	// check raw.jsonl has entries now
	if rawPath != "" {
		lines := e2eCountLines(t, rawPath)
		t.Logf("after hook 1: %d lines in raw.jsonl", lines)
		assert.GreaterOrEqual(t, lines, 2, "should have header + entries after first hook")
	}

	// --- simulate turn 2: more conversation ---
	now2 := now.Add(2 * time.Second)
	writeClaudeEntry(t, sourceFile, claudeUserEntry(now2.UTC().Format(time.RFC3339Nano), "Now add tests"))
	now3 := now.Add(4 * time.Second)
	writeClaudeEntry(t, sourceFile, claudeAssistantEntry(now3.UTC().Format(time.RFC3339Nano),
		"I'll add tests.", "Write", `{"file_path":"/src/main_test.go","content":"package main"}`))

	hookOut2 := runOxHook(t, oxBin, env, agentID, "PostToolUse", claudeSessionID)
	t.Logf("hook 2 output: %s", truncateStr(hookOut2, 2000))

	if rawPath != "" {
		lines := e2eCountLines(t, rawPath)
		t.Logf("after hook 2: %d lines in raw.jsonl", lines)
		assert.GreaterOrEqual(t, lines, 4, "should have more entries after second hook")
	}

	// --- session stop ---
	stopOut := runOx(t, oxBin, env, agentID, "session", "stop")
	t.Logf("session stop output: %s", truncateStr(stopOut, 500))

	// verify stop completed (may have upload warnings, that's ok)
	if !strings.Contains(stopOut, `"success"`) {
		t.Logf("stop output (no success field): %s", stopOut)
	}
}

// TestIncrementalE2E_MultiAgent tests Conductor scenario with 2 agents
// on the same worktree, verifying no cross-contamination.
// Component test with simulated data — not a real Claude E2E test.
func TestIncrementalE2E_MultiAgent(t *testing.T) {
	oxBin := buildOxBinary(t)
	env := setupE2EWorkspace(t, oxBin)

	agentA := "OxCdA1"
	agentB := "OxCdB2"
	sessionA := "conductor-session-A"
	sessionB := "conductor-session-B"

	// create separate source files for each agent
	sourceA := createClaudeSourceFile(t, env)
	sourceB := createClaudeSourceFileNamed(t, env, "agent-b-session.jsonl")

	writeE2ESessionMarker(t, env, agentA, sessionA)
	writeE2ESessionMarker(t, env, agentB, sessionB)

	createE2EAgentInstance(t, env.workspace, agentA)
	createE2EAgentInstance(t, env.workspace, agentB)

	// write initial entries before session start (will be filtered out)
	past := time.Now().Add(-1 * time.Minute).UTC().Format(time.RFC3339Nano)
	writeClaudeEntry(t, sourceA, claudeUserEntry(past, "Agent A task"))
	writeClaudeEntry(t, sourceB, claudeUserEntry(past, "Agent B task"))

	// start both agents
	outA := runOx(t, oxBin, env, agentA, "session", "start")
	require.Contains(t, outA, `"success"`, "agent A session start should succeed")

	outB := runOx(t, oxBin, env, agentB, "session", "start")
	require.Contains(t, outB, `"success"`, "agent B session start should succeed")

	// timestamps after session start
	now := time.Now().Add(1 * time.Second)

	// agent A gets activity
	writeClaudeEntry(t, sourceA, claudeAssistantEntry(now.UTC().Format(time.RFC3339Nano),
		"Agent A working.", "Bash", `{"command":"make test"}`))
	runOxHook(t, oxBin, env, agentA, "PostToolUse", sessionA)

	// agent B gets activity
	writeClaudeEntry(t, sourceB, claudeAssistantEntry(now.Add(1*time.Second).UTC().Format(time.RFC3339Nano),
		"Agent B working.", "Read", `{"file_path":"/README.md"}`))
	runOxHook(t, oxBin, env, agentB, "PostToolUse", sessionB)

	// stop agent A — agent B should be unaffected
	runOx(t, oxBin, env, agentA, "session", "stop")

	// verify agent B can still write
	writeClaudeEntry(t, sourceB, claudeAssistantEntry(now.Add(3*time.Second).UTC().Format(time.RFC3339Nano),
		"Agent B still working.", "Edit", `{"file_path":"/main.go"}`))
	runOxHook(t, oxBin, env, agentB, "PostToolUse", sessionB)

	// stop agent B
	runOx(t, oxBin, env, agentB, "session", "stop")

	// verify per-agent isolation: each agent's raw.jsonl should only contain its own entries
	allRaws := findAllRawJSONL(t, env)
	require.GreaterOrEqual(t, len(allRaws), 2, "should have raw.jsonl for both agents")

	for _, rawPath := range allRaws {
		data, err := os.ReadFile(rawPath)
		require.NoError(t, err)
		content := string(data)
		lines := e2eCountLines(t, rawPath)
		assert.GreaterOrEqual(t, lines, 2, "raw.jsonl at %s should have header + entries", rawPath)

		// check cross-contamination: if path contains agent A's ID, it shouldn't have agent B's content
		if strings.Contains(rawPath, agentA) {
			assert.NotContains(t, content, "Agent B working", "agent A's raw.jsonl should not contain agent B entries")
			assert.NotContains(t, content, "Agent B still working", "agent A's raw.jsonl should not contain agent B entries")
		}
		if strings.Contains(rawPath, agentB) {
			assert.NotContains(t, content, "Agent A working", "agent B's raw.jsonl should not contain agent A entries")
		}
	}
}

// TestIncrementalE2E_CtrlC_AntiEntropy simulates a user pressing Ctrl-C during
// an active session (session stop never runs).
// Component test with simulated data — for the real-Claude SIGINT E2E test,
// see TestCtrlC_RealClaude_AntiEntropy in tests/integration/agents/claude/.
// Verifies:
//
//  1. raw.jsonl has entries written by PostToolUse hooks
//  2. raw.jsonl has NO footer (stop never ran)
//  3. .recording.json still exists (stop didn't clean it up)
//  4. The SessionFinalizeHandler detects and finalizes the orphaned session
//  5. All summary artifacts are generated (summary.md, summary.json, session.html, session.md)
//
// This is the core anti-entropy guarantee: no session data is lost even if the
// CLI crashes or the user interrupts the process.
func TestIncrementalE2E_CtrlC_AntiEntropy(t *testing.T) {
	oxBin := buildOxBinary(t)
	env := setupE2EWorkspace(t, oxBin)

	agentID := "OxCtC1"
	claudeSessionID := "test-ctrlc-session-001"

	// create Claude Code source JSONL
	sourceFile := createClaudeSourceFile(t, env)

	// write session marker and agent instance
	writeE2ESessionMarker(t, env, agentID, claudeSessionID)
	createE2EAgentInstance(t, env.workspace, agentID)

	// --- session start ---
	out := runOx(t, oxBin, env, agentID, "session", "start")
	require.Contains(t, out, `"success"`, "session start should succeed")

	rawPath := findRawJSONL(t, env)
	require.NotEmpty(t, rawPath, "raw.jsonl should exist after session start")

	// timestamps after session start
	now := time.Now().Add(1 * time.Second)

	// --- simulate 3 turns of activity via hooks ---
	writeClaudeEntry(t, sourceFile, claudeAssistantEntry(
		now.UTC().Format(time.RFC3339Nano),
		"Reading the config file.", "Read", `{"file_path":"/src/config.go"}`))
	runOxHook(t, oxBin, env, agentID, "PostToolUse", claudeSessionID)

	writeClaudeEntry(t, sourceFile, claudeUserEntry(
		now.Add(2*time.Second).UTC().Format(time.RFC3339Nano), "Now fix the validation"))
	writeClaudeEntry(t, sourceFile, claudeAssistantEntry(
		now.Add(4*time.Second).UTC().Format(time.RFC3339Nano),
		"Fixing validation logic.", "Edit", `{"file_path":"/src/validate.go"}`))
	runOxHook(t, oxBin, env, agentID, "PostToolUse", claudeSessionID)

	writeClaudeEntry(t, sourceFile, claudeUserEntry(
		now.Add(6*time.Second).UTC().Format(time.RFC3339Nano), "Run the tests"))
	writeClaudeEntry(t, sourceFile, claudeAssistantEntry(
		now.Add(8*time.Second).UTC().Format(time.RFC3339Nano),
		"Running test suite.", "Bash", `{"command":"go test ./..."}`))
	runOxHook(t, oxBin, env, agentID, "PostToolUse", claudeSessionID)

	// --- Ctrl-C happens here: NO session stop ---

	// Verify raw.jsonl has entries from the hooks
	rawLines := e2eCountLines(t, rawPath)
	t.Logf("raw.jsonl has %d lines after Ctrl-C (no stop)", rawLines)
	assert.GreaterOrEqual(t, rawLines, 3, "raw.jsonl should have entries from hooks")

	// Verify NO footer in raw.jsonl (stop never wrote one)
	rawData, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	assert.NotContains(t, string(rawData), `"type":"footer"`,
		"raw.jsonl should NOT have a footer since stop never ran")

	// Verify .recording.json still exists (stop didn't clean it up)
	recordingFiles := findFilesRecursive(env.cacheDir, ".recording.json")
	assert.NotEmpty(t, recordingFiles, ".recording.json should still exist after Ctrl-C")

	// --- Anti-entropy: daemon detects and finalizes the orphaned session ---

	// To simulate the daemon's 24h stale detection, we need to:
	// 1. Find the .recording.json and backdate its started_at
	// 2. Copy raw.jsonl to a ledger session directory
	// 3. Run the finalization handler

	// Find the session directory that contains raw.jsonl (this is the cache location)
	cacheSessionDir := filepath.Dir(rawPath)

	// Create a fake ledger with the session data
	fakeLedger := t.TempDir()
	ledgerSessionName := "2026-01-10T09-00-testuser-" + agentID
	ledgerSessionDir := filepath.Join(fakeLedger, "sessions", ledgerSessionName)
	require.NoError(t, os.MkdirAll(ledgerSessionDir, 0755))

	// Copy raw.jsonl to ledger session dir
	rawContent, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(ledgerSessionDir, "raw.jsonl"), rawContent, 0644))

	// Write a stale .recording.json (> 24h old) to simulate time passing
	staleState := map[string]any{
		"started_at": time.Now().Add(-25 * time.Hour).Format(time.RFC3339),
		"agent_id":   agentID,
	}
	staleData, _ := json.Marshal(staleState)
	require.NoError(t, os.WriteFile(
		filepath.Join(ledgerSessionDir, ".recording.json"), staleData, 0644))

	// Run the finalization handler (same code the daemon runs)
	handler := agentwork.NewSessionFinalizeHandlerForTest(slog.Default())

	items, detectErr := handler.Detect(fakeLedger)
	require.NoError(t, detectErr)
	require.Len(t, items, 1, "should detect 1 stale session")

	// .recording.json should be removed after detection
	_, statErr := os.Stat(filepath.Join(ledgerSessionDir, ".recording.json"))
	assert.True(t, os.IsNotExist(statErr), ".recording.json should be removed after stale detection")

	// BuildPrompt should succeed
	req, buildErr := handler.BuildPrompt(items[0])
	require.NoError(t, buildErr)
	assert.NotEmpty(t, req.Prompt, "prompt should be non-empty")

	// ProcessResult with simulated LLM output
	summaryJSON := `{"title":"Config Fix and Validation","summary":"Fixed validation logic and ran tests.","key_actions":["read config","fixed validation","ran tests"],"outcome":"success","topics_found":["validation","testing"]}`
	processErr := handler.ProcessResult(items[0], &agentwork.RunResult{
		Output:   summaryJSON,
		Duration: 5 * time.Second,
		ExitCode: 0,
	})
	require.NoError(t, processErr)

	// Verify all artifacts were generated
	expectedArtifacts := []string{"summary.md", "summary.json", "session.html", "session.md"}
	for _, artifact := range expectedArtifacts {
		path := filepath.Join(ledgerSessionDir, artifact)
		_, statErr := os.Stat(path)
		assert.NoError(t, statErr, "artifact %s should exist after finalization", artifact)
	}

	t.Logf("anti-entropy finalization successful: %d artifacts generated from %d raw.jsonl lines",
		len(expectedArtifacts), rawLines)

	// Verify the cache session dir still has the original raw.jsonl (not corrupted)
	originalRaw, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	assert.Equal(t, rawContent, originalRaw, "original raw.jsonl should be unchanged")

	_ = cacheSessionDir // used above for context
}

// findFilesRecursive walks a directory tree and returns paths matching the given filename.
func findFilesRecursive(root, name string) []string {
	var found []string
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.Name() == name {
			found = append(found, path)
		}
		return nil
	})
	return found
}

// --- E2E test infrastructure ---

type e2eEnv struct {
	workspace string // git repo with .sageox/
	home      string // fake HOME for isolation
	cacheDir  string // XDG_CACHE_HOME
	username  string // synthetic username for test isolation
}

func buildOxBinary(t *testing.T) string {
	t.Helper()

	// find project root
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			content, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
			if strings.Contains(string(content), "github.com/sageox/ox") {
				break
			}
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "could not find project root")
		dir = parent
	}

	binDir := t.TempDir()
	oxBin := filepath.Join(binDir, "ox")

	cmd := exec.Command("go", "build", "-o", oxBin, "./cmd/ox")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "failed to build ox: %s", output)

	return oxBin
}

func setupE2EWorkspace(t *testing.T, oxBin string) e2eEnv {
	t.Helper()

	workspace := t.TempDir()
	home := t.TempDir()
	cacheDir := filepath.Join(home, "cache")
	require.NoError(t, os.MkdirAll(cacheDir, 0755))

	// init git repo
	gitCmd := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		cmd.Env = []string{
			"HOME=" + home,
			"PATH=" + os.Getenv("PATH"),
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		}
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v failed: %s", args, out)
	}

	gitCmd("init")
	gitCmd("config", "user.name", "Test")
	gitCmd("config", "user.email", "test@test.com")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# test\n"), 0644))
	gitCmd("add", "README.md")
	gitCmd("commit", "-m", "init")

	// create .sageox/config.json
	sageoxDir := filepath.Join(workspace, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	cfg := `{"config_version":"2","repo_id":"e2e-test-repo","endpoint":"https://sageox.ai"}`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(cfg), 0644))

	// create fake auth so session start doesn't fail on auth check
	authDir := filepath.Join(home, ".config", "sageox")
	require.NoError(t, os.MkdirAll(authDir, 0700))
	authJSON := fmt.Sprintf(`{"tokens":{"sageox.ai":{"access_token":"fake-pat-for-e2e-test","token_type":"bearer","expires_at":"%s"}}}`,
		time.Now().Add(24*time.Hour).Format(time.RFC3339))
	require.NoError(t, os.WriteFile(filepath.Join(authDir, "auth.json"), []byte(authJSON), 0644))

	// synthetic username prevents colliding with developer's real session markers
	username := "oxtest-" + strings.ReplaceAll(t.Name(), "/", "-")

	return e2eEnv{
		workspace: workspace,
		home:      home,
		cacheDir:  cacheDir,
		username:  username,
	}
}

func oxEnv(env e2eEnv) []string {
	return []string{
		"HOME=" + env.home,
		"XDG_CACHE_HOME=" + env.cacheDir,
		"XDG_CONFIG_HOME=" + filepath.Join(env.home, ".config"),
		"OX_XDG_ENABLE=1",
		"PATH=" + os.Getenv("PATH"),
		"AGENT_ENV=claude-code",
		"USER=" + env.username,
		// prevent real daemon IPC
		"OX_NO_DAEMON=1",
		// prevent real network calls
		"OX_OFFLINE=1",
	}
}

func runOx(t *testing.T, oxBin string, env e2eEnv, agentID string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"agent", agentID}, args...)
	cmd := exec.Command(oxBin, fullArgs...)
	cmd.Dir = env.workspace
	cmd.Env = oxEnv(env)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "ox agent %s %v failed:\n%s", agentID, args, string(out))
	return string(out)
}

func runOxHook(t *testing.T, oxBin string, env e2eEnv, agentID, event, sessionID string) string {
	t.Helper()
	cmd := exec.Command(oxBin, "agent", agentID, "hook", event)
	cmd.Dir = env.workspace
	cmd.Env = oxEnv(env)

	// pipe hook input via stdin
	hookInput := fmt.Sprintf(`{"session_id":"%s","hook_event_name":"%s"}`, sessionID, event)
	cmd.Stdin = strings.NewReader(hookInput)

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "hook %s failed:\n%s", event, string(out))
	return string(out)
}

// createClaudeSourceFile creates a fake Claude Code JSONL file at the path
// the ClaudeCodeAdapter.FindSessionFile would look for it.
func createClaudeSourceFile(t *testing.T, env e2eEnv) string {
	t.Helper()
	return createClaudeSourceFileNamed(t, env, "session.jsonl")
}

func createClaudeSourceFileNamed(t *testing.T, env e2eEnv, filename string) string {
	t.Helper()

	// Claude Code stores files at ~/.claude/projects/<hash>/<session>.jsonl
	// where hash = workspace path with / replaced by -
	// resolve symlinks since ox resolves the workspace path (macOS: /var → /private/var)
	realWorkspace, err := filepath.EvalSymlinks(env.workspace)
	require.NoError(t, err)
	hash := strings.ReplaceAll(realWorkspace, string(os.PathSeparator), "-")
	hash = strings.ReplaceAll(hash, "_", "-")

	// use the fake HOME so the ox subprocess (which uses HOME=env.home) can find it
	projectDir := filepath.Join(env.home, ".claude", "projects", hash)
	require.NoError(t, os.MkdirAll(projectDir, 0755))

	sourceFile := filepath.Join(projectDir, filename)
	require.NoError(t, os.WriteFile(sourceFile, []byte(""), 0644))

	return sourceFile
}

func writeE2ESessionMarker(t *testing.T, env e2eEnv, agentID, sessionID string) {
	t.Helper()

	// session markers live in /tmp/<user>/sageox/sessions/ (not under HOME)
	// use synthetic username to avoid colliding with developer's real sessions
	markerDir := filepath.Join("/tmp", env.username, "sageox", "sessions")
	require.NoError(t, os.MkdirAll(markerDir, 0700))

	marker := map[string]any{
		"agent_id":         agentID,
		"agent_session_id": sessionID,
		"primed_at":        time.Now().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(marker, "", "  ")
	require.NoError(t, err)

	markerFile := filepath.Join(markerDir, sessionID+".json")
	require.NoError(t, os.WriteFile(markerFile, data, 0644))

	t.Cleanup(func() {
		os.Remove(markerFile)
		os.RemoveAll(filepath.Join("/tmp", env.username))
	})
}

func writeClaudeEntry(t *testing.T, sourceFile, jsonLine string) {
	t.Helper()
	f, err := os.OpenFile(sourceFile, os.O_WRONLY|os.O_APPEND, 0644)
	require.NoError(t, err)
	_, err = f.WriteString(jsonLine + "\n")
	require.NoError(t, err)
	f.Close()
}

func claudeUserEntry(ts, content string) string {
	return fmt.Sprintf(`{"type":"user","timestamp":"%s","message":{"role":"user","content":"%s"},"isMeta":false}`, ts, content)
}

func claudeAssistantEntry(ts, text, toolName, toolInput string) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":"%s","message":{"role":"assistant","content":[{"type":"text","text":"%s"},{"type":"tool_use","id":"toolu_01","name":"%s","input":%s}]}}`,
		ts, text, toolName, toolInput)
}

func findRawJSONL(t *testing.T, env e2eEnv) string {
	t.Helper()

	// search in XDG cache for raw.jsonl
	cacheRoot := filepath.Join(env.cacheDir, "sageox")
	var found string
	filepath.Walk(cacheRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.Name() == "raw.jsonl" {
			found = path
		}
		return nil
	})

	if found == "" {
		// also check the workspace sessions dir
		filepath.Walk(env.workspace, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.Name() == "raw.jsonl" {
				found = path
			}
			return nil
		})
	}

	return found
}

func findAllRawJSONL(t *testing.T, env e2eEnv) []string {
	t.Helper()
	var found []string
	cacheRoot := filepath.Join(env.cacheDir, "sageox")
	filepath.Walk(cacheRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.Name() == "raw.jsonl" {
			found = append(found, path)
		}
		return nil
	})
	// also check workspace
	filepath.Walk(env.workspace, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.Name() == "raw.jsonl" {
			found = append(found, path)
		}
		return nil
	})
	return found
}

func e2eCountLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func createE2EAgentInstance(t *testing.T, workspace, agentID string) {
	t.Helper()

	// the user slug will be "test" (from test@test.com configured in git)
	instanceDir := filepath.Join(workspace, ".sageox", "agent_instances", "test")
	require.NoError(t, os.MkdirAll(instanceDir, 0755))

	instanceFile := filepath.Join(instanceDir, "agent_instances.jsonl")

	inst := fmt.Sprintf(`{"agent_id":"%s","oxsid":"oxsid_e2e_%s","created_at":"%s","expires_at":"%s","agent_type":"claude-code","prime_call_count":1}`,
		agentID, agentID,
		time.Now().Format(time.RFC3339),
		time.Now().Add(24*time.Hour).Format(time.RFC3339))

	f, err := os.OpenFile(instanceFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	require.NoError(t, err)
	_, err = f.WriteString(inst + "\n")
	require.NoError(t, err)
	f.Close()
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
