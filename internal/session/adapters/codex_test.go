package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeCodexSession writes a JSONL session file and returns its path.
func writeCodexSession(t *testing.T, dir string, name string, entries []map[string]any) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range entries {
		require.NoError(t, enc.Encode(e))
	}
	return path
}

// codexSessionMeta returns a session_meta entry for testing.
func codexSessionMeta(cwd, cliVersion string) map[string]any {
	return map[string]any{
		"timestamp": "2026-02-27T10:00:00.000Z",
		"type":      "session_meta",
		"payload": map[string]any{
			"id":          "test-session-id",
			"cwd":         cwd,
			"cli_version": cliVersion,
		},
	}
}

// codexTurnContext returns a turn_context entry with a model.
func codexTurnContext(model string) map[string]any {
	return map[string]any{
		"timestamp": "2026-02-27T10:00:01.000Z",
		"type":      "turn_context",
		"payload": map[string]any{
			"model": model,
		},
	}
}

// codexUserMsg returns a response_item/message/user entry.
func codexUserMsg(text string) map[string]any {
	return map[string]any{
		"timestamp": "2026-02-27T10:00:02.000Z",
		"type":      "response_item",
		"payload": map[string]any{
			"type": "message",
			"role": "user",
			"content": []map[string]any{
				{"type": "input_text", "text": text},
			},
		},
	}
}

// codexAssistantMsg returns a response_item/message/assistant entry.
func codexAssistantMsg(text string) map[string]any {
	return map[string]any{
		"timestamp": "2026-02-27T10:00:03.000Z",
		"type":      "response_item",
		"payload": map[string]any{
			"type": "message",
			"role": "assistant",
			"content": []map[string]any{
				{"type": "output_text", "text": text},
			},
		},
	}
}

// codexFunctionCall returns a response_item/function_call entry.
func codexFunctionCall(name, args string) map[string]any {
	return codexFunctionCallWithID(name, args, "")
}

// codexFunctionCallWithID returns a response_item/function_call entry with a call_id.
func codexFunctionCallWithID(name, args, callID string) map[string]any {
	payload := map[string]any{
		"type":      "function_call",
		"name":      name,
		"arguments": args,
	}
	if callID != "" {
		payload["call_id"] = callID
	}
	return map[string]any{
		"timestamp": "2026-02-27T10:00:04.000Z",
		"type":      "response_item",
		"payload":   payload,
	}
}

// codexFunctionCallOutput returns a response_item/function_call_output entry.
func codexFunctionCallOutput(callID, output string) map[string]any {
	return map[string]any{
		"timestamp": "2026-02-27T10:00:05.000Z",
		"type":      "response_item",
		"payload": map[string]any{
			"type":    "function_call_output",
			"call_id": callID,
			"output":  output,
		},
	}
}

func TestCodexAdapter_Name(t *testing.T) {
	assert.Equal(t, "codex", (&CodexAdapter{}).Name())
}

func TestCodexAdapter_ParseLine_UserMessage(t *testing.T) {
	adapter := &CodexAdapter{}

	t.Run("genuine user message", func(t *testing.T) {
		line, _ := json.Marshal(codexUserMsg("can you run ox agent prime"))
		entries, err := adapter.parseLine(line)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, "user", entries[0].Role)
		assert.Equal(t, "can you run ox agent prime", entries[0].Content)
	})

	t.Run("AGENTS.md injection classified as system", func(t *testing.T) {
		line, _ := json.Marshal(codexUserMsg("# AGENTS.md instructions for /some/path\n\nsome content"))
		entries, err := adapter.parseLine(line)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, "system", entries[0].Role)
	})

	t.Run("permissions instructions classified as system", func(t *testing.T) {
		line, _ := json.Marshal(codexUserMsg("<permissions instructions>\nFilesystem sandboxing..."))
		entries, err := adapter.parseLine(line)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, "system", entries[0].Role)
	})

	t.Run("environment_context classified as system", func(t *testing.T) {
		line, _ := json.Marshal(codexUserMsg("<environment_context>\n  <cwd>/some/path</cwd>\n</environment_context>"))
		entries, err := adapter.parseLine(line)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, "system", entries[0].Role)
	})
}

