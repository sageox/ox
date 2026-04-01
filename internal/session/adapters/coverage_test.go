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

// --- Codex readFromOffset (0% coverage) ---

func TestCodexAdapter_ReadFromOffset(t *testing.T) {
	adapter := &CodexAdapter{}

	t.Run("reads entries from offset zero", func(t *testing.T) {
		dir := t.TempDir()
		path := writeCodexSession(t, dir, "session.jsonl", []map[string]any{
			codexSessionMeta("/project", "0.106.0"),
			codexUserMsg("hello"),
			codexAssistantMsg("hi there"),
		})

		entries, newOffset, err := adapter.ReadFromOffset(path, 0)
		require.NoError(t, err)
		assert.Greater(t, newOffset, int64(0))
		// session_meta is skipped, user + assistant
		require.Len(t, entries, 2)
		assert.Equal(t, "user", entries[0].Role)
		assert.Equal(t, "assistant", entries[1].Role)
	})

	t.Run("incremental read only gets new entries", func(t *testing.T) {
		dir := t.TempDir()
		path := writeCodexSession(t, dir, "session.jsonl", []map[string]any{
			codexUserMsg("first"),
		})

		entries1, offset1, err := adapter.ReadFromOffset(path, 0)
		require.NoError(t, err)
		require.Len(t, entries1, 1)
		assert.Equal(t, "first", entries1[0].Content)

		// append more entries
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
		require.NoError(t, err)
		enc := json.NewEncoder(f)
		require.NoError(t, enc.Encode(codexAssistantMsg("second")))
		require.NoError(t, enc.Encode(codexFunctionCall("shell", `{"cmd":"ls"}`)))
		f.Close()

		entries2, offset2, err := adapter.ReadFromOffset(path, offset1)
		require.NoError(t, err)
		assert.Greater(t, offset2, offset1)
		require.Len(t, entries2, 2)
		assert.Equal(t, "assistant", entries2[0].Role)
		assert.Equal(t, "tool", entries2[1].Role)
	})

	t.Run("no new entries returns same offset", func(t *testing.T) {
		dir := t.TempDir()
		path := writeCodexSession(t, dir, "session.jsonl", []map[string]any{
			codexUserMsg("only message"),
		})

		_, offset1, err := adapter.ReadFromOffset(path, 0)
		require.NoError(t, err)

		entries2, offset2, err := adapter.ReadFromOffset(path, offset1)
		require.NoError(t, err)
		assert.Empty(t, entries2)
		assert.Equal(t, offset1, offset2)
	})

	t.Run("file not found returns error", func(t *testing.T) {
		_, _, err := adapter.ReadFromOffset("/nonexistent/file.jsonl", 0)
		require.Error(t, err)
	})

	t.Run("skips malformed lines", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "bad.jsonl")
		content := "{not valid json\n"
		line, _ := json.Marshal(codexUserMsg("valid"))
		content += string(line) + "\n"
		require.NoError(t, os.WriteFile(path, []byte(content), 0644))

		entries, _, err := adapter.ReadFromOffset(path, 0)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, "valid", entries[0].Content)
	})
}

// --- Codex Detect (0% coverage) ---

func TestCodexAdapter_Detect(t *testing.T) {
	adapter := &CodexAdapter{}

	t.Run("returns true when sessions directory exists", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		sessionsDir := filepath.Join(home, ".codex", "sessions")
		require.NoError(t, os.MkdirAll(sessionsDir, 0755))

		assert.True(t, adapter.Detect())
	})

	t.Run("returns false when sessions directory missing", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		// .codex exists but no sessions subdir
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".codex"), 0755))

		assert.False(t, adapter.Detect())
	})

	t.Run("returns false when codex directory missing", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		assert.False(t, adapter.Detect())
	})
}

// --- parseCodexTimestamp (40% coverage) ---

func TestParseCodexTimestamp(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantY   int
		wantM   time.Month
		wantD   int
		wantZ   bool // expect zero time
	}{
		{
			name:  "RFC3339Nano",
			input: "2026-03-15T14:30:00.123456789Z",
			wantY: 2026, wantM: time.March, wantD: 15,
		},
		{
			name:  "RFC3339 without nanos",
			input: "2026-03-15T14:30:00Z",
			wantY: 2026, wantM: time.March, wantD: 15,
		},
		{
			name:  "empty string",
			input: "",
			wantZ: true,
		},
		{
			name:  "invalid format",
			input: "not-a-timestamp",
			wantZ: true,
		},
		{
			name:  "date only (no time)",
			input: "2026-03-15",
			wantZ: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseCodexTimestamp(tt.input)
			if tt.wantZ {
				assert.True(t, got.IsZero(), "expected zero time for input %q", tt.input)
			} else {
				assert.Equal(t, tt.wantY, got.Year())
				assert.Equal(t, tt.wantM, got.Month())
				assert.Equal(t, tt.wantD, got.Day())
			}
		})
	}
}

