package store

import (
	"path/filepath"
	"testing"

	"github.com/blevesearch/bleve/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createDirtyBleveIndex creates an on-disk Bleve index with the given content,
// closes it, and returns the path. The caller can then open it via AttachDirtyIndexByID.
func createDirtyBleveIndex(t *testing.T, content string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "dirty")
	mapping := bleve.NewIndexMapping()
	idx, err := bleve.New(dir, mapping)
	require.NoError(t, err, "create bleve index")
	require.NoError(t, idx.Index("doc", map[string]interface{}{"content": content}))
	require.NoError(t, idx.Close())
	return dir
}

// --- A. Multi-overlay lifecycle ---

// TestAttachMultipleOverlays_AllSearchable verifies that content from all
// simultaneously attached overlays appears in search results.
// Failure prevented: overlays silently dropped when multiple attached.
func TestAttachMultipleOverlays_AllSearchable(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	tokens := []string{"alpha_unique_token", "bravo_unique_token", "charlie_unique_token"}
	for i, tok := range tokens {
		dir := createDirtyBleveIndex(t, tok)
		require.NoError(t, s.AttachDirtyIndexByID(tok, dir), "attach overlay %d", i)
	}

	assert.Equal(t, 3, s.DirtyOverlayCount())

	for _, tok := range tokens {
		q := bleve.NewMatchQuery(tok)
		req := bleve.NewSearchRequest(q)
		result, err := s.CombinedCodeIndex.Search(req)
		require.NoError(t, err)
		assert.Equal(t, uint64(1), result.Total, "expected to find %s in combined search", tok)
	}
}

// TestDetachOneOverlay_OthersStillWork verifies that detaching one overlay
// does not corrupt or close the remaining overlays.
// Failure prevented: detaching one overlay corrupts or closes others.
func TestDetachOneOverlay_OthersStillWork(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	ids := []string{"first", "second", "third"}
	tokens := []string{"delta_unique", "echo_unique", "foxtrot_unique"}
	for i, id := range ids {
		dir := createDirtyBleveIndex(t, tokens[i])
		require.NoError(t, s.AttachDirtyIndexByID(id, dir))
	}

	// detach the middle overlay
	s.DetachDirtyIndexByID("second")
	assert.Equal(t, 2, s.DirtyOverlayCount())

	// remaining overlays should still be searchable
	for _, tok := range []string{"delta_unique", "foxtrot_unique"} {
		q := bleve.NewMatchQuery(tok)
		req := bleve.NewSearchRequest(q)
		result, err := s.CombinedCodeIndex.Search(req)
		require.NoError(t, err)
		assert.Equal(t, uint64(1), result.Total, "expected %s still searchable", tok)
	}

	// detached overlay content should be gone
	q := bleve.NewMatchQuery("echo_unique")
	req := bleve.NewSearchRequest(q)
	result, err := s.CombinedCodeIndex.Search(req)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), result.Total, "detached overlay content should not appear")
}

// TestDetachAll_BaselineOnlyResults verifies that DetachDirtyOverlay clears
// all overlays and only baseline results remain.
// Failure prevented: stale overlay references after full detach.
func TestDetachAll_BaselineOnlyResults(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	// index something in the baseline code index
	require.NoError(t, s.CodeIndex.Index("baseline", map[string]interface{}{"content": "baseline_content"}))

	// attach overlays with distinct content
	for _, id := range []string{"wt1", "wt2"} {
		dir := createDirtyBleveIndex(t, "overlay_"+id)
		require.NoError(t, s.AttachDirtyIndexByID(id, dir))
	}

	// detach all
	s.DetachDirtyOverlay()
	assert.Equal(t, 0, s.DirtyOverlayCount())

	// baseline should still be searchable
	q := bleve.NewMatchQuery("baseline_content")
	req := bleve.NewSearchRequest(q)
	result, err := s.CombinedCodeIndex.Search(req)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), result.Total)

	// overlay content should be gone
	q = bleve.NewMatchQuery("overlay_wt1")
	req = bleve.NewSearchRequest(q)
	result, err = s.CombinedCodeIndex.Search(req)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), result.Total)
}