func TestCodexAdapter_ParseLine_AssistantMessage(t *testing.T) {
	adapter := &CodexAdapter{}

	t.Run("assistant message with output_text", func(t *testing.T) {
		line, _ := json.Marshal(codexAssistantMsg("Session started with agent `OxxVy7`."))
		entries, err := adapter.parseLine(line)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, "assistant", entries[0].Role)
		assert.Equal(t, "Session started with agent `OxxVy7`.", entries[0].Content)
	})
}

func TestCodexAdapter_ParseLine_DeveloperMessage(t *testing.T) {
	adapter := &CodexAdapter{}

	t.Run("developer role is skipped", func(t *testing.T) {
		line, _ := json.Marshal(map[string]any{
			"timestamp": "2026-02-27T10:00:00.000Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type": "message",
				"role": "developer",
				"content": []map[string]any{
					{"type": "input_text", "text": "<permissions instructions>\n..."},
				},
			},
		})
		entries, err := adapter.parseLine(line)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})
}

func TestCodexAdapter_ParseLine_FunctionCall(t *testing.T) {
	adapter := &CodexAdapter{}

	t.Run("function_call becomes tool entry", func(t *testing.T) {
		line, _ := json.Marshal(codexFunctionCall("exec_command", `{"cmd":"ox agent prime"}`))
		entries, err := adapter.parseLine(line)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, "tool", entries[0].Role)
		assert.Equal(t, "exec_command", entries[0].ToolName)
		assert.Equal(t, `{"cmd":"ox agent prime"}`, entries[0].ToolInput)
	})
}

