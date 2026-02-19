package html

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildImportedSession returns a StoredSession that simulates an imported JSONL
// session (root-level content, "ts" timestamps, _meta header with username).
func buildImportedSession() *session.StoredSession {
	return &session.StoredSession{
		Info: session.SessionInfo{
			Filename: "raw.jsonl",
			FilePath: "/tmp/test/sessions/2026-01-20T14-00-testdev-Ox1234/raw.jsonl",
		},
		Meta: &session.StoreMeta{
			Version:   "1",
			AgentType: "claude-code",
			AgentID:   "test-import-001",
			Username:  "testdev",
			OxVersion: "0.9.0",
			CreatedAt: time.Date(2026, 1, 20, 14, 0, 0, 0, time.UTC),
		},
		Entries: []map[string]any{
			{"type": "user", "content": "Fix the login validation bug in auth.go", "ts": "2026-01-20T14:00:01Z", "seq": float64(1)},
			{"type": "assistant", "content": "I'll investigate the login validation issue. Let me read the auth file first.", "ts": "2026-01-20T14:00:05Z", "seq": float64(2)},
			{"type": "tool", "tool_name": "Read", "tool_input": "/src/auth.go", "ts": "2026-01-20T14:00:10Z", "seq": float64(3)},
			{"type": "tool_result", "result": "package auth\n\nfunc ValidateLogin(email string) bool {\n\treturn email != \"\"\n}", "ts": "2026-01-20T14:00:11Z", "seq": float64(4)},
			{"type": "assistant", "content": "The validation is too permissive. I'll fix this.", "ts": "2026-01-20T14:00:15Z", "seq": float64(5)},
			{"type": "system", "content": "Build completed successfully.", "ts": "2026-01-20T14:00:20Z", "seq": float64(6)},
			{"type": "user", "content": "Looks good, run the tests", "ts": "2026-01-20T14:00:25Z", "seq": float64(7)},
			{"type": "assistant", "content": "All tests pass.", "ts": "2026-01-20T14:00:30Z", "seq": float64(8)},
		},
		Footer: map[string]any{
			"closed_at": "2026-01-20T14:30:00Z",
		},
	}
}

// buildStandardSession returns a StoredSession that simulates a standard
// recording (nested data.content, "timestamp" field, type="header" header).
func buildStandardSession() *session.StoredSession {
	return &session.StoredSession{
		Info: session.SessionInfo{
			Filename: "raw.jsonl",
			FilePath: "/tmp/test/sessions/2026-01-20T14-00-testdev-Ox5678/raw.jsonl",
		},
		Meta: &session.StoreMeta{
			Version:      "1.0",
			AgentType:    "claude-code",
			AgentVersion: "1.2.0",
			Model:        "claude-sonnet-4",
			Username:     "testdev",
			OxVersion:    "0.9.0",
			CreatedAt:    time.Date(2026, 1, 20, 14, 0, 0, 0, time.UTC),
		},
		Entries: []map[string]any{
			{"type": "message", "timestamp": "2026-01-20T14:00:01Z", "data": map[string]any{"role": "user", "content": "Create a new API endpoint for user profiles"}},
			{"type": "message", "timestamp": "2026-01-20T14:00:05Z", "data": map[string]any{"role": "assistant", "content": "I'll create the user profile endpoint."}},
			{"type": "tool_call", "timestamp": "2026-01-20T14:00:10Z", "data": map[string]any{"tool_name": "Read", "input": "{\"file_path\": \"/src/routes.go\"}"}},
			{"type": "tool_result", "timestamp": "2026-01-20T14:00:11Z", "data": map[string]any{"tool_name": "Read", "output": "package routes\n\nfunc SetupRoutes(r *Router) {}"}},
			{"type": "message", "timestamp": "2026-01-20T14:00:15Z", "data": map[string]any{"role": "assistant", "content": "I've added the profile endpoint."}},
			{"type": "message", "timestamp": "2026-01-20T14:00:20Z", "data": map[string]any{"role": "user", "content": "Perfect, ship it"}},
		},
		Footer: map[string]any{
			"closed_at": "2026-01-20T14:30:00Z",
		},
	}
}

// --- Path 1: internal/session/html/generator.go ---

func TestRegression_Path1_ImportedSession_Metadata(t *testing.T) {
	gen, err := NewGenerator()
	require.NoError(t, err)

	sess := buildImportedSession()
	html, err := gen.Generate(sess)
	require.NoError(t, err)
	htmlStr := string(html)

	// username must appear in HTML output
	assert.Contains(t, htmlStr, "testdev", "username should appear in metadata")
	// agent type must appear
	assert.Contains(t, htmlStr, "claude-code", "agent type should appear in metadata")
}

func TestRegression_Path1_ImportedSession_RootLevelContent(t *testing.T) {
	gen, err := NewGenerator()
	require.NoError(t, err)

	sess := buildImportedSession()
	html, err := gen.Generate(sess)
	require.NoError(t, err)
	htmlStr := string(html)

	// root-level content entries must render (not empty)
	assert.Contains(t, htmlStr, "Fix the login validation bug", "user root-level content must render")
	assert.Contains(t, htmlStr, "investigate the login validation", "assistant root-level content must render")
	assert.Contains(t, htmlStr, "All tests pass", "final assistant message must render")
}

