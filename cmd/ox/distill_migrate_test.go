package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Legacy fact file migration ---

// TestMigrateLegacyFactFiles verifies legacy fact files are renamed with UUID7.
// Failure prevented: legacy files cause duplicate extraction and stale dedup lookups.
func TestMigrateLegacyFactFiles(t *testing.T) {
	tcPath := t.TempDir()
	factsDir := filepath.Join(tcPath, "memory", ".github-facts")
	require.NoError(t, os.MkdirAll(factsDir, 0755))

	// create legacy files
	require.NoError(t, os.WriteFile(
		filepath.Join(factsDir, "2026-03-30-pr-42.jsonl"),
		[]byte(`{"_meta":{"source_hash":"abc"}}`), 0644))
	require.NoError(t, os.WriteFile(
		filepath.Join(factsDir, "2026-03-30-issue-99.jsonl"),
		[]byte(`{"_meta":{"source_hash":"def"}}`), 0644))
	require.NoError(t, os.WriteFile(
		filepath.Join(factsDir, "2026-03-30-commits.jsonl"),
		[]byte(`{"_meta":{"source_hash":"ghi"}}`), 0644))

	count, err := MigrateLegacyFactFiles(tcPath, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, 3, count)

	// verify legacy files are gone
	entries, err := os.ReadDir(factsDir)
	require.NoError(t, err)
	assert.Len(t, entries, 3)

	for _, e := range entries {
		name := e.Name()
		// all files should now have UUID7 format
		assert.Regexp(t, `^\d{4}-\d{2}-\d{2}-[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[0-9a-f]{4}-[0-9a-f]{12}-(pr-\d+|issue-\d+|commits)\.jsonl$`, name,
			"file should match UUID7 pattern: %s", name)

		// content should be preserved
		data, err := os.ReadFile(filepath.Join(factsDir, name))
		require.NoError(t, err)
		assert.Contains(t, string(data), `"source_hash"`)
	}
}

// TestMigrateLegacyFactFiles_SkipsUUID7Named verifies already-migrated files are skipped.
// Failure prevented: double-migration corrupts filenames.
func TestMigrateLegacyFactFiles_SkipsUUID7Named(t *testing.T) {
	tcPath := t.TempDir()
	factsDir := filepath.Join(tcPath, "memory", ".github-facts")
	require.NoError(t, os.MkdirAll(factsDir, 0755))

	// create a file that already has UUID7 naming
	uuid7Name := "2026-03-30-01961234-5678-7abc-def0-123456789abc-pr-42.jsonl"
	require.NoError(t, os.WriteFile(
		filepath.Join(factsDir, uuid7Name),
		[]byte(`{"_meta":{"source_hash":"abc"}}`), 0644))

	count, err := MigrateLegacyFactFiles(tcPath, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	// file should still exist with original name
	_, err = os.Stat(filepath.Join(factsDir, uuid7Name))
	assert.NoError(t, err)
}

// TestMigrateLegacyFactFiles_EmptyDir verifies migration on empty dir is a no-op.
func TestMigrateLegacyFactFiles_EmptyDir(t *testing.T) {
	tcPath := t.TempDir()
	count, err := MigrateLegacyFactFiles(tcPath, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

// TestMigrateLegacyFactFiles_Idempotent verifies running twice is safe.
func TestMigrateLegacyFactFiles_Idempotent(t *testing.T) {
	tcPath := t.TempDir()
	factsDir := filepath.Join(tcPath, "memory", ".github-facts")
	require.NoError(t, os.MkdirAll(factsDir, 0755))

	require.NoError(t, os.WriteFile(
		filepath.Join(factsDir, "2026-03-30-pr-42.jsonl"),
		[]byte(`{"_meta":{"source_hash":"abc"}}`), 0644))

	c1, err := MigrateLegacyFactFiles(tcPath, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, 1, c1)

	c2, err := MigrateLegacyFactFiles(tcPath, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, 0, c2)
}

// --- B. Daily summary reference updates ---

// TestUpdateDailySummaryRefs verifies old source references are updated.
// Failure prevented: daily summaries reference non-existent legacy filenames after migration.
func TestUpdateDailySummaryRefs(t *testing.T) {
	tcPath := t.TempDir()
	factsDir := filepath.Join(tcPath, "memory", ".github-facts")
	dailyDir := filepath.Join(tcPath, "memory", "daily")
	require.NoError(t, os.MkdirAll(factsDir, 0755))
	require.NoError(t, os.MkdirAll(dailyDir, 0755))

	// create a UUID7-named fact file (simulating post-migration state)
	uuid7Name := "2026-03-30-01961234-5678-7abc-def0-123456789abc-pr-42.jsonl"
	require.NoError(t, os.WriteFile(
		filepath.Join(factsDir, uuid7Name),
		[]byte(`{"_meta":{"source_hash":"abc"}}`), 0644))

	// create a daily summary referencing the old name
	dailyContent := `---
sources:
  - memory/.github-facts/2026-03-30-pr-42.jsonl
  - memory/.session-facts/2026-03-30/session1.jsonl
---
# Daily Memory — 2026-03-30

Some content here.
`
	require.NoError(t, os.WriteFile(
		filepath.Join(dailyDir, "2026-03-30.md"),
		[]byte(dailyContent), 0644))

	count, err := UpdateDailySummaryRefs(tcPath, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// verify the reference was updated
	data, err := os.ReadFile(filepath.Join(dailyDir, "2026-03-30.md"))
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, uuid7Name, "should contain new UUID7 filename")
	assert.NotContains(t, content, "2026-03-30-pr-42.jsonl", "should not contain legacy filename")
	// session-facts reference should be unchanged
	assert.Contains(t, content, "memory/.session-facts/2026-03-30/session1.jsonl")
}

// TestUpdateDailySummaryRefs_NoChanges verifies no-op when refs are already current.
func TestUpdateDailySummaryRefs_NoChanges(t *testing.T) {
	tcPath := t.TempDir()
	dailyDir := filepath.Join(tcPath, "memory", "daily")
	require.NoError(t, os.MkdirAll(dailyDir, 0755))

	// daily with already-UUID7 refs
	dailyContent := `---
sources:
  - memory/.github-facts/2026-03-30-01961234-5678-7abc-def0-123456789abc-pr-42.jsonl
---
# Daily Memory — 2026-03-30

Content.
`
	require.NoError(t, os.WriteFile(
		filepath.Join(dailyDir, "2026-03-30.md"),
		[]byte(dailyContent), 0644))

	count, err := UpdateDailySummaryRefs(tcPath, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

// TestUpdateDailySummaryRefs_EmptyDir verifies no-op on missing daily dir.
func TestUpdateDailySummaryRefs_EmptyDir(t *testing.T) {
	tcPath := t.TempDir()
	count, err := UpdateDailySummaryRefs(tcPath, slog.Default())
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}
