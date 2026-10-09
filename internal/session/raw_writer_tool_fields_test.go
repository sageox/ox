package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRawWriterRedactsEveryToolField prevents built-in detectors from silently
// protecting message text while the same credential survives in tool data.
func TestRawWriterRedactsEveryToolField(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"content", "input", "output"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "raw.jsonl")
			writer, err := NewRawWriter(path, "")
			require.NoError(t, err)
			entry := SessionEntry{Type: EntryTypeTool, ToolName: "read"}
			canary := "AKIAIOSFODNN7EXAMPLE"
			switch field {
			case "content":
				entry.Content = canary
			case "input":
				entry.ToolInput = canary
			case "output":
				entry.ToolOutput = canary
			}
			require.NoError(t, writer.WriteEntry(&entry))
			require.NoError(t, writer.CloseAndSync())
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NotContains(t, string(data), canary)
			require.Contains(t, string(data), "[REDACTED_AWS_KEY]")
			// The writer's documented mutation contract protects downstream
			// consumers of the same entry as well as the on-disk bytes.
			require.NotContains(t, entry.Content+entry.ToolInput+entry.ToolOutput, canary)
		})
	}
}
