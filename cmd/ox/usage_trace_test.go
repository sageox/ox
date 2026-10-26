package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/agentx"
	"github.com/sageox/ox/internal/oxactivity"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contexttrace"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// Usage trace: the PostToolUse hook records every ox call an AI coworker makes
// as an `invoked` context-trace event, so `ox brief` can say what ox put in
// front of the model. These tests drive the REAL handleAfterTool.

const usageAgentID = "OxAb12"

// usageTraceEnv isolates config, the cache, and the daemon socket (a fake
// daemon serves whispers). Returns the project root.
func usageTraceEnv(t *testing.T, whispers []whisperstore.WhisperEntry) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", home)
	root := codexActivityEnv(t, whispers)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox", "config.json"),
		[]byte(`{"config_version":"2","repo_id":"repo_usage_trace"}`), 0o644))
	return root
}

// startUsageRecording starts a live recording for usageAgentID.
func startUsageRecording(t *testing.T, root string) *session.RecordingState {
	t.Helper()
	state, err := session.StartRecording(root, session.StartRecordingOptions{AgentID: usageAgentID, AdapterName: "claude-code"})
	require.NoError(t, err)
	return state
}

func runAfterTool(t *testing.T, root, agentType, agentID string, input *agentx.HookInput) string {
	t.Helper()
	ctx := &HookContext{
		Phase: phaseAfterTool, AgentType: agentType, ProjectRoot: root,
		Input: input, Marker: &SessionMarker{AgentID: agentID},
	}
	var hookErr error
	out := string(captureRealStdout(t, func() { hookErr = handleAfterTool(ctx) }))
	require.NoError(t, hookErr, "a usage-trace problem must never fail the hook")
	return out
}

func invokedEvents(t *testing.T, path string) []contexttrace.Event {
	t.Helper()
	events, err := contexttrace.ReadEvents(path)
	require.NoError(t, err)
	var out []contexttrace.Event
	for _, ev := range events {
		if ev.Type == contexttrace.EventInvoked {
			out = append(out, ev)
		}
	}
	return out
}

func sessionTracePath(state *session.RecordingState) string {
	return filepath.Join(state.SessionPath, contexttrace.FileName)
}

func bashStdout(t *testing.T, stdout string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"stdout": stdout, "stderr": "warning: some noise on stderr"})
	require.NoError(t, err)
	return raw
}

func jsonString(t *testing.T, s string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(s)
	require.NoError(t, err)
	return raw
}

// --- A. ox calls land in the session trace, with their size ---

// TestAfterTool_RecordsOxCallIntoSessionTrace is the customer claim: an ox
// call an AI coworker makes, in either harness and on either surface, shows up
// in the session trace with what it returned. Failure prevented: `ox brief`
// reports zero ox calls (or zero tokens) for a session that ran them.
func TestAfterTool_RecordsOxCallIntoSessionTrace(t *testing.T) {
	murmurs := strings.Repeat("m", 1203) // the JSON `ox murmur list` printed
	tests := []struct {
		name      string
		agentType string
		input     func(t *testing.T) *agentx.HookInput
		label     string
		command   string
		refType   string
		bytes     int
	}{
		{"claude Bash murmur list", "claude-code", func(t *testing.T) *agentx.HookInput {
			return &agentx.HookInput{ToolName: "Bash", ToolInput: shellInput(t, "command", "ox agent OxAb12 murmur list --json"), ToolResponse: bashStdout(t, murmurs)}
		}, "checked murmurs", "murmur list", "murmur", len(murmurs)},
		{"codex exec_command cmd", "codex", func(t *testing.T) *agentx.HookInput {
			return &agentx.HookInput{ToolName: "exec_command", ToolInput: shellInput(t, "cmd", "ox agent OxAb12 murmur list --json"), ToolResponse: jsonString(t, murmurs)}
		}, "checked murmurs", "murmur list", "murmur", len(murmurs)},
		{"codex local_shell argv", "codex", func(t *testing.T) *agentx.HookInput {
			return &agentx.HookInput{ToolName: "local_shell", ToolInput: shellInput(t, "command", []string{"bash", "-lc", "ox agent OxAb12 murmur list --json"}), ToolResponse: jsonString(t, murmurs)}
		}, "checked murmurs", "murmur list", "murmur", len(murmurs)},
		{"SageOx MCP ListMurmurs", "claude-code", func(t *testing.T) *agentx.HookInput {
			return &agentx.HookInput{ToolName: "mcp__claude_ai_sageox__ListMurmurs", ToolInput: json.RawMessage(`{}`), ToolResponse: json.RawMessage(`[{"text":"` + murmurs + `"}]`)}
		}, "checked murmurs", "mcp ListMurmurs", "murmur", len(`[{"text":"`) + len(murmurs) + len(`"}]`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := usageTraceEnv(t, nil)
			state := startUsageRecording(t, root)

			runAfterTool(t, root, tt.agentType, usageAgentID, tt.input(t))

			events := invokedEvents(t, sessionTracePath(state))
			require.Len(t, events, 1)
			ev := events[0]
			assert.Equal(t, contexttrace.SourceOxCLI, ev.Source)
			assert.Equal(t, tt.label, ev.Label)
			assert.Equal(t, tt.command, ev.Command)
			assert.Equal(t, tt.refType, ev.RefType)
			assert.Equal(t, tt.bytes, ev.OutputBytes)
			assert.Equal(t, (tt.bytes+3)/4, ev.InlineTokens, "~4 bytes per token")
			assert.False(t, ev.Failed)
			assert.Empty(t, ev.Query, "murmur list has no search subject")
		})
	}
}

