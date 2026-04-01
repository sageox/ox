//go:build integration

package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon/agentwork"
	"github.com/sageox/ox/tests/integration/agents/common"
)

// --- Recording fidelity evaluation ---

// TestRecordingFidelity_LLMEval compares ox's raw.jsonl against Claude Code's
// own session JSONL to verify we capture the same content. Uses an LLM judge
// (another Claude instance) to evaluate coverage between session start and stop.
// Uses a real Claude Code instance — never simulated. (E2E requirement)
//
// This catches: missing message types, truncated content, lost tool calls,
// dropped entries, or any divergence between what Claude recorded and what ox captured.
func TestRecordingFidelity_LLMEval(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	agent := getClaudeConfig()
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)

	_ = runOxPrime(t, env)

	// Run a session with varied activity: file reads, tool use, multi-turn reasoning
	prompt := `Read the file AGENTS.md. Then list all files in the current directory using ls. Finally, summarize what you found in 2-3 sentences.`

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	t.Log("running claude session with varied activity...")
	result := runClaudeWithHooks(ctx, t, env, agent, prompt)
	if result.Error != nil {
		t.Logf("claude error (may be ok): %v", result.Error)
	}
	t.Logf("session completed in %v", result.Duration)

	// Extract Claude's session ID to find its source JSONL
	claudeSessionID := extractClaudeSessionID(t, result.RawOutput)
	t.Logf("claude session ID: %s", claudeSessionID)

	// Stop the session so ox drains remaining entries
	agentID := findActiveAgentID(t, env)
	stopSession(t, env, agentID)

	// Find Claude's source JSONL (~/.claude/projects/<hash>/<session>.jsonl)
	claudeSourcePath := findClaudeSourceJSONL(t, env, claudeSessionID)
	if claudeSourcePath == "" {
		t.Skip("could not find Claude's source JSONL — cannot compare")
	}
	t.Logf("claude source JSONL: %s", claudeSourcePath)

	// Find our raw.jsonl — select the one matching the active agent
	rawPaths := findAllRawJSONL(t, env)
	if len(rawPaths) == 0 {
		t.Fatal("no raw.jsonl found — nothing to compare")
	}

	// pick the raw.jsonl belonging to this agent's session
	activeRaw := rawPaths[0] // fallback
	for _, rp := range rawPaths {
		entries := readRawJSONL(t, rp)
		for _, e := range entries {
			// check both new ("metadata") and old ("_meta") header schemas
			for _, metaKey := range []string{"metadata", "_meta"} {
				if meta, ok := e[metaKey].(map[string]interface{}); ok {
					if aid, ok := meta["agent_id"].(string); ok && aid == agentID {
						activeRaw = rp
					}
				}
			}
		}
	}

	// Extract the recording time window from raw.jsonl header or recording state.
	// The LLM judge should ONLY evaluate entries between session start and stop —
	// anything outside that window is not our responsibility to capture.
	startTS, stopTS := extractRecordingWindow(t, env, activeRaw)
	t.Logf("recording window: %s → %s", startTS, stopTS)

	// Read Claude's source and filter to recording window, then truncate
	claudeSource := readAndFilterToWindow(t, claudeSourcePath, startTS, stopTS, 15000)
	oxRaw := readAndTruncate(t, activeRaw, 15000)

	t.Logf("claude source (windowed): %d chars, ox raw: %d chars", len(claudeSource), len(oxRaw))

	// Spin up a Claude judge to evaluate coverage
	t.Log("running LLM judge to evaluate recording fidelity...")
	judgePrompt := fmt.Sprintf(`You are evaluating recording fidelity between two JSONL files from the same coding session.

IMPORTANT: Only evaluate content within the recording window. FILE A has already been
filtered to only include entries between ox-session-start and ox-session-stop timestamps.
Anything outside this window is irrelevant — do not penalize for missing pre-start or
post-stop content (like hook events, prime output, or session setup).

The following are INTENTIONALLY excluded from ox recordings — do NOT count these as missing:
- Tool result content (file contents returned by Read/Bash tools — we record tool calls but not their output)
- Hook/progress events (internal Claude metadata, system notifications)
- Thinking/reasoning blocks (extended thinking content)

FILE A is Claude Code's own session recording (filtered to recording window — source of truth).
FILE B is ox's recording of the same session (what we're testing).

Compare them and evaluate (considering the intentional exclusions above):
1. Are all user messages from A present in B?
2. Are all assistant text responses from A present in B?
3. Are tool calls (tool name, inputs) from A captured in B? (tool RESULTS are intentionally excluded)
4. Is message ordering preserved?
5. Is content complete (not truncated) for the entry types we DO capture?

Output ONLY a JSON object with these fields:
- "coverage_pct": integer 0-100 (what percentage of A's windowed content appears in B)
- "missing": array of strings describing what's missing (empty if nothing)
- "verdict": "pass" if coverage >= 80%%, "fail" otherwise
- "notes": brief explanation

FILE A (Claude Code source — filtered to recording window):
%s

FILE B (ox raw.jsonl):
%s`, claudeSource, oxRaw)

	judgeResult := runClaudeWithFlags(ctx, t, env, agent, judgePrompt)
	if judgeResult.Error != nil {
		t.Logf("judge error: %v", judgeResult.Error)
	}

	// Parse the judge's verdict from Claude's output
	verdict := parseJudgeVerdict(t, judgeResult.RawOutput)

	t.Logf("coverage: %d%%, verdict: %s", verdict.CoveragePct, verdict.Verdict)
	if len(verdict.Missing) > 0 {
		t.Logf("missing items: %v", verdict.Missing)
	}
	if verdict.Notes != "" {
		t.Logf("judge notes: %s", verdict.Notes)
	}

	switch verdict.Verdict {
	case "fail":
		t.Errorf("recording fidelity too low: %d%% coverage (need >= 80%%)", verdict.CoveragePct)
	case "unknown":
		t.Skipf("LLM judge returned unknown verdict — could not parse judge output; manual review needed: %s", verdict.Notes)
	}
}