// TestDirtyOverlayCount_TracksAttachDetach verifies the count tracks map state correctly.
// Failure prevented: map bookkeeping errors.
func TestDirtyOverlayCount_TracksAttachDetach(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	assert.Equal(t, 0, s.DirtyOverlayCount())

	for i, id := range []string{"a", "b", "c"} {
		dir := createDirtyBleveIndex(t, "count_"+id)
		require.NoError(t, s.AttachDirtyIndexByID(id, dir))
		assert.Equal(t, i+1, s.DirtyOverlayCount())
	}

	s.DetachDirtyIndexByID("b")
	assert.Equal(t, 2, s.DirtyOverlayCount())

	s.DetachDirtyOverlay()
	assert.Equal(t, 0, s.DirtyOverlayCount())
}

// TestReattachSameID_ReplacesExisting verifies that attaching a new overlay
// with the same ID replaces the previous one without leaking index handles.
// Failure prevented: duplicate map entries or leaked index handles.
func TestReattachSameID_ReplacesExisting(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	dir1 := createDirtyBleveIndex(t, "original_version")
	require.NoError(t, s.AttachDirtyIndexByID("x", dir1))

	dir2 := createDirtyBleveIndex(t, "replacement_version")
	require.NoError(t, s.AttachDirtyIndexByID("x", dir2))

	assert.Equal(t, 1, s.DirtyOverlayCount(), "should still be 1 overlay after re-attach")

	// only the replacement content should be searchable
	q := bleve.NewMatchQuery("original_version")
	req := bleve.NewSearchRequest(q)
	result, err := s.CombinedCodeIndex.Search(req)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), result.Total, "old overlay content should be gone")

	q = bleve.NewMatchQuery("replacement_version")
	req = bleve.NewSearchRequest(q)
	result, err = s.CombinedCodeIndex.Search(req)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), result.Total, "new overlay content should be present")
}

// --- B. Backwards compatibility ---

// TestLegacyAttachDirtyIndex_StillWorks verifies the old single-overlay API
// still functions correctly using the default key.
// Failure prevented: backwards-incompatible API change.
func TestLegacyAttachDirtyIndex_StillWorks(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	dir := createDirtyBleveIndex(t, "legacy_content")
	require.NoError(t, s.AttachDirtyIndex(dir))
	assert.Equal(t, 1, s.DirtyOverlayCount())

	q := bleve.NewMatchQuery("legacy_content")
	req := bleve.NewSearchRequest(q)
	result, err := s.CombinedCodeIndex.Search(req)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), result.Total)
}

// TestLegacyAttachDirtyOverlay_InMemory_StillWorks verifies the in-memory test
// overlay API still works as before.
// Failure prevented: test-only overlay API regression.
func TestLegacyAttachDirtyOverlay_InMemory_StillWorks(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}
	s := openStore(t)

	require.NoError(t, s.AttachDirtyOverlay())
	assert.Equal(t, 1, s.DirtyOverlayCount())

	// index directly into the dirty overlay
	dirtyIdx := s.DirtyCodeIndex()
	require.NotNil(t, dirtyIdx)
	require.NoError(t, dirtyIdx.Index("mem-doc", map[string]interface{}{"content": "inmemory_data"}))

	q := bleve.NewMatchQuery("inmemory_data")
	req := bleve.NewSearchRequest(q)
	result, err := s.CombinedCodeIndex.Search(req)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), result.Total)

	// detach should clean up
	s.DetachDirtyOverlay()
	assert.Equal(t, 0, s.DirtyOverlayCount())
	assert.Equal(t, s.CodeIndex, s.CombinedCodeIndex)
}
