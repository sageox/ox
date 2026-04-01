//go:build integration

// Tests for the tail-mode session recording pipeline with real Codex CLI.
// Unlike Claude Code (which uses hooks), Codex sessions are recorded by the
// daemon tailing ~/.codex/sessions/.../rollout-*.jsonl in real time.
//
// These tests verify the full E2E pipeline:
//  1. ox agent prime detects Codex (no hooks), sets WatchMode="tail"
//  2. CLI sends session_watch_start IPC to daemon
//  3. Daemon's SessionWatcherManager tails the Codex session file
//  4. Entries flow into raw.jsonl via the codex adapter
//  5. ox session stop sends session_watch_stop, daemon finalizes
//
// Run with:
//
//	go test -tags=integration -timeout=5m -run TestCodex ./tests/integration/agents/codex/ -v
package codex

import (
	"bytes"
	"context"
	"encoding/json"
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

func getCodexConfig() *common.AgentConfig {
	configs := common.DefaultAgentConfigs()
	return configs[common.AgentCodex]
}

// TestCodexTailRecording_E2E verifies the full tail-mode recording pipeline
// using a real Codex CLI instance.
//
// Flow:
//  1. Set up test environment, run ox agent prime (detects Codex, WatchMode=tail)
//  2. Run codex exec with a simple prompt that triggers tool use
//  3. Verify .recording.json has WatchMode="tail" and SessionFile set
//  4. Verify raw.jsonl was populated by the daemon's tail watcher
//  5. Stop the session, verify EntryCount is accurate
//
// This catches: adapter detection failures, IPC dispatch bugs, TailWatcher
// entry conversion errors, EntryCount inflation, and session lifecycle issues.
func TestCodexTailRecording_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	agent := getCodexConfig()
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)

	// prime with AGENT_ENV=codex so ox detects Codex and sets WatchMode=tail
	primeOutput := runOxPrimeForCodex(t, env)
	agentID := extractAgentID(t, primeOutput)
	require.NotEmpty(t, agentID, "prime should generate an agent_id")
	t.Logf("agent ID: %s", agentID)

	t.Run("recording_state_is_tail_mode", func(t *testing.T) {
		matches := findFilesRecursive(env.RootDir, ".recording.json")
		require.NotEmpty(t, matches, ".recording.json should exist after prime")

		for _, path := range matches {
			data, err := os.ReadFile(path)
			require.NoError(t, err)

			var state struct {
				WatchMode   string `json:"watch_mode"`
				SessionFile string `json:"session_file"`
				AgentID     string `json:"agent_id"`
				AdapterName string `json:"adapter_name"`
			}
			require.NoError(t, json.Unmarshal(data, &state))

			if state.AgentID != agentID {
				continue
			}

			assert.Equal(t, "tail", state.WatchMode,
				"Codex sessions must use tail mode (no hooks)")
			assert.Equal(t, "codex", state.AdapterName,
				"adapter should be codex")
			t.Logf("recording state: watch_mode=%s adapter=%s session_file=%s",
				state.WatchMode, state.AdapterName, state.SessionFile)
		}
	})

	// run codex exec with a simple prompt
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	t.Log("running codex exec with prompt...")
	result := runCodexExec(ctx, t, env, agent,
		`Read the file AGENTS.md and tell me what it says. Keep your response under 30 words.`)
	if result.Error != nil {
		t.Logf("codex error (may be ok): %v", result.Error)
	}
	t.Logf("codex completed in %v", result.Duration)

	// give the daemon's TailWatcher time to process the final entries
	time.Sleep(2 * time.Second)

	t.Run("raw_jsonl_populated", func(t *testing.T) {
		rawPaths := findAllRawJSONL(t, env)
		if len(rawPaths) == 0 {
			logSearchedPaths(t, env)
			t.Fatal("no raw.jsonl found — tail-mode recording did not work")
		}

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
		t.Logf("entry types: %v", entryTypes)

		require.Greater(t, totalEntries, 0,
			"tail watcher should have captured entries from Codex session")
	})

	t.Run("entry_count_accurate", func(t *testing.T) {
		// count actual entries in raw.jsonl
		rawPaths := findAllRawJSONL(t, env)
		require.NotEmpty(t, rawPaths)

		actualEntries := 0
		for _, rawPath := range rawPaths {
			for _, e := range readRawJSONL(t, rawPath) {
				eType, _ := e["type"].(string)
				if eType != "header" {
					actualEntries++
				}
			}
		}

		// read EntryCount from .recording.json
		matches := findFilesRecursive(env.RootDir, ".recording.json")
		for _, path := range matches {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var state struct {
				AgentID    string `json:"agent_id"`
				EntryCount int    `json:"entry_count"`
			}
			if json.Unmarshal(data, &state) != nil || state.AgentID != agentID {
				continue
			}
			t.Logf("EntryCount in .recording.json: %d, actual raw.jsonl entries: %d",
				state.EntryCount, actualEntries)

			// EntryCount must match actual entries (not be inflated)
			assert.Equal(t, actualEntries, state.EntryCount,
				"EntryCount must equal actual entries (linear, not quadratic)")
		}
	})

	t.Run("session_stop", func(t *testing.T) {
		stopSession(t, env, agentID)

		rawPaths := findAllRawJSONL(t, env)
		require.NotEmpty(t, rawPaths, "raw.jsonl should still exist after stop")

		hasUser := false
		hasAssistant := false
		for _, rawPath := range rawPaths {
			for _, e := range readRawJSONL(t, rawPath) {
				switch e["type"] {
				case "user":
					hasUser = true
				case "assistant":
					hasAssistant = true
				}
			}
		}
		assert.True(t, hasUser, "raw.jsonl should contain user entries")
		assert.True(t, hasAssistant, "raw.jsonl should contain assistant entries")
	})
}