// --- extractRawUserText (25% coverage) ---

func TestExtractRawUserText(t *testing.T) {
	adapter := &ClaudeCodeAdapter{}

	t.Run("nil message returns empty", func(t *testing.T) {
		got := adapter.extractRawUserText(&claudeCodeEntry{Message: nil})
		assert.Equal(t, "", got)
	})

	t.Run("string content returns as-is", func(t *testing.T) {
		entry := &claudeCodeEntry{
			Message: &claudeCodeMessage{Content: "hello world"},
		}
		got := adapter.extractRawUserText(entry)
		assert.Equal(t, "hello world", got)
	})

	t.Run("array content extracts text blocks", func(t *testing.T) {
		entry := &claudeCodeEntry{
			Message: &claudeCodeMessage{
				Content: []interface{}{
					map[string]interface{}{"type": "text", "text": "part one"},
					map[string]interface{}{"type": "tool_result", "content": "ignored"},
					map[string]interface{}{"type": "text", "text": "part two"},
				},
			},
		}
		got := adapter.extractRawUserText(entry)
		assert.Equal(t, "part one\npart two", got)
	})

	t.Run("array with no text blocks returns empty", func(t *testing.T) {
		entry := &claudeCodeEntry{
			Message: &claudeCodeMessage{
				Content: []interface{}{
					map[string]interface{}{"type": "tool_result", "content": "only tool result"},
				},
			},
		}
		got := adapter.extractRawUserText(entry)
		assert.Equal(t, "", got)
	})

	t.Run("non-string non-array content returns empty", func(t *testing.T) {
		entry := &claudeCodeEntry{
			Message: &claudeCodeMessage{Content: 42},
		}
		got := adapter.extractRawUserText(entry)
		assert.Equal(t, "", got)
	})
}

// --- classifyUserContent edge cases (85.2% coverage) ---

func TestClassifyUserContent_EdgeCases(t *testing.T) {
	adapter := &ClaudeCodeAdapter{}

	t.Run("nil message returns skip", func(t *testing.T) {
		content, class := adapter.classifyUserContent(&claudeCodeEntry{Message: nil})
		assert.Equal(t, "", content)
		assert.Equal(t, userContentSkip, class)
	})

	t.Run("isMeta with array content extracts text and classifies as system", func(t *testing.T) {
		entry := &claudeCodeEntry{
			IsMeta: true,
			Message: &claudeCodeMessage{
				Content: []interface{}{
					map[string]interface{}{"type": "text", "text": "meta context info"},
				},
			},
		}
		content, class := adapter.classifyUserContent(entry)
		assert.Equal(t, userContentSystem, class)
		assert.Contains(t, content, "meta context info")
	})

	t.Run("isMeta with system tags strips tags", func(t *testing.T) {
		entry := &claudeCodeEntry{
			IsMeta: true,
			Message: &claudeCodeMessage{
				Content: "<system-reminder>some info</system-reminder> real meta content",
			},
		}
		content, class := adapter.classifyUserContent(entry)
		assert.Equal(t, userContentSystem, class)
		assert.Contains(t, content, "real meta content")
		assert.NotContains(t, content, "system-reminder")
	})

	t.Run("isMeta with only system tags uses original content", func(t *testing.T) {
		entry := &claudeCodeEntry{
			IsMeta: true,
			Message: &claudeCodeMessage{
				Content: "<system-reminder>only tags</system-reminder>",
			},
		}
		content, class := adapter.classifyUserContent(entry)
		assert.Equal(t, userContentSystem, class)
		// when stripping results in empty string, falls back to original
		assert.NotEmpty(t, content)
	})

	t.Run("non-string non-array content skipped", func(t *testing.T) {
		entry := &claudeCodeEntry{
			Message: &claudeCodeMessage{Content: 12345},
		}
		content, class := adapter.classifyUserContent(entry)
		assert.Equal(t, "", content)
		assert.Equal(t, userContentSkip, class)
	})

	t.Run("array with non-map items skipped gracefully", func(t *testing.T) {
		entry := &claudeCodeEntry{
			Message: &claudeCodeMessage{
				Content: []interface{}{
					"just a string, not a map",
					42,
				},
			},
		}
		content, class := adapter.classifyUserContent(entry)
		assert.Equal(t, "", content)
		assert.Equal(t, userContentSkip, class)
	})
}

