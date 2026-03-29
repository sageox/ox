package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/codedb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckCodeIndexAtDir_EmptyIndex is a regression test for the case where the index
// directory and schema exist but no commits were ever written — the result of an
// indexing run that was interrupted after DB creation but before any data was stored.
// ox doctor must report this as a failure, not "healthy".
func TestCheckCodeIndexAtDir_EmptyIndex(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	// Create the index with schema only — no commits written.
	// This reproduces the exact state that caused the false "healthy" report.
	db, err := codedb.Open(tmp)
	require.NoError(t, err)
	db.Close()

	result := checkCodeIndexAtDir(tmp, false)

	assert.False(t, result.passed, "index dir exists with 0 commits must report failed, not healthy")
	assert.False(t, result.skipped, "should not be skipped")
	assert.NotEmpty(t, result.detail, "must include fix instructions for empty index")
}

// TestCheckCodeIndexAtDir_EmptyIndex_Fix verifies that --fix removes the empty index.
func TestCheckCodeIndexAtDir_EmptyIndex_Fix(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	indexDir := filepath.Join(tmp, "codedb")

	db, err := codedb.Open(indexDir)
	require.NoError(t, err)
	db.Close()

	result := checkCodeIndexAtDir(indexDir, true)

	assert.True(t, result.passed, "--fix on empty index should pass after removal")

	_, statErr := os.Stat(indexDir)
	assert.True(t, os.IsNotExist(statErr), "index dir must be removed by --fix")
}

// TestCheckCodeIndexAtDir_NoIndex verifies that a missing index is a pass,
// not an error (user simply hasn't run ox code index yet).
func TestCheckCodeIndexAtDir_NoIndex(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	nonExistent := filepath.Join(tmp, "codedb-does-not-exist")

	result := checkCodeIndexAtDir(nonExistent, false)

	assert.True(t, result.passed, "missing index dir should be a pass, not failure")
}
