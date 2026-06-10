//go:build !short

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRecordEntriesToSession_RedactsSecrets is the load-bearing security
// test for ox-hxyq: recordEntriesToSession must route every byte through
// the session.RawWriter redaction chokepoint. Before the fix this path
// opened raw.jsonl directly and encoded tool_output verbatim, landing
// secrets unredacted in the team-visible LFS ledger.
//
// Failure prevented: a secret in tool_output (or content/tool_input)
// reaches raw.jsonl unredacted because the recording path bypassed
// RawWriter.
func TestRecordEntriesToSession_RedactsSecrets(t *testing.T) {
	projectRoot := setupIncrementalTest(t)
	state := startTestRecording(t, projectRoot, "OxRedact", "claude-code")

	// canaries spanning the three redaction layers and all three string
	// fields (content, tool_input, tool_output).
	awsKey := "AKIAIOSFODNN7EXAMPLE"
	// split-string OpenAI-shaped key so GitHub's secret scanner doesn't
	// block the push; the redactor sees the concatenated whole.
	openaiKey := "sk-" + "abcdefghijklmnopqrst" + "T3Blbk" + "FJ" + "abcdefghijklmnopqrst"

	entries := []sessionRecordInput{
		{
			Type:       "tool",
			Content:    "ran a command",
			ToolName:   "Bash",
			ToolInput:  "echo $OPENAI_API_KEY",
			ToolOutput: "AWS_SECRET_ACCESS_KEY=" + awsKey + "\nkey=" + openaiKey,
		},
	}

	recorded, err := recordEntriesToSession(projectRoot, state, entries)
	require.NoError(t, err)
	assert.Equal(t, 1, recorded)

	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
	data, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	out := string(data)

	// raw secrets must NOT survive the chokepoint.
	assert.NotContains(t, out, awsKey, "AWS key leaked through recording path")
	assert.NotContains(t, out, openaiKey, "OpenAI key leaked through recording path")

	// the redaction marker proves the chokepoint actually fired.
	assert.Contains(t, out, "[REDACTED", "expected redaction marker in raw.jsonl")

	// seq numbering and the entry payload must still round-trip.
	lines := readJSONLLines(t, rawPath)
	require.Len(t, lines, 1)
	assert.Equal(t, "tool", lines[0]["type"])
	assert.Equal(t, "Bash", lines[0]["tool_name"])
	// seq starts at state.EntryCount (0 for a fresh recording).
	assert.EqualValues(t, 0, lines[0]["seq"])
}

// TestRecordEntriesToSession_FileModeIsOwnerOnly verifies the recording
// path creates raw.jsonl as 0600 (owner-only), not world-readable.
// raw.jsonl holds full conversation content; 0644 would leak it to every
// local user.
func TestRecordEntriesToSession_FileModeIsOwnerOnly(t *testing.T) {
	projectRoot := setupIncrementalTest(t)
	state := startTestRecording(t, projectRoot, "OxMode", "claude-code")

	_, err := recordEntriesToSession(projectRoot, state, []sessionRecordInput{
		{Type: "user", Content: "hello"},
	})
	require.NoError(t, err)

	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
	fi, err := os.Stat(rawPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), fi.Mode().Perm(),
		"raw.jsonl must be owner-only (0600), got %o", fi.Mode().Perm())
}

// TestRecordEntriesToSession_PreservesSeqNumbering verifies multi-entry
// seq numbering continues from state.EntryCount. The legacy direct-encoder
// path numbered entries state.EntryCount+i; RawWriter must preserve that.
func TestRecordEntriesToSession_PreservesSeqNumbering(t *testing.T) {
	projectRoot := setupIncrementalTest(t)
	state := startTestRecording(t, projectRoot, "OxSeq", "claude-code")
	state.EntryCount = 5 // simulate prior entries already recorded

	_, err := recordEntriesToSession(projectRoot, state, []sessionRecordInput{
		{Type: "user", Content: "a"},
		{Type: "assistant", Content: "b"},
	})
	require.NoError(t, err)

	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
	lines := readJSONLLines(t, rawPath)
	require.Len(t, lines, 2)
	assert.EqualValues(t, 5, lines[0]["seq"])
	assert.EqualValues(t, 6, lines[1]["seq"])
}

// TestWriteRawHeader_FileModeIsOwnerOnly verifies the header write also
// produces a 0600 raw.jsonl. writeRawHeader truncates at session start, so
// it owns the initial file mode.
func TestWriteRawHeader_FileModeIsOwnerOnly(t *testing.T) {
	projectRoot := setupIncrementalTest(t)
	state := startTestRecording(t, projectRoot, "OxHdrMode", "claude-code")

	require.NoError(t, writeRawHeader(projectRoot, state))

	rawPath := filepath.Join(state.SessionPath, "raw.jsonl")
	fi, err := os.Stat(rawPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), fi.Mode().Perm(),
		"raw.jsonl header write must be owner-only (0600), got %o", fi.Mode().Perm())

	// header contents must be unchanged.
	lines := readJSONLLines(t, rawPath)
	require.Len(t, lines, 1)
	assert.Equal(t, "header", lines[0]["type"])
	meta, ok := lines[0]["metadata"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "OxHdrMode", meta["agent_id"])
}
