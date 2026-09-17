package session

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadSessionHeaderDoesNotReadLargeConversation(t *testing.T) {
	for _, header := range []string{
		`{"type":"header","metadata":{"agent_id":"OxHeader","agent_type":"codex","username":"reader"}}`,
		`{"_meta":{"agent_id":"OxHeader","agent_type":"codex","username":"reader"}}`,
		`{"metadata":{"agent_id":"OxHeader","agent_type":"codex","username":"reader"}}`,
	} {
		t.Run(header[:12], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "raw.jsonl")
			file, err := os.Create(path)
			require.NoError(t, err)
			_, err = file.WriteString(header + "\n")
			require.NoError(t, err)
			// Invalid body bytes deliberately ensure the metadata reader never parses
			// entries or depends on the conversation fitting a whole-result reader.
			require.NoError(t, file.Truncate(65*1024*1024))
			require.NoError(t, file.Close())
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			meta, err := ReadSessionHeader(path)
			runtime.ReadMemStats(&after)
			require.NoError(t, err)
			require.Equal(t, "OxHeader", meta.AgentID)
			require.Equal(t, "reader", meta.Username)
			require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(256*1024))
		})
	}
}