func TestCodexAdapter_ParseLine_SkippedTypes(t *testing.T) {
	adapter := &CodexAdapter{}

	skipped := []string{"reasoning", "web_search_call"}
	for _, itemType := range skipped {
		t.Run("skips "+itemType, func(t *testing.T) {
			line, _ := json.Marshal(map[string]any{
				"timestamp": "2026-02-27T10:00:00.000Z",
				"type":      "response_item",
				"payload": map[string]any{
					"type":   itemType,
					"output": "some output",
				},
			})
			entries, err := adapter.parseLine(line)
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}

	t.Run("skips non-response_item entries", func(t *testing.T) {
		for _, entryType := range []string{"session_meta", "turn_context", "event_msg"} {
			line, _ := json.Marshal(map[string]any{
				"type":    entryType,
				"payload": map[string]any{"model": "gpt-5"},
			})
			entries, err := adapter.parseLine(line)
			require.NoError(t, err)
			assert.Empty(t, entries, "expected %s to be skipped", entryType)
		}
	})
}

func TestCodexAdapter_ParseLine_FunctionCallOutput(t *testing.T) {
	adapter := &CodexAdapter{}

	t.Run("error output captured with IsError", func(t *testing.T) {
		line, _ := json.Marshal(codexFunctionCallOutput("call_abc", "Process exited with code 1"))
		entries, err := adapter.parseLine(line)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, "tool", entries[0].Role)
		assert.Equal(t, "Process exited with code 1", entries[0].ToolOutput)
		assert.True(t, entries[0].IsError)
		assert.Equal(t, "call_abc", entries[0].CallID)
	})

	t.Run("success output skipped", func(t *testing.T) {
		line, _ := json.Marshal(codexFunctionCallOutput("call_abc", "Process exited with code 0"))
		entries, err := adapter.parseLine(line)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})

	t.Run("empty output skipped", func(t *testing.T) {
		line, _ := json.Marshal(codexFunctionCallOutput("call_abc", ""))
		entries, err := adapter.parseLine(line)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})

	t.Run("non-exit-code output skipped", func(t *testing.T) {
		// regular output without error pattern is not captured
		line, _ := json.Marshal(codexFunctionCallOutput("call_abc", "file contents here"))
		entries, err := adapter.parseLine(line)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})
}

func TestCodexAdapter_MergeToolEntries(t *testing.T) {
	ts := time.Date(2026, 2, 27, 10, 0, 0, 0, time.UTC)

	t.Run("merges function_call with matching function_call_output", func(t *testing.T) {
		entries := []RawEntry{
			{Timestamp: ts, Role: "tool", ToolName: "exec_command", ToolInput: `{"cmd":"ls"}`, CallID: "call_1"},
			{Timestamp: ts, Role: "tool", ToolOutput: "Process exited with code 1", IsError: true, CallID: "call_1"},
		}
		merged := mergeToolEntries(entries)
		require.Len(t, merged, 1)
		assert.Equal(t, "exec_command", merged[0].ToolName)
		assert.Equal(t, `{"cmd":"ls"}`, merged[0].ToolInput)
		assert.Equal(t, "Process exited with code 1", merged[0].ToolOutput)
		assert.True(t, merged[0].IsError)
	})

	t.Run("no merge when CallIDs differ", func(t *testing.T) {
		entries := []RawEntry{
			{Timestamp: ts, Role: "tool", ToolName: "exec_command", ToolInput: `{"cmd":"ls"}`, CallID: "call_1"},
			{Timestamp: ts, Role: "tool", ToolOutput: "Process exited with code 1", IsError: true, CallID: "call_2"},
		}
		merged := mergeToolEntries(entries)
		require.Len(t, merged, 2)
	})

	t.Run("no merge when CallID is empty", func(t *testing.T) {
		entries := []RawEntry{
			{Timestamp: ts, Role: "tool", ToolName: "exec_command", ToolInput: `{"cmd":"ls"}`},
			{Timestamp: ts, Role: "tool", ToolOutput: "Process exited with code 1", IsError: true},
		}
		merged := mergeToolEntries(entries)
		require.Len(t, merged, 2)
	})

	t.Run("non-tool entries pass through", func(t *testing.T) {
		entries := []RawEntry{
			{Timestamp: ts, Role: "user", Content: "hello"},
			{Timestamp: ts, Role: "tool", ToolName: "exec_command", ToolInput: `{"cmd":"ls"}`, CallID: "call_1"},
			{Timestamp: ts, Role: "tool", ToolOutput: "Process exited with code 127", IsError: true, CallID: "call_1"},
			{Timestamp: ts, Role: "assistant", Content: "done"},
		}
		merged := mergeToolEntries(entries)
		require.Len(t, merged, 3)
		assert.Equal(t, "user", merged[0].Role)
		assert.Equal(t, "tool", merged[1].Role)
		assert.Equal(t, "exec_command", merged[1].ToolName)
		assert.Equal(t, "Process exited with code 127", merged[1].ToolOutput)
		assert.Equal(t, "assistant", merged[2].Role)
	})

	t.Run("orphaned function_call_output passes through", func(t *testing.T) {
		// function_call_output without a preceding function_call (e.g., out-of-order arrival)
		entries := []RawEntry{
			{Timestamp: ts, Role: "tool", ToolOutput: "Process exited with code 1", IsError: true, CallID: "call_orphan"},
			{Timestamp: ts, Role: "tool", ToolName: "exec_command", ToolInput: `{"cmd":"ls"}`, CallID: "call_2"},
		}
		merged := mergeToolEntries(entries)
		require.Len(t, merged, 2)
		assert.Equal(t, "call_orphan", merged[0].CallID)
		assert.True(t, merged[0].IsError)
		assert.Equal(t, "exec_command", merged[1].ToolName)
	})

	t.Run("empty and single-entry slices unchanged", func(t *testing.T) {
		assert.Empty(t, mergeToolEntries(nil))
		assert.Len(t, mergeToolEntries([]RawEntry{{Role: "user"}}), 1)
	})
}

func TestCodexAdapter_ReadMetadata(t *testing.T) {
	adapter := &CodexAdapter{}

	t.Run("extracts cli_version and model", func(t *testing.T) {
		dir := t.TempDir()
		path := writeCodexSession(t, dir, "session.jsonl", []map[string]any{
			codexSessionMeta("/some/project", "0.106.0"),
			codexTurnContext("gpt-5.3-codex"),
		})

		meta, err := adapter.ReadMetadata(path)
		require.NoError(t, err)
		require.NotNil(t, meta)
		assert.Equal(t, "0.106.0", meta.AgentVersion)
		assert.Equal(t, "gpt-5.3-codex", meta.Model)
	})

	t.Run("returns nil when no metadata present", func(t *testing.T) {
		dir := t.TempDir()
		path := writeCodexSession(t, dir, "session.jsonl", []map[string]any{
			codexUserMsg("hello"),
		})

		meta, err := adapter.ReadMetadata(path)
		require.NoError(t, err)
		assert.Nil(t, meta)
	})
}

func TestCodexAdapter_Read(t *testing.T) {
	adapter := &CodexAdapter{}

	t.Run("reads conversation entries in order", func(t *testing.T) {
		dir := t.TempDir()
		path := writeCodexSession(t, dir, "session.jsonl", []map[string]any{
			codexSessionMeta("/project", "0.106.0"),
			codexTurnContext("gpt-5.3-codex"),
			codexUserMsg("can you run ox agent prime"),
			codexFunctionCall("exec_command", `{"cmd":"ox agent prime"}`),
			codexAssistantMsg("Session started."),
		})

		entries, err := adapter.Read(path)
		require.NoError(t, err)
		require.Len(t, entries, 3) // user, tool, assistant (session_meta and turn_context skipped)

		assert.Equal(t, "user", entries[0].Role)
		assert.Equal(t, "can you run ox agent prime", entries[0].Content)

		assert.Equal(t, "tool", entries[1].Role)
		assert.Equal(t, "exec_command", entries[1].ToolName)

		assert.Equal(t, "assistant", entries[2].Role)
		assert.Equal(t, "Session started.", entries[2].Content)
	})

	t.Run("merges error output into preceding function_call", func(t *testing.T) {
		dir := t.TempDir()
		path := writeCodexSession(t, dir, "session.jsonl", []map[string]any{
			codexSessionMeta("/project", "0.106.0"),
			codexUserMsg("run tests"),
			codexFunctionCallWithID("exec_command", `{"cmd":"make test"}`, "call_xyz"),
			codexFunctionCallOutput("call_xyz", "Process exited with code 2"),
			codexAssistantMsg("Tests failed."),
		})

		entries, err := adapter.Read(path)
		require.NoError(t, err)
		require.Len(t, entries, 3) // user, merged tool, assistant

		assert.Equal(t, "tool", entries[1].Role)
		assert.Equal(t, "exec_command", entries[1].ToolName)
		assert.Equal(t, `{"cmd":"make test"}`, entries[1].ToolInput)
		assert.Equal(t, "Process exited with code 2", entries[1].ToolOutput)
		assert.True(t, entries[1].IsError)
	})

	t.Run("success output not captured", func(t *testing.T) {
		dir := t.TempDir()
		path := writeCodexSession(t, dir, "session.jsonl", []map[string]any{
			codexSessionMeta("/project", "0.106.0"),
			codexFunctionCallWithID("exec_command", `{"cmd":"ls"}`, "call_ok"),
			codexFunctionCallOutput("call_ok", "Process exited with code 0"),
		})

		entries, err := adapter.Read(path)
		require.NoError(t, err)
		require.Len(t, entries, 1) // only function_call, output skipped
		assert.Equal(t, "exec_command", entries[0].ToolName)
		assert.Empty(t, entries[0].ToolOutput)
	})

	t.Run("skips system injections", func(t *testing.T) {
		dir := t.TempDir()
		path := writeCodexSession(t, dir, "session.jsonl", []map[string]any{
			codexUserMsg("# AGENTS.md instructions for /project\n\ncontent"),
			codexUserMsg("what is your ox id"),
		})

		entries, err := adapter.Read(path)
		require.NoError(t, err)
		require.Len(t, entries, 2) // system + user
		assert.Equal(t, "system", entries[0].Role)
		assert.Equal(t, "user", entries[1].Role)
	})
}

func TestCodexAdapter_FindSessionFile(t *testing.T) {
	adapter := &CodexAdapter{}

	t.Run("finds session by cwd and agentID", func(t *testing.T) {
		cwd := t.TempDir()
		home := t.TempDir()
		t.Setenv("HOME", home)
		oldWD, err := os.Getwd()
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = os.Chdir(oldWD)
		})
		require.NoError(t, os.Chdir(cwd))
		actualCWD, err := os.Getwd()
		require.NoError(t, err)

		now := time.Now()
		dateDir := filepath.Join(home, ".codex", "sessions",
			now.Format("2006"), now.Format("01"), now.Format("02"))
		require.NoError(t, os.MkdirAll(dateDir, 0755))

		// wrong project — should not be selected
		writeCodexSession(t, dateDir, "rollout-other.jsonl", []map[string]any{
			codexSessionMeta("/other/project", "0.106.0"),
			codexUserMsg("irrelevant"),
		})

		// right project, contains agentID
		target := writeCodexSession(t, dateDir, "rollout-target.jsonl", []map[string]any{
			codexSessionMeta(actualCWD, "0.106.0"),
			// simulate function_call_output containing agentID from ox agent prime
			{"timestamp": "2026-02-27T10:00:01.000Z", "type": "response_item",
				"payload": map[string]any{"type": "function_call_output", "output": `agent_id: "TESTID123"`}},
		})

		since := time.Now().Add(-5 * time.Minute)
		got, err := adapter.FindSessionFile("TESTID123", since)
		require.NoError(t, err)
		assert.Equal(t, target, got)
	})

	t.Run("falls back to most recent cwd match when agentID not found", func(t *testing.T) {
		cwd := t.TempDir()
		home := t.TempDir()
		t.Setenv("HOME", home)
		oldWD, err := os.Getwd()
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = os.Chdir(oldWD)
		})
		require.NoError(t, os.Chdir(cwd))
		actualCWD, err := os.Getwd()
		require.NoError(t, err)

		now := time.Now()
		dateDir := filepath.Join(home, ".codex", "sessions",
			now.Format("2006"), now.Format("01"), now.Format("02"))
		require.NoError(t, os.MkdirAll(dateDir, 0755))

		// two sessions for the same project, no agentID in either
		writeCodexSession(t, dateDir, "rollout-older.jsonl", []map[string]any{
			codexSessionMeta(actualCWD, "0.106.0"),
			codexUserMsg("older session"),
		})
		// ensure newer is actually newer
		time.Sleep(10 * time.Millisecond)
		newer := writeCodexSession(t, dateDir, "rollout-newer.jsonl", []map[string]any{
			codexSessionMeta(actualCWD, "0.106.0"),
			codexUserMsg("newer session"),
		})

		since := time.Now().Add(-5 * time.Minute)
		got, err := adapter.FindSessionFile("MISSING", since)
		require.NoError(t, err)
		assert.Equal(t, newer, got)
	})

	t.Run("returns ErrSessionNotFound when no cwd match exists", func(t *testing.T) {
		cwd := t.TempDir()
		home := t.TempDir()
		t.Setenv("HOME", home)
		oldWD, err := os.Getwd()
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = os.Chdir(oldWD)
		})
		require.NoError(t, os.Chdir(cwd))

		now := time.Now()
		dateDir := filepath.Join(home, ".codex", "sessions",
			now.Format("2006"), now.Format("01"), now.Format("02"))
		require.NoError(t, os.MkdirAll(dateDir, 0755))

		writeCodexSession(t, dateDir, "rollout-other.jsonl", []map[string]any{
			codexSessionMeta("/other/project", "0.106.0"),
			codexUserMsg("irrelevant"),
		})

		_, err = adapter.FindSessionFile("", time.Now().Add(-5*time.Minute))
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrSessionNotFound)
	})

	t.Run("searches beyond today and yesterday", func(t *testing.T) {
		cwd := t.TempDir()
		home := t.TempDir()
		t.Setenv("HOME", home)
		oldWD, err := os.Getwd()
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = os.Chdir(oldWD)
		})
		require.NoError(t, os.Chdir(cwd))
		actualCWD, err := os.Getwd()
		require.NoError(t, err)

		threeDaysAgo := time.Now().AddDate(0, 0, -3)
		dateDir := filepath.Join(home, ".codex", "sessions",
			threeDaysAgo.Format("2006"), threeDaysAgo.Format("01"), threeDaysAgo.Format("02"))
		require.NoError(t, os.MkdirAll(dateDir, 0755))

		target := writeCodexSession(t, dateDir, "rollout-old-date.jsonl", []map[string]any{
			codexSessionMeta(actualCWD, "0.106.0"),
			codexUserMsg("session in older date directory"),
		})

		got, err := adapter.FindSessionFile("", time.Now().Add(-5*time.Minute))
		require.NoError(t, err)
		assert.Equal(t, target, got)
	})

	t.Run("falls back when active session has no recent writes", func(t *testing.T) {
		cwd := t.TempDir()
		home := t.TempDir()
		t.Setenv("HOME", home)
		oldWD, err := os.Getwd()
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = os.Chdir(oldWD)
		})
		require.NoError(t, os.Chdir(cwd))
		actualCWD, err := os.Getwd()
		require.NoError(t, err)

		now := time.Now()
		dateDir := filepath.Join(home, ".codex", "sessions",
			now.Format("2006"), now.Format("01"), now.Format("02"))
		require.NoError(t, os.MkdirAll(dateDir, 0755))

		target := writeCodexSession(t, dateDir, "rollout-quiet.jsonl", []map[string]any{
			codexSessionMeta(actualCWD, "0.106.0"),
			codexUserMsg("quiet but active session"),
		})

		oldMod := time.Now().Add(-30 * time.Minute)
		require.NoError(t, os.Chtimes(target, oldMod, oldMod))

		got, err := adapter.FindSessionFile("", time.Now().Add(-5*time.Minute))
		require.NoError(t, err)
		assert.Equal(t, target, got)
	})
}

