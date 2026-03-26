package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests that complement session_lint_test.go with additional edge cases

func TestLintRawJSONLFile_AllEntryTypes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")

	content := `{"type":"header","started_at":"2026-01-01T00:00:00Z"}
{"type":"user","content":"hello","ts":"2026-01-01T00:00:01Z","seq":1}
{"type":"assistant","content":"hi there","ts":"2026-01-01T00:00:02Z","seq":2}
{"type":"system","content":"system message","ts":"2026-01-01T00:00:03Z","seq":3}
{"type":"tool","tool_name":"read","tool_output":"file contents","ts":"2026-01-01T00:00:04Z","seq":4}
{"type":"footer","ended_at":"2026-01-01T00:01:00Z"}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	result := lintRawJSONLFile(path, "test-session")

	assert.True(t, result.Valid)
	assert.Empty(t, result.Errors)
	assert.Equal(t, 4, result.EntryCount)
	assert.True(t, result.HasHeader)
	assert.True(t, result.HasFooter)
	assert.Equal(t, 1, result.TypeCounts["user"])
	assert.Equal(t, 1, result.TypeCounts["assistant"])
	assert.Equal(t, 1, result.TypeCounts["system"])
	assert.Equal(t, 1, result.TypeCounts["tool"])
}

func TestLintRawJSONLFile_NonexistentFile(t *testing.T) {
	t.Parallel()

	result := lintRawJSONLFile("/nonexistent/path/raw.jsonl", "missing")

	assert.False(t, result.Valid)
	assert.NotEmpty(t, result.Errors)
	assert.Contains(t, result.Errors[0], "cannot open file")
}

func TestLintRawJSONLFile_InvalidType(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")

	content := `{"type":"user","content":"hello","seq":1}
{"type":"banana","content":"invalid type","seq":2}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	result := lintRawJSONLFile(path, "bad-type")

	assert.False(t, result.Valid)
	assert.NotEmpty(t, result.Errors)
	assert.Contains(t, result.Errors[0], "banana")
}

func TestLintRawJSONLFile_EmptyContentWarning(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")

	content := `{"type":"user","content":"","ts":"2026-01-01T00:00:01Z","seq":1}
{"type":"assistant","content":"reply","ts":"2026-01-01T00:00:02Z","seq":2}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	result := lintRawJSONLFile(path, "empty-content")

	assert.True(t, result.Valid)
	assert.NotEmpty(t, result.Warnings)
	assert.Contains(t, result.Warnings[0], "empty content")
}

func TestLintRawJSONLFile_ToolMissingBothFields(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")

	content := `{"type":"tool","ts":"2026-01-01T00:00:01Z","seq":1}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	result := lintRawJSONLFile(path, "bad-tool")

	assert.True(t, result.Valid)
	assert.NotEmpty(t, result.Warnings)
	assert.Contains(t, result.Warnings[0], "missing both tool_name and tool_output")
}

func TestLintRawJSONLFile_TruncatesOnManyErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")

	var content string
	for i := 0; i < 25; i++ {
		content += `{"type":"invalid","content":"bad","seq":1}` + "\n"
	}
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	result := lintRawJSONLFile(path, "many-errors")

	assert.False(t, result.Valid)
	assert.Contains(t, result.Errors[len(result.Errors)-1], "truncated")
}

func TestLintRawJSONLFile_BlankLinesAreIgnored(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")

	content := `{"type":"user","content":"hello","ts":"2026-01-01T00:00:01Z","seq":1}

{"type":"assistant","content":"world","ts":"2026-01-01T00:00:02Z","seq":2}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	result := lintRawJSONLFile(path, "blanks")

	assert.True(t, result.Valid)
	assert.Equal(t, 2, result.EntryCount)
}

func TestLintRawJSONLFile_NoHeaderOrFooter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")

	content := `{"type":"user","content":"hello","ts":"2026-01-01T00:00:01Z","seq":1}
{"type":"assistant","content":"world","ts":"2026-01-01T00:00:02Z","seq":2}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	result := lintRawJSONLFile(path, "no-header-footer")

	assert.True(t, result.Valid)
	assert.False(t, result.HasHeader)
	assert.False(t, result.HasFooter)
}

func TestLintRawJSONLFile_TimestampFieldAlternate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")

	// uses "timestamp" instead of "ts"
	content := `{"type":"user","content":"hello","timestamp":"2026-01-01T00:00:01Z","seq":1}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	result := lintRawJSONLFile(path, "alt-ts")

	assert.True(t, result.Valid)
	assert.Empty(t, result.Warnings) // should not warn about missing timestamp
}

func TestLintRawJSONLFile_SystemEntryEmptyContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")

	content := `{"type":"system","content":"","ts":"2026-01-01T00:00:01Z","seq":1}
{"type":"user","content":"hello","ts":"2026-01-01T00:00:02Z","seq":2}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	result := lintRawJSONLFile(path, "system-empty")

	assert.True(t, result.Valid)
	assert.NotEmpty(t, result.Warnings)
	assert.Contains(t, result.Warnings[0], "system entry has empty content")
}

func TestValidRawEntryTypes_Coverage(t *testing.T) {
	t.Parallel()

	assert.True(t, validRawEntryTypes["user"])
	assert.True(t, validRawEntryTypes["assistant"])
	assert.True(t, validRawEntryTypes["system"])
	assert.True(t, validRawEntryTypes["tool"])
	assert.False(t, validRawEntryTypes["header"])
	assert.False(t, validRawEntryTypes["footer"])
	assert.False(t, validRawEntryTypes[""])
	assert.False(t, validRawEntryTypes["unknown"])
}
