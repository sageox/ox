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
