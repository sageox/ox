package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildMdRegressionImported returns an imported-format session for markdown tests.
func buildMdRegressionImported() *StoredSession {
	return &StoredSession{
		Info: SessionInfo{
			Filename: "raw.jsonl",
			FilePath: "/tmp/test/sessions/2026-01-20T14-00-testdev-Ox1234/raw.jsonl",
		},
		Meta: &StoreMeta{
			Version:   "1",
			AgentType: "claude-code",
			AgentID:   "test-import-001",
			Username:  "testdev",
			OxVersion: "0.9.0",
			CreatedAt: time.Date(2026, 1, 20, 14, 0, 0, 0, time.UTC),
		},
		Entries: []map[string]any{
			{"type": "user", "content": "Fix the login validation bug in auth.go", "ts": "2026-01-20T14:00:01Z", "seq": float64(1)},
			{"type": "assistant", "content": "I'll investigate the login validation issue.", "ts": "2026-01-20T14:00:05Z", "seq": float64(2)},
			{"type": "tool", "tool_name": "Read", "tool_input": "/src/auth.go", "ts": "2026-01-20T14:00:10Z", "seq": float64(3)},
			{"type": "tool_result", "result": "package auth\n\nfunc ValidateLogin() {}", "ts": "2026-01-20T14:00:11Z", "seq": float64(4)},
			{"type": "assistant", "content": "The validation is too permissive.", "ts": "2026-01-20T14:00:15Z", "seq": float64(5)},
			{"type": "system", "content": "Build completed successfully.", "ts": "2026-01-20T14:00:20Z", "seq": float64(6)},
			{"type": "user", "content": "Looks good, run the tests", "ts": "2026-01-20T14:00:25Z", "seq": float64(7)},
			{"type": "assistant", "content": "All tests pass.", "ts": "2026-01-20T14:00:30Z", "seq": float64(8)},
		},
		Footer: map[string]any{
			"closed_at": "2026-01-20T14:30:00Z",
		},
	}
}

