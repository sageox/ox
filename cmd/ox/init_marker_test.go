//go:build !short

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/repotools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateRepoMarker(t *testing.T) {
	tmpDir := t.TempDir()
	requireSageoxDir(t, tmpDir)
	sageoxDir := filepath.Join(tmpDir, ".sageox")

	repoID := "repo_testUUID123"
	repoSalt := "abc123"
	gitIdentity := &repotools.GitIdentity{
		Name:  "Test User",
		Email: "test@example.com",
	}

	err := createRepoMarker(sageoxDir, repoID, repoSalt, gitIdentity, nil)
	require.NoError(t, err, "createRepoMarker failed")

	// verify marker file exists
	markerPath := filepath.Join(sageoxDir, ".repo_testUUID123")
	require.FileExists(t, markerPath, "marker file was not created")

	// read and verify content
	data, err := os.ReadFile(markerPath)
	require.NoError(t, err, "failed to read marker")

	var marker map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &marker), "failed to unmarshal marker")

	// verify fields
	assert.Equal(t, repoID, marker["repo_id"], "repo_id mismatch")
	assert.Equal(t, "git", marker["type"], "type mismatch")
	assert.Equal(t, repoSalt, marker["repo_salt"], "repo_salt mismatch")
	assert.Equal(t, "test@example.com", marker["init_by_email"], "init_by_email mismatch")
	assert.Equal(t, "Test User", marker["init_by_name"], "init_by_name mismatch")
	assert.NotEmpty(t, marker["init_at"], "expected init_at to be set")
}

func TestCreateRepoMarker_NilGitIdentity(t *testing.T) {
	tmpDir := t.TempDir()
	requireSageoxDir(t, tmpDir)
	sageoxDir := filepath.Join(tmpDir, ".sageox")

	repoID := "repo_testUUID456"
	err := createRepoMarker(sageoxDir, repoID, "", nil, nil)
	require.NoError(t, err, "createRepoMarker failed")

	// verify marker file exists
	markerPath := filepath.Join(sageoxDir, ".repo_testUUID456")
	data, err := os.ReadFile(markerPath)
	require.NoError(t, err, "failed to read marker")

	var marker map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &marker), "failed to unmarshal marker")

	// verify git identity fields are not present
	_, exists := marker["init_by_email"]
	assert.False(t, exists, "expected init_by_email to not exist")
	_, exists = marker["init_by_name"]
	assert.False(t, exists, "expected init_by_name to not exist")
}

func TestCreateRepoMarker_PartialGitIdentity_OnlyEmail(t *testing.T) {
	tmpDir := t.TempDir()
	requireSageoxDir(t, tmpDir)
	sageoxDir := filepath.Join(tmpDir, ".sageox")

	repoID := "repo_partialEmail"
	gitIdentity := &repotools.GitIdentity{
		Name:  "",
		Email: "user@example.com",
	}

	err := createRepoMarker(sageoxDir, repoID, "salt123", gitIdentity, nil)
	require.NoError(t, err, "createRepoMarker failed")

	markerPath := filepath.Join(sageoxDir, ".repo_partialEmail")
	data, err := os.ReadFile(markerPath)
	require.NoError(t, err, "failed to read marker")

	var marker map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &marker), "failed to unmarshal marker")

	assert.Equal(t, "user@example.com", marker["init_by_email"], "expected email to be set")
	_, exists := marker["init_by_name"]
	assert.False(t, exists, "expected name to not exist")
}

func TestCreateRepoMarker_PartialGitIdentity_OnlyName(t *testing.T) {
	tmpDir := t.TempDir()
	requireSageoxDir(t, tmpDir)
	sageoxDir := filepath.Join(tmpDir, ".sageox")

	repoID := "repo_partialName"
	gitIdentity := &repotools.GitIdentity{
		Name:  "Test User",
		Email: "",
	}

	err := createRepoMarker(sageoxDir, repoID, "salt456", gitIdentity, nil)
	require.NoError(t, err, "createRepoMarker failed")

	markerPath := filepath.Join(sageoxDir, ".repo_partialName")
	data, err := os.ReadFile(markerPath)
	require.NoError(t, err, "failed to read marker")

	var marker map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &marker), "failed to unmarshal marker")

	assert.Equal(t, "Test User", marker["init_by_name"], "expected name to be set")
	_, exists := marker["init_by_email"]
	assert.False(t, exists, "expected email to not exist")
}

