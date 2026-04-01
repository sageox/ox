//go:build integration

package claude

import (
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
)

// judgeVerdict holds the LLM judge's evaluation of recording fidelity.
type judgeVerdict struct {
	CoveragePct int      `json:"coverage_pct"`
	Missing     []string `json:"missing"`
	Verdict     string   `json:"verdict"`
	Notes       string   `json:"notes"`
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

	// XML format: agent_id is an attribute on <session-context agent_id="XYZ">
	if idx := strings.Index(primeOutput, `agent_id="`); idx != -1 {
		rest := primeOutput[idx+len(`agent_id="`):]
		if end := strings.Index(rest, `"`); end > 0 {
			return rest[:end]
		}
	}

	// fallback: JSON format (legacy)
	start := strings.Index(primeOutput, "{")
	if start == -1 {
		t.Fatalf("no agent_id found in prime output:\n%.500s", primeOutput)
	}

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
