package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/sageox/agentx/setup"
)

// --- Startup banner tests ---
// These verify that emitStartupBanner produces the correct systemMessage JSON
// for Claude Code sessions and stays silent for other agents.

// TestEmitStartupBanner_ClaudeCodeAutoRecording verifies banner includes
// the agent name, team context mention, and recording notice when auto-recording is on.
// Failure prevented: users don't see recording disclosure at session start.
func TestEmitStartupBanner_ClaudeCodeAutoRecording(t *testing.T) {
	projectRoot := initBannerTestProject(t, "auto")
	t.Setenv("OX_SESSION_RECORDING", "auto")

	ctx := &HookContext{
		AgentType:   "claude-code",
		ProjectRoot: projectRoot,
	}

	output := captureBannerStdout(t, func() {
		emitStartupBanner(ctx)
	})

	msg := parseBannerSystemMessage(t, output)

	if !strings.Contains(msg, "Claude Code") {
		t.Errorf("expected agent display name 'Claude Code' in message, got: %s", msg)
	}
	if !strings.Contains(msg, "enhanced by team context from SageOx") {
		t.Errorf("expected 'enhanced by team context from SageOx' in message, got: %s", msg)
	}
	if !strings.Contains(msg, "recorded and shared") {
		t.Errorf("expected recording notice in message, got: %s", msg)
	}
}

// TestEmitStartupBanner_ClaudeCodeNoRecording verifies banner shows agent name
// and team context without recording notice when recording is disabled.
// Failure prevented: false recording disclosure when recording is off.
func TestEmitStartupBanner_ClaudeCodeNoRecording(t *testing.T) {
	projectRoot := initBannerTestProject(t, "disabled")
	t.Setenv("OX_SESSION_RECORDING", "disabled")

	ctx := &HookContext{
		AgentType:   "claude-code",
		ProjectRoot: projectRoot,
	}

	output := captureBannerStdout(t, func() {
		emitStartupBanner(ctx)
	})

	msg := parseBannerSystemMessage(t, output)

	if !strings.Contains(msg, "Claude Code") {
		t.Errorf("expected agent display name 'Claude Code' in message, got: %s", msg)
	}
	if !strings.Contains(msg, "enhanced by team context from SageOx") {
		t.Errorf("expected 'enhanced by team context from SageOx' in message, got: %s", msg)
	}
	if strings.Contains(msg, "recorded") {
		t.Errorf("should not mention recording when disabled, got: %s", msg)
	}
}

// TestEmitStartupBanner_NonClaudeAgent verifies no output for non-Claude agents.
// Failure prevented: raw JSON leaking into terminals of agents that don't
// understand the systemMessage protocol.
func TestEmitStartupBanner_NonClaudeAgent(t *testing.T) {
	projectRoot := initBannerTestProject(t, "auto")

	ctx := &HookContext{
		AgentType:   "gemini",
		ProjectRoot: projectRoot,
	}

	output := captureBannerStdout(t, func() {
		emitStartupBanner(ctx)
	})

	if strings.TrimSpace(output) != "" {
		t.Errorf("expected no output for non-Claude agent, got: %s", output)
	}
}

// --- helpers ---

func initBannerTestProject(t *testing.T, recordingMode string) string {
	t.Helper()
	dir := t.TempDir()
	sageoxDir := filepath.Join(dir, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatal(err)
	}
	cfgContent := `{"session_recording":"` + recordingMode + `"}`
	if err := os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(cfgContent), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func captureBannerStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	return string(buf[:n])
}

func parseBannerSystemMessage(t *testing.T, output string) string {
	t.Helper()
	var resp map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &resp); err != nil {
		t.Fatalf("expected valid JSON, got: %q (err: %v)", output, err)
	}
	msg, ok := resp["systemMessage"]
	if !ok {
		t.Fatal("missing systemMessage key in response")
	}
	return msg
}