// --- isCodexToolError edge cases ---
// These tests verify the INTENT: non-zero exit codes are errors, zero is success,
// and unknown patterns default to non-error.
func TestIsCodexToolError_EdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		isError bool
	}{
		// negative exit codes are errors
		{name: "negative exit code", output: "Process exited with code -1", isError: true},
		// zero-prefixed non-zero codes are errors (not "code 0")
		{name: "zero-prefixed non-zero code", output: "Process exited with code 01", isError: true},
		// trailing space after code number: still has the prefix, still != "code 0"
		{name: "trailing space after code", output: "Process exited with code 1 ", isError: true},
		// no number after "code ": has prefix but != "code 0", treated as error.
		// NOTE: this is arguably a bug — no exit code number shouldn't be an error —
		// but the current implementation returns true because it matches the prefix
		// and doesn't equal "Process exited with code 0".
		{name: "no number after code prefix", output: "Process exited with code ", isError: true},
		// leading whitespace prevents prefix match, so returns false.
		// NOTE: arguably a bug if the output has leading whitespace, but the current
		// implementation uses HasPrefix which requires exact start-of-string match.
		{name: "leading and trailing whitespace", output: "  Process exited with code 1  ", isError: false},
		// newline between "code" and number breaks the prefix match
		{name: "newline in output", output: "Process exited with code\n1", isError: false},
		// empty string handled by early return
		{name: "empty string", output: "", isError: false},
		// whitespace-only is not a known error pattern
		{name: "whitespace only", output: "   ", isError: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isCodexToolError(tc.output)
			assert.Equal(t, tc.isError, got, "isCodexToolError(%q)", tc.output)
		})
	}
}