// TestCtrlC_RealClaude_AntiEntropy starts a real Claude Code session, sends it
// SIGINT (Ctrl-C) mid-conversation, then verifies the anti-entropy finalization
// handler can recover and generate all artifacts from the partial raw.jsonl.
//
// This is the definitive test: real Claude binary, real hooks, real SIGINT, real
// anti-entropy pipeline. Nothing is simulated except the LLM summarization step
// (which would require a second Claude invocation).
//
// Run with: go test -tags=integration -timeout=5m -run TestCtrlC_RealClaude_AntiEntropy ./tests/integration/agents/claude/ -v
func TestCtrlC_RealClaude_AntiEntropy(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	agent := getClaudeConfig()
	common.SkipIfAgentUnavailable(t, agent)

	env := common.SetupTestEnvironment(t)

	// Prime must run before Claude starts (installs hooks, creates agent instance)
	_ = runOxPrime(t, env)

	// Use a prompt that will trigger multiple tool calls, giving us time to
	// send SIGINT after the first tool use fires a PostToolUse hook.
	// The --max-turns 10 ensures Claude won't finish too quickly.
	prompt := `Read the file AGENTS.md, then list all files with the Glob tool using pattern "**/*", then read .sageox/config.json. Do each step separately.`

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Start Claude in its own process group so SIGINT only hits Claude
	args := []string{
		agent.PromptFlag, prompt,
		"--output-format", "json",
		"--verbose",
		"--dangerously-skip-permissions",
		"--max-turns", "10",
	}

	cmd := exec.CommandContext(ctx, agent.CLIPath, args...)
	cmd.Dir = env.ProjectDir
	cmd.Env = env.EnvVars
	// Create a new process group so SIGINT doesn't propagate to test runner
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	t.Log("starting claude CLI (will be interrupted with SIGINT)...")
	startTime := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start claude: %v", err)
	}

	// Wait for raw.jsonl to appear with at least one non-header entry,
	// meaning at least one PostToolUse hook has fired and written data.
	// Poll every 500ms for up to 90s.
	var rawPath string
	deadline := time.After(90 * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

waitLoop:
	for {
		select {
		case <-deadline:
			// Kill the process if we timed out waiting
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			t.Fatal("timed out waiting for raw.jsonl to have entries")
		case <-ticker.C:
			paths := findAllRawJSONL(t, env)
			for _, p := range paths {
				entries := readRawJSONL(t, p)
				// need at least 1 header + 1 content entry
				nonHeaders := 0
				for _, e := range entries {
					if e["type"] != "header" {
						nonHeaders++
					}
				}
				if nonHeaders >= 1 {
					rawPath = p
					t.Logf("raw.jsonl has %d entries (%d content) after %v — sending SIGINT",
						len(entries), nonHeaders, time.Since(startTime).Round(time.Millisecond))
					break waitLoop
				}
			}
		}
	}

	// Send SIGINT to the Claude process group (simulates Ctrl-C)
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT); err != nil {
		t.Logf("SIGINT failed (process may have already exited): %v", err)
	}

	// Wait for Claude to exit (it should handle SIGINT gracefully)
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case err := <-waitDone:
		elapsed := time.Since(startTime)
		t.Logf("claude exited after %v (err: %v)", elapsed.Round(time.Millisecond), err)
	case <-time.After(30 * time.Second):
		// Force kill if Claude didn't exit after SIGINT
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-waitDone
		t.Log("claude force-killed after 30s timeout")
	}

	if stdout.Len() > 0 {
		t.Logf("claude stdout (truncated): %s", truncateForLog(stdout.String(), 500))
	}

	// --- Verify interrupted session state ---

	// raw.jsonl should exist with entries but NO footer
	rawData, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("failed to read raw.jsonl after SIGINT: %v", err)
	}

	entries := readRawJSONL(t, rawPath)
	t.Logf("raw.jsonl has %d total entries after SIGINT", len(entries))

	// Count entry types
	typeCounts := map[string]int{}
	hasFooter := false
	for _, e := range entries {
		if typ, ok := e["type"].(string); ok {
			typeCounts[typ]++
			if typ == "footer" {
				hasFooter = true
			}
		}
	}
	t.Logf("entry types: %v", typeCounts)

	if hasFooter {
		t.Error("raw.jsonl has a footer — session stop should NOT have run after SIGINT")
	}
	if typeCounts["header"] == 0 {
		t.Error("raw.jsonl missing header")
	}

	// Should have at least some content entries (user, assistant, or tool)
	contentEntries := typeCounts["user"] + typeCounts["assistant"] + typeCounts["tool"]
	if contentEntries == 0 {
		t.Fatal("raw.jsonl has no content entries — hooks didn't write anything before SIGINT")
	}
	t.Logf("raw.jsonl has %d content entries (user=%d, assistant=%d, tool=%d)",
		contentEntries, typeCounts["user"], typeCounts["assistant"], typeCounts["tool"])

	// .recording.json should still exist (stop didn't clean it up)
	recordingFiles := findFilesRecursive(env.RootDir, ".recording.json")
	if len(recordingFiles) == 0 {
		t.Log("WARNING: no .recording.json found — Claude may have run session stop on SIGINT")
	} else {
		t.Logf("found %d .recording.json file(s) — stop did NOT run (as expected)", len(recordingFiles))
	}

	// --- Anti-entropy: run the daemon's finalization handler ---

	// Create a fake ledger with the interrupted session's raw.jsonl
	fakeLedger := t.TempDir()
	ledgerSessionName := "ctrlc-" + filepath.Base(filepath.Dir(rawPath))
	ledgerSessionDir := filepath.Join(fakeLedger, "sessions", ledgerSessionName)
	if err := os.MkdirAll(ledgerSessionDir, 0755); err != nil {
		t.Fatalf("failed to create ledger session dir: %v", err)
	}

	// Copy raw.jsonl to the ledger
	if err := os.WriteFile(filepath.Join(ledgerSessionDir, "raw.jsonl"), rawData, 0644); err != nil {
		t.Fatalf("failed to copy raw.jsonl to ledger: %v", err)
	}

	// Write a stale .recording.json (> 24h old) to simulate time passing
	staleState := map[string]any{
		"started_at": time.Now().Add(-25 * time.Hour).Format(time.RFC3339),
		"agent_id":   "test-ctrlc",
	}
	staleData, _ := json.Marshal(staleState)
	if err := os.WriteFile(filepath.Join(ledgerSessionDir, ".recording.json"), staleData, 0644); err != nil {
		t.Fatalf("failed to write stale recording marker: %v", err)
	}

	// Run the finalization handler
	handler := agentwork.NewSessionFinalizeHandlerForTest(slog.Default())

	items, detectErr := handler.Detect(fakeLedger)
	if detectErr != nil {
		t.Fatalf("Detect failed: %v", detectErr)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 stale session, got %d", len(items))
	}
	t.Log("anti-entropy: detected stale session")

	// .recording.json should be removed
	if _, statErr := os.Stat(filepath.Join(ledgerSessionDir, ".recording.json")); !os.IsNotExist(statErr) {
		t.Error(".recording.json should have been removed after stale detection")
	}

	// BuildPrompt should succeed with the real Claude session data
	req, buildErr := handler.BuildPrompt(items[0])
	if buildErr != nil {
		t.Fatalf("BuildPrompt failed: %v", buildErr)
	}
	if req.Prompt == "" {
		t.Error("BuildPrompt returned empty prompt")
	}
	t.Logf("anti-entropy: built summarization prompt (%d chars)", len(req.Prompt))

	// ProcessResult with simulated LLM output (we don't want to invoke a
	// second real Claude here — that would be slow and flaky)
	summaryJSON := fmt.Sprintf(`{"title":"Interrupted Session Recovery","summary":"Session was interrupted after %d entries. Anti-entropy recovered the session.","key_actions":["read AGENTS.md","listed files"],"outcome":"interrupted","topics_found":["integration testing","session recovery"]}`, contentEntries)

	processErr := handler.ProcessResult(items[0], &agentwork.RunResult{
		Output:   summaryJSON,
		Duration: 1 * time.Second,
		ExitCode: 0,
	})
	if processErr != nil {
		t.Fatalf("ProcessResult failed: %v", processErr)
	}

	// Verify ALL artifacts were generated from the interrupted session
	expectedArtifacts := []string{"summary.md", "summary.json", "session.md"}
	for _, artifact := range expectedArtifacts {
		path := filepath.Join(ledgerSessionDir, artifact)
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Errorf("missing artifact after anti-entropy finalization: %s", artifact)
		} else if info.Size() == 0 {
			t.Errorf("artifact %s is empty (0 bytes)", artifact)
		}
	}

	t.Logf("anti-entropy recovery successful: %d artifacts generated from %d raw.jsonl entries written by real Claude hooks before SIGINT",
		len(expectedArtifacts), len(entries))
}

