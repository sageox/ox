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

// --- C. Data version round-trip ---

// TestReadWriteDataVersion verifies round-trip persistence of version data.
// Failure prevented: migration version lost across restarts.
func TestReadWriteDataVersion(t *testing.T) {
	tmp := t.TempDir()

	// read non-existent — should return version 0
	v, err := ReadDataVersion(tmp)
	require.NoError(t, err)
	assert.Equal(t, 0, v.Version)
	assert.NotNil(t, v.Migrations)

	// write and read back
	now := time.Now().UTC().Truncate(time.Second)
	v.Version = 2
	v.Migrations["content_hash_filenames"] = now
	require.NoError(t, WriteDataVersion(tmp, v))

	got, err := ReadDataVersion(tmp)
	require.NoError(t, err)
	assert.Equal(t, 2, got.Version)
	assert.Equal(t, now.Unix(), got.Migrations["content_hash_filenames"].Unix())
}

// --- D. NeedsMigration ---

// TestNeedsMigration verifies migration detection.
// Failure prevented: migration skipped when still needed, or re-run unnecessarily.
func TestNeedsMigration(t *testing.T) {
	tmp := t.TempDir()

	// no version file — needs migration
	assert.True(t, NeedsMigration(tmp))

	// version 0 — needs migration
	v := &DataVersionFile{Version: 0, Migrations: make(map[string]time.Time)}
	require.NoError(t, WriteDataVersion(tmp, v))
	assert.True(t, NeedsMigration(tmp))

	// version current — no migration needed
	v.Version = CurrentDataVersion
	require.NoError(t, WriteDataVersion(tmp, v))
	assert.False(t, NeedsMigration(tmp))
}

// --- E. MarkMigration ---

// TestMarkMigration_BumpsVersionWhenComplete verifies version bumps after all migrations.
// Failure prevented: version stays at 0 even after all migrations complete.
func TestMarkMigration_BumpsVersionWhenComplete(t *testing.T) {
	tmp := t.TempDir()

	require.NoError(t, MarkMigration(tmp, MigrationContentHashFilenames))
	v, _ := ReadDataVersion(tmp)
	assert.Equal(t, 0, v.Version, "version should not bump with partial migrations")

	require.NoError(t, MarkMigration(tmp, MigrationUUID7FactFilenames))
	v, _ = ReadDataVersion(tmp)
	assert.Equal(t, 0, v.Version, "version should not bump with partial migrations")

	require.NoError(t, MarkMigration(tmp, MigrationDailySummaryRefs))
	v, _ = ReadDataVersion(tmp)
	assert.Equal(t, CurrentDataVersion, v.Version, "version should bump when all migrations done")
}

// --- F. Empty ledger ---

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
