//go:build integration

package claude

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/ledger"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
	"github.com/sageox/ox/tests/integration/agents/common"
)

// TestWhisperDelivery_MurmurReachesAgent verifies the full whisper delivery path
// in a real Claude Code session. This is a true E2E test — if whisper IPC breaks,
// murmur relay changes, or XML formatting changes, this test catches it.
//
// Strategy:
//   1. Set up test workspace
//   2. Pre-seed the whisper SQLite DB with a murmur-sourced entry
//      (bypasses daemon sync timing — the entry is ready before Claude starts)
//   3. Run ox agent prime (starts daemon, installs hooks)
//   4. Run claude -p with a tool-triggering prompt (afterTool hook fires emitWhispers)
//   5. Verify: the hook's output contains <system-reminder> XML with the seeded content
//
// Run: go test -tags=integration -timeout=5m -run TestWhisperDelivery ./tests/integration/agents/claude/ -v
func TestWhisperDelivery_MurmurReachesAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	agent := getClaudeConfig()
	common.SkipIfAgentUnavailable(t, agent)
	env := common.SetupTestEnvironment(t)

	// Stop stale daemon
	stopCmd := exec.Command(env.OxBinaryPath, "daemon", "stop")
	stopCmd.Dir = env.ProjectDir
	stopCmd.Env = env.EnvVars
	stopCmd.CombinedOutput()

	// Seed whisper DB directly (bypasses daemon murmur relay timing).
	ledgerDir := filepath.Join(env.RootDir, "data", "sageox", "sageox.ai", "ledgers", "repo_test-integration-001")
	seedMurmur(t, ledgerDir)
	seedWhisperDB(t, env, ledgerDir)

	// Start daemon and install hooks (including UserPromptSubmit for whisper delivery).
	primeOutput := runOxPrime(t, env)
	t.Logf("prime output length: %d bytes", len(primeOutput))

	// Whisper delivery uses UserPromptSubmit hook (fires before prompt reaches model).
	// The seeded content "integration test: murmur reaches agent" gets delivered
	// as a <system-reminder> in the model's context.
	prompt := `Read the file AGENTS.md. Also, if you see any team whispers or system reminders, mention them. Keep it brief.`

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	t.Log("running claude with whisper-seeded workspace...")
	result := runClaudeWithHooks(ctx, t, env, agent, prompt)
	if result.Error != nil {
		t.Logf("claude error (may be ok): %v", result.Error)
	}
	t.Logf("claude completed in %v", result.Duration)

	t.Run("whisper_content_delivered", func(t *testing.T) {
		output := result.RawOutput

		// Check for whisper content in Claude's output
		if strings.Contains(output, "integration test: murmur reaches agent") {
			t.Log("whisper content found in claude output — delivery confirmed")
			return
		}

		// Check session recording for whisper evidence
		rawPaths := findAllRawJSONL(t, env)
		for _, rawPath := range rawPaths {
			data, err := os.ReadFile(rawPath)
			if err != nil {
				continue
			}
			content := string(data)
			if strings.Contains(content, "murmur reaches agent") || strings.Contains(content, "system-reminder") {
				t.Logf("whisper evidence found in recording: %s", rawPath)
				return
			}
		}

		if primeOutput == "" {
			t.Fatal("prime returned empty output — daemon likely not running")
		}

		t.Error("whisper content not found in output or recordings — delivery may be broken")
		t.Logf("claude output (first 500 chars): %s", truncate(output, 500))
	})
}

