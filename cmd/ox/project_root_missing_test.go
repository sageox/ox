package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Every project-scoped writer opens the checkout as an os.Root first. A
// checkout that is gone must fail (or report nothing to do) without creating
// it, or anything under it, through a pathname.
func TestProjectInstructionWriters_MissingCheckoutCreatesNothing(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "removed-checkout")

	_, _, err := readProjectSettingsFileRaw(gone, projectClaudeSettingsFile)
	assert.Error(t, err, "readProjectSettingsFileRaw")
	assert.Error(t, InstallProjectClaudeHooks(gone), "InstallProjectClaudeHooks")
	assert.Error(t, cleanupLocalSettingsOxHooks(gone), "cleanupLocalSettingsOxHooks")
	_, err = injectOxPrime(gone)
	assert.Error(t, err, "injectOxPrime")
	assert.False(t, hasPrimeMarker(gone, "ox:prime"), "hasPrimeMarker")
	changed, err := EnsureOxPrimeMarker(gone)
	assert.Error(t, err, "EnsureOxPrimeMarker")
	assert.False(t, changed)
	injected, err := ensureMarkersInFile(filepath.Join(gone, "AGENTS.md"), "", true, true)
	assert.Error(t, err, "ensureMarkersInFile")
	assert.False(t, injected)
	changed, err = syncProjectStatusLine(gone)
	assert.Error(t, err, "syncProjectStatusLine")
	assert.False(t, changed)

	_, statErr := os.Lstat(gone)
	assert.True(t, os.IsNotExist(statErr), "a missing checkout must not be recreated")
}
