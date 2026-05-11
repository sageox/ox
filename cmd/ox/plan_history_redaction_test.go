package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWritePlanHistoryRaw_RedactsCredentialsInContent is a regression
// guard: user-supplied planning JSONL (via `ox agent <id> session
// capture-prior` / session import) was a write path that bypassed the
// redactor. If the input carries an AWS key in the Content field, the
// rendered raw.jsonl must NOT contain the canary bytes.
func TestWritePlanHistoryRaw_RedactsCredentialsInContent(t *testing.T) {
	dir := t.TempDir()
	rawPath := filepath.Join(dir, "raw.jsonl")

	entries := []planHistoryEntry{
		{Type: "user", Content: "review this snippet: aws_access_key_id=AKIAIOSFODNN7EXAMPLE"},
		{Type: "assistant", Content: "I see the key — that should be rotated."},
	}
	require.NoError(t, writePlanHistoryRaw(rawPath, entries, nil, "test-agent"))

	bytes, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	out := string(bytes)

	assert.NotContains(t, out, "AKIAIOSFODNN7EXAMPLE",
		"AWS canary reached planning raw.jsonl unredacted: %s", out)
	assert.Contains(t, out, "[REDACTED_AWS_KEY]",
		"expected redaction slug in raw.jsonl")
}

// TestWritePlanHistoryRaw_RedactsCredentialsInToolInput verifies the
// tool_input field gets the same treatment as content. Planning
// sessions captured from a real coding agent often have tool_input
// strings with commands the agent ran — those can pick up tokens too.
func TestWritePlanHistoryRaw_RedactsCredentialsInToolInput(t *testing.T) {
	dir := t.TempDir()
	rawPath := filepath.Join(dir, "raw.jsonl")

	entries := []planHistoryEntry{
		{
			Type:      "tool",
			Content:   "ran the curl command",
			ToolName:  "Bash",
			ToolInput: `curl -H "Authorization: Bearer ya29.a0ARrdaMthisIsATokenValue1234567890" https://api.example.com`,
		},
	}
	require.NoError(t, writePlanHistoryRaw(rawPath, entries, nil, "test-agent"))

	bytes, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	out := string(bytes)

	assert.NotContains(t, out, "ya29.a0ARrdaMthisIsATokenValue",
		"Bearer canary reached planning raw.jsonl unredacted: %s", out)
	assert.True(t,
		strings.Contains(out, "[REDACTED_BEARER_TOKEN]"),
		"expected bearer-redaction slug in raw.jsonl")
}

// TestWritePlanHistoryRaw_PreservesCleanContent verifies redaction
// doesn't false-positive on legitimate planning prose.
func TestWritePlanHistoryRaw_PreservesCleanContent(t *testing.T) {
	dir := t.TempDir()
	rawPath := filepath.Join(dir, "raw.jsonl")

	const clean = "let's add a new feature to the parser"
	entries := []planHistoryEntry{
		{Type: "user", Content: clean},
	}
	require.NoError(t, writePlanHistoryRaw(rawPath, entries, nil, "test-agent"))

	bytes, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	assert.Contains(t, string(bytes), clean,
		"legitimate planning content must survive unchanged")
}