func TestCreateRepoMarker_EmptyRepoSalt(t *testing.T) {
	tmpDir := t.TempDir()
	requireSageoxDir(t, tmpDir)
	sageoxDir := filepath.Join(tmpDir, ".sageox")

	repoID := "repo_emptySalt"
	err := createRepoMarker(sageoxDir, repoID, "", nil, nil)
	require.NoError(t, err, "createRepoMarker failed")

	markerPath := filepath.Join(sageoxDir, ".repo_emptySalt")
	data, err := os.ReadFile(markerPath)
	require.NoError(t, err, "failed to read marker")

	var marker map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &marker), "failed to unmarshal marker")

	// empty repo_salt should still be in the marker (as empty string)
	salt, exists := marker["repo_salt"]
	assert.True(t, exists, "repo_salt field should exist in marker")
	assert.Equal(t, "", salt, "expected empty repo_salt")
}

func TestExtractUUIDSuffix(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"repo_01jfk3mab", "01jfk3mab"},
		{"repo_abc123", "abc123"},
		{"repo_", ""},
		{"01jfk3mab", "01jfk3mab"}, // already without prefix
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := extractUUIDSuffix(tt.input)
			assert.Equal(t, tt.expected, result, "extractUUIDSuffix(%s) mismatch", tt.input)
		})
	}
}

func TestExtractUUIDSuffix_EmptyString(t *testing.T) {
	result := extractUUIDSuffix("")
	assert.Equal(t, "", result, "extractUUIDSuffix(\"\") mismatch")
}

func TestExtractUUIDSuffix_NoPrefix(t *testing.T) {
	result := extractUUIDSuffix("justauuid")
	assert.Equal(t, "justauuid", result, "extractUUIDSuffix(\"justauuid\") mismatch")
}

func TestDetectExistingRepoMarkers(t *testing.T) {
	tmpDir := t.TempDir()
	requireSageoxDir(t, tmpDir)
	sageoxDir := filepath.Join(tmpDir, ".sageox")

	// create multiple marker files
	markers := []struct {
		filename string
		repoID   string
	}{
		{".repo_01jfk3mab", "repo_01jfk3mab"},
		{".repo_01jfk3mac", "repo_01jfk3mac"},
		{".repo_01jfk3mad", "repo_01jfk3mad"},
	}

	currentEndpoint := endpoint.Get()
	for _, m := range markers {
		markerData := map[string]string{
			"repo_id":  m.repoID,
			"type":     "git",
			"endpoint": currentEndpoint,
		}
		data, _ := json.Marshal(markerData)
		markerPath := filepath.Join(sageoxDir, m.filename)
		require.NoError(t, os.WriteFile(markerPath, data, 0644), "failed to write marker %s", m.filename)
	}

	// test detection
	repoIDs, err := detectExistingRepoMarkers(sageoxDir)
	require.NoError(t, err, "detectExistingRepoMarkers failed")

	require.Len(t, repoIDs, 3, "expected 3 repo IDs")

	// verify IDs are sorted (UUIDv7 time-sortable)
	expected := []string{"repo_01jfk3mab", "repo_01jfk3mac", "repo_01jfk3mad"}
	for i, id := range expected {
		assert.Equal(t, id, repoIDs[i], "repoIDs[%d] mismatch", i)
	}
}