func TestRegression_Path1_ImportedSession_EntryTypes(t *testing.T) {
	gen, err := NewGenerator()
	require.NoError(t, err)

	sess := buildImportedSession()
	html, err := gen.Generate(sess)
	require.NoError(t, err)
	htmlStr := string(html)

	// all entry types should have correct CSS classes
	assert.Contains(t, htmlStr, "message-user", "user entries should get message-user class")
	assert.Contains(t, htmlStr, "message-assistant", "assistant entries should get message-assistant class")
	assert.Contains(t, htmlStr, "message-tool", "tool entries should get message-tool class")
	assert.Contains(t, htmlStr, "message-system", "system entries should get message-system class")
}

func TestRegression_Path1_ImportedSession_ToolCallDetails(t *testing.T) {
	gen, err := NewGenerator()
	require.NoError(t, err)

	sess := buildImportedSession()
	html, err := gen.Generate(sess)
	require.NoError(t, err)
	htmlStr := string(html)

	// tool name and input should appear
	assert.Contains(t, htmlStr, "Read", "tool name should appear")
	assert.Contains(t, htmlStr, "/src/auth.go", "tool input should appear")
}

func TestRegression_Path1_StandardSession_NestedContent(t *testing.T) {
	gen, err := NewGenerator()
	require.NoError(t, err)

	sess := buildStandardSession()
	html, err := gen.Generate(sess)
	require.NoError(t, err)
	htmlStr := string(html)

	// nested data.content entries must render
	assert.Contains(t, htmlStr, "Create a new API endpoint", "nested user content must render")
	assert.Contains(t, htmlStr, "user profile endpoint", "nested assistant content must render")
}

func TestRegression_Path1_StandardSession_Metadata(t *testing.T) {
	gen, err := NewGenerator()
	require.NoError(t, err)

	sess := buildStandardSession()
	html, err := gen.Generate(sess)
	require.NoError(t, err)
	htmlStr := string(html)

	assert.Contains(t, htmlStr, "testdev", "username should appear")
	assert.Contains(t, htmlStr, "claude-code", "agent type should appear")
	assert.Contains(t, htmlStr, "claude-sonnet-4", "model should appear")
}

func TestRegression_Path1_GenerateToFile_SessionHTML(t *testing.T) {
	gen, err := NewGenerator()
	require.NoError(t, err)

	tmpDir := t.TempDir()
	outputPath := filepath.Join(tmpDir, "session.html")

	sess := buildImportedSession()
	err = gen.GenerateToFile(sess, outputPath)
	require.NoError(t, err)

	// verify file was created with expected name
	_, err = os.Stat(outputPath)
	require.NoError(t, err, "session.html should be created")

	content, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "<!DOCTYPE html>")
}

// --- Standard session with all metadata fields ---

func TestRegression_Path1_AllMetadataFields(t *testing.T) {
	gen, err := NewGenerator()
	require.NoError(t, err)

	sess := buildStandardSession()
	html, err := gen.Generate(sess)
	require.NoError(t, err)
	htmlStr := string(html)

	// all metadata fields should appear somewhere in the output
	assert.Contains(t, htmlStr, "testdev", "username")
	assert.Contains(t, htmlStr, "claude-code", "agent type")
	assert.Contains(t, htmlStr, "claude-sonnet-4", "model")
	assert.Contains(t, htmlStr, "30m", "duration should be computed from created_at and closed_at")
}

// --- Mixed format session (both root and nested content) ---

func TestRegression_Path1_MixedFormatSession(t *testing.T) {
	gen, err := NewGenerator()
	require.NoError(t, err)

	sess := &session.StoredSession{
		Meta: &session.StoreMeta{
			AgentType: "claude-code",
			Username:  "mixeduser",
		},
		Entries: []map[string]any{
			// root-level content (imported format)
			{"type": "user", "content": "Root level message", "ts": "2026-01-20T14:00:01Z"},
			// nested content (standard format)
			{"type": "message", "timestamp": "2026-01-20T14:00:05Z", "data": map[string]any{"role": "assistant", "content": "Nested level response"}},
		},
	}

	html, err := gen.Generate(sess)
	require.NoError(t, err)
	htmlStr := string(html)

	// both formats must render their content
	assert.Contains(t, htmlStr, "Root level message", "root-level content must render")
	assert.Contains(t, htmlStr, "Nested level response", "nested content must render")
}

// --- Verify no per-entry timestamps in output ---

func TestRegression_Path1_NoPerEntryTimestamps(t *testing.T) {
	gen, err := NewGenerator()
	require.NoError(t, err)

	sess := buildImportedSession()
	html, err := gen.Generate(sess)
	require.NoError(t, err)
	htmlStr := string(html)

	// per-entry timestamps should NOT appear in the message cards
	// (timestamps were removed per user request)
	// the time format "14:00:01" should not appear inline with messages
	// but header-level started/ended times are still ok
	messageSection := htmlStr
	if idx := strings.Index(htmlStr, `id="messages"`); idx > 0 {
		messageSection = htmlStr[idx:]
	}
	// individual entry times like 14:00:01, 14:00:05, etc should not appear in message area
	assert.NotContains(t, messageSection, "14:00:01", "per-entry timestamps should not appear in messages")
	assert.NotContains(t, messageSection, "14:00:05", "per-entry timestamps should not appear in messages")
}
