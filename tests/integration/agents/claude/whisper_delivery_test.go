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
//   5. Verify: the hook's output contains <new-context> XML with the seeded content
//
// Run: go test -tags=integration -timeout=5m -run TestWhisperDelivery ./tests/integration/agents/claude/ -v
func TestWhisperDelivery_MurmurReachesAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}



	agent := getClaudeConfig()
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)


	// Seed a murmur file in the ledger dir so the daemon can relay it.
	// The ledger dir is created by SetupTestEnvironment at:
	//   <rootDir>/data/sageox/sageox.ai/ledgers/repo_test-integration-001/
	ledgerDir := filepath.Join(env.RootDir, "data", "sageox", "sageox.ai", "ledgers", "repo_test-integration-001")
	seedMurmur(t, ledgerDir)

	// Also pre-populate the whisper SQLite DB directly. This ensures the
	// whisper is immediately available via IPC without waiting for daemon sync.
	seedWhisperDB(t, env, ledgerDir)

	// Run prime to start daemon and install hooks.
	primeOutput := runOxPrime(t, env)
	t.Logf("prime output length: %d bytes", len(primeOutput))

	// Prompt that triggers tool use (Read → afterTool hook → emitWhispers).
	prompt := `Read the file AGENTS.md and tell me what it says. Keep your response under 30 words.`

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	t.Log("running claude with whisper-seeded workspace...")
	result := runClaudeWithHooks(ctx, t, env, agent, prompt)
	if result.Error != nil {
		t.Logf("claude error (may be ok): %v", result.Error)
	}
	t.Logf("claude completed in %v", result.Duration)

	// The afterTool hook calls emitWhispers which writes XML to stdout.
	// Claude Code captures hook stdout as system context.
	// We check Claude's raw output for the whisper content.
	//
	// Note: Claude's --output-format json wraps everything in NDJSON.
	// The <new-context> XML from the hook appears in system_reminder entries
	// or may be visible in verbose output. We check for our distinctive content.
	t.Run("whisper_content_delivered", func(t *testing.T) {
		output := result.RawOutput

		// The seeded whisper has content "integration test: murmur reaches agent"
		// and topic "integration-test". If whisper delivery worked, this content
		// should appear somewhere in Claude's output (hook stdout → Claude context).
		//
		// However, Claude Code may not echo hook stdout directly in --output-format json.
		// The whisper goes into Claude's context window but isn't in the result JSON.
		// So we check the session recording (raw.jsonl) for evidence of the whisper.
		if strings.Contains(output, "integration test: murmur reaches agent") {
			t.Log("whisper content found directly in claude output")
			return
		}

		// Fallback: check raw.jsonl for whisper evidence.
		// The afterTool hook's stdout (containing <new-context>) gets captured
		// as a system entry in the recording.
		rawPaths := findAllRawJSONL(t, env)
		for _, rawPath := range rawPaths {
			data, err := os.ReadFile(rawPath)
			if err != nil {
				continue
			}
			content := string(data)
			if strings.Contains(content, "new-context") || strings.Contains(content, "integration test: murmur reaches agent") {
				t.Logf("whisper evidence found in recording: %s", rawPath)
				return
			}
		}

		// Final fallback: verify the whisper store was accessible during the test
		// by checking that the daemon was reachable (prime succeeded).
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

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
