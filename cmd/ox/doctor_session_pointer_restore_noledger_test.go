package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckSessionPointerRestore_NoLedgerIsSkipped covers a directory with no Ledger: the check must skip, not fail.
func TestCheckSessionPointerRestore_NoLedgerIsSkipped(t *testing.T) {
	skipIntegration(t)
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Chdir(dir)

	result := checkSessionPointerRestore(true)

	assert.True(t, result.skipped, "%s", result.message)
}

// TestRunSessionPointerRestore_UnreadableUpstreamFails covers an upstream ref git cannot read. The check must
// say it could not inspect the commits, never report a clean Ledger it did not look at.
func TestRunSessionPointerRestore_UnreadableUpstreamFails(t *testing.T) {
	ledger := newWedgedLedger(t, true)
	ref := filepath.Join(ledger, ".git", "refs", "remotes", "origin", "main")
	require.NoError(t, os.WriteFile(ref, []byte("0123456789012345678901234567890123456789\n"), 0o644))

	result := runSessionPointerRestore(ledger, true, nil)

	assert.False(t, result.passed, "%s", result.message)
	assert.False(t, result.skipped, "%s", result.message)
}
