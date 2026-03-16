package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLintRawJSONLFile_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")
	content := `{"type":"user","content":"hello","ts":"2026-03-11T00:00:00Z","seq":1}
{"type":"assistant","content":"hi there","ts":"2026-03-11T00:00:01Z","seq":2}
{"type":"tool","content":"","tool_name":"Bash","tool_input":"ls","ts":"2026-03-11T00:00:02Z","seq":3}
{"type":"tool","content":"","tool_output":"file.txt","ts":"2026-03-11T00:00:03Z","seq":4}
{"type":"system","content":"context loaded","ts":"2026-03-11T00:00:04Z","seq":5}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	result := lintRawJSONLFile(path, "test-session")

	assert.True(t, result.Valid)
	assert.Empty(t, result.Errors)
	assert.Equal(t, 5, result.EntryCount)
	assert.Equal(t, 1, result.TypeCounts["user"])
	assert.Equal(t, 1, result.TypeCounts["assistant"])
	assert.Equal(t, 2, result.TypeCounts["tool"])
	assert.Equal(t, 1, result.TypeCounts["system"])
}

func TestLintRawJSONLFile_InvalidTypes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")
	content := `{"type":"queue-operation","content":"test","ts":"2026-03-11T00:00:00Z","seq":1}
{"type":"progress","content":"stuff","ts":"2026-03-11T00:00:01Z","seq":2}
{"type":"user","content":"valid","ts":"2026-03-11T00:00:02Z","seq":3}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	result := lintRawJSONLFile(path, "test-session")

	assert.False(t, result.Valid)
	assert.Len(t, result.Errors, 2)
	assert.Contains(t, result.Errors[0], "queue-operation")
	assert.Contains(t, result.Errors[1], "progress")
}

func TestLintRawJSONLFile_HeaderFooterSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")
	content := `{"type":"header","started_at":"2026-03-11T00:00:00Z"}
{"type":"user","content":"hello","ts":"2026-03-11T00:00:00Z","seq":1}
{"type":"assistant","content":"hi","ts":"2026-03-11T00:00:01Z","seq":2}
{"type":"footer","closed_at":"2026-03-11T00:00:02Z","entry_count":2}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	result := lintRawJSONLFile(path, "test-session")

	assert.True(t, result.Valid)
	assert.True(t, result.HasHeader)
	assert.True(t, result.HasFooter)
	assert.Equal(t, 2, result.EntryCount)
}

func TestLintRawJSONLFile_MetaLineSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")
	content := `{"_meta":{"schema_version":"1","source":"test","agent_id":"Ox1234"}}
{"type":"user","content":"hello","ts":"2026-03-11T00:00:00Z","seq":1}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	result := lintRawJSONLFile(path, "test-session")

	assert.True(t, result.Valid)
	assert.Equal(t, 1, result.EntryCount)
}

func TestLintRawJSONLFile_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(""), 0644))

	result := lintRawJSONLFile(path, "test-session")

	assert.False(t, result.Valid)
	assert.Contains(t, result.Errors[0], "no entries found")
}

func TestLintRawJSONLFile_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")
	content := `{"type":"user","content":"hello","ts":"2026-03-11T00:00:00Z","seq":1}
not valid json
{"type":"assistant","content":"hi","ts":"2026-03-11T00:00:01Z","seq":2}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	result := lintRawJSONLFile(path, "test-session")

	assert.False(t, result.Valid)
	assert.Len(t, result.Errors, 1)
	assert.Contains(t, result.Errors[0], "invalid JSON")
}

func TestLintRawJSONLFile_WarningsForMissingFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")
	content := `{"type":"user","content":"hello","seq":1}
{"type":"assistant","content":"","ts":"2026-03-11T00:00:01Z","seq":2}
{"type":"tool","content":"","ts":"2026-03-11T00:00:02Z","seq":3}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	result := lintRawJSONLFile(path, "test-session")

	// valid but with warnings
	assert.True(t, result.Valid)
	assert.NotEmpty(t, result.Warnings)

	// check specific warnings
	hasTimestampWarning := false
	hasEmptyContentWarning := false
	hasToolWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "missing ts/timestamp") {
			hasTimestampWarning = true
		}
		if strings.Contains(w, "empty content") {
			hasEmptyContentWarning = true
		}
		if strings.Contains(w, "tool entry missing") {
			hasToolWarning = true
		}
	}
	assert.True(t, hasTimestampWarning, "should warn about missing timestamp")
	assert.True(t, hasEmptyContentWarning, "should warn about empty content")
	assert.True(t, hasToolWarning, "should warn about tool missing tool_name/tool_output")
}

func TestLintRawJSONLFile_NonMonotonicSeq(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.jsonl")
	content := `{"type":"user","content":"hello","ts":"2026-03-11T00:00:00Z","seq":5}
{"type":"assistant","content":"hi","ts":"2026-03-11T00:00:01Z","seq":3}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))

	result := lintRawJSONLFile(path, "test-session")

	assert.True(t, result.Valid) // non-monotonic seq is a warning, not error
	hasSeqWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "not monotonically increasing") {
			hasSeqWarning = true
		}
	}
	assert.True(t, hasSeqWarning)
}
