package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blevesearch/bleve/v2"
	"github.com/stretchr/testify/require"
)

// TestMappingVersion_FreshIndexWritesMarker verifies that openOrCreateBleveIndex
// stamps the current mapping version on a brand-new sub-index so future opens
// don't mistake it for a pre-marker (v1) index and trigger a spurious rebuild.
func TestMappingVersion_FreshIndexWritesMarker(t *testing.T) {
	root := t.TempDir()
	codePath := filepath.Join(root, "bleve", "code")

	idx, err := openOrCreateBleveIndex(root, codePath, "code")
	require.NoError(t, err)
	require.NoError(t, idx.Close())

	gotVer, verErr := readMappingVersion(codePath)
	require.NoError(t, verErr)
	require.Equal(t, bleveMappingVersion("code"), gotVer,
		"fresh index should carry the current mapping version")
	require.False(t, HasNeedsReindexMarker(root, "code"),
		"fresh index shouldn't claim it needs a rebuild")
}

// TestMappingVersion_UpgradeFromPreMarker verifies ADR-018's upgrade path: an
// existing index with no .mapping_version marker (treated as v1) is detected on
// open, nuked + recreated with the new mapping, stamped at the current version,
// and accompanied by a .needs_reindex_<name> marker so the daemon's next pass
// does a full rebuild via the established self-heal flow.
//
// Failure prevented: stored-content segments from the old (default) mapping
// don't shrink in place — without this rebuild, ADR-018's 57% disk reduction
// wouldn't apply to existing caches.
func TestMappingVersion_UpgradeFromPreMarker(t *testing.T) {
	root := t.TempDir()
	codePath := filepath.Join(root, "bleve", "code")

	// Simulate a pre-v2 sub-index: default mapping, no version marker.
	require.NoError(t, os.MkdirAll(filepath.Dir(codePath), 0o755))
	oldIdx, err := bleve.New(codePath, bleve.NewIndexMapping())
	require.NoError(t, err)
	require.NoError(t, oldIdx.Index("blob_1", map[string]string{"content": "hello world"}))
	require.NoError(t, oldIdx.Close())

	_, statErr := os.Stat(filepath.Join(codePath, mappingVersionMarker))
	require.True(t, os.IsNotExist(statErr), "pre-condition: no version marker on simulated old index")

	// Open via the wrapper — must detect the v1 index and upgrade it.
	upIdx, err := openOrCreateBleveIndex(root, codePath, "code")
	require.NoError(t, err)
	defer func() { _ = upIdx.Close() }()

	count, err := upIdx.DocCount()
	require.NoError(t, err)
	require.Equal(t, uint64(0), count,
		"upgraded index must be empty — daemon's next pass refills it from the needs_reindex marker")
	gotVer, verErr := readMappingVersion(codePath)
	require.NoError(t, verErr)
	require.Equal(t, bleveMappingVersion("code"), gotVer,
		"upgraded index should be stamped at the current mapping version")
	require.True(t, HasNeedsReindexMarker(root, "code"),
		".needs_reindex_code must be set so the daemon refills the empty index")
}

// TestMappingVersion_SameVersionOpenPreservesData verifies a reopen at the
// current mapping version is a pure no-op: docs survive, no rebuild, no marker.
// Failure prevented: a bug that re-triggered the upgrade on every Open() would
// wipe a healthy index on every daemon restart.
func TestMappingVersion_SameVersionOpenPreservesData(t *testing.T) {
	root := t.TempDir()
	codePath := filepath.Join(root, "bleve", "code")

	idx, err := openOrCreateBleveIndex(root, codePath, "code")
	require.NoError(t, err)
	require.NoError(t, idx.Index("blob_1", map[string]string{"content": "hello"}))
	require.NoError(t, idx.Close())

	idx2, err := openOrCreateBleveIndex(root, codePath, "code")
	require.NoError(t, err)
	defer func() { _ = idx2.Close() }()

	count, err := idx2.DocCount()
	require.NoError(t, err)
	require.Equal(t, uint64(1), count,
		"reopen at current version must preserve docs — no spurious rebuild")
	require.False(t, HasNeedsReindexMarker(root, "code"),
		"no needs_reindex marker should be written for a same-version open")
}

// TestMappingVersion_AllAtV2 verifies that after ADR-018 phase 2 (OQ1
// resolution = Option D), all three sub-indexes are at the index-only mapping
// version. Bumping any of these must move the index-only invariant in sync
// across the trio: future opens of pre-v2 indexes get rebuilt via the
// .needs_reindex_<name> self-heal flow.
func TestMappingVersion_AllAtV2(t *testing.T) {
	require.Equal(t, 2, bleveMappingVersion("code"))
	require.Equal(t, 2, bleveMappingVersion("comment"))
	require.Equal(t, 2, bleveMappingVersion("diff"),
		"phase 2 flipped diff to index-only (Option D — drop diff snippet); see ADR-018")
}
