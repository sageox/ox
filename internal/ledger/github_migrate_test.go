package ledger

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Conflict marker repair ---

// TestRepairConflictMarkerFiles verifies corrupted files are deleted.
// Failure prevented: corrupted JSON files cause unmarshal errors during sync.
func TestRepairConflictMarkerFiles(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	// create a corrupted PR file
	dir := DateDir(tmp, now, "pr")
	require.NoError(t, os.MkdirAll(dir, 0755))
	corrupted := `<<<<<<< HEAD
{"number":409}
=======
{"number":409,"title":"new"}
>>>>>>> branch`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "409.json"), []byte(corrupted), 0644))

	// create a valid PR file
	valid := &PRFile{
		Number: 42, Title: "Valid", State: "open", Author: "alice",
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, WriteGitHubPR(tmp, valid))

	count, err := RepairConflictMarkerFiles(tmp, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// corrupted file should be gone
	_, err = os.Stat(filepath.Join(dir, "409.json"))
	assert.True(t, os.IsNotExist(err))

	// valid file should still exist
	files, err := ListGitHubDataFiles(tmp, "pr")
	require.NoError(t, err)
	assert.Len(t, files, 1)
}

// TestRepairConflictMarkerFiles_NoCorruption verifies no files deleted when clean.
func TestRepairConflictMarkerFiles_NoCorruption(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	pr := &PRFile{
		Number: 42, Title: "Clean", State: "open", Author: "alice",
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, WriteGitHubPR(tmp, pr))

	count, err := RepairConflictMarkerFiles(tmp, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

// --- B. Legacy file migration ---

// TestMigrateLegacyGitHubFiles verifies legacy files are renamed to content-hash format.
// Failure prevented: legacy files cause duplicate writes and sync state confusion.
func TestMigrateLegacyGitHubFiles(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	dir := DateDir(tmp, now, "pr")
	require.NoError(t, os.MkdirAll(dir, 0755))

	// create a legacy file
	pr := PRFile{
		Number: 42, Title: "Legacy", State: "open", Author: "alice",
		CreatedAt: now, UpdatedAt: now,
	}
	data, _ := json.MarshalIndent(&pr, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "42.json"), data, 0644))

	migrated, deleted, err := MigrateLegacyGitHubFiles(tmp, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, 1, migrated)
	assert.Equal(t, 0, deleted)

	// legacy file should be gone
	_, err = os.Stat(filepath.Join(dir, "42.json"))
	assert.True(t, os.IsNotExist(err))

	// hash-named file should exist
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Regexp(t, `^42-[0-9a-f]{8}\.json$`, entries[0].Name())

	// content should be preserved
	newData, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	require.NoError(t, err)
	assert.Equal(t, data, newData)
}

// TestMigrateLegacyGitHubFiles_SkipsHashNamed verifies hash-named files are not touched.
// Failure prevented: already-migrated files get double-processed.
func TestMigrateLegacyGitHubFiles_SkipsHashNamed(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	// write a file via the normal path (produces hash-named file)
	pr := &PRFile{
		Number: 42, Title: "Already Hashed", State: "open", Author: "alice",
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, WriteGitHubPR(tmp, pr))

	migrated, deleted, err := MigrateLegacyGitHubFiles(tmp, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, 0, migrated)
	assert.Equal(t, 0, deleted)
}

// TestMigrateLegacyGitHubFiles_DeletesCorrupted verifies conflict marker files are deleted.
// Failure prevented: corrupted files survive migration and cause ongoing parse errors.
func TestMigrateLegacyGitHubFiles_DeletesCorrupted(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	dir := DateDir(tmp, now, "issue")
	require.NoError(t, os.MkdirAll(dir, 0755))

	corrupted := `<<<<<<< HEAD
{"number":99}
=======
{"number":99,"title":"conflict"}
>>>>>>> branch`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "99.json"), []byte(corrupted), 0644))

	migrated, deleted, err := MigrateLegacyGitHubFiles(tmp, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, 0, migrated)
	assert.Equal(t, 1, deleted)

	_, err = os.Stat(filepath.Join(dir, "99.json"))
	assert.True(t, os.IsNotExist(err))
}

// TestMigrateLegacyGitHubFiles_DeletesCorruptedHashNamed verifies that hash-named
// files with conflict markers are detected and deleted.
// Failure prevented: corrupted hash-named files skip the conflict check and persist.
func TestMigrateLegacyGitHubFiles_DeletesCorruptedHashNamed(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	dir := DateDir(tmp, now, "pr")
	require.NoError(t, os.MkdirAll(dir, 0755))

	corrupted := `<<<<<<< HEAD
{"number":99}
=======
{"number":99,"title":"conflict"}
>>>>>>> branch`
	// Hash-named file (already migrated format) but with conflict markers
	require.NoError(t, os.WriteFile(filepath.Join(dir, "99-deadbeef.json"), []byte(corrupted), 0644))

	migrated, deleted, err := MigrateLegacyGitHubFiles(tmp, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, 0, migrated)
	assert.Equal(t, 1, deleted)

	// corrupted hash-named file should be gone
	_, err = os.Stat(filepath.Join(dir, "99-deadbeef.json"))
	assert.True(t, os.IsNotExist(err))
}

