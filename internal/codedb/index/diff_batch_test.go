package index

import (
	"testing"

	"github.com/blevesearch/bleve/v2"
	"github.com/sageox/ox/internal/codedb/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFlushDiffBatch_NonFatalOnBleveError is a regression test for the bug where
// a Bleve diff index write failure (e.g. "000000000002.zap: no such file or directory")
// caused the entire IndexLocalRepo to fail and prevented SQL commits/blobs from being
// cached, resulting in an infinite retry loop.
//
// After the fix, flushDiffBatch logs a warning on first failure and skips all further
// diff writes (diffIndexFailed=true). The function always returns nil so SQL indexing
// continues unaffected.
func TestFlushDiffBatch_NonFatalOnBleveError(t *testing.T) {
	t.Parallel()

	s, err := store.Open(t.TempDir())
	require.NoError(t, err)
	defer s.Close()

	// Build an indexState with a real store (real Bleve diff index).
	st := &indexState{
		store:     s,
		diffBatch: s.DiffIndex.NewBatch(),
		report:    func(string) {},
	}

	// Add a document to the batch so the flush actually runs.
	st.diffBatch.Index("diff_1", BleveDiffDoc{Content: "some diff content"})
	st.diffBatchN = 1

	// Normal flush should succeed and not set diffIndexFailed.
	err = st.flushDiffBatch(true)
	assert.NoError(t, err)
	assert.False(t, st.diffIndexFailed, "diffIndexFailed should not be set after a successful flush")
	assert.Equal(t, 0, st.diffBatchN, "batch counter should reset after successful flush")
}

// TestFlushDiffBatch_SkipsAfterFailure verifies that once diffIndexFailed is set
// (simulating a prior Bleve write error), subsequent calls are no-ops and return nil.
// This prevents cascading errors from a single bad Bleve segment.
func TestFlushDiffBatch_SkipsAfterFailure(t *testing.T) {
	t.Parallel()

	s, err := store.Open(t.TempDir())
	require.NoError(t, err)
	defer s.Close()

	st := &indexState{
		store:           s,
		diffBatch:       s.DiffIndex.NewBatch(),
		diffIndexFailed: true, // simulate prior failure
		report:          func(string) {},
	}

	// Add a document — should be silently skipped.
	st.diffBatch.Index("diff_1", BleveDiffDoc{Content: "content"})
	st.diffBatchN = 1

	err = st.flushDiffBatch(true)
	assert.NoError(t, err, "must return nil even when diffIndexFailed is set")
	assert.Equal(t, 1, st.diffBatchN, "batch counter should be unchanged when skipped")
}

// TestFlushDiffBatch_SQLUnaffectedByDiffFailure verifies that when the diff index
// is in a failed state, the SQL store is still writable and queryable. This is the
// core invariant: diff Bleve failure must not corrupt or block the SQL index.
func TestFlushDiffBatch_SQLUnaffectedByDiffFailure(t *testing.T) {
	t.Parallel()

	s, err := store.Open(t.TempDir())
	require.NoError(t, err)
	defer s.Close()

	// Write to SQL directly to confirm the store is healthy.
	_, err = s.Exec("INSERT INTO repos (name, path) VALUES (?, ?)", "test-repo", "/tmp/test")
	require.NoError(t, err)

	st := &indexState{
		store:           s,
		diffBatch:       s.DiffIndex.NewBatch(),
		diffIndexFailed: true,
		report:          func(string) {},
	}

	// Diff flush is a no-op.
	st.diffBatch.Index("diff_1", BleveDiffDoc{Content: "content"})
	st.diffBatchN = 1
	require.NoError(t, st.flushDiffBatch(true))

	// SQL store is still intact.
	var count int
	err = s.QueryRow("SELECT COUNT(*) FROM repos").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "SQL store must be unaffected by diff index failure")
}

// TestFlushDiffBatch_NoBatch is a guard for the zero-items case.
func TestFlushDiffBatch_NoBatch(t *testing.T) {
	t.Parallel()

	s, err := store.Open(t.TempDir())
	require.NoError(t, err)
	defer s.Close()

	st := &indexState{
		store:     s,
		diffBatch: s.DiffIndex.NewBatch(),
		report:    func(string) {},
	}

	// Empty batch should always be a no-op.
	err = st.flushDiffBatch(true)
	assert.NoError(t, err)

	// Closed index — simulate a broken DiffIndex by closing the store.
	// After close, Batch() will error. diffIndexFailed should be set.
	s.Close()
	st.diffBatch = &bleve.Batch{}
	st.diffBatchN = 1
	err = st.flushDiffBatch(true)
	assert.NoError(t, err, "closed index must be non-fatal")
	assert.True(t, st.diffIndexFailed, "diffIndexFailed must be set after Batch() on closed index")
}
