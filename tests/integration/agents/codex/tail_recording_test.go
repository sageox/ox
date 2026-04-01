//go:build integration

// Tests for the tail-mode session recording pipeline with real Codex CLI.
// Unlike Claude Code (which uses hooks), Codex sessions are recorded by the
// daemon tailing ~/.codex/sessions/.../rollout-*.jsonl in real time.
//
// These tests verify the Codex integration surface:
//  1. ox agent prime detects Codex (no hooks), sets WatchMode="tail"
//  2. Codex exec creates a valid session file with parseable entries
//  3. The codex adapter correctly reads entries from a real Codex session
//  4. Session lifecycle (start/stop) works correctly
//
// The daemon TailWatcher pipeline (session file → raw.jsonl) is tested in
// internal/daemon/agentwork/session_watcher_test.go. This test verifies the
// real-world Codex integration that feeds into that pipeline.
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

	"github.com/sageox/ox/internal/session/adapters"
	"github.com/sageox/ox/internal/testguard"
	"github.com/sageox/ox/tests/integration/agents/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func getCodexConfig() *common.AgentConfig {
	configs := common.DefaultAgentConfigs()
	return configs[common.AgentCodex]
}

// TestCodexTailRecording_E2E verifies the Codex integration surface using
// a real Codex CLI instance.
//
// Flow:
//  1. Set up test environment, run ox agent prime --agent codex
//  2. Run codex exec with a simple prompt
//  3. Verify .recording.json has WatchMode="tail" and AdapterName="codex"
//  4. Verify Codex created a parseable session file
//  5. Verify the codex adapter can read real entries from the session file
//  6. Stop the session, verify lifecycle completes
//
// This catches: adapter detection failures, agent type misdetection,
// session file format changes, and session lifecycle issues.
func TestCodexTailRecording_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	agent := getCodexConfig()
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)

	// prime with --agent codex so ox detects Codex and sets WatchMode=tail.
	// Uses --agent flag (not AGENT_ENV) because agentx.CurrentAgent() may detect
	// the parent Claude Code session's env vars and override AGENT_ENV.
	primeOutput := runOxPrimeForCodex(t, env)
	agentID := extractAgentID(t, primeOutput)
	require.NotEmpty(t, agentID, "prime should generate an agent_id")
	t.Logf("agent ID: %s", agentID)

	t.Run("recording_state_is_tail_mode", func(t *testing.T) {
		matches := findFilesRecursive(env.RootDir, ".recording.json")
		require.NotEmpty(t, matches, ".recording.json should exist after prime")

		found := false
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
			found = true
			assert.Equal(t, "tail", state.WatchMode,
				"Codex sessions must use tail mode (no hooks)")
			assert.Equal(t, "codex", state.AdapterName,
				"adapter should be codex")
			t.Logf("recording state: watch_mode=%s adapter=%s session_file=%s",
				state.WatchMode, state.AdapterName, state.SessionFile)
		}
		require.True(t, found, "should find .recording.json for agent %s", agentID)
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

	// find the Codex session file created during codex exec
	sessionFile := findCodexSessionFile(t, env)
	t.Logf("codex session file: %s", sessionFile)

	t.Run("session_file_has_entries", func(t *testing.T) {
		data, err := os.ReadFile(sessionFile)
		require.NoError(t, err)

		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		require.Greater(t, len(lines), 1,
			"session file should have more than just session_meta")

		// verify session_meta is first line
		var meta map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(lines[0]), &meta))
		assert.Equal(t, "session_meta", meta["type"],
			"first line should be session_meta")

		t.Logf("session file has %d lines", len(lines))
	})

	t.Run("adapter_reads_real_entries", func(t *testing.T) {
		adapter := &adapters.CodexAdapter{}
		entries, err := adapter.Read(sessionFile)
		require.NoError(t, err)
		require.Greater(t, len(entries), 0,
			"codex adapter should parse entries from real session file")

		hasUser := false
		hasAssistant := false
		roleCount := map[string]int{}
		for _, e := range entries {
			roleCount[e.Role]++
			switch e.Role {
			case "user":
				hasUser = true
			case "assistant":
				hasAssistant = true
			}
		}
		t.Logf("adapter parsed %d entries: %v", len(entries), roleCount)

		assert.True(t, hasUser, "should have user entries from codex session")
		// assistant entries may be missing if Codex hit API rate limits
		if result.Error != nil {
			t.Logf("codex returned error, assistant entries may be missing (rate limit, etc.)")
		} else {
			assert.True(t, hasAssistant, "should have assistant entries from codex session")
		}
	})

	t.Run("adapter_reads_metadata", func(t *testing.T) {
		adapter := &adapters.CodexAdapter{}
		meta, err := adapter.ReadMetadata(sessionFile)
		require.NoError(t, err)
		require.NotNil(t, meta, "should extract metadata from real Codex session")

		t.Logf("metadata: agent_version=%s model=%s", meta.AgentVersion, meta.Model)

		assert.NotEmpty(t, meta.AgentVersion,
			"should extract Codex CLI version from session")
	})

	t.Run("session_stop", func(t *testing.T) {
		stopSession(t, env, agentID)
	})
}