// buildMdRegressionStandard returns a standard-format session for markdown tests.
func buildMdRegressionStandard() *StoredSession {
	return &StoredSession{
		Info: SessionInfo{
			Filename: "raw.jsonl",
			FilePath: "/tmp/test/sessions/2026-01-20T14-00-testdev-Ox5678/raw.jsonl",
		},
		Meta: &StoreMeta{
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

// ============================================================
// Markdown: internal/session/markdown.go
// ============================================================

func TestRegression_Markdown_ImportedSession_Metadata(t *testing.T) {
	gen := NewMarkdownGenerator()
	sess := buildMdRegressionImported()
	md, err := gen.Generate(sess)
	require.NoError(t, err)
	mdStr := string(md)

	assert.Contains(t, mdStr, "testdev", "username should appear in markdown metadata table")
	assert.Contains(t, mdStr, "claude-code", "agent type should appear in markdown")
}

func TestRegression_Markdown_ImportedSession_RootLevelContent(t *testing.T) {
	gen := NewMarkdownGenerator()
	sess := buildMdRegressionImported()
	md, err := gen.Generate(sess)
	require.NoError(t, err)
	mdStr := string(md)

	assert.Contains(t, mdStr, "Fix the login validation bug", "root-level user content must render in markdown")
	assert.Contains(t, mdStr, "investigate the login validation", "root-level assistant content must render in markdown")
	assert.Contains(t, mdStr, "All tests pass", "final assistant message must render")
}

func TestRegression_Markdown_ImportedSession_RoleHeaders(t *testing.T) {
	gen := NewMarkdownGenerator()
	sess := buildMdRegressionImported()
	md, err := gen.Generate(sess)
	require.NoError(t, err)
	mdStr := string(md)

	// user entries should show username in header
	assert.Contains(t, mdStr, "**testdev**", "user entries should show username as role header")
	// assistant entries should show formatted agent name
	assert.Contains(t, mdStr, "**Claude Code**", "assistant entries should show formatted agent name")
	// system entries
	assert.Contains(t, mdStr, "**System**", "system entries should show System header")
	// tool entries
	assert.Contains(t, mdStr, "**Tool Call**", "tool entries should show Tool Call header")
}

func TestRegression_Markdown_ImportedSession_EntryTypes(t *testing.T) {
	gen := NewMarkdownGenerator()
	sess := buildMdRegressionImported()
	md, err := gen.Generate(sess)
	require.NoError(t, err)
	mdStr := string(md)

	// entry count summary table was removed from footer;
	// verify entry types render via their role headers instead
	assert.Contains(t, mdStr, "**testdev**", "user entries should render with username header")
	assert.Contains(t, mdStr, "**Claude Code**", "assistant entries should render with agent name header")
}

func TestRegression_Markdown_ImportedSession_ToolDetails(t *testing.T) {
	gen := NewMarkdownGenerator()
	sess := buildMdRegressionImported()
	md, err := gen.Generate(sess)
	require.NoError(t, err)
	mdStr := string(md)

	// tool calls now render as compact `>_ ToolName` format
	assert.Contains(t, mdStr, "`>_ Read`", "tool name should appear in compact >_ format")
}

func TestRegression_Markdown_StandardSession_NestedContent(t *testing.T) {
	gen := NewMarkdownGenerator()
	sess := buildMdRegressionStandard()
	md, err := gen.Generate(sess)
	require.NoError(t, err)
	mdStr := string(md)

	assert.Contains(t, mdStr, "Create a new API endpoint", "nested user content must render in markdown")
	assert.Contains(t, mdStr, "user profile endpoint", "nested assistant content must render in markdown")
}

func TestRegression_Markdown_StandardSession_Duration(t *testing.T) {
	gen := NewMarkdownGenerator()
	sess := buildMdRegressionStandard()
	md, err := gen.Generate(sess)
	require.NoError(t, err)
	mdStr := string(md)

	// duration was removed from MetadataView/StatsView;
	// verify session still renders without it
	assert.Contains(t, mdStr, "## Conversation", "markdown should still contain conversation section")
	assert.NotContains(t, mdStr, "Duration", "duration field should not appear in metadata")
}

func TestRegression_Markdown_NoPerEntryTimestamps(t *testing.T) {
	gen := NewMarkdownGenerator()
	sess := buildMdRegressionImported()
	md, err := gen.Generate(sess)
	require.NoError(t, err)
	mdStr := string(md)

	// per-entry timestamps were removed (timestamps like _14:00:01_ should not appear)
	// note: header-level time is ok (Created: 2026-01-20T14:00:00Z)
	// look only in the conversation section
	convIdx := strings.Index(mdStr, "## Conversation")
	if convIdx > 0 {
		conversation := mdStr[convIdx:]
		assert.NotContains(t, conversation, "_14:00:", "per-entry timestamps should not appear in markdown conversation")
	}
}

func TestRegression_Markdown_GenerateToFile(t *testing.T) {
	tmpDir := t.TempDir()
	outputPath := filepath.Join(tmpDir, "session.md")

	gen := NewMarkdownGenerator()
	sess := buildMdRegressionImported()
	err := gen.GenerateToFile(sess, outputPath)
	require.NoError(t, err)

	content, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	mdStr := string(content)

	assert.Contains(t, mdStr, "# Agent Session", "should start with session header")
	assert.Contains(t, mdStr, "testdev", "username should appear")
	assert.Contains(t, mdStr, "Fix the login validation bug", "content should render")
}

// ============================================================
// ReadSessionFromPath integration tests for both formats
// ============================================================

func TestRegression_ReadSession_ImportedFormat(t *testing.T) {
	// read the actual imported_session.jsonl fixture
	fixturePath := filepath.Join("testdata", "imported_session.jsonl")
	if _, err := os.Stat(fixturePath); os.IsNotExist(err) {
		t.Skip("fixture not found")
	}

	sess, err := ReadSessionFromPath(fixturePath)
	require.NoError(t, err)

	require.NotNil(t, sess.Meta)
	assert.Equal(t, "claude-code", sess.Meta.AgentType, "should parse agent_type from _meta")
	assert.Equal(t, "testdev", sess.Meta.Username, "should parse username from _meta")
	assert.Equal(t, "1", sess.Meta.Version, "should parse schema_version as version")

	// entries should not include the _meta line
	assert.True(t, len(sess.Entries) >= 8, "should have all conversation entries")

	// first entry should be user with root-level content
	assert.Equal(t, "user", sess.Entries[0]["type"])
	assert.Contains(t, sess.Entries[0]["content"], "Fix the login validation bug")
}

func TestRegression_ReadSession_StandardFormat(t *testing.T) {
	fixturePath := filepath.Join("testdata", "standard_session.jsonl")
	if _, err := os.Stat(fixturePath); os.IsNotExist(err) {
		t.Skip("fixture not found")
	}

	sess, err := ReadSessionFromPath(fixturePath)
	require.NoError(t, err)

	require.NotNil(t, sess.Meta)
	assert.Equal(t, "claude-code", sess.Meta.AgentType)
	assert.Equal(t, "testdev", sess.Meta.Username, "should parse ox_username")
	assert.Equal(t, "1.0", sess.Meta.Version)
	assert.Equal(t, "0.9.0", sess.Meta.OxVersion, "should parse ox_version")

	// should have conversation entries (excluding header/footer)
	assert.True(t, len(sess.Entries) >= 6, "should have conversation entries")

	// footer should be parsed
	require.NotNil(t, sess.Footer)
	assert.Contains(t, sess.Footer, "closed_at")
}