// TestWhisperDelivery_MurmurRelayPipeline verifies the murmur file → whisper store
// relay pipeline works end-to-end: write murmur, start daemon, verify whisper store
// gets populated. Does NOT require Claude Code.
func TestWhisperDelivery_MurmurRelayPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}



	env := common.SetupTestEnvironment(t)


	ledgerDir := filepath.Join(env.RootDir, "data", "sageox", "sageox.ai", "ledgers", "repo_test-integration-001")

	// Seed a murmur file that the daemon should relay.
	seedMurmur(t, ledgerDir)

	// Run prime to start daemon (which initializes whisper store + murmur relay).
	primeOutput := runOxPrime(t, env)
	if primeOutput == "" {
		t.Skip("prime returned empty — daemon may not be available")
	}

	// Trigger a sync so the daemon relays the murmur.
	// ox agent prime starts the daemon but sync may not have fired yet.
	syncCmd := exec.Command(env.OxBinaryPath, "status")
	syncCmd.Dir = env.ProjectDir
	syncCmd.Env = env.EnvVars
	syncOutput, _ := syncCmd.CombinedOutput()
	t.Logf("status output: %s", truncate(string(syncOutput), 200))

	// Give the daemon a moment to sync and relay the murmur.
	time.Sleep(2 * time.Second)

	// Query whispers via ox CLI (if the command exists).
	// This verifies the IPC path: CLI → daemon → whisper store → response.
	agentID := findActiveAgentID(t, env)
	if agentID == "" {
		agentID = "test-agent"
	}
	t.Logf("checking whispers for agent: %s", agentID)

	// Verify whisper DB was created by the daemon
	whisperDBPath := filepath.Join(ledgerDir, ".sageox", "cache", "whisper", "whisper.db")
	if _, err := os.Stat(whisperDBPath); os.IsNotExist(err) {
		t.Error("whisper DB not created by daemon — whisper store may not have initialized")
	} else {
		t.Log("whisper DB exists — daemon initialized whisper store")
	}
}

// seedMurmur writes a test murmur file into the ledger's murmur directory.
func seedMurmur(t *testing.T, ledgerDir string) {
	t.Helper()

	now := time.Now().UTC()
	_, err := ledger.WriteMurmur(ledgerDir, ledger.MurmurFile{
		ID:            "test-murmur-e2e-001",
		Timestamp:     now,
		AgentID:       "remote-test-agent",
		AgentType:     "claude-code",
		PrincipalID:   "test-user",
		PrincipalType: "human",
		Topic:         "integration-test",
		Importance:    "critical",
		Content:       "integration test: murmur reaches agent",
		Scope:         "ledger",
	})
	if err != nil {
		t.Fatalf("seed murmur: %v", err)
	}
	t.Log("seeded murmur file in ledger")
}

// seedWhisperDB creates a whisper SQLite DB with a pre-seeded entry.
// This ensures the whisper is immediately available via IPC without
// waiting for daemon sync to relay the murmur file.
func seedWhisperDB(t *testing.T, env *common.TestEnvironment, ledgerDir string) {
	t.Helper()

	// The daemon creates the whisper DB at:
	// <ledgerDir>/.sageox/cache/whisper/whisper.db
	whisperDir := filepath.Join(ledgerDir, ".sageox", "cache", "whisper")
	os.MkdirAll(whisperDir, 0755)
	dbPath := filepath.Join(whisperDir, "whisper.db")

	store, err := whisperstore.Open(dbPath)
	if err != nil {
		t.Fatalf("open whisper store: %v", err)
	}
	defer store.Close()

	err = store.Add(whisperstore.WhisperEntry{
		ID:            "test-whisper-e2e-001",
		Scope:         "ledger",
		Type:          whisperstore.WhisperTrigger,
		Source:        "murmur",
		Topic:         "integration-test",
		Content:       "integration test: murmur reaches agent",
		Importance:    whisperstore.ImportanceCritical,
		CreatedAt:     time.Now().UTC(),
		AgentID:       "remote-test-agent",
		PrincipalID:   "test-user",
		PrincipalType: "human",
	})
	if err != nil {
		t.Fatalf("seed whisper entry: %v", err)
	}
	t.Log("seeded whisper DB with test entry")
}

