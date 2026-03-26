package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegenerateSessionHTML_CreatesHTMLFile(t *testing.T) {
	sessionPath := t.TempDir()

	storedSession := &session.StoredSession{
		Meta: &session.StoreMeta{
			Version:   "1",
			AgentType: "claude-code",
			Username:  "person-a",
		},
		Entries: []map[string]any{
			{"type": "user", "content": "Hello there"},
			{"type": "assistant", "content": "Hi, how can I help?"},
		},
	}

	err := regenerateSessionHTML(storedSession, sessionPath)
	require.NoError(t, err)

	htmlPath := filepath.Join(sessionPath, ledgerFileHTML)
	data, err := os.ReadFile(htmlPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "<!DOCTYPE html")
}

func TestRegenerateSessionHTML_OverwritesExisting(t *testing.T) {
	sessionPath := t.TempDir()
	htmlPath := filepath.Join(sessionPath, ledgerFileHTML)

	// write a dummy file first
	require.NoError(t, os.WriteFile(htmlPath, []byte("<html>old</html>"), 0644))

	storedSession := &session.StoredSession{
		Meta: &session.StoreMeta{
			Version:   "1",
			AgentType: "claude-code",
		},
		Entries: []map[string]any{
			{"type": "user", "content": "test"},
			{"type": "assistant", "content": "response"},
		},
	}

	err := regenerateSessionHTML(storedSession, sessionPath)
	require.NoError(t, err)

	data, err := os.ReadFile(htmlPath)
	require.NoError(t, err)
	// should have the new generated content, not the old dummy
	assert.NotEqual(t, "<html>old</html>", string(data))
	assert.Contains(t, string(data), "<!DOCTYPE html")
}

func TestMapEntriesToTyped_ToolFields(t *testing.T) {
	t.Parallel()

	input := []map[string]any{
		{
			"type":        "tool",
			"content":     "ran command",
			"tool_name":   "Bash",
			"tool_input":  "ls -la",
			"tool_output": "file1 file2",
		},
	}

	result := mapEntriesToTyped(input)
	require.Len(t, result, 1)
	assert.Equal(t, session.SessionEntryTypeTool, result[0].Type)
	assert.Equal(t, "Bash", result[0].ToolName)
	assert.Equal(t, "ls -la", result[0].ToolInput)
	assert.Equal(t, "file1 file2", result[0].ToolOutput)
}

func TestMapEntriesToTyped_MixedTypes(t *testing.T) {
	t.Parallel()

	input := []map[string]any{
		{"type": 123, "content": "non-string type is ignored"},
		{"type": "user", "content": 42},
		{"type": "assistant"},
	}

	result := mapEntriesToTyped(input)
	require.Len(t, result, 3)

	// non-string type yields zero value
	assert.Equal(t, session.EntryType(""), result[0].Type)
	// non-string content yields zero value
	assert.Equal(t, "", result[1].Content)
	// missing content yields zero value
	assert.Equal(t, session.SessionEntryTypeAssistant, result[2].Type)
	assert.Equal(t, "", result[2].Content)
}

func TestRegenerateArtifacts_EmptyEntries(t *testing.T) {
	sessionPath := t.TempDir()

	rawSession := &session.StoredSession{
		Meta: &session.StoreMeta{
			Version: "1",
		},
		Entries: []map[string]any{},
	}

	err := regenerateArtifacts(sessionPath, rawSession)
	require.NoError(t, err)

	// HTML should still be created even with empty entries
	htmlPath := filepath.Join(sessionPath, ledgerFileHTML)
	_, err = os.Stat(htmlPath)
	assert.NoError(t, err, "session.html should exist even with empty entries")
}

func TestRegenerateArtifacts_NilMeta(t *testing.T) {
	sessionPath := t.TempDir()

	rawSession := &session.StoredSession{
		Meta: nil,
		Entries: []map[string]any{
			{"type": "user", "content": "hello"},
		},
	}

	err := regenerateArtifacts(sessionPath, rawSession)
	require.NoError(t, err)

	htmlPath := filepath.Join(sessionPath, ledgerFileHTML)
	_, err = os.Stat(htmlPath)
	assert.NoError(t, err, "session.html should exist even with nil meta")
}