// --- classifyTextContent edge cases ---

func TestClassifyTextContent(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantClass userContentClass
		wantEmpty bool
	}{
		{
			name:      "empty string",
			input:     "",
			wantClass: userContentSkip,
			wantEmpty: true,
		},
		{
			name:      "Plan mode is active",
			input:     "Plan mode is active. Focus on design.",
			wantClass: userContentSystem,
		},
		{
			name:      "system-instruction hyphen variant",
			input:     "<system-instruction>content</system-instruction>",
			wantClass: userContentSystem,
		},
		{
			name:      "multiple system tags stripped to reveal user text",
			input:     "<system-reminder>x</system-reminder><local-command-stdout>y</local-command-stdout>actual question here",
			wantClass: userContentUser,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, class := classifyTextContent(tt.input)
			assert.Equal(t, tt.wantClass, class)
			if tt.wantEmpty {
				assert.Empty(t, content)
			}
		})
	}
}

// --- extractAssistantContent edge cases (87.5% coverage) ---

func TestExtractAssistantContent_EdgeCases(t *testing.T) {
	adapter := &ClaudeCodeAdapter{}

	t.Run("nil message returns nil", func(t *testing.T) {
		entries := adapter.extractAssistantContent(&claudeCodeEntry{Message: nil})
		assert.Nil(t, entries)
	})

	t.Run("non-array content returns nil", func(t *testing.T) {
		entry := &claudeCodeEntry{
			Message: &claudeCodeMessage{Content: "just a string"},
		}
		entries := adapter.extractAssistantContent(entry)
		assert.Nil(t, entries)
	})

	t.Run("empty text block is skipped", func(t *testing.T) {
		entry := &claudeCodeEntry{
			Message: &claudeCodeMessage{
				Content: []interface{}{
					map[string]interface{}{"type": "text", "text": ""},
					map[string]interface{}{"type": "text", "text": "actual content"},
				},
			},
		}
		entries := adapter.extractAssistantContent(entry)
		require.Len(t, entries, 1)
		assert.Equal(t, "actual content", entries[0].Content)
	})

	t.Run("unknown block type is skipped", func(t *testing.T) {
		entry := &claudeCodeEntry{
			Timestamp: "2026-01-05T10:00:00Z",
			Message: &claudeCodeMessage{
				Content: []interface{}{
					map[string]interface{}{"type": "unknown_type", "data": "ignored"},
					map[string]interface{}{"type": "text", "text": "kept"},
				},
			},
		}
		entries := adapter.extractAssistantContent(entry)
		require.Len(t, entries, 1)
		assert.Equal(t, "kept", entries[0].Content)
	})

	t.Run("invalid timestamp does not crash", func(t *testing.T) {
		entry := &claudeCodeEntry{
			Timestamp: "not-a-timestamp",
			Message: &claudeCodeMessage{
				Content: []interface{}{
					map[string]interface{}{"type": "text", "text": "content"},
				},
			},
		}
		entries := adapter.extractAssistantContent(entry)
		require.Len(t, entries, 1)
		assert.True(t, entries[0].Timestamp.IsZero())
	})

	t.Run("non-map items in content array are skipped", func(t *testing.T) {
		entry := &claudeCodeEntry{
			Message: &claudeCodeMessage{
				Content: []interface{}{
					"not a map",
					42,
					map[string]interface{}{"type": "text", "text": "valid"},
				},
			},
		}
		entries := adapter.extractAssistantContent(entry)
		require.Len(t, entries, 1)
		assert.Equal(t, "valid", entries[0].Content)
	})

	t.Run("only tool_use blocks no text", func(t *testing.T) {
		adapter := &ClaudeCodeAdapter{}
		entry := &claudeCodeEntry{
			Type: "assistant",
			Message: &claudeCodeMessage{
				Content: []interface{}{
					map[string]interface{}{
						"type": "tool_use",
						"name": "Read",
						"id":   "tool_001",
					},
					map[string]interface{}{
						"type": "tool_use",
						"name": "Write",
						"id":   "tool_002",
					},
				},
			},
		}
		entries := adapter.extractAssistantContent(entry)
		require.Len(t, entries, 2, "both tool_use blocks should produce entries")
		assert.Equal(t, "Read", entries[0].ToolName)
		assert.Equal(t, "Write", entries[1].ToolName)
	})
}