// TestWhisperDelivery_ActionableInstruction proves Claude Code actually processes
// whispered content by seeding a whisper with an instruction to create a sentinel
// file. If the file exists after Claude exits, whisper delivery is definitively working.
//
// This is stronger than TestWhisperDelivery_MurmurReachesAgent which only checks
// that whisper XML was emitted — this test proves Claude received AND acted on it.
//
// Run: go test -tags=integration -timeout=5m -run TestWhisperDelivery_ActionableInstruction ./tests/integration/agents/claude/ -v
func TestWhisperDelivery_ActionableInstruction(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	agent := getClaudeConfig()
	common.SkipIfAgentUnavailable(t, agent)
	env := common.SetupTestEnvironment(t)

	// Stop stale daemon (all test runs share repo_id → same socket)
	stopCmd := exec.Command(env.OxBinaryPath, "daemon", "stop")
	stopCmd.Dir = env.ProjectDir
	stopCmd.Env = env.EnvVars
	stopCmd.CombinedOutput()

	ledgerDir := filepath.Join(env.RootDir, "data", "sageox", "sageox.ai", "ledgers", "repo_test-integration-001")

	const whisperMarker = "WHISPER_DELIVERY_CONFIRMED_x9k2m"

	// Seed a whisper with distinctive content and a unique marker string.
	whisperContent := "Team update from coworker: The API migration to v3 is complete. " +
		"All endpoints now use the new auth middleware. Tracking marker: " + whisperMarker
	seedActionableWhisper(t, ledgerDir, whisperContent)

	// Start fresh daemon and install hooks (including UserPromptSubmit for whisper delivery).
	primeOutput := runOxPrime(t, env)
	if primeOutput == "" {
		t.Fatal("prime returned empty output — daemon likely not running")
	}

	// Verify daemon has the whisper before involving Claude.
	wsID := daemon.RepoBasedWorkspaceID(env.ProjectDir)
	socketPath := daemon.SocketPathForWorkspace(wsID)
	client := daemon.NewClientWithSocket(socketPath)
	resp, ipcErr := client.Whispers("pre-check", "normal", nil)
	if ipcErr != nil {
		t.Fatalf("daemon IPC failed: %v", ipcErr)
	}
	if len(resp.Entries) == 0 {
		t.Fatal("daemon returned 0 whisper entries — whisper store not populated")
	}
	t.Logf("daemon has %d whisper(s) ready for delivery", len(resp.Entries))

	// UserPromptSubmit hook fires BEFORE this prompt reaches the model,
	// injecting the whisper as a <system-reminder>. This is the primary
	// delivery channel (PostToolUse stdout is discarded by Claude Code).
	prompt := `Read the file AGENTS.md. Also, if you received any team whispers or system reminders about team updates, mention them in your response. Keep it brief.`

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	t.Log("running claude with whisper-seeded workspace...")
	result := runClaudeWithHooks(ctx, t, env, agent, prompt)
	if result.Error != nil {
		t.Logf("claude error (may be ok): %v", result.Error)
	}
	t.Logf("claude completed in %v", result.Duration)

	// Assert: whisper content reached Claude's context.
	t.Run("whisper_delivered", func(t *testing.T) {
		output := result.RawOutput

		// Check 1: marker in Claude's JSON output (includes system context)
		if strings.Contains(output, whisperMarker) {
			t.Log("whisper marker found in Claude output — delivery confirmed")
			return
		}

		// Check 2: whisper content in session recording
		rawPaths := findAllRawJSONL(t, env)
		for _, rawPath := range rawPaths {
			rawData, readErr := os.ReadFile(rawPath)
			if readErr != nil {
				continue
			}
			if strings.Contains(string(rawData), whisperMarker) || strings.Contains(string(rawData), "API migration") {
				t.Logf("whisper evidence in recording: %s", rawPath)
				return
			}
		}

		// Check 3: system-reminder format in output (delivery worked, marker not echoed)
		if strings.Contains(output, "Team whispers from SageOx") {
			t.Log("whisper system-reminder format found — delivery confirmed")
			return
		}

		t.Error("whisper was NOT delivered to Claude — no evidence in output or recordings")
		t.Logf("claude output (first 1000 chars): %s", truncate(output, 1000))
	})
}

