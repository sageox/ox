package nativeimport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A read-only view must neither leak custom/command-correlated secrets nor
// show a different conversation from the raw recording that actually uploads.
func TestPreviewEntriesMatchesImportedRecording(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".sageox"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox", "REDACT.md"), []byte("```redact\nregex \"private-acme-code\" -> [ACME]\n```\n"), 0600))
	start := time.Date(2026, 9, 12, 14, 3, 10, 0, time.UTC)
	raw := []adapters.RawEntry{
		{Timestamp: start, Role: "user", Content: "Debug private-acme-code"},
		{Timestamp: start.Add(time.Second), Role: "tool", ToolName: "Bash", ToolInput: `{"command":"gh auth token"}`, CallID: "secret"},
		{Timestamp: start.Add(2 * time.Second), Role: "tool", ToolOutput: "secret-without-credential-pattern", CallID: "secret"},
		{Timestamp: start.Add(3 * time.Second), Role: "tool", ToolName: "Bash", ToolInput: `{"command":"echo private-acme-code"}`, ToolOutput: "private-acme-code", IsError: true},
		{Timestamp: start.Add(4 * time.Second), Role: "assistant", Content: "Resolved private-acme-code"},
	}
	preview, err := PreviewEntries(root, raw)
	require.NoError(t, err)
	encoded, err := json.Marshal(preview)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "private-acme-code")
	assert.NotContains(t, string(encoded), "secret-without-credential-pattern")
	assert.Contains(t, string(encoded), "credential-output")
	path := filepath.Join(t.TempDir(), "raw.jsonl")
	_, err = WriteRaw(path, root, RawHeader{StartedAt: start, StoppedAt: start.Add(time.Hour), NativeID: claudeID}, raw)
	require.NoError(t, err)
	stored, err := session.ReadSessionFromPath(path)
	require.NoError(t, err)
	storedJSON, err := json.Marshal(stored.Entries)
	require.NoError(t, err)
	assert.JSONEq(t, string(storedJSON), string(encoded))
}

func TestPreviewEntriesRefusesInvalidPolicy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".sageox"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox", "REDACT.md"), []byte("```redact\nregex \"[\" -> [X]\n```\n"), 0600))
	entries, err := PreviewEntries(root, []adapters.RawEntry{{Role: "user", Content: "secret"}})
	require.ErrorContains(t, err, "invalid redaction policy")
	assert.Nil(t, entries)
}
