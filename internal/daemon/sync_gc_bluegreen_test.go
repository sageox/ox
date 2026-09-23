package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --------------------------------------------------------------------------
// gcPreserveCache + gcRestoreCache — defense-in-depth for codedb survival
// --------------------------------------------------------------------------

func TestGCPreserveRestore_CacheIntegrity(t *testing.T) {
	// real-world failure prevented: codedb indexes (~50MB) are expensive to rebuild;
	// if GC reclone loses them, users wait 30-60s for re-index on next query
	t.Parallel()

	srcRepo := t.TempDir()
	backupDir := filepath.Join(t.TempDir(), "backup")
	dstRepo := t.TempDir()

	// create cache with nested structure matching real codedb layout
	codedbDir := filepath.Join(srcRepo, ".sageox", "cache", "codedb")
	require.NoError(t, os.MkdirAll(codedbDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(codedbDir, "metadata.db"), []byte("sqlite-data"), 0o644))

	bleveDir := filepath.Join(codedbDir, "bleve", "code")
	require.NoError(t, os.MkdirAll(bleveDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bleveDir, "store"), []byte("bleve-store"), 0o644))

	// preserve
	require.NoError(t, gcPreserveCache(srcRepo, backupDir))

	// simulate GC destroying old repo
	require.NoError(t, os.RemoveAll(srcRepo))

	// restore to new location
	require.NoError(t, gcRestoreCache(backupDir, dstRepo))

	// verify all files survived with correct content
	got, err := os.ReadFile(filepath.Join(dstRepo, ".sageox", "cache", "codedb", "metadata.db"))
	require.NoError(t, err)
	assert.Equal(t, "sqlite-data", string(got), "metadata.db content must be byte-identical")

	got, err = os.ReadFile(filepath.Join(dstRepo, ".sageox", "cache", "codedb", "bleve", "code", "store"))
	require.NoError(t, err)
	assert.Equal(t, "bleve-store", string(got), "bleve store content must be byte-identical")
}

func TestGCPreserveCache_NoCacheDir_Noop(t *testing.T) {
	// real-world failure prevented: fresh clone has no cache yet — preserve
	// must succeed silently so GC proceeds
	t.Parallel()

	srcRepo := t.TempDir()
	backupDir := filepath.Join(t.TempDir(), "backup")

	err := gcPreserveCache(srcRepo, backupDir)
	assert.NoError(t, err)

	// backup dir should NOT be created when there's nothing to preserve
	_, statErr := os.Stat(backupDir)
	assert.True(t, os.IsNotExist(statErr), "backup dir must not exist when no cache to preserve")
}

func TestGCRestoreCache_NoBackup_Noop(t *testing.T) {
	// real-world failure prevented: if preservation was a noop (no cache),
	// restore must also be a noop — not fail
	t.Parallel()

	dstRepo := t.TempDir()
	err := gcRestoreCache("/nonexistent/backup", dstRepo)
	assert.NoError(t, err)
}

// --------------------------------------------------------------------------
// validateLedgerGCClone
// --------------------------------------------------------------------------

func TestValidateLedgerGCClone_ValidClone(t *testing.T) {
	// real-world failure prevented: if validation incorrectly rejects a good clone,
	// GC keeps the old (possibly bloated) clone and logs a misleading error
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sessions"), 0o755))

	s := gcTestScheduler(t)
	assert.True(t, s.validateLedgerGCClone(dir))
}

func TestValidateLedgerGCClone_MissingGit(t *testing.T) {
	// real-world failure prevented: if git clone partially failed (disk full, etc),
	// validation must reject it so the old clone is preserved
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sessions"), 0o755))
	// no .git directory

	s := gcTestScheduler(t)
	assert.False(t, s.validateLedgerGCClone(dir), "clone without .git must be rejected")
}

func TestValidateLedgerGCClone_MissingSessions(t *testing.T) {
	// real-world failure prevented: a clone without sessions/ would lose session
	// data — better to keep the old clone
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	// no sessions/

	s := gcTestScheduler(t)
	assert.False(t, s.validateLedgerGCClone(dir), "clone without sessions/ must be rejected")
}
