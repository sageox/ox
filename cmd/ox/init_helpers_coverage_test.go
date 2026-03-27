package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractUUIDSuffix_Coverage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{"standard repo ID", "repo_01jfk3mab", "01jfk3mab"},
		{"no prefix", "01jfk3mab", "01jfk3mab"},
		{"just prefix", "repo_", ""},
		{"empty string", "", ""},
		{"double prefix", "repo_repo_abc", "repo_abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractUUIDSuffix(tt.input)
			assert.Equal(t, tt.expect, got)
		})
	}
}

func TestStringSlicesEqual_Coverage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		a, b   []string
		expect bool
	}{
		{"both nil", nil, nil, true},
		{"both empty", []string{}, []string{}, true},
		{"equal single", []string{"a"}, []string{"a"}, true},
		{"equal multiple", []string{"a", "b", "c"}, []string{"a", "b", "c"}, true},
		{"different length", []string{"a"}, []string{"a", "b"}, false},
		{"same length different values", []string{"a", "b"}, []string{"a", "c"}, false},
		{"order matters", []string{"a", "b"}, []string{"b", "a"}, false},
		{"nil vs empty", nil, []string{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := stringSlicesEqual(tt.a, tt.b)
			assert.Equal(t, tt.expect, got)
		})
	}
}

func TestInitTracker_TrackAndRollback(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tracker := newInitTracker(dir)
	tracker.isFreshInit = true

	// create a file to track
	filePath := filepath.Join(dir, "created.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("new content"), 0644))
	tracker.trackCreatedFile(filePath)

	// create a dir to track
	subDir := filepath.Join(dir, "subdir")
	require.NoError(t, os.Mkdir(subDir, 0755))
	tracker.trackCreatedDir(subDir)

	// modify an existing file
	existingFile := filepath.Join(dir, "existing.txt")
	require.NoError(t, os.WriteFile(existingFile, []byte("original"), 0644))
	tracker.trackModifiedFile(existingFile)
	// simulate modification
	require.NoError(t, os.WriteFile(existingFile, []byte("modified"), 0644))

	// verify tracking state
	assert.Len(t, tracker.createdFiles, 1)
	assert.Len(t, tracker.createdDirs, 1)
	assert.Len(t, tracker.modifiedFiles, 1)

	// rollback
	tracker.rollback(true)

	// created file should be removed
	_, err := os.Stat(filePath)
	assert.True(t, os.IsNotExist(err), "created file should be removed on rollback")

	// empty directory should be removed
	_, err = os.Stat(subDir)
	assert.True(t, os.IsNotExist(err), "empty created dir should be removed on rollback")

	// modified file should be restored
	content, err := os.ReadFile(existingFile)
	require.NoError(t, err)
	assert.Equal(t, "original", string(content))
}

func TestInitTracker_TrackModifiedFile_Idempotent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tracker := newInitTracker(dir)

	filePath := filepath.Join(dir, "file.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("v1"), 0644))

	tracker.trackModifiedFile(filePath)
	// modify the file
	require.NoError(t, os.WriteFile(filePath, []byte("v2"), 0644))
	// second track should be a no-op (original snapshot preserved)
	tracker.trackModifiedFile(filePath)

	tracker.rollback(true)

	content, err := os.ReadFile(filePath)
	require.NoError(t, err)
	assert.Equal(t, "v1", string(content), "should restore to first snapshot, not second")
}

func TestInitTracker_TrackModifiedFile_NonexistentFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tracker := newInitTracker(dir)

	// tracking a nonexistent file should not panic or add to map
	tracker.trackModifiedFile(filepath.Join(dir, "nonexistent.txt"))
	assert.Empty(t, tracker.modifiedFiles)
}

func TestInitTracker_RollbackNotFreshInit_SkipsDirs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tracker := newInitTracker(dir)
	tracker.isFreshInit = false

	subDir := filepath.Join(dir, "keepdir")
	require.NoError(t, os.Mkdir(subDir, 0755))
	tracker.trackCreatedDir(subDir)

	tracker.rollback(true)

	// directory should NOT be removed since isFreshInit is false
	_, err := os.Stat(subDir)
	assert.NoError(t, err, "dir should not be removed when isFreshInit is false")
}

func TestInitTracker_RollbackNonEmptyDir_Skips(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tracker := newInitTracker(dir)
	tracker.isFreshInit = true

	subDir := filepath.Join(dir, "notempty")
	require.NoError(t, os.Mkdir(subDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(subDir, "leftover.txt"), []byte("data"), 0644))
	tracker.trackCreatedDir(subDir)

	tracker.rollback(true)

	// non-empty dir should be preserved
	_, err := os.Stat(subDir)
	assert.NoError(t, err, "non-empty dir should not be removed during rollback")
}

func TestInitTracker_TrackForceStage(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tracker := newInitTracker(dir)

	tracker.trackForceStage("/some/path/file.txt")
	tracker.trackForceStage("/another/path/file2.txt")
	assert.Len(t, tracker.forceStage, 2)
}

func TestInitTracker_RollbackEmpty(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tracker := newInitTracker(dir)
	tracker.isFreshInit = true

	// rollback with nothing tracked should not panic
	tracker.rollback(true)
}

