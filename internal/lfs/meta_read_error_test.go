package lfs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Metadata errors must identify the file and distinguish unresolved Git
// conflicts from other invalid JSON while preserving the underlying error.
func TestReadSessionMetaParseDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		conflict   bool
	}{
		{"invalid JSON", `{"version":`, false},
		{"stash conflict", "{\n<<<<<<< Updated upstream\n\"summary_attempts\": 2\n=======\n\"summary_attempts\": 3\n>>>>>>> Stashed changes\n}", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "meta.json")
			require.NoError(t, os.WriteFile(path, []byte(tc.data), 0600))
			meta, err := ReadSessionMeta(dir)
			require.Nil(t, meta)
			require.ErrorContains(t, err, path)
			var syntaxErr *json.SyntaxError
			require.ErrorAs(t, err, &syntaxErr)
			if tc.conflict {
				require.ErrorContains(t, err, "unresolved Git conflict markers")
			} else {
				require.NotContains(t, err.Error(), "Git conflict")
			}
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, tc.data, string(data), "diagnosis must not select a conflict side")
		})
	}
}

func TestReadSessionMetaReadErrorNamesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.json")
	require.NoError(t, os.Mkdir(path, 0700))
	_, err := ReadSessionMeta(dir)
	require.ErrorContains(t, err, path)
	var pathErr *os.PathError
	require.ErrorAs(t, err, &pathErr)
}
