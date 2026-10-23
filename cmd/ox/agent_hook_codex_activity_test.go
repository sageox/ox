package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// Codex activity notices: ox tells coworkers, in the Codex transcript, what it
// did for their AI coworker ("↳ Hook · ● ox  checked murmurs"), and who just
// whispered. These tests drive the real hook handlers against a fake daemon
// socket and parse stdout the way Codex does.

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
	t.Setenv("OX_COLOR_PROFILE", "")
	setCoworkerNoColor(t, "")

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

// setCoworkerNoColor sets NO_COLOR as the coworker's environment had it,
// before ox's own startup override.
func setCoworkerNoColor(t *testing.T, v string) {
	t.Helper()
	prev := coworkerNoColor
	coworkerNoColor = v
	t.Cleanup(func() { coworkerNoColor = prev })
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

// --- A. PostToolUse: ox tool calls are announced ---

// TestCodexAfterTool_AnnouncesOxToolCalls drives handleAfterTool for Codex.
// Failure prevented: coworkers watching Codex never see what ox did — or ox
// prints something Codex refuses to parse, which drops the notice silently.
func TestCodexAfterTool_AnnouncesOxToolCalls(t *testing.T) {
	tests := []struct {
		name  string
		tool  string
		input func(t *testing.T) json.RawMessage
		want  string
	}{
		{"murmur list via Bash", "Bash", func(t *testing.T) json.RawMessage { return shellInput(t, "command", "ox murmur list") }, "● ox  checked murmurs"},
		{"plan view via exec_command", "exec_command", func(t *testing.T) json.RawMessage { return shellInput(t, "cmd", "ox plan view x") }, "● ox  pulled plan"},
		{"plan view via local_shell argv", "local_shell", func(t *testing.T) json.RawMessage {
			return shellInput(t, "command", []string{"bash", "-lc", "ox plan view x"})
		}, "● ox  pulled plan"},
		{"SageOx MCP GetPlan", "mcp__sageox__GetPlan", func(t *testing.T) json.RawMessage { return json.RawMessage(`{"slug":"x"}`) }, "● ox  pulled plan"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := codexActivityEnv(t, nil)
			doc := decodeCodexHookDoc(t, runCodexAfterTool(t, root, "codex", tt.tool, tt.input(t)))
			assert.Equal(t, tt.want, doc.SystemMessage)
			assert.Nil(t, doc.HookSpecificOutput, "no whispers pending, so no model context")
			assert.Nil(t, doc.Continue, "a notice must never steer Codex's control flow")
			assert.Nil(t, doc.Decision)
		})
	}
}

// TestCodexAfterTool_SilentWhenNotOxWork keeps the transcript free of noise.
// Failure prevented: every `ls`, every ox heartbeat, or a coworker who turned
// the setting off still gets a "↳ Hook" line after each tool call.
func TestCodexAfterTool_SilentWhenNotOxWork(t *testing.T) {
	tests := []struct {
		name       string
		statusline bool
		tool       string
		command    string
	}{
		{"plain shell command", true, "Bash", "ls -la"},
		{"ox plumbing heartbeat", true, "Bash", "ox agent Oxa1b2 heartbeat"},
		{"setting off", false, "Bash", "ox murmur list"},
		{"non-SageOx MCP tool", true, "mcp__github__get_issue", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := codexActivityEnv(t, nil)
			setStatusline(t, tt.statusline)
			assert.Empty(t, runCodexAfterTool(t, root, "codex", tt.tool, shellInput(t, "command", tt.command)))
		})
	}
}

