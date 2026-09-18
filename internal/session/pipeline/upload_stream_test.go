package pipeline

import (
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCopySessionToLedgerProductionStreamsLargeRaw(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.jsonl")
	file, err := os.Create(source)
	require.NoError(t, err)
	_, err = file.WriteString("complete conversation\n")
	require.NoError(t, err)
	require.NoError(t, file.Truncate(65*1024*1024))
	require.NoError(t, file.Close())
	ledger := filepath.Join(dir, "ledger")
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	require.NoError(t, CopySessionToLedger(OSFileSystem{}, &Result{RawPath: source, EntryCount: 1}, ledger, "session"))
	runtime.ReadMemStats(&after)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(4*1024*1024))
	destination := filepath.Join(ledger, "sessions", "session", "raw.jsonl")
	digest := func(path string) []byte {
		file, err := os.Open(path)
		require.NoError(t, err)
		defer file.Close()
		hash := sha256.New()
		_, err = io.Copy(hash, file)
		require.NoError(t, err)
		return hash.Sum(nil)
	}
	require.Equal(t, digest(source), digest(destination))
	info, err := os.Stat(destination)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0644), info.Mode().Perm())
	t.Logf("manual CLI 65MiB copy total allocated bytes=%d", after.TotalAlloc-before.TotalAlloc)
}
