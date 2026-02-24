package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	sessionhtml "github.com/sageox/ox/internal/session/html"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildTemplateData_BasicSession(t *testing.T) {
	st := &session.StoredSession{
		Info: session.SessionInfo{
			Filename: "test-session.jsonl",
			FilePath: "/tmp/test-session.jsonl",
		},
		Entries: []map[string]any{
			{
				"type":      "message",
				"timestamp": "2026-01-14T10:00:00Z",
				"data": map[string]any{
					"role":    "user",
					"content": "Hello",
				},
			},
			{
				"type":      "message",
				"timestamp": "2026-01-14T10:00:05Z",
				"data": map[string]any{
					"role":    "assistant",
					"content": "Hi there!",
				},
			},
		},
	}

	data := buildTemplateData(st, nil)

	assert.Equal(t, "Agent Session", data.Title)
	assert.Len(t, data.Messages, 2)
	assert.Equal(t, "user", data.Messages[0].Type)
	assert.Equal(t, "assistant", data.Messages[1].Type)
	assert.NotNil(t, data.Statistics)
	assert.Equal(t, 2, data.Statistics.TotalMessages)
	assert.Equal(t, 1, data.Statistics.UserMessages)
}

func TestBuildTemplateData_WithMetadata(t *testing.T) {
	createdAt := time.Date(2026, 1, 14, 10, 0, 0, 0, time.UTC)
	st := &session.StoredSession{
		Info: session.SessionInfo{
			Filename: "test-session.jsonl",
		},
		Meta: &session.StoreMeta{
			AgentType: "claude-code",
			Model:     "claude-sonnet-4",
			Username:  "testuser",
			CreatedAt: createdAt,
		},
		Footer: map[string]any{
			"closed_at": "2026-01-14T10:30:00Z",
		},
		Entries: []map[string]any{},
	}

	data := buildTemplateData(st, nil)

	assert.Equal(t, "claude-code Session", data.Title)
	require.NotNil(t, data.Metadata)
	assert.Equal(t, "claude-code", data.Metadata.AgentType)
	assert.Equal(t, "claude-sonnet-4", data.Metadata.Model)
	assert.Equal(t, "testuser", data.Metadata.Username)
	// duration was removed from MetadataView
}

func TestBuildTemplateData_WithToolCalls(t *testing.T) {
	st := &session.StoredSession{
		Info: session.SessionInfo{
			Filename: "test-session.jsonl",
		},
		Entries: []map[string]any{
			{
				"type":      "tool_call",
				"timestamp": "2026-01-14T10:00:00Z",
				"data": map[string]any{
					"tool_name": "Read",
					"input":     `{"file_path": "/tmp/test.txt"}`,
				},
			},
			{
				"type":      "tool_result",
				"timestamp": "2026-01-14T10:00:01Z",
				"data": map[string]any{
					"tool_name": "Read",
					"output":    "file contents here",
				},
			},
		},
	}

	data := buildTemplateData(st, nil)

	assert.Len(t, data.Messages, 2)
	assert.Equal(t, "tool", data.Messages[0].Type)
	require.NotNil(t, data.Messages[0].ToolCall)
	assert.Equal(t, "Read", data.Messages[0].ToolCall.Name)
	assert.Contains(t, data.Messages[0].ToolCall.Input, "file_path")

	assert.Equal(t, "tool", data.Messages[1].Type)
	require.NotNil(t, data.Messages[1].ToolCall)
	assert.Equal(t, "Read", data.Messages[1].ToolCall.Name)
	assert.Equal(t, "file contents here", data.Messages[1].ToolCall.Output)

	// ToolCalls was removed from StatsView
}

func TestGenerateHTML_CreatesFile(t *testing.T) {
	tmpDir := t.TempDir()
	outputPath := filepath.Join(tmpDir, "output.html")

	st := &session.StoredSession{
		Info: session.SessionInfo{
			Filename: "test.jsonl",
		},
		Entries: []map[string]any{
			{
				"type": "message",
				"data": map[string]any{
					"role":    "user",
					"content": "Test message",
				},
			},
		},
	}

	err := generateHTML(st, outputPath)
	require.NoError(t, err)

	// verify file exists
	_, err = os.Stat(outputPath)
	require.NoError(t, err)

	// read and verify content
	content, err := os.ReadFile(outputPath)
	require.NoError(t, err)

	htmlStr := string(content)
	assert.Contains(t, htmlStr, "<!DOCTYPE html>")
	assert.Contains(t, htmlStr, "SageOx")
	assert.Contains(t, htmlStr, "Test message")
}

func TestGenerateHTML_EscapesHTML(t *testing.T) {
	tmpDir := t.TempDir()
	outputPath := filepath.Join(tmpDir, "output.html")

	st := &session.StoredSession{
		Info: session.SessionInfo{
			Filename: "test.jsonl",
		},
		Entries: []map[string]any{
			{
				"type": "message",
				"data": map[string]any{
					"role":    "user",
					"content": "<script>alert('xss')</script>",
				},
			},
		},
	}

	err := generateHTML(st, outputPath)
	require.NoError(t, err)

	content, err := os.ReadFile(outputPath)
	require.NoError(t, err)

	htmlStr := string(content)
	// should NOT contain raw script tags or their payload (XSS prevention)
	// goldmark strips raw HTML by default, so neither the tag nor the payload appears
	assert.NotContains(t, htmlStr, "<script>alert")
	assert.NotContains(t, htmlStr, "alert('xss')")
}

