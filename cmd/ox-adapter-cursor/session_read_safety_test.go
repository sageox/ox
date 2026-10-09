package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/stretchr/testify/require"
)

func TestCursorReaderRejectsNonregularOrUnreadableSourceWithoutEntries(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "directory.jsonl")
	require.NoError(t, os.Mkdir(directory, 0o700))
	batch, err := handleReadFromOffset(adapterprotocol.ReadFromOffsetParams{SessionFile: directory})
	require.ErrorContains(t, err, "expected a regular file")
	require.Nil(t, batch)
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission checks require Unix permissions without root privileges")
	}
	source := filepath.Join(root, "private-native-identity.jsonl")
	require.NoError(t, os.WriteFile(source, []byte("{}\n"), 0o600))
	require.NoError(t, os.Chmod(source, 0))
	t.Cleanup(func() { _ = os.Chmod(source, 0o600) })
	for _, offset := range []int64{0, 3} {
		batch, err := handleReadFromOffset(adapterprotocol.ReadFromOffsetParams{SessionFile: source, Offset: offset})
		require.ErrorIs(t, err, os.ErrPermission)
		require.ErrorContains(t, err, "source-unreadable")
		require.NotContains(t, err.Error(), source)
		require.Nil(t, batch, "an unreadable source must not acknowledge offsets or entries")
	}
}