// TestAfterTool_RecordsRetrievalQueryAndFailure covers the optional fields.
// Failure prevented: a retrieval's subject is lost (so the report cannot say
// what was searched), a failed call reads as a success, or a call with no
// response is reported as returning zero bytes of known size.
func TestAfterTool_RecordsRetrievalQueryAndFailure(t *testing.T) {
	root := usageTraceEnv(t, nil)
	state := startUsageRecording(t, root)

	runAfterTool(t, root, "claude-code", usageAgentID, &agentx.HookInput{
		ToolName: "Bash", ToolInput: shellInput(t, "command", `ox query --limit 5 "auth decisions"`), ToolResponse: bashStdout(t, "results"),
	})
	runAfterTool(t, root, "claude-code", usageAgentID, &agentx.HookInput{
		ToolName: "Bash", ToolInput: shellInput(t, "command", "ox plan view my-plan"), ToolError: "exit status 1",
	})

	events := invokedEvents(t, sessionTracePath(state))
	require.Len(t, events, 2)
	assert.Equal(t, "query", events[0].Command)
	assert.Equal(t, "auth decisions", events[0].Query)
	assert.Equal(t, "query", events[0].RefType)
	assert.Equal(t, len("results"), events[0].OutputBytes)

	assert.Equal(t, "plan view", events[1].Command)
	assert.True(t, events[1].Failed)
	assert.Zero(t, events[1].OutputBytes)

	raw, err := os.ReadFile(sessionTracePath(state))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	assert.NotContains(t, lines[len(lines)-1], "output_bytes", "unknown size is omitted, not recorded as 0")
	assert.NotContains(t, lines[len(lines)-1], "inline_tokens")
}

// TestAfterTool_NoInvokedEventForNonOxWork keeps the trace to ox calls.
// Failure prevented: every `ls` or heartbeat is billed as an ox call, or a
// prime run through Bash is counted twice (prime records its own event).
func TestAfterTool_NoInvokedEventForNonOxWork(t *testing.T) {
	tests := []struct {
		name    string
		command string
		// statusline reports whether the status line still learns of it
		statusline bool
	}{
		{"ls", "ls -la", false},
		{"heartbeat plumbing", "ox agent OxAb12 heartbeat", false},
		{"agent prime via Bash", "ox agent OxAb12 prime", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := usageTraceEnv(t, nil)
			state := startUsageRecording(t, root)

			runAfterTool(t, root, "claude-code", usageAgentID, &agentx.HookInput{
				ToolName: "Bash", ToolInput: shellInput(t, "command", tt.command), ToolResponse: bashStdout(t, "out"),
			})

			assert.Empty(t, invokedEvents(t, sessionTracePath(state)))
			assert.NoFileExists(t, filepath.Join(agentTraceCacheDir(root), usageAgentID+".jsonl"))
			if tt.statusline {
				assert.Equal(t, "loaded team context", oxactivity.LoadState(root, usageAgentID).LastAction)
			}
		})
	}
}

// --- B. Destination when not recording ---