// --- Codex parseLine edge cases ---

func TestCodexAdapter_ParseLine_EdgeCases(t *testing.T) {
	adapter := &CodexAdapter{}

	t.Run("nil payload is skipped", func(t *testing.T) {
		line, _ := json.Marshal(map[string]any{
			"type": "response_item",
			// no payload
		})
		entries, err := adapter.parseLine(line)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})

	t.Run("function_call with empty name is skipped", func(t *testing.T) {
		line, _ := json.Marshal(map[string]any{
			"timestamp": "2026-02-27T10:00:04.000Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type":      "function_call",
				"name":      "",
				"arguments": `{"x":1}`,
			},
		})
		entries, err := adapter.parseLine(line)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})

	t.Run("assistant message with no output_text blocks returns nil", func(t *testing.T) {
		line, _ := json.Marshal(map[string]any{
			"timestamp": "2026-02-27T10:00:03.000Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type": "message",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "thinking", "text": "internal thought"},
				},
			},
		})
		entries, err := adapter.parseLine(line)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})

	t.Run("user message with empty content returns nil", func(t *testing.T) {
		line, _ := json.Marshal(map[string]any{
			"timestamp": "2026-02-27T10:00:02.000Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type":    "message",
				"role":    "user",
				"content": []map[string]any{},
			},
		})
		entries, err := adapter.parseLine(line)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})

	t.Run("invalid JSON returns error", func(t *testing.T) {
		_, err := adapter.parseLine([]byte("{broken"))
		require.Error(t, err)
	})
}

// --- Codex ReadMetadata error paths ---

func TestCodexAdapter_ReadMetadata_ErrorPaths(t *testing.T) {
	adapter := &CodexAdapter{}

	t.Run("file not found", func(t *testing.T) {
		_, err := adapter.ReadMetadata("/nonexistent/session.jsonl")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to open session file")
	})

	t.Run("malformed JSON skipped gracefully", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "bad.jsonl")
		require.NoError(t, os.WriteFile(path, []byte("{bad json\n"), 0644))

		meta, err := adapter.ReadMetadata(path)
		require.NoError(t, err)
		assert.Nil(t, meta, "malformed-only file should return nil metadata")
	})

	t.Run("entries with nil payload skipped", func(t *testing.T) {
		dir := t.TempDir()
		path := writeCodexSession(t, dir, "session.jsonl", []map[string]any{
			// response_item with no payload
			{"type": "response_item"},
		})

		meta, err := adapter.ReadMetadata(path)
		require.NoError(t, err)
		assert.Nil(t, meta)
	})

	t.Run("version only (no model)", func(t *testing.T) {
		dir := t.TempDir()
		path := writeCodexSession(t, dir, "session.jsonl", []map[string]any{
			codexSessionMeta("/project", "1.2.3"),
		})

		meta, err := adapter.ReadMetadata(path)
		require.NoError(t, err)
		require.NotNil(t, meta)
		assert.Equal(t, "1.2.3", meta.AgentVersion)
		assert.Empty(t, meta.Model)
	})

	t.Run("model only (no version)", func(t *testing.T) {
		dir := t.TempDir()
		path := writeCodexSession(t, dir, "session.jsonl", []map[string]any{
			codexTurnContext("gpt-5"),
		})

		meta, err := adapter.ReadMetadata(path)
		require.NoError(t, err)
		require.NotNil(t, meta)
		assert.Empty(t, meta.AgentVersion)
		assert.Equal(t, "gpt-5", meta.Model)
	})
}

// --- Codex Read error paths ---

func TestCodexAdapter_Read_ErrorPaths(t *testing.T) {
	adapter := &CodexAdapter{}

	t.Run("file not found", func(t *testing.T) {
		_, err := adapter.Read("/nonexistent/session.jsonl")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to open session file")
	})

	t.Run("empty file returns empty slice", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "empty.jsonl")
		require.NoError(t, os.WriteFile(path, []byte(""), 0644))

		entries, err := adapter.Read(path)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})
}

