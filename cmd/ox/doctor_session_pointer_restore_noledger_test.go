package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
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
