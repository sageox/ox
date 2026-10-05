package autofix

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: an endpoint-wide sweep mistakes blue-green backups or GC
// staging directories for live Ledgers and mutates recovery evidence.
func TestDiscoverLedgerPaths_OnlyReturnsCanonicalGitCheckouts(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	const endpointURL = "https://example.invalid"
	root := paths.LedgersDataDir("", endpointURL)
	require.NoError(t, os.MkdirAll(root, 0o755))

	for _, name := range []string{"repo_b", "repo_a"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, name, ".git"), 0o755))
	}
	for _, name := range []string{
		"repo_a.bak.123", "repo_a.gc-cache", "repo_a.gc-diff",
		"repo_a.gc-untracked", "repo_partial", "not-a-ledger",
	} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, name), 0o755))
	}

	got, err := DiscoverLedgerPaths(endpointURL)

	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(root, "repo_a"), filepath.Join(root, "repo_b")}, got)
}

func TestDiscoverLedgerPaths_MissingStoreIsEmpty(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	got, err := DiscoverLedgerPaths("https://example.invalid")
	require.NoError(t, err)
	assert.Empty(t, got)
}
