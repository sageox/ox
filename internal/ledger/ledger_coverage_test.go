package ledger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResetGitHubTypeSyncState_RemovesExistingFile(t *testing.T) {
	tmp := t.TempDir()
	cacheDir := GitHubSyncCacheDir(tmp)
	require.NoError(t, os.MkdirAll(cacheDir, 0755))

	// write a state file first
	state := &GitHubTypeSyncState{
		LastSyncAt: time.Now(),
		Count:      5,
		KnownItems: map[int]KnownItem{1: {State: "open"}},
	}
	require.NoError(t, WriteGitHubTypeSyncState(tmp, "pr", state))

	// verify it exists
	_, err := ReadGitHubTypeSyncState(tmp, "pr")
	require.NoError(t, err)

	// reset should remove it
	err = ResetGitHubTypeSyncState(tmp, "pr")
	assert.NoError(t, err)

	// reading after reset should return zero-value state
	got, err := ReadGitHubTypeSyncState(tmp, "pr")
	require.NoError(t, err)
	assert.True(t, got.LastSyncAt.IsZero())
	assert.Equal(t, 0, got.Count)
	assert.Empty(t, got.KnownItems)
}

func TestResetGitHubTypeSyncState_NonexistentFile(t *testing.T) {
	tmp := t.TempDir()

	// resetting a file that doesn't exist should succeed silently
	err := ResetGitHubTypeSyncState(tmp, "pr")
	assert.NoError(t, err)
}

func TestResetGitHubTypeSyncState_Issue(t *testing.T) {
	tmp := t.TempDir()

	state := &GitHubTypeSyncState{
		LastSyncAt: time.Now(),
		Count:      3,
		KnownItems: map[int]KnownItem{10: {State: "closed"}},
	}
	require.NoError(t, WriteGitHubTypeSyncState(tmp, "issue", state))

	err := ResetGitHubTypeSyncState(tmp, "issue")
	assert.NoError(t, err)

	got, err := ReadGitHubTypeSyncState(tmp, "issue")
	require.NoError(t, err)
	assert.True(t, got.LastSyncAt.IsZero())
}

func TestWriteAndReadGitHubPR_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	mergedAt := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)

	pr := &PRFile{
		Number:      42,
		Title:       "Add feature",
		Body:        "Implements new capability",
		Author:      "developer",
		State:       "merged",
		Labels:      []string{"enhancement"},
		CreatedAt:   now,
		MergedAt:    &mergedAt,
		UpdatedAt:   mergedAt,
		MergeCommit: "abc123",
		URL:         "https://github.com/org/repo/pull/42",
		Comments: []PRComment{
			{
				Author:    "reviewer",
				Body:      "LGTM",
				CreatedAt: now.Add(time.Hour),
			},
		},
		Commits: []PRCommit{
			{
				SHA:    "def456",
				Author: "developer",
				Date:   now,
				Msg:    "initial commit",
			},
		},
	}

	err := WriteGitHubPR(tmp, pr)
	require.NoError(t, err)

	got, err := ReadGitHubPR(tmp, 42, now)
	require.NoError(t, err)

	assert.Equal(t, pr.Number, got.Number)
	assert.Equal(t, pr.Title, got.Title)
	assert.Equal(t, pr.Body, got.Body)
	assert.Equal(t, pr.Author, got.Author)
	assert.Equal(t, pr.State, got.State)
	assert.Equal(t, pr.Labels, got.Labels)
	assert.Equal(t, pr.MergeCommit, got.MergeCommit)
	assert.Equal(t, pr.URL, got.URL)
	assert.Len(t, got.Comments, 1)
	assert.Equal(t, "LGTM", got.Comments[0].Body)
	assert.Len(t, got.Commits, 1)
	assert.Equal(t, "def456", got.Commits[0].SHA)
}

func TestReadGitHubPR_NotFound(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	_, err := ReadGitHubPR(tmp, 999, now)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "read PR 999")
}

