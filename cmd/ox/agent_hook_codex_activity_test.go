package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// Codex hook output: whispers reach the Codex model through PostToolUse's JSON
// envelope, and ox prints no coworker-visible notice (no systemMessage) —
// Codex has no status line API, and per-call transcript lines were noise.
// These tests drive the real hook handlers against a fake daemon socket and
// parse stdout the way Codex does.

// codexHookWire mirrors Codex's PostToolUse / UserPromptSubmit command-output
// wire structs (codex-rs/hooks/src/schema.rs, rust-v0.154.0). Codex rejects
// unknown fields, so decoding with DisallowUnknownFields catches a field
// Codex would refuse.
type codexHookWire struct {
	Continue           *bool   `json:"continue"`
	StopReason         *string `json:"stopReason"`
	SuppressOutput     *bool   `json:"suppressOutput"`
	SystemMessage      string  `json:"systemMessage"`
	Decision           *string `json:"decision"`
	Reason             *string `json:"reason"`
	HookSpecificOutput *struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// decodeCodexHookDoc parses stdout as Codex does: exactly one JSON object.
func decodeCodexHookDoc(t *testing.T, out string) codexHookWire {
	t.Helper()
	require.True(t, strings.HasPrefix(strings.TrimSpace(out), "{"), "Codex only parses stdout that is a JSON object; got %q", out)
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	var doc codexHookWire
	require.NoError(t, dec.Decode(&doc), "stdout must be a Codex hook document: %q", out)
	var extra json.RawMessage
	require.ErrorIs(t, dec.Decode(&extra), io.EOF, "stdout must hold ONE document; Codex drops output with trailing data: %q", out)
	return doc
}

// codexActivityEnv isolates config and the daemon socket, and serves whispers
// from a fake daemon on this workspace's socket. Returns the project root.
func codexActivityEnv(t *testing.T, whispers []whisperstore.WhisperEntry) string {
	t.Helper()
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", "/tmp") // short path: unix socket limit ~104 chars
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	// As in the real binary: main.go's init forces NO_COLOR=1 inside a harness.
	t.Setenv("NO_COLOR", "1")

	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".sageox"), 0o755))
	require.NoError(t, exec.Command("git", "-C", repo, "init", "-q").Run())
	t.Chdir(repo)
	startFakeWhisperDaemon(t, whispers)
	return repo
}

// startFakeWhisperDaemon answers every whispers request with entries and
// everything else with success.
func startFakeWhisperDaemon(t *testing.T, entries []whisperstore.WhisperEntry) {
	t.Helper()
	sock := daemon.SocketPath()
	require.NoError(t, os.MkdirAll(filepath.Dir(sock), 0o700))
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	whispersData, err := json.Marshal(daemon.WhispersResponse{Entries: entries})
	require.NoError(t, err)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				line, _ := bufio.NewReader(conn).ReadBytes('\n')
				var msg daemon.Message
				resp := daemon.Response{Success: true}
				if json.Unmarshal(line, &msg) == nil && msg.Type == daemon.MsgTypeWhispers {
					resp.Data = whispersData
				}
				data, _ := json.Marshal(resp)
				_, _ = conn.Write(append(data, '\n'))
			}()
		}
	}()
}

func setStatusline(t *testing.T, on bool) {
	t.Helper()
	value := "off"
	if on {
		value = "on"
	}
	require.NoError(t, SetConfigValue("statusline", value, ConfigLevelUser, ""))
}

func shellInput(t *testing.T, field string, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{field: value})
	require.NoError(t, err)
	return raw
}

func teammateMurmur() whisperstore.WhisperEntry {
	return whisperstore.WhisperEntry{
		ID: "w-ryan", Scope: "ledger", Type: "murmur", Source: "murmur", Topic: "wip",
		Content: "Refactoring auth middleware", Importance: "normal",
		CreatedAt: time.Now(), AgentID: "OxRyan1", PrincipalID: "ryan-snodgrass",
	}
}

func runCodexAfterTool(t *testing.T, root, agentType, tool string, input json.RawMessage) string {
	t.Helper()
	ctx := &HookContext{
		Phase: phaseAfterTool, AgentType: agentType, ProjectRoot: root,
		Input:  &agentx.HookInput{ToolName: tool, ToolInput: input},
		Marker: &SessionMarker{AgentID: testAgentID},
	}
	var hookErr error
	out := string(captureRealStdout(t, func() { hookErr = handleAfterTool(ctx) }))
	require.NoError(t, hookErr)
	return out
}

// --- A. PostToolUse ---

