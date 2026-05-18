package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
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

// TestCheckCodeIndexAtDir_CorruptMapping_TargetedRebuild verifies that an
// empty bleve mapping doc (single sub-index) is repaired surgically: only the
// affected sub-index dir is recreated, while peer sub-indexes survive byte-for-byte.
//
// Under the current recovery flow:
//  1. store.Open SELF-HEALS the corrupt mapping on first read (nuke +
//     recreate + writes .needs_reindex_comment marker).
//  2. Without --fix, doctor surfaces the marker as a Warning ("auto-repair
//     in progress, daemon will rebuild on next pass") — not a failure.
//  3. With --fix, doctor calls RebuildBleveSubIndex synchronously for the
//     comment marker (the SQL flag reset path), preserving code/diff
//     byte-for-byte.
//
// Failure prevented: doctor's pre-self-heal remedy was os.RemoveAll(dataDir),
// destroying ~600MB of code data + ~600MB of diff data to recover from a
// few-byte mapping corruption that only affected the comment sub-index.
func TestCheckCodeIndexAtDir_CorruptMapping_TargetedRebuild(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}

	tmp := t.TempDir()
	db, err := codedb.Open(tmp)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	// truncate the comment sub-index's persisted mapping doc
	boltPath := filepath.Join(tmp, "bleve", "comment", "store", "root.bolt")
	corruptCommentMapping(t, boltPath)

	// without --fix: must surface the auto-repair state — must NOT report
	// "healthy" and must NOT silently pass. WarningCheck reports passed=true
	// + warning=true so the doctor exit code stays clean while the message
	// makes the in-flight repair visible.
	result := checkCodeIndexAtDir(tmp, false)
	assert.True(t, result.warning, "corrupt mapping must surface as a warning (auto-repair in progress); got message=%q", result.message)
	assert.Contains(t, result.message, "comment",
		"warning must name the affected sub-index")

	// snapshot code/diff state BEFORE fix (Open's self-heal above touched
	// only the comment sub-index; this proves the --fix path also stays
	// surgical).
	codeBoltPath := filepath.Join(tmp, "bleve", "code", "store", "root.bolt")
	diffBoltPath := filepath.Join(tmp, "bleve", "diff", "store", "root.bolt")
	codeBefore := mustStatSize(t, codeBoltPath)
	diffBefore := mustStatSize(t, diffBoltPath)

	// with --fix: surgically rebuild via RebuildBleveSubIndex
	result = checkCodeIndexAtDir(tmp, true)
	assert.True(t, result.passed, "fix must succeed: message=%q detail=%q", result.message, result.detail)

	// peer sub-indexes must survive (size unchanged is sufficient — a full
	// nuke would have removed the file entirely)
	codeAfter := mustStatSize(t, codeBoltPath)
	diffAfter := mustStatSize(t, diffBoltPath)
	assert.Equal(t, codeBefore, codeAfter, "code sub-index must not be touched by targeted rebuild")
	assert.Equal(t, diffBefore, diffAfter, "diff sub-index must not be touched by targeted rebuild")

	// marker must be cleared after surgical rebuild
	assert.False(t, mustHasMarker(t, tmp, "comment"),
		"comment marker must be cleared after surgical --fix rebuild")

	// post-rebuild Open must succeed
	db2, err := codedb.Open(tmp)
	require.NoError(t, err, "Open must succeed after targeted rebuild")
	require.NoError(t, db2.Close())
}

// corruptCommentMapping zeroes the latest snapshot's mapping doc in the
// comment sub-index — same on-disk state observed in production.
func corruptCommentMapping(t *testing.T, boltPath string) {
	t.Helper()
	db, err := bbolt.Open(boltPath, 0600, &bbolt.Options{Timeout: 2 * time.Second})
	require.NoError(t, err)
	require.NoError(t, db.Update(func(tx *bbolt.Tx) error {
		snaps := tx.Bucket([]byte{'s'})
		require.NotNil(t, snaps)
		var lastKey []byte
		require.NoError(t, snaps.ForEach(func(k, _ []byte) error {
			lastKey = append(lastKey[:0], k...)
			return nil
		}))
		snap := snaps.Bucket(lastKey)
		require.NotNil(t, snap)
		internal := snap.Bucket([]byte{'i'})
		require.NotNil(t, internal)
		return internal.Put([]byte("_mapping"), []byte{})
	}))
	require.NoError(t, db.Close())
}

func mustStatSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info.Size()
}

// mustHasMarker reports whether a needs_reindex marker is currently present
// for the named sub-index under dataDir.
func mustHasMarker(t *testing.T, dataDir, name string) bool {
	t.Helper()
	return store.HasNeedsReindexMarker(dataDir, name)
}