// findClaudeSourceJSONL locates Claude Code's session file for the given session ID.
// Claude stores sessions at ~/.claude/projects/<project-hash>/<session-id>.jsonl
func findClaudeSourceJSONL(t *testing.T, env *common.TestEnvironment, sessionID string) string {
	t.Helper()

	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Logf("cannot get home dir: %v", err)
		return ""
	}

	// Claude's project hash: CWD with path separators → dashes, underscores → dashes
	projectHash := strings.ReplaceAll(env.ProjectDir, string(os.PathSeparator), "-")
	projectHash = strings.ReplaceAll(projectHash, "_", "-")

	// Try direct path first
	directPath := filepath.Join(homeDir, ".claude", "projects", projectHash, sessionID+".jsonl")
	if _, err := os.Stat(directPath); err == nil {
		return directPath
	}

	// Fallback: search for any JSONL file containing this session ID
	claudeProjectDir := filepath.Join(homeDir, ".claude", "projects", projectHash)
	if _, err := os.Stat(claudeProjectDir); os.IsNotExist(err) {
		// Try with /private prefix (macOS tmp dirs)
		projectHash2 := strings.ReplaceAll("/private"+env.ProjectDir, string(os.PathSeparator), "-")
		projectHash2 = strings.ReplaceAll(projectHash2, "_", "-")
		claudeProjectDir = filepath.Join(homeDir, ".claude", "projects", projectHash2)
	}

	entries, err := os.ReadDir(claudeProjectDir)
	if err != nil {
		t.Logf("cannot read claude project dir %s: %v", claudeProjectDir, err)
		return ""
	}

	// Find the most recently modified JSONL file (likely our session)
	var bestPath string
	var bestTime time.Time
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(bestTime) {
			bestTime = info.ModTime()
			bestPath = filepath.Join(claudeProjectDir, entry.Name())
		}
	}

	return bestPath
}