func TestWriteAndReadGitHubIssue_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 2, 20, 8, 0, 0, 0, time.UTC)
	closedAt := time.Date(2026, 2, 25, 14, 0, 0, 0, time.UTC)

	issue := &IssueFile{
		Number:    77,
		Title:     "Bug report",
		Body:      "Something is broken",
		Author:    "reporter",
		State:     "closed",
		Labels:    []string{"bug", "priority-high"},
		CreatedAt: now,
		ClosedAt:  &closedAt,
		UpdatedAt: closedAt,
		URL:       "https://github.com/org/repo/issues/77",
		Comments: []IssueComment{
			{
				Author:    "maintainer",
				Body:      "Fixed in v2",
				CreatedAt: closedAt,
			},
		},
	}

	err := WriteGitHubIssue(tmp, issue)
	require.NoError(t, err)

	// verify file exists via findLatestFile (content-hash filename)
	dir := DateDir(tmp, now, "issue")
	path, err := findLatestFile(dir, 77)
	require.NoError(t, err)
	assert.Contains(t, filepath.Base(path), "77-") // hash-based filename

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var got IssueFile
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, issue.Number, got.Number)
	assert.Equal(t, issue.Title, got.Title)
	assert.Equal(t, issue.State, got.State)
	assert.Len(t, got.Comments, 1)
	assert.Equal(t, "Fixed in v2", got.Comments[0].Body)
}

func TestListGitHubDataFiles_NoDataDir(t *testing.T) {
	tmp := t.TempDir()

	files, err := ListGitHubDataFiles(tmp, "pr")
	assert.NoError(t, err)
	assert.Empty(t, files)
}

func TestListGitHubDataFiles_EmptyDataDir(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.MkdirAll(GitHubDataDir(tmp), 0755))

	files, err := ListGitHubDataFiles(tmp, "pr")
	assert.NoError(t, err)
	assert.Empty(t, files)
}

func TestListGitHubDataFiles_FiltersByType(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)

	// write both a PR and an issue on the same date
	pr := &PRFile{
		Number:    1,
		Title:     "PR one",
		Author:    "dev",
		State:     "open",
		CreatedAt: now,
		UpdatedAt: now,
		URL:       "https://example.com/1",
	}
	issue := &IssueFile{
		Number:    2,
		Title:     "Issue two",
		Author:    "user",
		State:     "open",
		CreatedAt: now,
		UpdatedAt: now,
		URL:       "https://example.com/2",
	}
	require.NoError(t, WriteGitHubPR(tmp, pr))
	require.NoError(t, WriteGitHubIssue(tmp, issue))

	prFiles, err := ListGitHubDataFiles(tmp, "pr")
	require.NoError(t, err)
	assert.Len(t, prFiles, 1, "should only find PR files")

	issueFiles, err := ListGitHubDataFiles(tmp, "issue")
	require.NoError(t, err)
	assert.Len(t, issueFiles, 1, "should only find issue files")
}

func TestListGitHubDataFiles_SkipsNonJSON(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)

	// write a real PR
	pr := &PRFile{
		Number:    1,
		Title:     "PR one",
		Author:    "dev",
		State:     "open",
		CreatedAt: now,
		UpdatedAt: now,
		URL:       "https://example.com/1",
	}
	require.NoError(t, WriteGitHubPR(tmp, pr))

	// write a non-JSON file in the same dir
	prDir := DateDir(tmp, now, "pr")
	require.NoError(t, os.WriteFile(filepath.Join(prDir, "notes.txt"), []byte("not json"), 0644))

	files, err := ListGitHubDataFiles(tmp, "pr")
	require.NoError(t, err)
	assert.Len(t, files, 1, "should skip non-JSON files")
}

func TestListGitHubDataFiles_MultipleFiles(t *testing.T) {
	tmp := t.TempDir()
	now := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	yesterday := now.AddDate(0, 0, -1)

	// write PRs across two dates
	for i, ts := range []time.Time{now, now, yesterday} {
		pr := &PRFile{
			Number:    i + 1,
			Title:     "PR",
			Author:    "dev",
			State:     "open",
			CreatedAt: ts,
			UpdatedAt: ts,
			URL:       "https://example.com",
		}
		require.NoError(t, WriteGitHubPR(tmp, pr))
	}

	files, err := ListGitHubDataFiles(tmp, "pr")
	require.NoError(t, err)
	assert.Len(t, files, 3)
}

func TestComputeGitHubDataPaths_SingleDay(t *testing.T) {
	paths := ComputeGitHubDataPaths(1)
	assert.Len(t, paths, 1)
	assert.Contains(t, paths[0], "data/github/")
}