// TestCodexAfterTool_WhispersAndNoticeShareOneDocument covers the merge.
// Failure prevented: whisper text printed ahead of the notice JSON makes Codex
// drop BOTH, and a whisper fetched (and so marked delivered) on PostToolUse but
// printed as plain text never reaches the Codex model.
func TestCodexAfterTool_WhispersAndNoticeShareOneDocument(t *testing.T) {
	tests := []struct {
		name       string
		statusline bool
		command    string
		wantNotice string
	}{
		{"ox call with whisper", true, "ox murmur list", "● ox  checked murmurs"},
		{"non-ox call with whisper", true, "ls", ""},
		{"setting off keeps whisper delivery", false, "ox murmur list", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := codexActivityEnv(t, []whisperstore.WhisperEntry{teammateMurmur()})
			setStatusline(t, tt.statusline)

			doc := decodeCodexHookDoc(t, runCodexAfterTool(t, root, "codex", "Bash", shellInput(t, "command", tt.command)))
			assert.Equal(t, tt.wantNotice, doc.SystemMessage)
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
	assert.NotContains(t, out, "systemMessage")
}

// --- B. Color ---

func writeCodexRollout(t *testing.T, meta string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(meta+"\n"+`{"type":"response_item","payload":{}}`+"\n"), 0o600))
	raw, err := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "transcript_path": path})
	require.NoError(t, err)
	return raw
}

// TestCodexNotice_ColorOnlyWhereCodexRendersIt checks the color gate.
// Failure prevented: escape bytes shown as literal "[38;5;35m" garbage in a
// Codex TUI older than 0.162.0, the desktop app, or `codex exec`; color
// emitted despite the coworker's NO_COLOR; or color never emitted because ox
// forces NO_COLOR=1 on itself at startup (every case here runs with it set).
func TestCodexNotice_ColorOnlyWhereCodexRendersIt(t *testing.T) {
	const tui0162 = `{"type":"session_meta","payload":{"originator":"codex-tui","cli_version":"0.162.0","source":"cli"}}`
	tests := []struct {
		name      string
		meta      string // first rollout line; "" means no transcript_path
		noColor   string
		profile   string
		wantColor bool
	}{
		{"TUI at the first ANSI release", tui0162, "", "", true},
		{"newer TUI", `{"type":"session_meta","payload":{"originator":"codex-tui","cli_version":"0.164.0","source":"cli"}}`, "", "", true},
		{"older TUI prints escapes literally", `{"type":"session_meta","payload":{"originator":"codex-tui","cli_version":"0.154.0","source":"cli"}}`, "", "", false},
		{"prerelease of the ANSI release", `{"type":"session_meta","payload":{"originator":"codex-tui","cli_version":"0.162.0-alpha.17.2","source":"cli"}}`, "", "", false},
		{"desktop app", `{"type":"session_meta","payload":{"originator":"codex_work_desktop","cli_version":"0.164.0","source":"vscode"}}`, "", "", false},
		{"codex exec", `{"type":"session_meta","payload":{"originator":"codex_exec","cli_version":"0.164.0","source":"exec"}}`, "", "", false},
		{"first line is not session_meta", `{"type":"response_item","payload":{"cli_version":"0.164.0","source":"cli"}}`, "", "", false},
		{"no transcript", "", "", "", false},
		{"NO_COLOR is sacred", tui0162, "1", "", false},
		{"ascii profile", tui0162, "", "ascii", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
			t.Setenv("NO_COLOR", "1") // forced by main.go's init in every harness
			setCoworkerNoColor(t, tt.noColor)
			t.Setenv("OX_COLOR_PROFILE", tt.profile)
			var raw []byte
			if tt.meta != "" {
				raw = writeCodexRollout(t, tt.meta)
			}
			ctx := &HookContext{AgentType: "codex", Input: &agentx.HookInput{
				ToolName: "Bash", ToolInput: shellInput(t, "command", "ox murmur list"), RawBytes: raw,
			}}

			notice := codexToolNotice(ctx)
			assert.Equal(t, tt.wantColor, strings.Contains(notice, "\x1b"), "notice %q", notice)
			assert.Equal(t, "● ox  checked murmurs", ansi.Strip(notice), "color may only add escape sequences")
		})
	}
}

func TestCodexVersionAtLeast(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"0.162.0", true}, {"0.162.1", true}, {"0.170.0", true}, {"1.0.0", true}, {"v0.162.0", true},
		{"0.161.9", false}, {"0.154.0", false}, {"0.162.0-alpha.1", false},
		{"", false}, {"0.162", false}, {"abc", false}, {"0.x.0", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, codexVersionAtLeast(tt.version, codexANSIMinVersion), tt.version)
	}
}

