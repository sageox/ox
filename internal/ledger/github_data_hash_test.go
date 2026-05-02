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

// --- A. Content-hash idempotency ---

// TestWriteGitHubPR_Idempotent verifies writing the same PR twice produces the same file.
// Failure prevented: duplicate files created on repeated sync of unchanged data.
func TestWriteGitHubPR_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	pr := &PRFile{
		Number:    42,
		Title:     "Feature",
		Author:    "alice",
		State:     "open",
		CreatedAt: now,
		UpdatedAt: now,
	}

	require.NoError(t, WriteGitHubPR(tmp, pr))
	require.NoError(t, WriteGitHubPR(tmp, pr))

	dir := DateDir(tmp, now, "pr")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	// exactly one file — second write was skipped (idempotent)
	assert.Len(t, entries, 1)
	assert.Contains(t, entries[0].Name(), "42-")
}

// TestWriteGitHubIssue_Idempotent verifies writing the same issue twice produces the same file.
// Failure prevented: duplicate issue files from repeated sync.
func TestWriteGitHubIssue_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	issue := &IssueFile{
		Number:    77,
		Title:     "Bug",
		Author:    "bob",
		State:     "open",
		CreatedAt: now,
		UpdatedAt: now,
	}

	require.NoError(t, WriteGitHubIssue(tmp, issue))
	require.NoError(t, WriteGitHubIssue(tmp, issue))

	dir := DateDir(tmp, now, "issue")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	assert.Contains(t, entries[0].Name(), "77-")
}

// --- B. Different content produces different file ---

// TestWriteGitHubPR_DifferentContent verifies that updating a PR creates a new file.
// Failure prevented: updated PR data silently lost because hash collision collapsed two versions.
func TestWriteGitHubPR_DifferentContent(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	later := now.Add(time.Hour)

	pr1 := &PRFile{
		Number: 42, Title: "Draft", State: "open", Author: "alice",
		CreatedAt: now, UpdatedAt: now,
	}
	pr2 := &PRFile{
		Number: 42, Title: "Final", State: "merged", Author: "alice",
		CreatedAt: now, UpdatedAt: later, MergedAt: &later,
	}

	require.NoError(t, WriteGitHubPR(tmp, pr1))
	require.NoError(t, WriteGitHubPR(tmp, pr2))

	dir := DateDir(tmp, now, "pr")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 2, "different content should produce two files")
}

// --- C. ReadGitHubPR finds latest version ---

// TestReadGitHubPR_FindsLatest verifies that ReadGitHubPR returns the version
// with the latest updated_at when multiple hash-variant files exist.
// Failure prevented: stale PR data returned after an update.
func TestReadGitHubPR_FindsLatest(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	later := now.Add(time.Hour)

	// write old version
	pr1 := &PRFile{
		Number: 42, Title: "Draft", State: "open", Author: "alice",
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, WriteGitHubPR(tmp, pr1))

	// write new version
	pr2 := &PRFile{
		Number: 42, Title: "Final", State: "merged", Author: "alice",
		CreatedAt: now, UpdatedAt: later, MergedAt: &later,
	}
	require.NoError(t, WriteGitHubPR(tmp, pr2))

	got, err := ReadGitHubPR(tmp, 42, now)
	require.NoError(t, err)
	assert.Equal(t, "Final", got.Title)
	assert.Equal(t, "merged", got.State)
}

// --- D. Backward compatibility with legacy filenames ---

// TestReadGitHubPR_LegacyFallback verifies that ReadGitHubPR can read old-format
// {number}.json files for backward compatibility with existing ledger data.
// Failure prevented: crash or missing-data after upgrade on repos with pre-hash files.
func TestReadGitHubPR_LegacyFallback(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	// write a legacy-format file directly (no hash in name)
	dir := DateDir(tmp, now, "pr")
	require.NoError(t, os.MkdirAll(dir, 0755))

	pr := PRFile{
		Number: 99, Title: "Legacy PR", State: "open", Author: "alice",
		CreatedAt: now, UpdatedAt: now,
	}
	data, _ := json.MarshalIndent(&pr, "", "  ")
	legacyPath := filepath.Join(dir, "99.json")
	require.NoError(t, os.WriteFile(legacyPath, data, 0644))

	got, err := ReadGitHubPR(tmp, 99, now)
	require.NoError(t, err)
	assert.Equal(t, "Legacy PR", got.Title)
}

