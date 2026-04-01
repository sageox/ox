//go:build integration

// Tests for the Codex CLI hook → ox agent hook pipeline.
// These verify that when Codex fires SessionStart hooks from .codex/hooks.json,
// the ox lifecycle hooks execute correctly and session recording starts.
//
// PREREQUISITE: The codex_hooks feature must be enabled AND functional:
//
//	codex features enable codex_hooks
//
// The test probes for hook functionality using a canary command. If hooks aren't
// executing commands yet (the feature is "under development" as of April 2026),
// the test skips with an actionable message.
//
// Run with:
//
//	go test -tags=integration -timeout=5m -run TestCodexHook ./tests/integration/agents/codex/ -v
package codex

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/testguard"
	"github.com/sageox/ox/tests/integration/agents/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// probeCodexHooksExecute checks whether the codex_hooks feature actually
// executes hook commands (not just emits lifecycle events). Returns true if
// a canary touch command creates a file during codex exec.
//
// This is necessary because codex_hooks is "under development" — the feature
// flag enables event recognition but command execution may not be implemented.
func probeCodexHooksExecute(t *testing.T) bool {
	t.Helper()

	probeDir := t.TempDir()

	// set up minimal project with canary hook
	codexDir := filepath.Join(probeDir, ".codex")
	require.NoError(t, os.MkdirAll(codexDir, 0755))

	// init git so codex exec has a working directory
	cmd := exec.Command("git", "init")
	cmd.Dir = probeDir
	cmd.CombinedOutput()

	canaryFile := filepath.Join(probeDir, "hook_canary")
	hooks := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []map[string]interface{}{
				{
					"matcher": "",
					"hooks": []map[string]interface{}{
						{
							"type":    "command",
							"command": "touch " + canaryFile,
						},
					},
				},
			},
		},
	}

	hooksData, err := json.MarshalIndent(hooks, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(codexDir, "hooks.json"), hooksData, 0644))

	// enable the codex_hooks feature flag in project config.toml —
	// without this, Codex ignores hooks.json entirely
	configToml := "[features]\ncodex_hooks = true\n"
	require.NoError(t, os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte(configToml), 0644))

	// run codex exec with a trivial prompt — we only care if the hook fires
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	probe := exec.CommandContext(ctx, "codex", "exec",
		"--enable", "codex_hooks",
		"-s", "workspace-write",
		"Say done")
	probe.Dir = probeDir
	// strip Claude env vars to avoid agent detection confusion
	probe.Env = stripClaudeEnvVars(os.Environ()) // safe: probing codex CLI directly, not ox
	probe.CombinedOutput() // ignore errors (rate limits, etc.)

	// check if the canary file was created
	_, err = os.Stat(canaryFile)
	return err == nil
}

// TestCodexHookSessionStart_RecordingState verifies that when Codex fires
// SessionStart hooks, the ox agent hook pipeline starts session recording
// with correct state.
//
// Flow:
//  1. Probe whether codex_hooks actually executes commands (skip if not)
//  2. Set up test environment with ox built
//  3. Run ox integrate install --codex to create .codex/hooks.json
//  4. Run codex exec which fires SessionStart hook → ox agent hook SessionStart
//  5. Verify .recording.json exists with correct agent type and watch mode
//
// This catches: hook command format mismatches, event name mismatches,
// hook execution failures, and recording state issues.
func TestCodexHookSessionStart_RecordingState(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	agent := getCodexConfig()
	common.SkipIfAgentUnavailable(t, agent)

	// probe whether hooks actually execute commands
	if !probeCodexHooksExecute(t) {
		t.Skip("codex_hooks feature is not executing commands yet " +
			"(feature is 'under development' — hook lifecycle events fire but " +
			"commands are not executed). Re-run when Codex ships hook execution.")
	}

	env := common.SetupTestEnvironment(t)

	// install codex hooks via ox integrate
	installOut, installCode, _ := testguard.RunOx(t, env.OxBinaryPath, env.ProjectDir, env.EnvVars,
		"integrate", "install", "--codex")
	require.Equal(t, 0, installCode, "ox integrate install --codex failed: %s", installOut)
	t.Log("installed codex hooks via ox integrate")

	// verify hooks.json was created with ox hooks
	hooksPath := filepath.Join(env.ProjectDir, ".codex", "hooks.json")
	hooksData, err := os.ReadFile(hooksPath)
	require.NoError(t, err, "hooks.json should exist after install")
	require.Contains(t, string(hooksData), "ox agent hook",
		"hooks.json should contain ox agent hook command")
	t.Logf("hooks.json contents:\n%s", string(hooksData))

	// run ox agent prime first to bootstrap agent instance
	primeOutput := runOxPrimeForCodex(t, env)
	agentID := extractAgentID(t, primeOutput)
	require.NotEmpty(t, agentID)
	t.Logf("agent ID: %s", agentID)

	// run codex exec — this should fire SessionStart hook → ox agent hook
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	t.Log("running codex exec (hooks should fire SessionStart)...")
	result := runCodexExec(ctx, t, env, agent,
		`Read the file AGENTS.md and tell me what it says. Keep your response under 30 words.`)
	if result.Error != nil {
		t.Logf("codex error (may be ok — rate limits): %v", result.Error)
	}
	t.Logf("codex completed in %v", result.Duration)

	t.Run("recording_exists_with_hook_mode", func(t *testing.T) {
		matches := findFilesRecursive(env.RootDir, ".recording.json")
		require.NotEmpty(t, matches, ".recording.json should exist after hook fires")

		found := false
		for _, path := range matches {
			data, err := os.ReadFile(path)
			require.NoError(t, err)

			var state struct {
				AgentID     string `json:"agent_id"`
				WatchMode   string `json:"watch_mode"`
				AdapterName string `json:"adapter_name"`
				ParentPID   int    `json:"parent_pid"`
			}
			require.NoError(t, json.Unmarshal(data, &state))

			if state.AgentID == "" {
				continue
			}
			found = true

			t.Logf("recording state: agent_id=%s watch_mode=%s adapter=%s parent_pid=%d",
				state.AgentID, state.WatchMode, state.AdapterName, state.ParentPID)

			// hooks mode should be set (not tail — tail is for hookless Codex)
			assert.NotEqual(t, "tail", state.WatchMode,
				"with hooks installed, watch_mode should not be 'tail'")
			assert.Greater(t, state.ParentPID, 0,
				"ParentPID should be set by hook")
		}
		require.True(t, found, "should find .recording.json with agent state")
	})

	t.Run("session_stop", func(t *testing.T) {
		stopSession(t, env, agentID)
	})
}