func TestReadSessionFromPath_Integration(t *testing.T) {
	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "input.jsonl")

	// create a test JSONL file
	content := `{"type":"header","metadata":{"version":"1.0","agent_type":"test-agent"}}
{"type":"message","data":{"role":"user","content":"Hello"}}
{"type":"message","data":{"role":"assistant","content":"Hi!"}}
{"type":"footer","closed_at":"2026-01-14T10:00:00Z"}
`
	err := os.WriteFile(inputPath, []byte(content), 0644)
	require.NoError(t, err)

	// read session
	st, err := session.ReadSessionFromPath(inputPath)
	require.NoError(t, err)

	assert.Equal(t, "input.jsonl", st.Info.Filename)
	assert.NotNil(t, st.Meta)
	assert.Equal(t, "test-agent", st.Meta.AgentType)
	assert.Len(t, st.Entries, 2) // excludes header and footer
	assert.NotNil(t, st.Footer)
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		expected string
	}{
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m 30s"},
		{5 * time.Minute, "5m"},
		{65 * time.Minute, "1h 5m"},
		{2 * time.Hour, "2h"},
		{2*time.Hour + 30*time.Minute, "2h 30m"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := sessionhtml.FormatDuration(tt.duration)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMapEntryType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"user", "user"},
		{"assistant", "assistant"},
		{"message", "assistant"}, // legacy format maps to assistant
		{"tool", "tool"},
		{"tool_call", "tool"},
		{"tool_result", "tool"},
		{"system", "system"},
		{"unknown", "info"},
		{"", "info"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := mapEntryType(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMapRoleToType(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"user", "user"},
		{"assistant", "assistant"},
		{"system", "system"},
		{"unknown", "info"},
		{"", "info"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := mapRoleToType(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestReclassifyByContent_SkillExpansion(t *testing.T) {
	// skill prompt expansions contain <!-- ox-hash: --> marker
	rawContent := "<!-- ox-hash: b1e68f3b2727 ver: 0.17.0 -->\nStart recording this agent session to the project ledger.\n\nUse when:\n- Beginning a new coding session"
	got := reclassifyByContent("user", "<p>Start recording this agent session</p>", rawContent)
	assert.Equal(t, "system", got, "skill expansion should be reclassified as system")
}

func TestReclassifyByContent_NormalUser(t *testing.T) {
	got := reclassifyByContent("user", "<p>Fix the login bug</p>", "Fix the login bug")
	assert.Equal(t, "user", got, "normal user message should stay as user")
}

func TestReclassifyByContent_SystemReminder(t *testing.T) {
	got := reclassifyByContent("user", "<p>&lt;system-reminder&gt;context&lt;/system-reminder&gt;</p>", "<system-reminder>context</system-reminder>")
	assert.Equal(t, "system", got, "system-reminder should be reclassified")
}

func TestReclassifyByContent_NonUserUnchanged(t *testing.T) {
	// non-user types should never be reclassified
	got := reclassifyByContent("assistant", "<p>Hello</p>", "<!-- ox-hash: fake -->")
	assert.Equal(t, "assistant", got, "assistant should not be reclassified even with ox-hash")
}

func TestBuildTemplateData_FallbackDuration(t *testing.T) {
	// session with zero CreatedAt (old sessions) but entry timestamps present
	st := &session.StoredSession{
		Info: session.SessionInfo{Filename: "test.jsonl"},
		Meta: &session.StoreMeta{
			AgentType: "claude-code",
			// CreatedAt is zero — old session
		},
		Entries: []map[string]any{
			{
				"type":      "user",
				"content":   "Hello",
				"timestamp": "2026-01-14T10:00:00Z",
			},
			{
				"type":      "assistant",
				"content":   "Hi!",
				"timestamp": "2026-01-14T10:05:30Z",
			},
		},
	}

	data := buildTemplateData(st, nil)

	require.NotNil(t, data.Metadata)
	// duration was removed from MetadataView; with zero CreatedAt and no footer,
	// StartedAt/EndedAt remain zero (no entry-based backfill without Duration)
	assert.True(t, data.Metadata.StartedAt.IsZero(), "StartedAt should be zero when CreatedAt is zero")
}

func TestBuildTemplateData_MetaDurationTakesPrecedence(t *testing.T) {
	// session where meta CreatedAt + footer closed_at are set
	createdAt := time.Date(2026, 1, 14, 10, 0, 0, 0, time.UTC)
	st := &session.StoredSession{
		Info: session.SessionInfo{Filename: "test.jsonl"},
		Meta: &session.StoreMeta{
			AgentType: "claude-code",
			CreatedAt: createdAt,
		},
		Footer: map[string]any{
			"closed_at": "2026-01-14T10:30:00Z",
		},
		Entries: []map[string]any{
			{
				"type":      "user",
				"content":   "Hello",
				"timestamp": "2026-01-14T10:05:00Z",
			},
		},
	}

	data := buildTemplateData(st, nil)
	// duration was removed from MetadataView; verify StartedAt uses meta.CreatedAt
	require.NotNil(t, data.Metadata)
	assert.Equal(t, createdAt, data.Metadata.StartedAt, "StartedAt should use meta.CreatedAt")
}

func TestBuildMessageView_SkillExpansionCollapsed(t *testing.T) {
	// a user entry with skill expansion content should be reclassified as system
	entry := map[string]any{
		"type":      "user",
		"content":   "<!-- ox-hash: abc123 ver: 0.17.0 -->\nStart recording this session.\n\nKeywords: session start, record",
		"timestamp": "2026-01-14T10:00:00Z",
	}

	msg := buildMessageView(1, entry, "User", "Assistant")
	assert.Equal(t, "system", msg.Type, "skill expansion should be system")
	assert.Equal(t, "System", msg.SenderLabel)
}

func TestBuildMessageView_OxNativeFormat(t *testing.T) {
	// ox native format: type at root, content at root, timestamp field
	entry := map[string]any{
		"type":      "user",
		"content":   "Hello world",
		"timestamp": "2026-01-14T10:00:00Z",
	}

	msg := buildMessageView(1, entry, "User", "Assistant")

	assert.Equal(t, 1, msg.ID)
	assert.Equal(t, "user", msg.Type)
	assert.Contains(t, msg.Content, "Hello world")
	assert.False(t, msg.Timestamp.IsZero(), "timestamp should be parsed")
}

func TestBuildMessageView_LegacyNestedFormat(t *testing.T) {
	// legacy format: type="message", data.role determines type, data.content for content
	entry := map[string]any{
		"type":      "message",
		"timestamp": "2026-01-14T10:00:00Z",
		"data": map[string]any{
			"role":    "user",
			"content": "Hello from nested",
		},
	}

	msg := buildMessageView(2, entry, "User", "Assistant")

	assert.Equal(t, 2, msg.ID)
	assert.Equal(t, "user", msg.Type) // should be mapped from data.role
	assert.Contains(t, msg.Content, "Hello from nested")
	assert.False(t, msg.Timestamp.IsZero())
}

func TestBuildMessageView_AlternativeTimestampField(t *testing.T) {
	// alternative format: uses "ts" instead of "timestamp"
	entry := map[string]any{
		"type":    "user",
		"content": "Hello with ts",
		"ts":      "2026-01-14T10:00:00Z",
	}

	msg := buildMessageView(3, entry, "User", "Assistant")

	assert.Equal(t, "user", msg.Type)
	assert.Contains(t, msg.Content, "Hello with ts")
	assert.False(t, msg.Timestamp.IsZero(), "should parse 'ts' field")
}

func TestReadSessionFromPath_LegacyMetaHeader(t *testing.T) {
	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "input.jsonl")

	// create a test JSONL file with _meta header format
	content := `{"_meta":{"schema_version":"1","agent_type":"claude-code","session_id":"test-session","started_at":"2026-01-14T10:00:00Z"}}
{"ts":"2026-01-14T10:00:01Z","type":"user","content":"Hello","seq":1}
{"ts":"2026-01-14T10:00:02Z","type":"assistant","content":"Hi there!","seq":2}
`
	err := os.WriteFile(inputPath, []byte(content), 0644)
	require.NoError(t, err)

	// read session
	st, err := session.ReadSessionFromPath(inputPath)
	require.NoError(t, err)

	assert.NotNil(t, st.Meta)
	assert.Equal(t, "claude-code", st.Meta.AgentType)
	assert.Equal(t, "1", st.Meta.Version)
	assert.Len(t, st.Entries, 2)
}

func TestReadSessionFromPath_StandardHeaderFormat(t *testing.T) {
	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "input.jsonl")

	// create a test JSONL file with standard header format
	content := `{"type":"header","metadata":{"version":"1.0","agent_type":"claude-code","ox_username":"ryan","started_at":"2026-01-14T10:00:00Z"}}
{"type":"message","timestamp":"2026-01-14T10:00:01Z","data":{"role":"user","content":"Create a branch"}}
{"type":"message","timestamp":"2026-01-14T10:00:02Z","data":{"role":"assistant","content":"I'll create that for you."}}
{"type":"footer","closed_at":"2026-01-14T10:30:00Z"}
`
	err := os.WriteFile(inputPath, []byte(content), 0644)
	require.NoError(t, err)

	// read session
	st, err := session.ReadSessionFromPath(inputPath)
	require.NoError(t, err)

	assert.NotNil(t, st.Meta)
	assert.Equal(t, "claude-code", st.Meta.AgentType)
	assert.Equal(t, "ryan", st.Meta.Username)
	assert.Len(t, st.Entries, 2) // excludes header and footer
	assert.NotNil(t, st.Footer)
}