func TestDetectExistingRepoMarkers_NoMarkers(t *testing.T) {
	tmpDir := t.TempDir()
	requireSageoxDir(t, tmpDir)
	sageoxDir := filepath.Join(tmpDir, ".sageox")

	repoIDs, err := detectExistingRepoMarkers(sageoxDir)
	require.NoError(t, err, "unexpected error")

	assert.Empty(t, repoIDs, "expected no repo IDs")
}

func TestDetectExistingRepoMarkers_NonExistentDir(t *testing.T) {
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")

	// don't create the directory
	repoIDs, err := detectExistingRepoMarkers(sageoxDir)
	require.NoError(t, err, "unexpected error")

	assert.Empty(t, repoIDs, "expected no repo IDs for non-existent dir")
}

func TestDetectExistingRepoMarkers_SkipsInvalidFiles(t *testing.T) {
	tmpDir := t.TempDir()
	requireSageoxDir(t, tmpDir)
	sageoxDir := filepath.Join(tmpDir, ".sageox")

	// create a valid marker with endpoint matching current endpoint
	currentEndpoint := endpoint.Get()
	validMarker := filepath.Join(sageoxDir, ".repo_valid")
	validData := map[string]string{"repo_id": "repo_valid", "endpoint": currentEndpoint}
	data, _ := json.Marshal(validData)
	require.NoError(t, os.WriteFile(validMarker, data, 0644), "failed to write valid marker")

	// create invalid markers that should be skipped
	invalidMarker := filepath.Join(sageoxDir, ".repo_invalid")
	require.NoError(t, os.WriteFile(invalidMarker, []byte("not json"), 0644), "failed to write invalid marker")

	emptyMarker := filepath.Join(sageoxDir, ".repo_empty")
	emptyData := map[string]string{}
	data, _ = json.Marshal(emptyData)
	require.NoError(t, os.WriteFile(emptyMarker, data, 0644), "failed to write empty marker")

	// create a regular file (not starting with .repo_)
	regularFile := filepath.Join(sageoxDir, "config.json")
	require.NoError(t, os.WriteFile(regularFile, []byte("{}"), 0644), "failed to write regular file")

	repoIDs, err := detectExistingRepoMarkers(sageoxDir)
	require.NoError(t, err, "unexpected error")

	// should only find the valid marker
	require.Len(t, repoIDs, 1, "expected 1 repo ID")

	assert.Equal(t, "repo_valid", repoIDs[0], "expected repo_valid")
}

func TestDetectExistingRepoMarkers_MalformedJSON(t *testing.T) {
	tmpDir := t.TempDir()
	requireSageoxDir(t, tmpDir)
	sageoxDir := filepath.Join(tmpDir, ".sageox")

	// create marker with malformed JSON
	markerPath := filepath.Join(sageoxDir, ".repo_malformed")
	require.NoError(t, os.WriteFile(markerPath, []byte("{not valid json"), 0644), "failed to write malformed marker")

	// create valid marker with endpoint matching current endpoint
	currentEndpoint := endpoint.Get()
	validMarker := filepath.Join(sageoxDir, ".repo_valid")
	validData := map[string]string{"repo_id": "repo_valid", "endpoint": currentEndpoint}
	data, _ := json.Marshal(validData)
	require.NoError(t, os.WriteFile(validMarker, data, 0644), "failed to write valid marker")

	repoIDs, err := detectExistingRepoMarkers(sageoxDir)
	require.NoError(t, err, "unexpected error")

	// should skip malformed and only return valid
	require.Len(t, repoIDs, 1, "expected 1 repo ID")

	assert.Equal(t, "repo_valid", repoIDs[0], "expected repo_valid")
}

func TestDetectExistingRepoMarkers_MissingRepoIDField(t *testing.T) {
	tmpDir := t.TempDir()
	requireSageoxDir(t, tmpDir)
	sageoxDir := filepath.Join(tmpDir, ".sageox")

	// create marker without repo_id field
	markerPath := filepath.Join(sageoxDir, ".repo_nofield")
	markerData := map[string]string{"type": "git"}
	data, _ := json.Marshal(markerData)
	require.NoError(t, os.WriteFile(markerPath, data, 0644), "failed to write marker")

	repoIDs, err := detectExistingRepoMarkers(sageoxDir)
	require.NoError(t, err, "unexpected error")

	// should skip marker with missing repo_id field
	assert.Empty(t, repoIDs, "expected 0 repo IDs")
}