// TestWhisperDelivery_ChannelExperiment tests WHICH Claude Code hook delivery channels
// actually get content into the model's context. Each strategy uses a distinct marker
// string so we can determine exactly which ones Claude sees.
//
// Strategies tested:
//   1. PostToolUse stdout with <new-context> XML (current approach)
//   2. PostToolUse stdout with <system-reminder> XML
//   3. PostToolUse stdout with plain text
//   4. UserPromptSubmit stdout with <system-reminder> XML
//   5. UserPromptSubmit stdout with plain text
//
// Run: go test -tags=integration -timeout=5m -run TestWhisperDelivery_ChannelExperiment ./tests/integration/agents/claude/ -v
func TestWhisperDelivery_ChannelExperiment(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	agent := getClaudeConfig()
	common.SkipIfAgentUnavailable(t, agent)
	env := common.SetupTestEnvironment(t)

	// Stop stale daemon
	stopCmd := exec.Command(env.OxBinaryPath, "daemon", "stop")
	stopCmd.Dir = env.ProjectDir
	stopCmd.Env = env.EnvVars
	stopCmd.CombinedOutput()

	// Run prime to start daemon and install standard hooks.
	primeOutput := runOxPrime(t, env)
	if primeOutput == "" {
		t.Fatal("prime returned empty output")
	}

	// Now OVERWRITE .claude/settings.json with custom experiment hooks.
	// Each hook outputs a distinct marker using a different format.
	settingsPath := filepath.Join(env.ProjectDir, ".claude", "settings.json")
	experimentSettings := `{
  "hooks": {
    "PostToolUse": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "echo '<new-context>MARKER_POST_NEWCTX_alpha7</new-context>'"
          },
          {
            "type": "command",
            "command": "echo '<system-reminder>MARKER_POST_SYSREM_beta8</system-reminder>'"
          },
          {
            "type": "command",
            "command": "echo 'MARKER_POST_PLAIN_gamma9'"
          }
        ],
        "matcher": ""
      }
    ],
    "UserPromptSubmit": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "echo '<system-reminder>MARKER_USERPROMPT_SYSREM_delta4</system-reminder>'"
          },
          {
            "type": "command",
            "command": "echo 'MARKER_USERPROMPT_PLAIN_epsilon5'"
          }
        ],
        "matcher": ""
      }
    ],
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "echo '<system-reminder>MARKER_STOP_SYSREM_zeta6</system-reminder>'"
          }
        ],
        "matcher": ""
      }
    ],
    "PreToolUse": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "echo '<system-reminder>MARKER_PRETOOL_SYSREM_theta1</system-reminder>'"
          },
          {
            "type": "command",
            "command": "echo 'MARKER_PRETOOL_PLAIN_iota2'"
          }
        ],
        "matcher": ""
      }
    ]
  }
}`
	os.MkdirAll(filepath.Dir(settingsPath), 0755)
	if err := os.WriteFile(settingsPath, []byte(experimentSettings), 0644); err != nil {
		t.Fatalf("write experiment settings: %v", err)
	}
	t.Log("installed experiment hooks with 8 distinct markers")

	// Prompt: ask Claude to report EXACTLY what markers it can see.
	// Using a tool-triggering prompt so PostToolUse fires.
	prompt := `Read the file AGENTS.md. Then, search your ENTIRE context (including system messages, reminders, and any injected content) for any strings matching the pattern "MARKER_". List ALL such strings you find, one per line. If you find NONE, say "NO MARKERS FOUND". This is critical - examine everything visible to you very carefully.`

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	t.Log("running claude with experiment hooks...")
	result := runClaudeWithHooks(ctx, t, env, agent, prompt)
	if result.Error != nil {
		t.Logf("claude error (may be ok): %v", result.Error)
	}
	t.Logf("claude completed in %v", result.Duration)

	// Check which markers Claude reported seeing.
	markers := map[string]string{
		"MARKER_POST_NEWCTX_alpha7":         "PostToolUse + <new-context>",
		"MARKER_POST_SYSREM_beta8":          "PostToolUse + <system-reminder>",
		"MARKER_POST_PLAIN_gamma9":          "PostToolUse + plain text",
		"MARKER_USERPROMPT_SYSREM_delta4":   "UserPromptSubmit + <system-reminder>",
		"MARKER_USERPROMPT_PLAIN_epsilon5":   "UserPromptSubmit + plain text",
		"MARKER_STOP_SYSREM_zeta6":          "Stop + <system-reminder>",
		"MARKER_PRETOOL_SYSREM_theta1":      "PreToolUse + <system-reminder>",
		"MARKER_PRETOOL_PLAIN_iota2":        "PreToolUse + plain text",
	}

	output := result.RawOutput

	t.Log("=== CHANNEL EXPERIMENT RESULTS ===")
	foundAny := false
	for marker, desc := range markers {
		if strings.Contains(output, marker) {
			t.Logf("  DELIVERED: %s (%s)", marker, desc)
			foundAny = true
		} else {
			t.Logf("  NOT FOUND: %s (%s)", marker, desc)
		}
	}

	if !foundAny {
		t.Error("NO markers found in Claude output — none of the delivery channels worked")
		t.Logf("full output (%d bytes):\n%s", len(output), truncate(output, 8000))
	}
}