// --- Claude Code Read/ReadMetadata error paths ---

func TestClaudeCodeAdapter_Read_FileNotFound(t *testing.T) {
	adapter := &ClaudeCodeAdapter{}
	_, err := adapter.Read("/nonexistent/session.jsonl")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open session file")
}

func TestClaudeCodeAdapter_ReadMetadata_FileNotFound(t *testing.T) {
	adapter := &ClaudeCodeAdapter{}
	_, err := adapter.ReadMetadata("/nonexistent/session.jsonl")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open session file")
}

func TestClaudeCodeAdapter_ReadMetadata_MalformedJSON(t *testing.T) {
	adapter := &ClaudeCodeAdapter{}
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("{malformed\n"), 0644))

	meta, err := adapter.ReadMetadata(path)
	require.NoError(t, err)
	assert.Nil(t, meta, "malformed-only file should return nil metadata")
}

// --- Claude Code parseLine edge cases ---

func TestClaudeCodeAdapter_ParseLine_EdgeCases(t *testing.T) {
	adapter := &ClaudeCodeAdapter{}

	t.Run("invalid JSON returns error", func(t *testing.T) {
		_, err := adapter.parseLine([]byte("{broken"))
		require.Error(t, err)
	})

	t.Run("unknown type returns nil", func(t *testing.T) {
		entries, err := adapter.parseLine([]byte(`{"type":"custom_event","data":"x"}`))
		require.NoError(t, err)
		assert.Nil(t, entries)
	})

	t.Run("user with no timestamp still parses", func(t *testing.T) {
		jsonl := `{"type":"user","message":{"role":"user","content":"no ts"}}`
		entries, err := adapter.parseLine([]byte(jsonl))
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.True(t, entries[0].Timestamp.IsZero())
		assert.Equal(t, "no ts", entries[0].Content)
	})

	t.Run("user with invalid timestamp still parses", func(t *testing.T) {
		jsonl := `{"type":"user","timestamp":"invalid","message":{"role":"user","content":"bad ts"}}`
		entries, err := adapter.parseLine([]byte(jsonl))
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.True(t, entries[0].Timestamp.IsZero())
	})

	t.Run("assistant with nil message returns nil", func(t *testing.T) {
		jsonl := `{"type":"assistant"}`
		entries, err := adapter.parseLine([]byte(jsonl))
		require.NoError(t, err)
		assert.Nil(t, entries)
	})

	t.Run("assistant with empty content array returns nil", func(t *testing.T) {
		jsonl := `{"type":"assistant","message":{"role":"assistant","content":[]}}`
		entries, err := adapter.parseLine([]byte(jsonl))
		require.NoError(t, err)
		assert.Nil(t, entries)
	})
}

// --- stripSystemTags coverage ---