func TestDetectRepoMarkersForEndpoint_Coverage_NonexistentDir(t *testing.T) {
	t.Parallel()

	result, err := detectRepoMarkersForEndpoint("/nonexistent/path", "https://sageox.ai")
	assert.NoError(t, err)
	assert.Nil(t, result)
}

func TestDetectRepoMarkersForEndpoint_Coverage_EmptyDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	result, err := detectRepoMarkersForEndpoint(dir, "https://sageox.ai")
	assert.NoError(t, err)
	assert.Empty(t, result)
}

func TestDetectRepoMarkersForEndpoint_Coverage_MatchingMarker(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	marker := repoMarker{
		RepoID:   "repo_abc123",
		Endpoint: "https://sageox.ai",
	}
	data, err := json.Marshal(marker)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".repo_abc123"), data, 0644))

	result, err := detectRepoMarkersForEndpoint(dir, "https://sageox.ai")
	assert.NoError(t, err)
	assert.Equal(t, []string{"repo_abc123"}, result)
}

func TestDetectRepoMarkersForEndpoint_Coverage_DifferentEndpoint(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	marker := repoMarker{
		RepoID:   "repo_xyz",
		Endpoint: "https://other.sageox.ai",
	}
	data, err := json.Marshal(marker)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".repo_xyz"), data, 0644))

	result, err := detectRepoMarkersForEndpoint(dir, "https://sageox.ai")
	assert.NoError(t, err)
	assert.Empty(t, result)
}

func TestDetectRepoMarkersForEndpoint_Coverage_MultipleMarkersSorted(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ep := "https://sageox.ai"

	for _, id := range []string{"repo_zzz", "repo_aaa", "repo_mmm"} {
		marker := repoMarker{RepoID: id, Endpoint: ep}
		data, _ := json.Marshal(marker)
		suffix := extractUUIDSuffix(id)
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".repo_"+suffix), data, 0644))
	}

	result, err := detectRepoMarkersForEndpoint(dir, ep)
	assert.NoError(t, err)
	assert.Equal(t, []string{"repo_aaa", "repo_mmm", "repo_zzz"}, result)
}

func TestDetectRepoMarkersForEndpoint_Coverage_SkipsNonRepoFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0644))
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".repo_direntry"), 0755))

	result, err := detectRepoMarkersForEndpoint(dir, "https://sageox.ai")
	assert.NoError(t, err)
	assert.Empty(t, result)
}

func TestDetectRepoMarkersForEndpoint_Coverage_InvalidJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".repo_bad"), []byte("not json"), 0644))

	result, err := detectRepoMarkersForEndpoint(dir, "https://sageox.ai")
	assert.NoError(t, err)
	assert.Empty(t, result, "invalid JSON markers should be skipped")
}

func TestCreateSageoxGitignore_WritesExpectedContent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")

	err := createSageoxGitignore(path)
	require.NoError(t, err)

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, sageoxGitignoreContent, string(content))
}

func TestCreateSageoxGitignore_FailsOnInvalidPath(t *testing.T) {
	t.Parallel()

	err := createSageoxGitignore("/nonexistent/dir/.gitignore")
	assert.Error(t, err)
}

func TestInjectIntoFile_AppendsSection(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	require.NoError(t, os.WriteFile(path, []byte("# Instructions\n"), 0644))

	status, err := injectIntoFile(path, OxPrimeLine, "CLAUDE.md")
	require.NoError(t, err)
	assert.Equal(t, injectedNew, status)

	content, _ := os.ReadFile(path)
	assert.Contains(t, string(content), OxPrimeLine)
	assert.Contains(t, string(content), "# Instructions")
}

func TestInjectIntoFile_CanonicalAlreadyExists(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	require.NoError(t, os.WriteFile(path, []byte("# Doc\n"+OxPrimeLine+"\n"), 0644))

	status, err := injectIntoFile(path, OxPrimeLine, "CLAUDE.md")
	require.NoError(t, err)
	assert.Equal(t, alreadyPresent, status)
}

func TestInjectIntoFile_DetectsLegacyAsUpgrade(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	require.NoError(t, os.WriteFile(path, []byte("Run ox agent prime before starting.\n"), 0644))

	status, err := injectIntoFile(path, OxPrimeLine, "CLAUDE.md")
	require.NoError(t, err)
	assert.Equal(t, injectedUpgrade, status)

	content, _ := os.ReadFile(path)
	assert.Contains(t, string(content), OxPrimeLine)
}

func TestInjectIntoFile_ErrorOnMissingFile(t *testing.T) {
	t.Parallel()

	_, err := injectIntoFile("/nonexistent/path/file.md", OxPrimeLine, "file.md")
	assert.Error(t, err)
}

func TestInjectStatusConstants_Coverage(t *testing.T) {
	t.Parallel()

	assert.NotEqual(t, injectedNew, alreadyPresent)
	assert.NotEqual(t, injectedNew, injectedUpgrade)
	assert.NotEqual(t, injectedNew, symlinkCreated)
	assert.NotEqual(t, alreadyPresent, injectedUpgrade)
}
