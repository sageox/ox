package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPendingUploadIDIsBoundToRawRecording(t *testing.T) {
	cache := t.TempDir()
	raw := filepath.Join(cache, "raw.jsonl")
	const id = "ses_019d0000-0000-7000-8000-000000000007"
	require.Empty(t, PendingUploadID(filepath.Join(cache, "missing")))
	require.Empty(t, PendingUploadID(cache))
	require.Error(t, WriteUploadRetryPending(cache, id), "a marker requires an existing recording")

	require.NoError(t, os.WriteFile(raw, []byte("original recording"), 0o600))
	require.NoError(t, WriteUploadRetryPending(cache, id))
	require.Equal(t, id, PendingUploadID(cache))

	require.NoError(t, os.WriteFile(raw, []byte("replacement recording"), 0o600))
	require.Empty(t, PendingUploadID(cache), "the stale marker grants no ownership")

	require.NoError(t, os.WriteFile(filepath.Join(cache, UploadRetryPendingFile), []byte("malformed marker"), 0o600))
	require.Empty(t, PendingUploadID(cache))
}