func TestDetectExistingRepoMarkers_EmptyRepoID(t *testing.T) {
	tmpDir := t.TempDir()
	requireSageoxDir(t, tmpDir)
	sageoxDir := filepath.Join(tmpDir, ".sageox")

	// create marker with empty repo_id
	markerPath := filepath.Join(sageoxDir, ".repo_empty")
	markerData := map[string]string{"repo_id": "", "type": "git"}
	data, _ := json.Marshal(markerData)
	require.NoError(t, os.WriteFile(markerPath, data, 0644), "failed to write marker")

	repoIDs, err := detectExistingRepoMarkers(sageoxDir)
	require.NoError(t, err, "unexpected error")

	// should skip marker with empty repo_id
	assert.Empty(t, repoIDs, "expected 0 repo IDs")
}

func TestDetectExistingRepoMarkers_MultipleMarkers(t *testing.T) {
	tmpDir := t.TempDir()
	requireSageoxDir(t, tmpDir)
	sageoxDir := filepath.Join(tmpDir, ".sageox")

	// create multiple marker files (simulating concurrent inits from different devs)
	currentEndpoint := endpoint.Get()
	markers := []struct {
		repoID string
		initAt string
	}{
		{"repo_01aaa111", "2025-01-01T00:00:00Z"}, // oldest (should be first after sort)
		{"repo_01ccc333", "2025-01-03T00:00:00Z"}, // newest
		{"repo_01bbb222", "2025-01-02T00:00:00Z"}, // middle
	}

	for _, m := range markers {
		uuid := extractUUIDSuffix(m.repoID)
		markerPath := filepath.Join(sageoxDir, ".repo_"+uuid)
		data, _ := json.Marshal(map[string]string{
			"repo_id":  m.repoID,
			"type":     "git",
			"init_at":  m.initAt,
			"endpoint": currentEndpoint,
		})
		require.NoError(t, os.WriteFile(markerPath, data, 0644), "failed to write marker %s", m.repoID)
	}

	// detect all markers
	repoIDs, err := detectExistingRepoMarkers(sageoxDir)
	require.NoError(t, err, "detectExistingRepoMarkers failed")

	// should find all 3 markers
	require.Len(t, repoIDs, 3, "expected 3 repo IDs")

	// should be sorted (UUIDv7 is time-sortable, so alphabetical = chronological)
	assert.Equal(t, "repo_01aaa111", repoIDs[0], "expected first repo_id to be oldest")
}

func TestDetectExistingRepoMarkers_SortOrder(t *testing.T) {
	tmpDir := t.TempDir()
	requireSageoxDir(t, tmpDir)
	sageoxDir := filepath.Join(tmpDir, ".sageox")

	// create markers in reverse order to test sorting
	currentEndpoint := endpoint.Get()
	repoIDs := []string{"repo_zzz999", "repo_aaa111", "repo_mmm555"}
	for _, id := range repoIDs {
		uuid := extractUUIDSuffix(id)
		markerPath := filepath.Join(sageoxDir, ".repo_"+uuid)
		data, _ := json.Marshal(map[string]string{"repo_id": id, "endpoint": currentEndpoint})
		require.NoError(t, os.WriteFile(markerPath, data, 0644), "failed to write marker")
	}

	detected, err := detectExistingRepoMarkers(sageoxDir)
	require.NoError(t, err, "detectExistingRepoMarkers failed")

	// verify sorted order
	expected := []string{"repo_aaa111", "repo_mmm555", "repo_zzz999"}
	for i, id := range expected {
		assert.Equal(t, id, detected[i], "position %d mismatch", i)
	}
}