// TestAfterTool_NotRecordingWritesAgentCache keeps the call when no recording
// is live. Failure prevented: an AI coworker that runs ox before (or without)
// recording leaves no usage trace at all, or writes into the git-tracked
// project tree.
func TestAfterTool_NotRecordingWritesAgentCache(t *testing.T) {
	root := usageTraceEnv(t, nil)

	runAfterTool(t, root, "claude-code", usageAgentID, &agentx.HookInput{
		ToolName: "Bash", ToolInput: shellInput(t, "command", "ox murmur list"), ToolResponse: bashStdout(t, "abcd"),
	})

	events := invokedEvents(t, filepath.Join(root, ".sageox", "cache", "context-trace", usageAgentID+".jsonl"))
	require.Len(t, events, 1)
	assert.Equal(t, "checked murmurs", events[0].Label)
	assert.Equal(t, 4, events[0].OutputBytes)
}

// TestAfterTool_InvalidAgentIDWritesNothing guards the cache file name.
// Failure prevented: a marker carrying a crafted agent ID writes outside the
// cache dir.
func TestAfterTool_InvalidAgentIDWritesNothing(t *testing.T) {
	root := usageTraceEnv(t, nil)
	runAfterTool(t, root, "claude-code", "../../evil", &agentx.HookInput{
		ToolName: "Bash", ToolInput: shellInput(t, "command", "ox murmur list"),
	})
	entries, _ := os.ReadDir(agentTraceCacheDir(root))
	for _, e := range entries {
		assert.NotContains(t, e.Name(), "evil")
	}
	assert.NoFileExists(t, filepath.Join(root, "evil.jsonl"))
	assert.NoFileExists(t, filepath.Join(filepath.Dir(root), "evil.jsonl"))
}

// TestPruneAgentTraceCache drops week-old per-agent files only. Failure
// prevented: the not-recording cache grows forever, or pruning deletes prime's
// shared trace file or a fresh agent's file.
func TestPruneAgentTraceCache(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-8 * 24 * time.Hour)
	write := func(name string, mtime time.Time) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte("{}\n"), 0o644))
		require.NoError(t, os.Chtimes(p, mtime, mtime))
		return p
	}
	stale := write("OxOld1.jsonl", old)
	fresh := write("OxNew1.jsonl", time.Now())
	primeShared := write(contexttrace.FileName, old)
	foreign := write("notes.txt", old)

	pruneAgentTraceCache(dir, time.Now())

	assert.NoFileExists(t, stale)
	assert.FileExists(t, fresh)
	assert.FileExists(t, primeShared, "prime's shared trace is not ours to prune")
	assert.FileExists(t, foreign)
}

// --- C. The hook's output never changes ---

// TestAfterTool_UnwritableTraceKeepsHookOutput breaks the trace file (a
// directory where the file should be — fails on every platform). Failure
// prevented: a full disk or a bad session dir turns every ox call into a hook
// error or changes what the harness reads from stdout.
func TestAfterTool_UnwritableTraceKeepsHookOutput(t *testing.T) {
	input := func() *agentx.HookInput {
		return &agentx.HookInput{ToolName: "Bash", ToolInput: shellInput(t, "command", "ox murmur list"), ToolResponse: bashStdout(t, "x")}
	}

	root := usageTraceEnv(t, []whisperstore.WhisperEntry{teammateMurmur()})
	state := startUsageRecording(t, root)
	require.NoError(t, os.MkdirAll(sessionTracePath(state), 0o755))
	broken := runAfterTool(t, root, "claude-code", usageAgentID, input())

	control := usageTraceEnv(t, []whisperstore.WhisperEntry{teammateMurmur()})
	startUsageRecording(t, control)
	require.NoError(t, SetConfigValue("recap.trace_consultations", "off", ConfigLevelUser, ""))
	want := runAfterTool(t, control, "claude-code", usageAgentID, input())

	assert.NotEmpty(t, want, "control: a pending whisper is printed")
	assert.Equal(t, want, broken)
}