// --- mergeToolEntries intent verification ---
// These tests verify the merge contract: consecutive call+output pairs with matching
// CallIDs produce a single entry with all four fields populated; non-adjacent or
// wrong-order entries are never merged.
func TestMergeToolEntries_IntentVerification(t *testing.T) {
	ts := time.Date(2026, 2, 27, 10, 0, 0, 0, time.UTC)

	t.Run("merged entry has all four tool fields", func(t *testing.T) {
		// after merge, the resulting entry must have ToolName, ToolInput, ToolOutput, and IsError
		entries := []RawEntry{
			{Timestamp: ts, Role: "tool", ToolName: "exec_command", ToolInput: `{"cmd":"make test"}`, CallID: "call_1"},
			{Timestamp: ts, Role: "tool", ToolOutput: "Process exited with code 2", IsError: true, CallID: "call_1"},
		}
		merged := mergeToolEntries(entries)
		require.Len(t, merged, 1)
		assert.Equal(t, "exec_command", merged[0].ToolName, "ToolName must survive merge")
		assert.Equal(t, `{"cmd":"make test"}`, merged[0].ToolInput, "ToolInput must survive merge")
		assert.Equal(t, "Process exited with code 2", merged[0].ToolOutput, "ToolOutput must be merged in")
		assert.True(t, merged[0].IsError, "IsError must be merged in")
	})

	t.Run("multiple consecutive pairs produce multiple merged entries", func(t *testing.T) {
		entries := []RawEntry{
			{Timestamp: ts, Role: "tool", ToolName: "exec_command", ToolInput: `{"cmd":"ls"}`, CallID: "call_1"},
			{Timestamp: ts, Role: "tool", ToolOutput: "Process exited with code 1", IsError: true, CallID: "call_1"},
			{Timestamp: ts, Role: "tool", ToolName: "read_file", ToolInput: `{"path":"x.go"}`, CallID: "call_2"},
			{Timestamp: ts, Role: "tool", ToolOutput: "Process exited with code 127", IsError: true, CallID: "call_2"},
		}
		merged := mergeToolEntries(entries)
		require.Len(t, merged, 2)
		assert.Equal(t, "exec_command", merged[0].ToolName)
		assert.Equal(t, "Process exited with code 1", merged[0].ToolOutput)
		assert.Equal(t, "read_file", merged[1].ToolName)
		assert.Equal(t, "Process exited with code 127", merged[1].ToolOutput)
	})

	t.Run("non-adjacent entries with same CallID do not merge", func(t *testing.T) {
		// an intervening assistant message prevents merge even with matching CallIDs
		entries := []RawEntry{
			{Timestamp: ts, Role: "tool", ToolName: "exec_command", ToolInput: `{"cmd":"ls"}`, CallID: "call_1"},
			{Timestamp: ts, Role: "assistant", Content: "intervening message"},
			{Timestamp: ts, Role: "tool", ToolOutput: "Process exited with code 1", IsError: true, CallID: "call_1"},
		}
		merged := mergeToolEntries(entries)
		require.Len(t, merged, 3, "intervening entry must prevent merge")
		assert.Equal(t, "exec_command", merged[0].ToolName)
		assert.Empty(t, merged[0].ToolOutput, "call entry must not have output when unmerged")
		assert.Equal(t, "assistant", merged[1].Role)
		assert.Equal(t, "Process exited with code 1", merged[2].ToolOutput)
	})

	t.Run("output before call does not merge (wrong order)", func(t *testing.T) {
		entries := []RawEntry{
			{Timestamp: ts, Role: "tool", ToolOutput: "Process exited with code 1", IsError: true, CallID: "call_1"},
			{Timestamp: ts, Role: "tool", ToolName: "exec_command", ToolInput: `{"cmd":"ls"}`, CallID: "call_1"},
		}
		merged := mergeToolEntries(entries)
		require.Len(t, merged, 2, "output-before-call must not merge")
		// first entry is the orphaned output (no ToolName)
		assert.Empty(t, merged[0].ToolName)
		assert.Equal(t, "Process exited with code 1", merged[0].ToolOutput)
		// second entry is the call (no ToolOutput)
		assert.Equal(t, "exec_command", merged[1].ToolName)
		assert.Empty(t, merged[1].ToolOutput)
	})

	t.Run("multiple intervening entries prevent merge", func(t *testing.T) {
		entries := []RawEntry{
			{Timestamp: ts, Role: "tool", ToolName: "exec_command", ToolInput: `{"cmd":"ls"}`, CallID: "call_1"},
			{Timestamp: ts, Role: "user", Content: "msg1"},
			{Timestamp: ts, Role: "assistant", Content: "msg2"},
			{Timestamp: ts, Role: "user", Content: "msg3"},
			{Timestamp: ts, Role: "tool", ToolOutput: "Process exited with code 1", IsError: true, CallID: "call_1"},
		}
		merged := mergeToolEntries(entries)
		require.Len(t, merged, 5, "multiple intervening entries must prevent merge")
	})
}

func TestCodexAdapter_Registered(t *testing.T) {
	// use an isolated registry so ResetRegistry() calls in other tests don't interfere
	ResetRegistry()
	defer ResetRegistry()
	Register(&CodexAdapter{})

	got, err := GetAdapter("codex")
	require.NoError(t, err)
	assert.Equal(t, "codex", got.Name())
}

func TestCodexAdapter_Alias(t *testing.T) {
	ResetRegistry()
	defer ResetRegistry()
	Register(&CodexAdapter{})

	// "Codex" (capital C) should resolve via case-insensitive alias lookup
	got, err := GetAdapter("Codex")
	require.NoError(t, err)
	assert.Equal(t, "codex", got.Name())
}