// TestCodexAfterTool_NoNoticeForOxCalls keeps the Codex transcript quiet.
// Failure prevented: a "↳ Hook · ● ox  checked murmurs" line after every ox
// call — attention spent on ox plumbing instead of the coworker's work.
func TestCodexAfterTool_NoNoticeForOxCalls(t *testing.T) {
	tests := []struct {
		name       string
		statusline bool
		tool       string
		input      func(t *testing.T) json.RawMessage
	}{
		{"murmur list via Bash", true, "Bash", func(t *testing.T) json.RawMessage { return shellInput(t, "command", "ox murmur list") }},
		{"plan view via exec_command", true, "exec_command", func(t *testing.T) json.RawMessage { return shellInput(t, "cmd", "ox plan view x") }},
		{"SageOx MCP GetPlan", true, "mcp__sageox__GetPlan", func(t *testing.T) json.RawMessage { return json.RawMessage(`{"slug":"x"}`) }},
		{"plain shell command", true, "Bash", func(t *testing.T) json.RawMessage { return shellInput(t, "command", "ls -la") }},
		{"setting off", false, "Bash", func(t *testing.T) json.RawMessage { return shellInput(t, "command", "ox murmur list") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := codexActivityEnv(t, nil)
			setStatusline(t, tt.statusline)
			assert.Empty(t, runCodexAfterTool(t, root, "codex", tt.tool, tt.input(t)))
		})
	}
}

// TestCodexAfterTool_WhispersRideTheEnvelope delivers pending whispers as
// additionalContext in one JSON document with no systemMessage key.
// Failure prevented: a whisper fetched (and so marked delivered) on
// PostToolUse but printed as plain text, which Codex ignores; or a stray
// "systemMessage": "" that a stricter Codex schema rejects.
func TestCodexAfterTool_WhispersRideTheEnvelope(t *testing.T) {
	tests := []struct {
		name       string
		statusline bool
		command    string
	}{
		{"ox call", true, "ox murmur list"},
		{"non-ox call", true, "ls"},
		{"status line off keeps whisper delivery", false, "ox murmur list"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := codexActivityEnv(t, []whisperstore.WhisperEntry{teammateMurmur()})
			setStatusline(t, tt.statusline)

			out := runCodexAfterTool(t, root, "codex", "Bash", shellInput(t, "command", tt.command))
			doc := decodeCodexHookDoc(t, out)
			assert.NotContains(t, out, "systemMessage", "ox sends no coworker-visible notice")
			assert.Nil(t, doc.Continue, "whisper delivery must never steer Codex's control flow")
			assert.Nil(t, doc.Decision)
			require.NotNil(t, doc.HookSpecificOutput, "the whisper must reach the model")
			assert.Equal(t, "PostToolUse", doc.HookSpecificOutput.HookEventName, "Codex requires hookEventName")
			assert.Contains(t, doc.HookSpecificOutput.AdditionalContext, "<system-reminder>")
			assert.Contains(t, doc.HookSpecificOutput.AdditionalContext, "Refactoring auth middleware")
		})
	}
}

// TestClaudeAfterTool_Unchanged keeps the Codex path out of Claude Code.
// Failure prevented: a Codex JSON envelope leaking into Claude Code hooks.
func TestClaudeAfterTool_Unchanged(t *testing.T) {
	root := codexActivityEnv(t, nil)
	assert.Empty(t, runCodexAfterTool(t, root, "claude-code", "Bash", shellInput(t, "command", "ox murmur list")))

	root = codexActivityEnv(t, []whisperstore.WhisperEntry{teammateMurmur()})
	out := runCodexAfterTool(t, root, "claude-code", "Bash", shellInput(t, "command", "ox murmur list"))
	assert.True(t, strings.HasPrefix(out, "<system-reminder>"), "Claude Code keeps plain-text whispers; got %q", out)
	assert.NotContains(t, out, "hookSpecificOutput")
}

// --- B. UserPromptSubmit ---

func runPromptHook(t *testing.T, root, agentType string) string {
	t.Helper()
	ctx := &HookContext{
		Phase: phasePrompt, AgentType: agentType, ProjectRoot: root,
		Input:  &agentx.HookInput{RawBytes: []byte(`{"hook_event_name":"UserPromptSubmit","prompt":"hello"}`)},
		Marker: &SessionMarker{AgentID: testAgentID},
	}
	var hookErr error
	out := string(captureRealStdout(t, func() { hookErr = handlePrompt(ctx) }))
	require.NoError(t, hookErr)
	return out
}

// TestCodexPrompt_PlainStdoutLikeClaude gives Codex exactly the plain prompt
// output Claude Code gets. Failure prevented: a teammate's whisper wrapped in
// a JSON notice document, changing what the Codex model reads (Codex gives it
// the trimmed plain stdout).
func TestCodexPrompt_PlainStdoutLikeClaude(t *testing.T) {
	withFakeRunner(t, &fakeRunner{})
	root := codexActivityEnv(t, []whisperstore.WhisperEntry{teammateMurmur()})

	plain := runPromptHook(t, root, "claude-code")
	require.Contains(t, plain, "<system-reminder>", "fixture must deliver the whisper")
	require.Contains(t, plain, "Refactoring auth middleware")
	out := runPromptHook(t, root, "codex")
	assert.Equal(t, plain, out)
	assert.False(t, strings.HasPrefix(strings.TrimSpace(out), "{"), "no JSON envelope on UserPromptSubmit: %q", out)
}
