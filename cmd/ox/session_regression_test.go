package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	sessionhtml "github.com/sageox/ox/internal/session/html"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildRegressionImportedSession returns a StoredSession simulating imported JSONL
// (root-level content, "ts" timestamps, _meta header with username).
func buildRegressionImportedSession() *session.StoredSession {
	return &session.StoredSession{
		Info: session.SessionInfo{
			Filename: ledgerFileRaw,
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
			{"type": "assistant", "content": "I'll investigate the login validation issue.", "ts": "2026-01-20T14:00:05Z", "seq": float64(2)},
			{"type": "tool", "tool_name": "Read", "tool_input": "/src/auth.go", "ts": "2026-01-20T14:00:10Z", "seq": float64(3)},
			{"type": "tool_result", "result": "package auth\n\nfunc ValidateLogin() {}", "ts": "2026-01-20T14:00:11Z", "seq": float64(4)},
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

// buildRegressionStandardSession returns a StoredSession simulating a standard recording
// (nested data.content, "timestamp" field, type="header" metadata).
func buildRegressionStandardSession() *session.StoredSession {
	return &session.StoredSession{
		Info: session.SessionInfo{
			Filename: ledgerFileRaw,
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

// ============================================================
// Consolidated HTML generator (formerly Path 2 + Path 3, now single path)
// ============================================================

func TestRegression_ImportedSession_Metadata(t *testing.T) {
	sess := buildRegressionImportedSession()
	data := sessionhtml.BuildTemplateData(sess, nil)

	require.NotNil(t, data.Metadata)
	assert.Equal(t, "testdev", data.Metadata.Username, "username must appear in metadata")
	assert.Equal(t, "claude-code", data.Metadata.AgentType, "agent type must appear")
}

func TestRegression_ImportedSession_RootLevelContent(t *testing.T) {
	sess := buildRegressionImportedSession()
	data := sessionhtml.BuildTemplateData(sess, nil)

	require.True(t, len(data.Messages) >= 3, "should have at least 3 messages")

	// user messages with root-level content must render
	found := false
	for _, msg := range data.Messages {
		if strings.Contains(string(msg.Content), "Fix the login validation bug") {
			found = true
			break
		}
	}
	assert.True(t, found, "root-level user content must render")

	// assistant messages with root-level content must render
	found = false
	for _, msg := range data.Messages {
		if strings.Contains(string(msg.Content), "investigate the login validation") {
			found = true
			break
		}
	}
	assert.True(t, found, "root-level assistant content must render")
}

func TestRegression_ImportedSession_EntryTypes(t *testing.T) {
	sess := buildRegressionImportedSession()
	data := sessionhtml.BuildTemplateData(sess, nil)

	typeSet := make(map[string]bool)
	for _, msg := range data.Messages {
		typeSet[msg.Type] = true
	}

	assert.True(t, typeSet["user"], "should have user type entries")
	assert.True(t, typeSet["assistant"], "should have assistant type entries")
	assert.True(t, typeSet["tool"], "should have tool type entries")
	assert.True(t, typeSet["system"], "should have system type entries")
}

func TestRegression_ImportedSession_SenderLabels(t *testing.T) {
	sess := buildRegressionImportedSession()
	data := sessionhtml.BuildTemplateData(sess, nil)

	for _, msg := range data.Messages {
		switch msg.Type {
		case "user":
			assert.Equal(t, "testdev", msg.SenderLabel, "user entries should show username as sender label")
		case "assistant":
			assert.Equal(t, "Claude Code", msg.SenderLabel, "assistant entries should show formatted agent type")
		case "system":
			assert.Equal(t, "System", msg.SenderLabel)
		case "tool":
			assert.Equal(t, "Tool Call", msg.SenderLabel)
		}
	}
}

func TestRegression_StandardSession_NestedContent(t *testing.T) {
	sess := buildRegressionStandardSession()
	data := sessionhtml.BuildTemplateData(sess, nil)

	found := false
	for _, msg := range data.Messages {
		if strings.Contains(string(msg.Content), "Create a new API endpoint") {
			found = true
			break
		}
	}
	assert.True(t, found, "nested data.content user messages must render")
}

func TestRegression_StandardSession_Duration(t *testing.T) {
	sess := buildRegressionStandardSession()
	data := sessionhtml.BuildTemplateData(sess, nil)

	require.NotNil(t, data.Metadata)
	assert.False(t, data.Metadata.StartedAt.IsZero(), "StartedAt should be set")
	assert.False(t, data.Metadata.EndedAt.IsZero(), "EndedAt should be set")
}

func TestRegression_GenerateHTML_CreatesFile(t *testing.T) {
	tmpDir := t.TempDir()
	outputPath := filepath.Join(tmpDir, ledgerFileHTML)

	sess := buildRegressionImportedSession()
	err := generateHTML(sess, outputPath)
	require.NoError(t, err)

	content, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	htmlStr := string(content)

	assert.Contains(t, htmlStr, "<!DOCTYPE html>")
	assert.Contains(t, htmlStr, "SageOx")
	assert.Contains(t, htmlStr, "testdev", "username should appear in generated HTML")
	assert.Contains(t, htmlStr, "Fix the login validation bug", "user content must render")
}

func TestRegression_TimestampParsing_Ts(t *testing.T) {
	sess := buildRegressionImportedSession()
	data := sessionhtml.BuildTemplateData(sess, nil)

	for _, msg := range data.Messages {
		if msg.Type == "user" || msg.Type == "assistant" {
			assert.False(t, msg.Timestamp.IsZero(), "timestamp should be parsed from 'ts' field")
		}
	}
}

func TestRegression_TimestampParsing_Standard(t *testing.T) {
	sess := buildRegressionStandardSession()
	data := sessionhtml.BuildTemplateData(sess, nil)

	for _, msg := range data.Messages {
		assert.False(t, msg.Timestamp.IsZero(), "timestamp should be parsed from 'timestamp' field")
	}
}

func TestRegression_MapEntryType_Correctness(t *testing.T) {
	// regression: "user" and "assistant" were falling through to "info"
	// now tested via the consolidated generator which uses the same mapping
	sess := buildRegressionImportedSession()
	data := sessionhtml.BuildTemplateData(sess, nil)

	typeSet := make(map[string]bool)
	for _, msg := range data.Messages {
		typeSet[msg.Type] = true
	}

	// verify no "info" type for known entry types
	assert.True(t, typeSet["user"], "user should map correctly")
	assert.True(t, typeSet["assistant"], "assistant should map correctly")
	assert.True(t, typeSet["tool"], "tool should map correctly")
	assert.True(t, typeSet["system"], "system should map correctly")
}
