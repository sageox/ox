package main

import (
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/pipeline"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- buildSessionStartOutput ---

func TestBuildSessionStartOutput(t *testing.T) {
	started := time.Date(2026, 3, 25, 14, 30, 0, 0, time.UTC)

	tests := []struct {
		name        string
		agentID     string
		adapterName string
		sessionFile string
		title       string
		notice      string
		wantGeneric bool // true if adapter is "generic" → extra fields populated
	}{
		{
			name:        "claude-code adapter omits generic fields",
			agentID:     "OxABC1",
			adapterName: "claude-code",
			sessionFile: "/some/path/session.jsonl",
			title:       "Auth refactor",
			notice:      "",
			wantGeneric: false,
		},
		{
			name:        "generic adapter populates session_file and format_hint",
			agentID:     "OxDEF2",
			adapterName: "generic",
			sessionFile: "/tmp/sessions/input.jsonl",
			title:       "",
			notice:      "First session notice",
			wantGeneric: true,
		},
		{
			name:        "empty adapter is not generic",
			agentID:     "OxGHI3",
			adapterName: "",
			sessionFile: "",
			title:       "",
			notice:      "",
			wantGeneric: false,
		},
		{
			name:        "codex adapter omits generic fields",
			agentID:     "OxJKL4",
			adapterName: "codex",
			sessionFile: "/codex/session.jsonl",
			title:       "Migration plan",
			notice:      "",
			wantGeneric: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := buildSessionStartOutput(tt.agentID, tt.adapterName, tt.sessionFile, tt.title, tt.notice, started)

			assert.True(t, out.Success)
			assert.Equal(t, "session_start", out.Type)
			assert.Equal(t, tt.agentID, out.AgentID)
			assert.Equal(t, tt.title, out.Title)
			assert.Equal(t, tt.adapterName, out.Adapter)
			assert.Equal(t, started.Format(time.RFC3339), out.Started)
			assert.Equal(t, tt.notice, out.Notice)
			assert.Equal(t, sessionStartGuidance, out.Guidance)
			assert.Contains(t, out.Hint, "ox-session-stop")

			if tt.wantGeneric {
				assert.Equal(t, tt.sessionFile, out.SessionFile)
				assert.Equal(t, genericFormatHint, out.FormatHint)
				assert.NotEmpty(t, out.NextActions)
				// next actions should reference the agent ID
				assert.Contains(t, out.NextActions[0], tt.agentID)
			} else {
				assert.Empty(t, out.SessionFile, "non-generic adapter should omit SessionFile")
				assert.Empty(t, out.FormatHint, "non-generic adapter should omit FormatHint")
				assert.Nil(t, out.NextActions, "non-generic adapter should omit NextActions")
			}
		})
	}
}

// --- isAuthRelatedError ---

func TestIsAuthRelatedError(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		// positive matches
		{"authentication required", true},
		{"AUTHENTICATION REQUIRED", true},
		{"credentials expired for user", true},
		{"no git credentials found", true},
		{"git credentials have empty token", true},
		{"run 'ox login' first", true},
		{"invalid auth token", true},
		{"HTTP 401 Unauthorized", true},
		{"HTTP 403 Forbidden", true},
		{"credential refresh failed", true},
		{"PAT rejected by server", true},

		// negative matches
		{"network timeout after 30s", false},
		{"file not found: /tmp/session.jsonl", false},
		{"", false},
		{"git push failed: non-fast-forward", false},
		{"LFS upload: server returned 500", false},
		{"invalid JSON in config.json", false},
	}

	for _, tt := range tests {
		t.Run(tt.msg, func(t *testing.T) {
			got := pipeline.IsAuthRelatedError(tt.msg)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- convertStoredEntries ---

func TestConvertStoredEntries(t *testing.T) {
	t.Run("converts all field types", func(t *testing.T) {
		stored := []map[string]any{
			{
				"type":       "user",
				"content":    "Fix the login bug",
				"timestamp":  "2026-03-25T14:00:00Z",
				"tool_name":  "",
				"tool_input": "",
			},
			{
				"type":       "tool",
				"content":    "PASS",
				"tool_name":  "bash",
				"tool_input": "go test ./...",
				"timestamp":  "2026-03-25T14:01:00Z",
			},
			{
				"type":    "assistant",
				"content": "All tests pass.",
			},
		}

		entries := convertStoredEntries(stored)
		require.Len(t, entries, 3)

		assert.Equal(t, session.EntryType("user"), entries[0].Type)
		assert.Equal(t, "Fix the login bug", entries[0].Content)
		assert.Equal(t, 2026, entries[0].Timestamp.Year())

		assert.Equal(t, session.EntryType("tool"), entries[1].Type)
		assert.Equal(t, "bash", entries[1].ToolName)
		assert.Equal(t, "go test ./...", entries[1].ToolInput)

		assert.Equal(t, session.EntryType("assistant"), entries[2].Type)
		assert.True(t, entries[2].Timestamp.IsZero(), "missing timestamp should be zero")
	})

	t.Run("handles empty input", func(t *testing.T) {
		entries := convertStoredEntries(nil)
		assert.Empty(t, entries)
	})

	t.Run("handles entries with missing fields", func(t *testing.T) {
		stored := []map[string]any{
			{}, // all fields missing
			{"type": "system"},
			{"content": "orphan content"},
		}

		entries := convertStoredEntries(stored)
		require.Len(t, entries, 3)

		// empty entry: zero-value fields
		assert.Equal(t, session.EntryType(""), entries[0].Type)
		assert.Empty(t, entries[0].Content)

		// type-only
		assert.Equal(t, session.EntryType("system"), entries[1].Type)
		assert.Empty(t, entries[1].Content)

		// content-only
		assert.Equal(t, session.EntryType(""), entries[2].Type)
		assert.Equal(t, "orphan content", entries[2].Content)
	})

	t.Run("ignores invalid timestamp format", func(t *testing.T) {
		stored := []map[string]any{
			{
				"type":      "user",
				"content":   "hello",
				"timestamp": "not-a-timestamp",
			},
		}

		entries := convertStoredEntries(stored)
		require.Len(t, entries, 1)
		assert.True(t, entries[0].Timestamp.IsZero(), "invalid timestamp should result in zero time")
	})

	t.Run("handles wrong value types gracefully", func(t *testing.T) {
		stored := []map[string]any{
			{
				"type":      42,    // wrong type: int instead of string
				"content":   true,  // wrong type: bool instead of string
				"timestamp": 12345, // wrong type: int instead of string
			},
		}

		entries := convertStoredEntries(stored)
		require.Len(t, entries, 1)
		// type assertion fails silently; fields remain zero-valued
		assert.Equal(t, session.EntryType(""), entries[0].Type)
		assert.Empty(t, entries[0].Content)
	})
}

// --- isManualSessionAgent ---

func TestIsManualSessionAgent(t *testing.T) {
	tests := []struct {
		agentType string
		want      bool
	}{
		{"codex", true},
		{"Codex", true}, // canonicalAgentType lowercases
		{"claude-code", false},
		{"claude code", false},
		{"generic", false},
		{"", false},
		{"unknown-agent", false},
	}

	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			got := isManualSessionAgent(tt.agentType)
			assert.Equal(t, tt.want, got)
		})
	}
}
