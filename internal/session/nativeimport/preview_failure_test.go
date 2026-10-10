package nativeimport

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session/adapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An invalid native timestamp must not expose a partial preview or leave an
// unsafe entry in the staged recording. Earlier valid entries remain readable
// for diagnosis, but the failure prevents the recording from being completed.
func TestPreviewEntriesRejectsUnserializableEntry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OX_XDG_DISABLE", "")
	root := t.TempDir()
	raw := []adapters.RawEntry{
		{Role: "user", Timestamp: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), Content: "valid opening request"},
		{Role: "assistant", Timestamp: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), Content: "entry that must not escape"},
	}
	entries, err := PreviewEntries(root, raw)
	require.ErrorContains(t, err, "redact entry 1")
	require.ErrorContains(t, err, "year outside of range")
	assert.Nil(t, entries, "a partial preview must never be returned as safe content")
	files, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Empty(t, files, "preview creates no staged recording")

	rawPath := filepath.Join(t.TempDir(), "raw.jsonl")
	written, err := WriteRaw(rawPath, root, RawHeader{NativeID: claudeID}, raw)
	require.ErrorContains(t, err, "write entry 1")
	assert.Zero(t, written, "failed recording is never reported as completed")
	data, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "valid opening request")
	assert.NotContains(t, string(data), "entry that must not escape")
	assert.NotContains(t, string(data), `"type":"footer"`, "an incomplete recording must not acquire a success footer")
}

// Invalid redaction policy must be rejected before the destination is opened,
// so refusing an import cannot truncate a previously valid raw recording.
func TestWriteRawInvalidPolicyPreservesExistingRecording(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OX_XDG_DISABLE", "")
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".sageox"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".sageox", "REDACT.md"), []byte("```redact\nregex \"[\" -> [X]\n```\n"), 0o600))
	rawPath := filepath.Join(t.TempDir(), "raw.jsonl")
	original := []byte("previous recording must survive\n")
	require.NoError(t, os.WriteFile(rawPath, original, 0o600))

	written, err := WriteRaw(rawPath, root, RawHeader{NativeID: claudeID}, []adapters.RawEntry{{Role: "user", Content: "unredacted source"}})
	require.ErrorContains(t, err, "invalid redaction policy")
	assert.Zero(t, written)
	data, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	assert.Equal(t, original, data, "policy validation precedes truncation")
}