func TestStripSystemTags(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "system-reminder",
			input: "<system-reminder>content</system-reminder>",
			want:  "",
		},
		{
			name:  "system_instruction (underscore)",
			input: "<system_instruction>content</system_instruction>",
			want:  "",
		},
		{
			name:  "system-instruction (hyphen)",
			input: "<system-instruction>content</system-instruction>",
			want:  "",
		},
		{
			name:  "local-command-stdout",
			input: "<local-command-stdout>output</local-command-stdout>",
			want:  "",
		},
		{
			name:  "local-command-caveat",
			input: "<local-command-caveat>warning</local-command-caveat>",
			want:  "",
		},
		{
			name:  "command-name",
			input: "<command-name>/test</command-name>",
			want:  "",
		},
		{
			name:  "command-message",
			input: "<command-message>msg</command-message>",
			want:  "",
		},
		{
			name:  "command-args",
			input: "<command-args>--flag</command-args>",
			want:  "",
		},
		{
			name:  "task-notification",
			input: "<task-notification><task-id>abc</task-id></task-notification>",
			want:  "",
		},
		{
			name:  "mixed tags with user text preserved",
			input: "<system-reminder>x</system-reminder>hello<local-command-stdout>y</local-command-stdout> world",
			want:  "hello world",
		},
		{
			name:  "no tags returns original",
			input: "plain text",
			want:  "plain text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripSystemTags(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- Codex classifyCodexUserContent edge cases ---

func TestClassifyCodexUserContent_EdgeCases(t *testing.T) {
	t.Run("empty blocks returns empty", func(t *testing.T) {
		text, isSystem := classifyCodexUserContent(nil)
		assert.Empty(t, text)
		assert.False(t, isSystem)
	})

	t.Run("blocks with non-input_text types ignored", func(t *testing.T) {
		blocks := []codexContentBlock{
			{Type: "output_text", Text: "ignored"},
			{Type: "input_text", Text: "kept"},
		}
		text, isSystem := classifyCodexUserContent(blocks)
		assert.Equal(t, "kept", text)
		assert.False(t, isSystem)
	})

	t.Run("empty text in input_text block", func(t *testing.T) {
		blocks := []codexContentBlock{
			{Type: "input_text", Text: ""},
		}
		text, isSystem := classifyCodexUserContent(blocks)
		assert.Empty(t, text)
		assert.False(t, isSystem)
	})
}

// --- Codex sessionCWD edge cases ---

func TestCodexAdapter_SessionCWD(t *testing.T) {
	adapter := &CodexAdapter{}

	t.Run("file not found returns empty", func(t *testing.T) {
		got := adapter.sessionCWD("/nonexistent/file.jsonl")
		assert.Empty(t, got)
	})

	t.Run("file with no session_meta returns empty", func(t *testing.T) {
		dir := t.TempDir()
		path := writeCodexSession(t, dir, "no-meta.jsonl", []map[string]any{
			codexUserMsg("hello"),
		})
		got := adapter.sessionCWD(path)
		assert.Empty(t, got)
	})

	t.Run("extracts cwd from session_meta", func(t *testing.T) {
		dir := t.TempDir()
		path := writeCodexSession(t, dir, "with-meta.jsonl", []map[string]any{
			codexSessionMeta("/my/project", "1.0.0"),
			codexUserMsg("hello"),
		})
		got := adapter.sessionCWD(path)
		assert.Equal(t, "/my/project", got)
	})
}

// --- Claude Detect edge cases ---

func TestClaudeCodeAdapter_Detect_NoEnvVar(t *testing.T) {
	adapter := &ClaudeCodeAdapter{}

	t.Run("returns false when no env and no claude dir", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("CLAUDE_CODE_ENTRYPOINT", "")

		assert.False(t, adapter.Detect())
	})

	t.Run("returns false when claude dir exists but no projects subdir", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("CLAUDE_CODE_ENTRYPOINT", "")
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0755))

		assert.False(t, adapter.Detect())
	})

	t.Run("returns false when projects dir is empty", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("CLAUDE_CODE_ENTRYPOINT", "")
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude", "projects"), 0755))

		assert.False(t, adapter.Detect())
	})

	t.Run("returns true when projects dir has content", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("CLAUDE_CODE_ENTRYPOINT", "")
		projectsDir := filepath.Join(home, ".claude", "projects")
		require.NoError(t, os.MkdirAll(filepath.Join(projectsDir, "some-project"), 0755))

		assert.True(t, adapter.Detect())
	})
}

// --- GenericJSONLAdapter ReadMetadata edge cases ---

func TestGenericJSONLAdapter_ReadMetadata_MalformedFirstLine(t *testing.T) {
	adapter := &GenericJSONLAdapter{}

	t.Run("malformed JSON first line returns nil", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "bad-first.jsonl")
		require.NoError(t, os.WriteFile(path, []byte("{bad json\n"), 0644))

		meta, err := adapter.ReadMetadata(path)
		require.NoError(t, err)
		assert.Nil(t, meta)
	})

	t.Run("header with empty metadata object returns nil", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "empty-meta.jsonl")
		require.NoError(t, os.WriteFile(path, []byte(`{"type":"header","metadata":{}}`+"\n"), 0644))

		meta, err := adapter.ReadMetadata(path)
		require.NoError(t, err)
		assert.Nil(t, meta)
	})
}

// --- Claude sessionContainsAgentID error path ---

func TestClaudeCodeAdapter_SessionContainsAgentID_FileNotFound(t *testing.T) {
	adapter := &ClaudeCodeAdapter{}
	result := adapter.sessionContainsAgentID("/nonexistent/file.jsonl", "agent-123")
	assert.False(t, result, "should return false for nonexistent file")
}

// --- Codex sessionContainsAgentID error path ---

func TestCodexAdapter_SessionContainsAgentID_FileNotFound(t *testing.T) {
	adapter := &CodexAdapter{}
	result := adapter.sessionContainsAgentID("/nonexistent/file.jsonl", "agent-123")
	assert.False(t, result, "should return false for nonexistent file")
}