// --- helpers ---

// runOxPrimeForCodex runs ox agent prime with --agent codex.
func runOxPrimeForCodex(t *testing.T, env *common.TestEnvironment) string {
	t.Helper()

	cmd := testguard.OxCmd(t, env.OxBinaryPath, env.ProjectDir, env.EnvVars,
		"agent", "prime", "--agent", "codex")

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

	// codex exec defaults to sandbox=read-only, which blocks ox agent prime
	// from writing to .sageox/. Real users run interactive codex (workspace-write
	// by default), so we match that here.
	args := []string{"exec", "-s", "workspace-write", prompt}

	cmdCtx, cancel := context.WithTimeout(ctx, agent.Timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, agent.CLIPath, args...)
	cmd.Dir = env.ProjectDir
	// strip Claude Code env vars so that when Codex runs `ox agent prime`
	// internally, agentx.CurrentAgent() detects Codex (not Claude Code from
	// inherited env vars like AGENT_ENV=claude, CLAUDE_CODE_ENTRYPOINT, etc.)
	cmd.Env = stripClaudeEnvVars(env.EnvVars)

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

	cmd := testguard.OxCmd(t, env.OxBinaryPath, env.ProjectDir, env.EnvVars,
		"agent", agentID, "session", "stop")

	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "session stop failed: %s", string(output))
	t.Log("session stop completed successfully")
}

// findCodexSessionFile locates the most recent Codex session file matching
// the test project directory.
func findCodexSessionFile(t *testing.T, env *common.TestEnvironment) string {
	t.Helper()

	home, err := os.UserHomeDir()
	require.NoError(t, err)

	sessionsDir := filepath.Join(home, ".codex", "sessions")
	now := time.Now()

	// scan today and yesterday (in case test crosses midnight or UTC offset)
	var candidates []string
	for day := 0; day < 2; day++ {
		d := now.AddDate(0, 0, -day)
		dateDir := filepath.Join(sessionsDir, d.Format("2006"), d.Format("01"), d.Format("02"))
		entries, err := os.ReadDir(dateDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
				candidates = append(candidates, filepath.Join(dateDir, entry.Name()))
			}
		}
	}

	require.NotEmpty(t, candidates, "no Codex session files found in ~/.codex/sessions/")

	// find the one matching our test project's CWD
	// resolve symlinks because macOS /var → /private/var
	resolvedProjectDir, _ := filepath.EvalSymlinks(env.ProjectDir)

	for i := len(candidates) - 1; i >= 0; i-- {
		data, err := os.ReadFile(candidates[i])
		if err != nil {
			continue
		}
		// check first line for session_meta with our CWD
		firstLine := strings.SplitN(string(data), "\n", 2)[0]
		if strings.Contains(firstLine, env.ProjectDir) || strings.Contains(firstLine, resolvedProjectDir) {
			return candidates[i]
		}
	}

	t.Fatalf("no Codex session file found matching project dir %s (resolved: %s, candidates: %d)",
		env.ProjectDir, resolvedProjectDir, len(candidates))
	return ""
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

// stripClaudeEnvVars removes Claude Code environment variables that would
// cause agentx.CurrentAgent() to misdetect Claude Code inside a Codex session.
func stripClaudeEnvVars(env []string) []string {
	var filtered []string
	for _, v := range env {
		key := v[:strings.IndexByte(v, '=')]
		if strings.HasPrefix(key, "CLAUDE") || key == "AGENT_ENV" {
			continue
		}
		filtered = append(filtered, v)
	}
	return filtered
}