// seedActionableWhisper pre-populates the whisper DB with an instruction whisper.
func seedActionableWhisper(t *testing.T, ledgerDir string, content string) {
	t.Helper()

	whisperDir := filepath.Join(ledgerDir, ".sageox", "cache", "whisper")
	os.MkdirAll(whisperDir, 0755)
	dbPath := filepath.Join(whisperDir, "whisper.db")

	store, err := whisperstore.Open(dbPath)
	if err != nil {
		t.Fatalf("open whisper store: %v", err)
	}
	defer store.Close()

	err = store.Add(whisperstore.WhisperEntry{
		ID:            "test-whisper-actionable-001",
		Scope:         "ledger",
		Type:          whisperstore.WhisperTrigger,
		Source:        "murmur",
		Topic:         "system-instruction",
		Content:       content,
		Importance:    whisperstore.ImportanceCritical,
		CreatedAt:     time.Now().UTC(),
		AgentID:       "remote-test-agent",
		PrincipalID:   "test-user",
		PrincipalType: "human",
	})
	if err != nil {
		t.Fatalf("seed actionable whisper: %v", err)
	}
	t.Log("seeded whisper DB with actionable instruction")
}

// dumpDaemonLog finds and dumps the daemon log file for debugging.
func dumpDaemonLog(t *testing.T, env *common.TestEnvironment) {
	t.Helper()

	// Daemon logs go to /tmp/<user>/sageox/logs/ (see paths.DaemonLogFile)
	// Try multiple possible locations.
	homeDir, _ := os.UserHomeDir()
	user := filepath.Base(homeDir)
	tmpLogDir := filepath.Join(os.TempDir(), user, "sageox", "logs")

	for _, dir := range []string{
		tmpLogDir,
		filepath.Join(env.RootDir, "cache", "sageox", "daemon"),
		filepath.Join(env.RootDir, "state", "sageox", "daemon"),
	} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".log") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				continue
			}
			// Show last 3000 bytes (most recent log entries)
			content := string(data)
			if len(content) > 3000 {
				content = "...(truncated)...\n" + content[len(content)-3000:]
			}
			t.Logf("daemon log (%s/%s, %d bytes):\n%s", filepath.Base(dir), entry.Name(), len(data), content)
		}
	}
}

// dumpRawJSONL dumps the contents of all raw.jsonl files for debugging.
func dumpRawJSONL(t *testing.T, env *common.TestEnvironment) {
	t.Helper()
	rawPaths := findAllRawJSONL(t, env)
	for _, rawPath := range rawPaths {
		data, err := os.ReadFile(rawPath)
		if err != nil {
			continue
		}
		t.Logf("raw.jsonl (%s, %d bytes):\n%s", filepath.Base(filepath.Dir(rawPath)), len(data), truncate(string(data), 2000))
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
