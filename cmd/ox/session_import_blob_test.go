package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImportReadBlobBoundsReceiptBeforeAllocation(t *testing.T) {
	_, repo := createBareAndClone(t)
	path := filepath.Join(repo, "large.json")
	file, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(5*1024*1024))
	require.NoError(t, file.Close())
	require.NoError(t, os.WriteFile(filepath.Join(repo, "small.json"), []byte(`{"valid":true}`), 0600))
	require.NoError(t, os.Symlink("small.json", filepath.Join(repo, "link.json")))
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "receipt fixture")
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	data, exists, err := importReadBlob(context.Background(), repo, "HEAD", "large.json")
	runtime.ReadMemStats(&after)
	require.ErrorContains(t, err, "metadata limit")
	require.True(t, exists)
	require.Nil(t, data)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(1024*1024))
	data, exists, err = importReadBlob(context.Background(), repo, "HEAD", "small.json")
	require.NoError(t, err)
	require.True(t, exists)
	require.JSONEq(t, `{"valid":true}`, string(data))
	_, exists, err = importReadBlob(context.Background(), repo, "HEAD", "missing.json")
	require.NoError(t, err)
	require.False(t, exists)
	_, exists, err = importReadBlob(context.Background(), repo, "HEAD", "link.json")
	require.ErrorContains(t, err, "regular Git blob")
	require.True(t, exists)
}