// --- C. UserPromptSubmit: teammate whispers are announced ---

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

// TestCodexPrompt_TeammateWhisperNotice drives handlePrompt for Codex against
// the same whispers Claude Code receives.
// Failure prevented: wrapping the prompt output in JSON changes what the
// Codex model reads (Codex gives it the TRIMMED plain stdout, or
// additionalContext verbatim), or a notice fires for ox's own murmurs or
// non-murmur nudges.
func TestCodexPrompt_TeammateWhisperNotice(t *testing.T) {
	ownMurmur := teammateMurmur()
	ownMurmur.AgentID = testAgentID
	nudge := teammateMurmur()
	nudge.Source = "plan"

	tests := []struct {
		name       string
		statusline bool
		whispers   []whisperstore.WhisperEntry
		wantNotice string // "" means stdout must be byte-identical to today's plain output
	}{
		{"teammate murmur", true, []whisperstore.WhisperEntry{teammateMurmur()}, "● ox  whisper from ryan-snodgrass [wip] Refactoring auth middleware"},
		{"own murmur", true, []whisperstore.WhisperEntry{ownMurmur}, ""},
		{"non-murmur whisper", true, []whisperstore.WhisperEntry{nudge}, ""},
		{"setting off", false, []whisperstore.WhisperEntry{teammateMurmur()}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withFakeRunner(t, &fakeRunner{})
			root := codexActivityEnv(t, tt.whispers)
			setStatusline(t, tt.statusline)

			plain := runPromptHook(t, root, "claude-code")
			require.Contains(t, plain, "<system-reminder>", "fixture must deliver the whisper")
			out := runPromptHook(t, root, "codex")

			if tt.wantNotice == "" {
				assert.Equal(t, plain, out, "without a notice Codex must get exactly today's plain stdout")
				return
			}
			doc := decodeCodexHookDoc(t, out)
			assert.Equal(t, tt.wantNotice, doc.SystemMessage)
			require.NotNil(t, doc.HookSpecificOutput)
			assert.Equal(t, "UserPromptSubmit", doc.HookSpecificOutput.HookEventName)
			assert.Equal(t, strings.TrimSpace(plain), doc.HookSpecificOutput.AdditionalContext,
				"the model must read exactly what Codex's plain-stdout path would have given it")
		})
	}
}

// TestCodexPromptStdout_PassthroughForOtherAgents pins the non-Codex path.
// Failure prevented: Claude Code output buffered (lost on an early exit) or
// re-encoded.
func TestCodexPromptStdout_PassthroughForOtherAgents(t *testing.T) {
	for _, agentType := range []string{"claude-code", "gemini", ""} {
		t.Run(fmt.Sprintf("agent=%q", agentType), func(t *testing.T) {
			var out bytes.Buffer
			s := newCodexPromptStdout(&HookContext{AgentType: agentType}, &out)
			s.whispers = []whisperstore.WhisperEntry{teammateMurmur()}
			_, err := s.Write([]byte("<system-reminder>x</system-reminder>\n"))
			require.NoError(t, err)
			assert.Equal(t, "<system-reminder>x</system-reminder>\n", out.String(), "written through immediately")
			s.flush()
			assert.Equal(t, "<system-reminder>x</system-reminder>\n", out.String(), "flush adds nothing")
		})
	}
}

// TestCodexWhisperNotice_LongMurmurStaysOneLine caps the notice width.
// Failure prevented: a paragraph-long murmur flooding the Codex transcript.
func TestCodexWhisperNotice_LongMurmurStaysOneLine(t *testing.T) {
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "config.yaml"))
	long := teammateMurmur()
	long.Content = strings.Repeat("refactoring the auth middleware and its callers ", 10)

	notice := codexWhisperNotice(&HookContext{AgentType: "codex"}, testAgentID, []whisperstore.WhisperEntry{long})
	assert.True(t, strings.HasPrefix(notice, "● ox  whisper from ryan-snodgrass [wip] refactoring"), notice)
	assert.True(t, strings.HasSuffix(notice, "…"), "shortened with an ellipsis: %q", notice)
	assert.LessOrEqual(t, ansi.StringWidth(notice), codexNoticeWidth)
}