// TestCodexHookInstall_WritesValidHooksJSON verifies that ox integrate install --codex
// creates a valid .codex/hooks.json that Codex can parse. This test does NOT require
// hooks to be functional — it verifies the ox-side output is correct.
//
// This catches: malformed JSON, wrong event names, missing required fields.
func TestCodexHookInstall_WritesValidHooksJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	agent := getCodexConfig()
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)

	// create .codex/ directory (simulates codex being initialized in project)
	codexDir := filepath.Join(env.ProjectDir, ".codex")
	require.NoError(t, os.MkdirAll(codexDir, 0755))

	// install hooks
	installOut, installCode, _ := testguard.RunOx(t, env.OxBinaryPath, env.ProjectDir, env.EnvVars,
		"integrate", "install", "--codex")
	require.Equal(t, 0, installCode, "install failed: %s", installOut)

	// read and validate the hooks file
	hooksPath := filepath.Join(codexDir, "hooks.json")
	data, err := os.ReadFile(hooksPath)
	require.NoError(t, err)

	// must be valid JSON
	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &raw), "hooks.json must be valid JSON")

	// must have hooks key
	hooks, ok := raw["hooks"].(map[string]interface{})
	require.True(t, ok, "hooks.json must have 'hooks' object")

	// must have SessionStart event
	sessionStart, ok := hooks["SessionStart"].([]interface{})
	require.True(t, ok, "hooks must have 'SessionStart' event")
	require.NotEmpty(t, sessionStart, "SessionStart must have entries")

	// verify hook entries have required fields
	for _, entry := range sessionStart {
		entryMap, ok := entry.(map[string]interface{})
		require.True(t, ok)

		hooksList, ok := entryMap["hooks"].([]interface{})
		require.True(t, ok, "entry must have 'hooks' array")

		for _, hook := range hooksList {
			hookMap, ok := hook.(map[string]interface{})
			require.True(t, ok)

			assert.Equal(t, "command", hookMap["type"],
				"hook type must be 'command'")
			assert.NotEmpty(t, hookMap["command"],
				"hook must have a command")
		}
	}

	// verify ox hook command is present
	assert.Contains(t, string(data), "ox agent hook SessionStart",
		"must contain ox agent hook for SessionStart")

	t.Logf("hooks.json is valid with %d SessionStart entries", len(sessionStart))
}

// TestCodexHookDoctor_DetectsInstalledHooks verifies that ox doctor correctly
// detects Codex hooks when installed. This tests the doctor → checkAgentHooks
// pipeline without requiring hooks to be functional.
func TestCodexHookDoctor_DetectsInstalledHooks(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	agent := getCodexConfig()
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)

	// install hooks first
	codexDir := filepath.Join(env.ProjectDir, ".codex")
	require.NoError(t, os.MkdirAll(codexDir, 0755))

	installOut, installCode, _ := testguard.RunOx(t, env.OxBinaryPath, env.ProjectDir, env.EnvVars,
		"integrate", "install", "--codex")
	require.Equal(t, 0, installCode, "install failed: %s", installOut)

	// run doctor and check it sees the hooks
	doctorOut, doctorCode, _ := testguard.RunOx(t, env.OxBinaryPath, env.ProjectDir, env.EnvVars,
		"doctor")
	t.Logf("doctor output:\n%s", doctorOut)

	// doctor may exit non-zero for other checks (auth, etc) but should show Codex hooks as installed
	_ = doctorCode
	assert.True(t,
		strings.Contains(doctorOut, "Codex") && strings.Contains(doctorOut, "installed"),
		"doctor should report Codex hooks as installed")
}
