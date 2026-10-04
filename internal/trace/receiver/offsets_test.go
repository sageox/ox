package receiver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/trace/model"
	"github.com/stretchr/testify/require"
)

func TestSnapshotOffsets(t *testing.T) {
	spool := filepath.Join(t.TempDir(), "spool")
	got, err := SnapshotOffsets(spool, []string{sessionA})
	require.NoError(t, err)
	require.Equal(t, model.Offsets{}, got[sessionA])
	require.Len(t, got, 1)
	require.NoError(t, os.MkdirAll(filepath.Join(spool, sessionA), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(spool, sessionA, "traces.jsonl"), []byte("span\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(spool, sessionA, "logs.jsonl"), []byte("event\n"), 0600))
	got, err = SnapshotOffsets(spool, []string{sessionA, sessionB})
	require.NoError(t, err)
	require.Equal(t, model.Offsets{Spans: 5, Events: 6}, got[sessionA])
	require.Equal(t, model.Offsets{}, got[sessionB])
	require.NoError(t, os.Remove(filepath.Join(spool, sessionA, "logs.jsonl")))
	require.NoError(t, os.Symlink(filepath.Join(spool, sessionA, "traces.jsonl"), filepath.Join(spool, sessionA, "logs.jsonl")))
	got, err = SnapshotOffsets(spool, []string{sessionA, sessionB, "../bad"})
	require.Error(t, err)
	require.NotContains(t, got, sessionA)
	require.NotContains(t, got, "../bad")
	require.Contains(t, got, sessionB)
}

func TestSnapshotOffsetsUnsafeSpool(t *testing.T) {
	for _, kind := range []string{"file", "symlink", "lock"} {
		t.Run(kind, func(t *testing.T) {
			spool := filepath.Join(t.TempDir(), "spool")
			switch kind {
			case "file":
				require.NoError(t, os.WriteFile(spool, []byte("not-directory"), 0600))
			case "symlink":
				require.NoError(t, os.Symlink(t.TempDir(), spool))
			case "lock":
				require.NoError(t, os.Mkdir(spool, 0700))
				require.NoError(t, os.Mkdir(filepath.Join(spool, ".lock"), 0700))
			}
			got, err := SnapshotOffsets(spool, []string{sessionA})
			require.Error(t, err)
			require.Empty(t, got, "an unreadable boundary must not become a zero offset")
		})
	}
}
