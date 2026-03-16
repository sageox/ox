package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateEntries_Clean(t *testing.T) {
	entries := []session.Entry{
		{Type: session.EntryTypeUser, Content: "hello"},
		{Type: session.EntryTypeAssistant, Content: "hi there"},
		{Type: session.EntryTypeTool, ToolName: "Bash", ToolInput: "ls"},
	}

	v := validateEntries(entries)
	assert.False(t, v.hasIssues())
	assert.Equal(t, 1, v.Counts["user"])
	assert.Equal(t, 1, v.Counts["assistant"])
	assert.Equal(t, 1, v.Counts["tool"])
}

func TestValidateEntries_LeakedQueueOperation(t *testing.T) {
	entries := []session.Entry{
		{Type: session.EntryTypeUser, Content: "hello"},
		{Type: "queue-operation", Content: "enqueue"},
		{Type: session.EntryTypeAssistant, Content: "response"},
	}

	v := validateEntries(entries)
	assert.True(t, v.hasIssues())
	assert.Len(t, v.Errors, 1)
	assert.Contains(t, v.Errors[0], "queue-operation")
	assert.Contains(t, v.Errors[0], "leaked through adapter")
}

func TestValidateEntries_LeakedProgress(t *testing.T) {
	entries := []session.Entry{
		{Type: session.EntryTypeUser, Content: "hello"},
		{Type: "progress", Content: "tool running"},
		{Type: session.EntryTypeAssistant, Content: "done"},
	}

	v := validateEntries(entries)
	assert.True(t, v.hasIssues())
	assert.Len(t, v.Errors, 1)
	assert.Contains(t, v.Errors[0], "progress")
}

func TestValidateEntries_NoUserMessages(t *testing.T) {
	entries := make([]session.Entry, 0, 10)
	for i := 0; i < 6; i++ {
		entries = append(entries, session.Entry{Type: session.EntryTypeAssistant, Content: "response"})
	}

	v := validateEntries(entries)
	assert.True(t, v.hasIssues())
	// should have warning about no user messages
	found := false
	for _, w := range v.Warnings {
		if strings.Contains(w, "no user messages") {
			found = true
		}
	}
	assert.True(t, found, "expected warning about missing user messages")
}

func TestValidateEntries_NoAssistantMessages(t *testing.T) {
	entries := make([]session.Entry, 0, 10)
	for i := 0; i < 6; i++ {
		entries = append(entries, session.Entry{Type: session.EntryTypeUser, Content: "prompt"})
	}

	v := validateEntries(entries)
	assert.True(t, v.hasIssues())
	found := false
	for _, w := range v.Warnings {
		if strings.Contains(w, "no assistant messages") {
			found = true
		}
	}
	assert.True(t, found, "expected warning about missing assistant messages")
}

func TestValidateEntries_SmallSession_NoWarnings(t *testing.T) {
	// sessions with <= 5 entries shouldn't warn about missing types
	entries := []session.Entry{
		{Type: session.EntryTypeTool, ToolName: "Bash", Content: "output"},
	}

	v := validateEntries(entries)
	assert.False(t, v.hasIssues())
}

func TestValidateRawJSONLFile_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")

	lines := []map[string]any{
		{"type": "header", "version": "1.0"},
		{"type": "user", "content": "hello", "timestamp": "2026-01-01T00:00:00Z"},
		{"type": "assistant", "content": "hi", "timestamp": "2026-01-01T00:00:01Z"},
		{"type": "tool", "tool_name": "Bash", "timestamp": "2026-01-01T00:00:02Z"},
		{"type": "footer", "entry_count": 3},
	}

	writeJSONL(t, path, lines)

	v := validateRawJSONLFile(path)
	assert.Len(t, v.Errors, 0)
	assert.Equal(t, 1, v.Counts["user"])
	assert.Equal(t, 1, v.Counts["assistant"])
	assert.Equal(t, 1, v.Counts["tool"])
}

func TestValidateRawJSONLFile_LeakedTypes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")

	lines := []map[string]any{
		{"type": "header", "version": "1.0"},
		{"type": "queue-operation", "content": "enqueue", "timestamp": "2026-01-01T00:00:00Z"},
		{"type": "user", "content": "hello", "timestamp": "2026-01-01T00:00:01Z"},
		{"type": "file-history-snapshot", "timestamp": "2026-01-01T00:00:02Z"},
	}

	writeJSONL(t, path, lines)

	v := validateRawJSONLFile(path)
	assert.True(t, len(v.Errors) >= 2, "expected errors for leaked types, got %d", len(v.Errors))
}

func TestValidateRawJSONLFile_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")

	writeJSONL(t, path, []map[string]any{
		{"type": "header", "version": "1.0"},
		{"type": "footer", "entry_count": 0},
	})

	v := validateRawJSONLFile(path)
	assert.True(t, len(v.Errors) > 0)
	assert.Contains(t, v.Errors[0], "no entries")
}

func TestValidateRawJSONLFile_LFSPointer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")

	err := os.WriteFile(path, []byte("version https://git-lfs.github.com/spec/v1\noid sha256:abc123\nsize 100\n"), 0644)
	require.NoError(t, err)

	v := validateRawJSONLFile(path)
	assert.True(t, len(v.Errors) > 0)
	assert.Contains(t, v.Errors[0], "LFS stub")
}

func TestValidation_Summary(t *testing.T) {
	v := &sessionValidation{
		Errors:   []string{"leaked type"},
		Warnings: []string{"missing user"},
		Counts:   map[string]int{"assistant": 5},
	}
	assert.Contains(t, v.summary(), "1 errors")
	assert.Contains(t, v.summary(), "1 warnings")
}

func TestValidation_SummaryEmpty(t *testing.T) {
	v := &sessionValidation{Counts: map[string]int{}}
	assert.Equal(t, "", v.summary())
}

func TestValidateHTMLConsistency_SmallHTML(t *testing.T) {
	dir := t.TempDir()
	rawPath := filepath.Join(dir, "raw.jsonl")
	htmlPath := filepath.Join(dir, "session.html")

	// write raw.jsonl with many entries
	var lines []map[string]any
	lines = append(lines, map[string]any{"type": "header", "version": "1.0"})
	for i := 0; i < 20; i++ {
		lines = append(lines, map[string]any{"type": "user", "content": "hello", "timestamp": "2026-01-01T00:00:00Z"})
	}
	writeJSONL(t, rawPath, lines)

	// write suspiciously small HTML
	require.NoError(t, os.WriteFile(htmlPath, []byte("<html>tiny</html>"), 0644))

	v := validateHTMLConsistency(htmlPath, rawPath)
	assert.True(t, v.hasIssues())
	assert.Contains(t, v.Warnings[0], "too small")
}

func TestValidateHTMLConsistency_MissingFiles(t *testing.T) {
	dir := t.TempDir()
	// both files missing — should return empty validation
	v := validateHTMLConsistency(filepath.Join(dir, "session.html"), filepath.Join(dir, "raw.jsonl"))
	assert.False(t, v.hasIssues())
}

// writeJSONL writes a slice of maps as JSONL to the given path.
func writeJSONL(t *testing.T, path string, lines []map[string]any) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, line := range lines {
		require.NoError(t, enc.Encode(line))
	}
}