// TestReadGitHubPR_HashPreferredOverLegacy verifies that when both hash-named
// and legacy files exist, the hash-named version is preferred.
// Failure prevented: old legacy file shadows newer hash-named updates.
func TestReadGitHubPR_HashPreferredOverLegacy(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	later := now.Add(time.Hour)

	dir := DateDir(tmp, now, "pr")
	require.NoError(t, os.MkdirAll(dir, 0755))

	// write legacy file
	legacy := PRFile{
		Number: 99, Title: "Old", State: "open", Author: "alice",
		CreatedAt: now, UpdatedAt: now,
	}
	legacyData, _ := json.MarshalIndent(&legacy, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "99.json"), legacyData, 0644))

	// write hash-named file with newer data
	updated := &PRFile{
		Number: 99, Title: "New", State: "merged", Author: "alice",
		CreatedAt: now, UpdatedAt: later, MergedAt: &later,
	}
	require.NoError(t, WriteGitHubPR(tmp, updated))

	got, err := ReadGitHubPR(tmp, 99, now)
	require.NoError(t, err)
	assert.Equal(t, "New", got.Title, "hash-named file should be preferred over legacy")
}

// --- E. rebuildSyncStateFromDisk deduplication ---

// TestRebuildSyncState_DeduplicatesByNumber verifies that when multiple hash-variant
// files exist for the same PR number, rebuildSyncStateFromDisk keeps only the latest.
// Failure prevented: inflated KnownItems count or stale state used for skip decisions.
func TestRebuildSyncState_DeduplicatesByNumber(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	later := now.Add(time.Hour)

	// write two versions of the same PR (different content = different hash)
	pr1 := &PRFile{
		Number: 42, Title: "Draft", State: "open", Author: "alice",
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, WriteGitHubPR(tmp, pr1))

	pr2 := &PRFile{
		Number: 42, Title: "Final", State: "merged", Author: "alice",
		CreatedAt: now, UpdatedAt: later, MergedAt: &later,
	}
	require.NoError(t, WriteGitHubPR(tmp, pr2))

	// verify two files exist on disk
	files, err := ListGitHubDataFiles(tmp, "pr")
	require.NoError(t, err)
	assert.Len(t, files, 2, "should have two hash-variant files")

	// rebuild should deduplicate to one entry
	state, err := rebuildSyncStateFromDisk(tmp, "pr", slog.Default())
	require.NoError(t, err)

	assert.Equal(t, 1, state.Count, "should deduplicate to 1 unique PR")
	assert.Equal(t, "merged", state.KnownItems[42].State, "should keep latest state")
	assert.Equal(t, later, state.KnownItems[42].UpdatedAt, "should keep latest updated_at")
}

// --- F. Content hash helper ---

// TestContentHash_Deterministic verifies the hash function is deterministic.
func TestContentHash_Deterministic(t *testing.T) {
	data := []byte(`{"number": 42, "title": "test"}`)
	h1 := contentHash(data)
	h2 := contentHash(data)
	assert.Equal(t, h1, h2)
	assert.Len(t, h1, 8)
}

// TestContentHash_DifferentInput verifies different inputs produce different hashes.
func TestContentHash_DifferentInput(t *testing.T) {
	h1 := contentHash([]byte(`{"number": 42}`))
	h2 := contentHash([]byte(`{"number": 43}`))
	assert.NotEqual(t, h1, h2)
}

// TestParseHashFilename verifies the filename parser.
func TestParseHashFilename(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     int
	}{
		{"valid hash file", "42-a1b2c3d4.json", 42},
		{"three digit number", "409-deadbeef.json", 409},
		{"legacy file", "42.json", -1},
		{"no hash", "42-.json", -1},
		{"short hash", "42-abc.json", -1},
		{"not json", "42-a1b2c3d4.txt", -1},
		{"no number", "-a1b2c3d4.json", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseHashFilename(tt.filename))
		})
	}
}

// TestFindLatestFile_NoFiles verifies findLatestFile returns ErrNotExist.
func TestFindLatestFile_NoFiles(t *testing.T) {
	tmp := t.TempDir()
	_, err := findLatestFile(tmp, 42)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// TestFindLatestFile_NonExistentDir verifies findLatestFile returns ErrNotExist for missing dir.
func TestFindLatestFile_NonExistentDir(t *testing.T) {
	_, err := findLatestFile("/nonexistent/path", 42)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// --- G. Filename format verification ---

// TestWriteGitHubPR_FilenameFormat verifies the written file matches {number}-{8hex}.json.
func TestWriteGitHubPR_FilenameFormat(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	pr := &PRFile{
		Number: 409, Title: "Test", State: "open", Author: "alice",
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, WriteGitHubPR(tmp, pr))

	dir := DateDir(tmp, now, "pr")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	name := entries[0].Name()
	assert.Regexp(t, `^409-[0-9a-f]{8}\.json$`, name)
}
