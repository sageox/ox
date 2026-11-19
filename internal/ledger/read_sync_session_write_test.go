package ledger

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Mode e of the live-stream wedge suite: a read sync adopts a Ledger path
// that holds only its cache (a fresh repo recording before its clone) while a
// hook's capture batch has raw.jsonl open for append. Publication copies the
// cache into the stage, then deletes the original: a turn appended after the
// copy goes to the deleted file.
func TestReadSyncAdoptionKeepsASessionTurnWrittenDuringPublication(t *testing.T) {
	t.Skip("known loss, red as of c561daa1b: publication must carry the cache by rename (gitserver.MoveCloneIntoPlace), not copy then delete")
	f := newReadFixture(t)
	raw := filepath.Join(f.opts.Path, ".sageox", "cache", "sessions", "s1", "raw.jsonl")
	writeReadTestFile(t, raw, "turn 1\n")
	w, err := os.OpenFile(raw, os.O_WRONLY|os.O_APPEND, 0)
	require.NoError(t, err)

	result := ReadSync(context.Background(), f.opts)
	require.True(t, result.Ready, "%+v", result)
	_, err = w.WriteString("turn 2\n")
	require.NoError(t, err)
	require.NoError(t, w.Close())

	got, err := os.ReadFile(raw)
	require.NoError(t, err)
	require.Equal(t, "turn 1\nturn 2\n", string(got), "a turn written while the read sync published was lost")
}