// --- helpers ---

// runOxPrimeForCodex runs ox agent prime with AGENT_ENV=codex.
func runOxPrimeForCodex(t *testing.T, env *common.TestEnvironment) string {
	t.Helper()

	cmd := exec.Command(env.OxBinaryPath, "agent", "prime")
	cmd.Dir = env.ProjectDir
	cmd.Env = append(env.EnvVars, "AGENT_ENV=codex")

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("ox agent prime output:\n%s", string(output))
		t.Fatalf("ox agent prime failed: %v", err)
	}

	t.Logf("ox agent prime (codex) completed (%d bytes output)", len(output))
	return string(output)
}

// extractAgentID extracts the agent_id from ox agent prime output.
func extractAgentID(t *testing.T, primeOutput string) string {
	t.Helper()

	// XML format: agent_id is an attribute on <session-context agent_id="XYZ">
	if idx := strings.Index(primeOutput, `agent_id="`); idx != -1 {
		rest := primeOutput[idx+len(`agent_id="`):]
		if end := strings.Index(rest, `"`); end > 0 {
			return rest[:end]
		}
	}

	// fallback: JSON format
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

// runCodexExec runs codex exec with the given prompt.
func runCodexExec(ctx context.Context, t *testing.T, env *common.TestEnvironment, agent *common.AgentConfig, prompt string) *common.AgentTestResult {
	t.Helper()

	result := &common.AgentTestResult{}
	start := time.Now()

	args := []string{"exec", prompt}

	cmdCtx, cancel := context.WithTimeout(ctx, agent.Timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, agent.CLIPath, args...)
	cmd.Dir = env.ProjectDir
	cmd.Env = append(env.EnvVars, "AGENT_ENV=codex")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result.Duration = time.Since(start)

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		}
		result.Error = err
	}

	result.RawOutput = stdout.String()
	if stderr.Len() > 0 {
		t.Logf("codex stderr: %s", stderr.String())
	}

	return result
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

// findAllRawJSONL finds all raw.jsonl files under the test root.
func findAllRawJSONL(t *testing.T, env *common.TestEnvironment) []string {
	t.Helper()
	return findFilesRecursive(env.RootDir, "raw.jsonl")
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

// logSearchedPaths logs where we looked for raw.jsonl.
func logSearchedPaths(t *testing.T, env *common.TestEnvironment) {
	t.Helper()
	t.Logf("searched for raw.jsonl under: %s", env.RootDir)

	allFiles := findFilesRecursive(env.RootDir, "")
	if len(allFiles) > 50 {
		allFiles = allFiles[:50]
	}
	for _, f := range allFiles {
		rel, _ := filepath.Rel(env.RootDir, f)
		t.Logf("  exists: %s", rel)
	}
}

// findFilesRecursive finds all files with the given name under root.
func findFilesRecursive(root, name string) []string {
	var matches []string
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() && (name == "" || info.Name() == name) {
			matches = append(matches, path)
		}
		return nil
	})
	return matches
}
