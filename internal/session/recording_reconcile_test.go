package session

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestCaptureReplayPreservesRecordedPrefix prevents a failed checkpoint from
// duplicating redacted output, including a crash immediately before the newline.
// These are synthetic RawWriter failure fixtures, not native compatibility data.
func TestCaptureReplayPreservesRecordedPrefix(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		checkpoint int
		cut        string
	}{
		{"first batch complete", 0, "complete"},
		{"later batch complete", 1, "complete"},
		{"first batch partial JSON", 0, "partial"},
		{"later batch partial JSON", 1, "partial"},
		{"first batch missing newline", 0, "newline"},
		{"later batch missing newline", 1, "newline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rawPath, entries, expected, boundaries := captureReplayFixture(t)
			end := len(expected)
			switch tc.cut {
			case "partial":
				end = boundaries[2] + 23
			case "newline":
				end--
			}
			require.NoError(t, os.WriteFile(rawPath, expected[:end], 0600))
			matched, err := ReconcileRawPrefix(rawPath, tc.checkpoint, entries[tc.checkpoint:], "")
			require.NoError(t, err)
			wantMatched := len(entries) - tc.checkpoint
			if tc.cut != "complete" {
				wantMatched--
			}
			require.Equal(t, wantMatched, matched)
			writer, err := NewRawWriter(rawPath, "")
			require.NoError(t, err)
			for _, entry := range entries[tc.checkpoint+matched:] {
				require.NoError(t, writer.WriteEntry(&entry))
			}
			require.NoError(t, writer.CloseAndSync())
			got, err := os.ReadFile(rawPath)
			require.NoError(t, err)
			require.Equal(t, string(expected), string(got))
			require.NotContains(t, string(got), "AKIAIOSFODNN7EXAMPLE")
		})
	}
}

// TestCaptureReplayRefusesUnprovenOutput prevents arbitrary corruption or a
// different source from being truncated or silently treated as already saved.
func TestCaptureReplayRefusesUnprovenOutput(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"different source", "unrelated partial", "corrupt complete", "missing source", "checkpoint beyond raw", "missing raw with checkpoint"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rawPath, entries, expected, _ := captureReplayFixture(t)
			checkpoint := 0
			switch name {
			case "different source":
				entries[0].Content = "another conversation"
			case "unrelated partial":
				expected = append(bytes.Clone(expected), []byte("unrelated manual content")...)
				entries = append(entries, Entry{Type: EntryTypeUser, Content: "next"})
			case "corrupt complete":
				expected = append(bytes.Clone(expected), []byte("{broken}\n")...)
			case "missing source":
				entries = nil
			case "checkpoint beyond raw":
				checkpoint = len(entries) + 1
			case "missing raw with checkpoint":
				require.NoError(t, os.Remove(rawPath))
				_, err := ReconcileRawPrefix(rawPath, 1, entries, "")
				require.Error(t, err)
				_, statErr := os.Stat(rawPath)
				require.ErrorIs(t, statErr, os.ErrNotExist)
				return
			}
			require.NoError(t, os.WriteFile(rawPath, expected, 0600))
			_, err := ReconcileRawPrefix(rawPath, checkpoint, entries, "")
			require.Error(t, err)
			got, readErr := os.ReadFile(rawPath)
			require.NoError(t, readErr)
			require.Equal(t, expected, got)
		})
	}
}

// TestCaptureReplayEveryWriteBoundary covers interruption at every byte, not
// only JSON syntax boundaries, including multibyte UTF-8 and escaped strings.
func TestCaptureReplayEveryWriteBoundary(t *testing.T) {
	if testing.Short() {
		t.Skip("short: exhaustive file-write interruption matrix")
	}
	t.Parallel()
	rawPath, entries, expected, boundaries := captureReplayFixture(t)
	for end := boundaries[1]; end <= len(expected); end++ {
		require.NoError(t, os.WriteFile(rawPath, expected[:end], 0600))
		matched, err := ReconcileRawPrefix(rawPath, 1, entries[1:], "")
		require.NoError(t, err, "crash byte %d", end)
		writer, err := NewRawWriter(rawPath, "")
		require.NoError(t, err)
		for _, entry := range entries[1+matched:] {
			require.NoError(t, writer.WriteEntry(&entry))
		}
		require.NoError(t, writer.CloseAndSync())
		got, err := os.ReadFile(rawPath)
		require.NoError(t, err)
		require.Equal(t, expected, got, "crash byte %d", end)
	}
}

func captureReplayFixture(t *testing.T) (string, []Entry, []byte, []int) {
	t.Helper()
	rawPath := filepath.Join(t.TempDir(), "raw.jsonl")
	entries := []Entry{
		{Type: EntryTypeUser, Content: "preserve earlier work", Timestamp: time.Date(2026, 9, 14, 1, 0, 0, 0, time.UTC)},
		{Type: EntryTypeAssistant, Content: "résumé \"quoted\"\nnext line"},
		{Type: EntryTypeTool, ToolName: "read", ToolInput: "fixture", ToolOutput: "key AKIAIOSFODNN7EXAMPLE", IsError: true},
	}
	writer, err := NewRawWriter(rawPath, "")
	require.NoError(t, err)
	require.NoError(t, writer.WriteRaw(map[string]any{"_meta": map[string]any{"schema_version": "1", "fixture": "synthetic crash recovery"}}))
	for _, entry := range entries {
		require.NoError(t, writer.WriteEntry(&entry))
	}
	require.NoError(t, writer.CloseAndSync())
	data, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	var boundaries []int
	for i, b := range data {
		if b == '\n' {
			boundaries = append(boundaries, i+1)
		}
	}
	require.Len(t, boundaries, len(entries)+1)
	return rawPath, entries, data, boundaries
}