func TestComputeGitHubDataPaths_MultipleDays(t *testing.T) {
	paths := ComputeGitHubDataPaths(7)
	assert.Len(t, paths, 7, "should produce one path per day")

	// each path should be unique
	seen := make(map[string]bool)
	for _, p := range paths {
		assert.False(t, seen[p], "duplicate path: %s", p)
		seen[p] = true
	}
}

func TestComputeGitHubDataPaths_ZeroDays(t *testing.T) {
	paths := ComputeGitHubDataPaths(0)
	assert.Empty(t, paths)
}

func TestDateDir_Format(t *testing.T) {
	ts := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	dir := DateDir("/ledger", ts, "pr")
	assert.Equal(t, filepath.Join("/ledger", "data/github/2026/01/05/pr"), dir)
}

func TestGitHubDataDir(t *testing.T) {
	dir := GitHubDataDir("/ledger")
	assert.Equal(t, filepath.Join("/ledger", "data/github"), dir)
}

func TestGitHubSyncCacheDir(t *testing.T) {
	dir := GitHubSyncCacheDir("/ledger")
	assert.Equal(t, filepath.Join("/ledger", ".sageox/cache/github_sync"), dir)
}

func TestSyncStateFile_KnownTypes(t *testing.T) {
	assert.Equal(t, "pr_sync_state.json", syncStateFile("pr"))
	assert.Equal(t, "issue_sync_state.json", syncStateFile("issue"))
}

func TestSyncStateFile_UnknownType(t *testing.T) {
	assert.Equal(t, "release_sync_state.json", syncStateFile("release"))
}

func TestReadGitHubTypeSyncState_NilKnownItems(t *testing.T) {
	tmp := t.TempDir()
	cacheDir := GitHubSyncCacheDir(tmp)
	require.NoError(t, os.MkdirAll(cacheDir, 0755))

	// write a state file without KnownItems field
	data := `{"last_sync_at":"2026-01-01T00:00:00Z","count":3}`
	path := filepath.Join(cacheDir, syncStateFile("pr"))
	require.NoError(t, os.WriteFile(path, []byte(data), 0644))

	got, err := ReadGitHubTypeSyncState(tmp, "pr")
	require.NoError(t, err)
	assert.NotNil(t, got.KnownItems, "should initialize nil KnownItems to empty map")
	assert.Equal(t, 3, got.Count)
}

func TestReadGitHubTypeSyncState_MigratesLegacyKnownStates(t *testing.T) {
	tmp := t.TempDir()
	cacheDir := GitHubSyncCacheDir(tmp)
	require.NoError(t, os.MkdirAll(cacheDir, 0755))

	// write a legacy state file with known_states (old format)
	data := `{"last_sync_at":"2026-01-01T00:00:00Z","count":2,"known_states":{"1":"open","2":"closed"}}`
	path := filepath.Join(cacheDir, syncStateFile("pr"))
	require.NoError(t, os.WriteFile(path, []byte(data), 0644))

	got, err := ReadGitHubTypeSyncState(tmp, "pr")
	require.NoError(t, err)
	assert.NotNil(t, got.KnownItems, "should migrate KnownStates to KnownItems")
	assert.Equal(t, 2, len(got.KnownItems))
	assert.Equal(t, "open", got.KnownItems[1].State)
	assert.Equal(t, "closed", got.KnownItems[2].State)
	assert.True(t, got.KnownItems[1].UpdatedAt.IsZero(), "migrated items should have zero UpdatedAt")
}

func TestWriteGitHubTypeSyncState_CreatesDir(t *testing.T) {
	tmp := t.TempDir()
	state := &GitHubTypeSyncState{
		LastSyncAt: time.Now(),
		Count:      1,
		KnownItems: map[int]KnownItem{1: {State: "open"}},
	}

	// cache dir doesn't exist yet; WriteGitHubTypeSyncState should create it
	err := WriteGitHubTypeSyncState(tmp, "pr", state)
	assert.NoError(t, err)

	// verify it was written
	got, err := ReadGitHubTypeSyncState(tmp, "pr")
	require.NoError(t, err)
	assert.Equal(t, 1, got.Count)
}
