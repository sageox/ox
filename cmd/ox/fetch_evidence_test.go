package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Size-only cache hits let a poisoned file survive every subsequent fetch.
func TestFetchEvidenceCacheVerifiesBytes(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "frame.jpg")
	expected := []byte("correct bytes")
	sum := sha256.Sum256(expected)
	oid := hex.EncodeToString(sum[:])
	require.NoError(t, os.WriteFile(filename, expected, 0644))
	require.True(t, cachedFetchDigestMatches(filename, oid))
	require.NoError(t, os.WriteFile(filename, []byte("corrupt bytes"), 0644))
	require.True(t, isCacheHit(filename, int64(len(expected))))
	require.False(t, cachedFetchDigestMatches(filename, oid))
}

func TestFetchEvidenceCacheRefusesMissingAndSymlinkPayload(t *testing.T) {
	dir := t.TempDir()
	require.False(t, cachedFetchDigestMatches(filepath.Join(dir, "missing.jpg"), validFetchOID))
	require.False(t, cachedFetchDigestMatches(dir, validFetchOID))
	payload := filepath.Join(dir, "target.jpg")
	content := []byte("correct bytes")
	sum := sha256.Sum256(content)
	require.NoError(t, os.WriteFile(payload, content, 0644))
	link := filepath.Join(dir, "linked.jpg")
	require.NoError(t, os.Symlink(payload, link))
	require.False(t, cachedFetchDigestMatches(link, hex.EncodeToString(sum[:])))
}
