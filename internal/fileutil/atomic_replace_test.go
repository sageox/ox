package fileutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Failed replacement must leave the destination and its contents intact and
// clean up the temporary publication file, for both atomic writing APIs.
func TestAtomicReplacementFailurePreservesDestination(t *testing.T) {
	for _, kind := range []string{"bytes", "json"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "state")
			require.NoError(t, os.Mkdir(path, 0700))
			child := filepath.Join(path, "preserve")
			require.NoError(t, os.WriteFile(child, []byte("existing"), 0600))
			var err error
			if kind == "bytes" {
				err = AtomicWriteBytes(path, []byte("replacement"), 0600)
			} else {
				err = AtomicWriteJSON(path, map[string]string{"value": "replacement"}, 0600)
			}
			require.ErrorContains(t, err, "rename")
			data, err := os.ReadFile(child)
			require.NoError(t, err)
			require.Equal(t, "existing", string(data))
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			require.Len(t, entries, 1, "temporary file must be cleaned up")
		})
	}
}