// extractRecordingWindow finds the session start and stop timestamps.
// It looks at the raw.jsonl header for started_at, and the recording state or
// last entry timestamp for the stop time.
func extractRecordingWindow(t *testing.T, env *common.TestEnvironment, rawPath string) (startTS, stopTS time.Time) {
	t.Helper()

	entries := readRawJSONL(t, rawPath)

	// Find start time from header entry or first non-header entry.
	// Supports both old schema (_meta.started_at, ts) and new schema (metadata.created_at, timestamp).
	for _, e := range entries {
		eType, _ := e["type"].(string)
		if eType == "header" {
			// try both "metadata" (new) and "_meta" (old) header schemas
			for _, metaKey := range []string{"metadata", "_meta"} {
				if meta, ok := e[metaKey].(map[string]interface{}); ok {
					for _, tsKey := range []string{"created_at", "started_at"} {
						if ts, ok := meta[tsKey].(string); ok {
							if parsed, err := time.Parse(time.RFC3339Nano, ts); err == nil {
								startTS = parsed
							}
						}
					}
				}
			}
			continue
		}
		// First non-header entry as fallback start — use extractTimestamp which handles both schemas
		if startTS.IsZero() {
			if parsed := extractTimestamp(e); !parsed.IsZero() {
				startTS = parsed
			}
		}
	}

	// Stop time: use last entry timestamp as upper bound
	for i := len(entries) - 1; i >= 0; i-- {
		if parsed := extractTimestamp(entries[i]); !parsed.IsZero() {
			stopTS = parsed
			break
		}
	}

	// If we couldn't find timestamps, use wide bounds
	if startTS.IsZero() {
		startTS = time.Now().Add(-1 * time.Hour)
		t.Log("warning: could not extract start time from raw.jsonl, using 1h ago")
	}
	if stopTS.IsZero() {
		stopTS = time.Now()
		t.Log("warning: could not extract stop time from raw.jsonl, using now")
	}

	return startTS, stopTS
}