// TestAfterTool_StdoutIdenticalWithTraceOnAndOff compares hook stdout with the
// usage write enabled and disabled. Failure prevented: recording usage leaks
// a byte into what Claude Code or Codex parses — for Codex that drops the
// whole hook document.
func TestAfterTool_StdoutIdenticalWithTraceOnAndOff(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		input     func(t *testing.T, root string) *agentx.HookInput
		check     func(t *testing.T, out string)
	}{
		{"claude plain whisper text", "claude-code", func(t *testing.T, _ string) *agentx.HookInput {
			return &agentx.HookInput{ToolName: "Bash", ToolInput: shellInput(t, "command", "ox murmur list"), ToolResponse: bashStdout(t, "x")}
		}, func(t *testing.T, out string) {
			assert.Contains(t, out, "Refactoring auth middleware")
		}},
		{"claude page nudge JSON", "claude-code", func(t *testing.T, root string) *agentx.HookInput {
			page := filepath.Join(root, "mockup.html")
			authoredPage(t, page)
			return &agentx.HookInput{ToolName: "Write", ToolInput: shellInput(t, "file_path", page)}
		}, func(t *testing.T, out string) {
			assert.True(t, json.Valid([]byte(out)), "page nudge stdout is one JSON document: %q", out)
		}},
		{"codex single JSON document", "codex", func(t *testing.T, _ string) *agentx.HookInput {
			return &agentx.HookInput{ToolName: "exec_command", ToolInput: shellInput(t, "cmd", "ox murmur list"), ToolResponse: jsonString(t, "x")}
		}, func(t *testing.T, out string) {
			doc := decodeCodexHookDoc(t, out)
			assert.Equal(t, "● ox  checked murmurs", doc.SystemMessage)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := func(traceOn bool) (string, string) {
				root := usageTraceEnv(t, []whisperstore.WhisperEntry{teammateMurmur()})
				state := startUsageRecording(t, root)
				if !traceOn {
					require.NoError(t, SetConfigValue("recap.trace_consultations", "off", ConfigLevelUser, ""))
				}
				in := tt.input(t, root)
				out := runAfterTool(t, root, tt.agentType, usageAgentID, in)
				events := invokedEvents(t, sessionTracePath(state))
				if traceOn && in.ToolName != "Write" {
					require.Len(t, events, 1, "control: the on run did record the call")
				} else {
					assert.Empty(t, events)
				}
				return strings.ReplaceAll(out, root, "<root>"), out
			}
			on, rawOn := run(true)
			off, _ := run(false)
			tt.check(t, rawOn)
			assert.Equal(t, off, on)
		})
	}
}

// --- D. Privacy and the opt-out ---

// TestAfterTool_MurmurBodyNeverReachesTrace posts a murmur with a distinctive
// body. Failure prevented: the trace — which syncs to the whole team's Ledger
// — carries what a coworker wrote, not just that they posted (ADR-027 rule 4).
func TestAfterTool_MurmurBodyNeverReachesTrace(t *testing.T) {
	const secret = "SECRET-BODY-xyz"
	root := usageTraceEnv(t, nil)
	state := startUsageRecording(t, root)

	for _, cmd := range []string{
		`ox murmur --topic=wip "` + secret + `"`,
		`ox agent OxAb12 murmur --topic wip ` + secret,
	} {
		runAfterTool(t, root, "claude-code", usageAgentID, &agentx.HookInput{
			ToolName: "Bash", ToolInput: shellInput(t, "command", cmd), ToolResponse: bashStdout(t, "posted: "+secret),
		})
	}
	runAfterTool(t, root, "claude-code", usageAgentID, &agentx.HookInput{
		ToolName: "mcp__claude_ai_sageox__PublishMurmur", ToolInput: json.RawMessage(`{"content":"` + secret + `"}`), ToolResponse: json.RawMessage(`{"echo":"` + secret + `"}`),
	})

	events := invokedEvents(t, sessionTracePath(state))
	require.Len(t, events, 3, "control: the posts were recorded")
	for _, ev := range events {
		assert.Equal(t, "posted murmur", ev.Label)
	}
	raw, err := os.ReadFile(sessionTracePath(state))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), secret)
}

// TestAfterTool_OptOutWritesNoInvokedEvent turns recap.trace_consultations
// off. Failure prevented: a coworker who opted out still has every ox call
// their AI coworker made synced to the team.
func TestAfterTool_OptOutWritesNoInvokedEvent(t *testing.T) {
	root := usageTraceEnv(t, nil)
	state := startUsageRecording(t, root)
	require.NoError(t, SetConfigValue("recap.trace_consultations", "off", ConfigLevelUser, ""))

	runAfterTool(t, root, "claude-code", usageAgentID, &agentx.HookInput{
		ToolName: "Bash", ToolInput: shellInput(t, "command", "ox murmur list"), ToolResponse: bashStdout(t, "x"),
	})

	assert.Empty(t, invokedEvents(t, sessionTracePath(state)))
	assert.NoFileExists(t, filepath.Join(agentTraceCacheDir(root), usageAgentID+".jsonl"))
	assert.Equal(t, "checked murmurs", oxactivity.LoadState(root, usageAgentID).LastAction, "the status line is a separate setting")
}