// TestMigrateLegacyGitHubFiles_Idempotent verifies running migration twice is safe.
// Failure prevented: migration crashes on second run.
func TestMigrateLegacyGitHubFiles_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	dir := DateDir(tmp, now, "pr")
	require.NoError(t, os.MkdirAll(dir, 0755))

	pr := PRFile{
		Number: 42, Title: "Legacy", State: "open", Author: "alice",
		CreatedAt: now, UpdatedAt: now,
	}
	data, _ := json.MarshalIndent(&pr, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "42.json"), data, 0644))

	// first run
	m1, d1, err := MigrateLegacyGitHubFiles(tmp, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, 1, m1)
	assert.Equal(t, 0, d1)

	// second run — nothing to do
	m2, d2, err := MigrateLegacyGitHubFiles(tmp, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, 0, m2)
	assert.Equal(t, 0, d2)
}

// --- C. Empty ledger ---

// TestMigrateLegacyGitHubFiles_EmptyLedger verifies migration on empty ledger is a no-op.
// Failure prevented: migration crashes on fresh/empty ledger.
func TestMigrateLegacyGitHubFiles_EmptyLedger(t *testing.T) {
	tmp := t.TempDir()

	migrated, deleted, err := MigrateLegacyGitHubFiles(tmp, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, 0, migrated)
	assert.Equal(t, 0, deleted)
}

// TestRepairConflictMarkerFiles_EmptyLedger verifies repair on empty ledger is a no-op.
func TestRepairConflictMarkerFiles_EmptyLedger(t *testing.T) {
	tmp := t.TempDir()

	count, err := RepairConflictMarkerFiles(tmp, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

// --- G. ScanLegacyGitHubFiles ---

// TestScanLegacyGitHubFiles verifies scan reports legacy and corrupted files without modifying them.
// Failure prevented: doctor --no-fix can't tell users which files need migration.
func TestScanLegacyGitHubFiles(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	dir := DateDir(tmp, now, "pr")
	require.NoError(t, os.MkdirAll(dir, 0755))

	// legacy file
	pr := PRFile{
		Number: 42, Title: "Legacy", State: "open", Author: "alice",
		CreatedAt: now, UpdatedAt: now,
	}
	data, _ := json.MarshalIndent(&pr, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "42.json"), data, 0644))

	// corrupted file
	corrupted := `<<<<<<< HEAD
{"number":99}
=======
{"number":99,"title":"conflict"}
>>>>>>> branch`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "99.json"), []byte(corrupted), 0644))

	// hash-named file (should be skipped)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "1-deadbeef.json"), data, 0644))

	legacy, corrupt, err := ScanLegacyGitHubFiles(tmp)
	require.NoError(t, err)
	assert.Len(t, legacy, 1, "should find 1 legacy file")
	assert.Len(t, corrupt, 1, "should find 1 corrupted file")

	// Verify files were NOT modified
	_, err = os.Stat(filepath.Join(dir, "42.json"))
	assert.NoError(t, err, "legacy file should still exist")
	_, err = os.Stat(filepath.Join(dir, "99.json"))
	assert.NoError(t, err, "corrupted file should still exist")
}

// TestScanLegacyGitHubFiles_EmptyLedger verifies scan on empty ledger returns nothing.
func TestScanLegacyGitHubFiles_EmptyLedger(t *testing.T) {
	tmp := t.TempDir()

	legacy, corrupt, err := ScanLegacyGitHubFiles(tmp)
	require.NoError(t, err)
	assert.Empty(t, legacy)
	assert.Empty(t, corrupt)
}

// TestScanLegacyGitHubFiles_RelativePaths verifies paths are relative to ledger root.
func TestScanLegacyGitHubFiles_RelativePaths(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	dir := DateDir(tmp, now, "pr")
	require.NoError(t, os.MkdirAll(dir, 0755))

	pr := PRFile{
		Number: 42, Title: "Legacy", State: "open", Author: "alice",
		CreatedAt: now, UpdatedAt: now,
	}
	data, _ := json.MarshalIndent(&pr, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "42.json"), data, 0644))

	legacy, _, err := ScanLegacyGitHubFiles(tmp)
	require.NoError(t, err)
	require.Len(t, legacy, 1)
	assert.Equal(t, filepath.Join("data", "github", "2026", "04", "01", "pr", "42.json"), legacy[0])
}