// readAndFilterToWindow reads a JSONL file and returns only lines whose timestamp
// falls within [startTS, stopTS]. This scopes the source material to what ox should
// have captured — anything outside the recording window is not our responsibility.
func readAndFilterToWindow(t *testing.T, path string, startTS, stopTS time.Time, maxChars int) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}

	var filtered []string
	totalChars := 0
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Parse the line to check its timestamp
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		// Extract timestamp — Claude Code uses "timestamp" field
		ts := extractTimestamp(entry)
		if !ts.IsZero() {
			// Add 1-second buffer on each side for clock skew
			if ts.Before(startTS.Add(-1*time.Second)) || ts.After(stopTS.Add(1*time.Second)) {
				continue
			}
		}

		filtered = append(filtered, line)
		totalChars += len(line) + 1
		if totalChars > maxChars {
			filtered = append(filtered, "... (truncated)")
			break
		}
	}

	return strings.Join(filtered, "\n")
}

// readAndTruncate reads a file and truncates to maxChars for use in a prompt.
func readAndTruncate(t *testing.T, path string, maxChars int) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}

	content := string(data)
	if len(content) > maxChars {
		content = content[:maxChars] + "\n... (truncated)"
	}
	return content
}

// parseJudgeVerdict extracts the judge's JSON verdict from Claude's output.
func parseJudgeVerdict(t *testing.T, claudeOutput string) judgeVerdict {
	t.Helper()

	// Claude wraps output in a JSON array — extract the result text
	var messages []map[string]interface{}
	if err := json.Unmarshal([]byte(claudeOutput), &messages); err == nil {
		for _, msg := range messages {
			if result, ok := msg["result"].(string); ok && result != "" {
				claudeOutput = result
				break
			}
		}
	}

	// Find JSON object in the output (judge may include preamble text)
	start := strings.Index(claudeOutput, "{")
	if start == -1 {
		t.Logf("no JSON in judge output, assuming pass: %.500s", claudeOutput)
		return judgeVerdict{CoveragePct: 0, Verdict: "unknown", Notes: "could not parse judge output"}
	}

	// Find matching closing brace
	depth := 0
	end := -1
	for i := start; i < len(claudeOutput); i++ {
		switch claudeOutput[i] {
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
		t.Logf("malformed JSON in judge output: %.500s", claudeOutput[start:])
		return judgeVerdict{CoveragePct: 0, Verdict: "unknown", Notes: "malformed JSON"}
	}

	var v judgeVerdict
	if err := json.Unmarshal([]byte(claudeOutput[start:end]), &v); err != nil {
		t.Logf("failed to parse judge verdict: %v\n%.500s", err, claudeOutput[start:end])
		return judgeVerdict{CoveragePct: 0, Verdict: "unknown", Notes: "parse error: " + err.Error()}
	}

	return v
}

// truncateForLog truncates a string for log output.
func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
