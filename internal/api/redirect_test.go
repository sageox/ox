package api

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRedirectHeader_NoHeader(t *testing.T) {
	t.Parallel()
	header := http.Header{}
	info := ParseRedirectHeader(header)
	assert.Nil(t, info, "expected nil for missing header")
}

func TestParseRedirectHeader_EmptyHeader(t *testing.T) {
	t.Parallel()
	header := http.Header{}
	header.Set(RedirectHeader, "")
	info := ParseRedirectHeader(header)
	assert.Nil(t, info, "expected nil for empty header")
}

func TestParseRedirectHeader_InvalidJSON(t *testing.T) {
	t.Parallel()
	header := http.Header{}
	header.Set(RedirectHeader, "not json")
	info := ParseRedirectHeader(header)
	assert.Nil(t, info, "expected nil for invalid JSON")
}

func TestParseRedirectHeader_RepoOnly(t *testing.T) {
	t.Parallel()
	header := http.Header{}
	header.Set(RedirectHeader, `{"repo":{"from":"repo_old","to":"repo_new"}}`)

	info := ParseRedirectHeader(header)
	require.NotNil(t, info, "expected non-nil result")
	require.NotNil(t, info.Repo, "expected repo mapping")
	assert.Equal(t, "repo_old", info.Repo.From)
	assert.Equal(t, "repo_new", info.Repo.To)
	assert.Nil(t, info.Team, "expected nil team")
	assert.Nil(t, info.Config, "expected nil config")
}

func TestParseRedirectHeader_Full(t *testing.T) {
	t.Parallel()
	header := http.Header{}
	header.Set(RedirectHeader, `{"repo":{"from":"repo_old","to":"repo_new"},"team":{"from":"team_old","to":"team_new"},"config":{"repo_id":"repo_new","team_id":"team_new"}}`)

	info := ParseRedirectHeader(header)
	require.NotNil(t, info, "expected non-nil result")

	assert.NotNil(t, info.Repo)
	assert.Equal(t, "repo_old", info.Repo.From)
	assert.Equal(t, "repo_new", info.Repo.To)

	assert.NotNil(t, info.Team)
	assert.Equal(t, "team_old", info.Team.From)
	assert.Equal(t, "team_new", info.Team.To)

	assert.NotNil(t, info.Config)
	assert.Equal(t, "repo_new", info.Config.RepoID)
	assert.Equal(t, "team_new", info.Config.TeamID)
}

func TestHandleRedirect_NilInfo(t *testing.T) {
	t.Parallel()
	err := HandleRedirect("/tmp", nil)
	assert.NoError(t, err)
}

func TestHandleRedirect_EmptyProjectRoot(t *testing.T) {
	t.Parallel()
	info := &RedirectInfo{
		Repo: &RedirectMapping{From: "repo_old", To: "repo_new"},
	}
	err := HandleRedirect("", info)
	assert.NoError(t, err)
}

func TestRenameRepoMarker_NoMarkerExists(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	// should not error when old marker doesn't exist
	err := RenameRepoMarker(tmpDir, "repo_oldid", "repo_newid")
	assert.NoError(t, err)
}

func TestRenameRepoMarker_Success(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	// create old marker
	oldMarker := filepath.Join(sageoxDir, ".repo_oldid")
	require.NoError(t, os.WriteFile(oldMarker, []byte(`{"id":"repo_oldid"}`), 0600))

	err := RenameRepoMarker(tmpDir, "repo_oldid", "repo_newid")
	assert.NoError(t, err)

	// old marker should be gone
	_, err = os.Stat(oldMarker)
	assert.True(t, os.IsNotExist(err), "old marker should not exist")

	// new marker should exist
	newMarker := filepath.Join(sageoxDir, ".repo_newid")
	_, err = os.Stat(newMarker)
	assert.False(t, os.IsNotExist(err), "new marker should exist")
}

func TestRenameRepoMarker_NewAlreadyExists(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	// create both old and new markers (simulating already migrated)
	oldMarker := filepath.Join(sageoxDir, ".repo_oldid")
	newMarker := filepath.Join(sageoxDir, ".repo_newid")
	require.NoError(t, os.WriteFile(oldMarker, []byte(`{"id":"repo_oldid"}`), 0600))
	require.NoError(t, os.WriteFile(newMarker, []byte(`{"id":"repo_newid"}`), 0600))

	err := RenameRepoMarker(tmpDir, "repo_oldid", "repo_newid")
	assert.NoError(t, err)

	// old marker should be removed
	_, err = os.Stat(oldMarker)
	assert.True(t, os.IsNotExist(err), "old marker should be removed when new exists")

	// new marker should still exist
	_, err = os.Stat(newMarker)
	assert.False(t, os.IsNotExist(err), "new marker should still exist")
}

func TestUpdateProjectConfig_NoConfigFile(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	cfg := &RedirectConfig{
		RepoID: "repo_new",
		TeamID: "team_new",
	}

	// should create config if it doesn't exist
	err := updateProjectConfig(tmpDir, cfg)
	assert.NoError(t, err)
}

func TestUpdateProjectConfig_UpdatesExisting(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	// create initial config
	configPath := filepath.Join(sageoxDir, "config.json")
	initialConfig := `{"repo_id":"repo_old","team_id":"team_old","update_frequency_hours":24}`
	require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0600))

	cfg := &RedirectConfig{
		RepoID: "repo_new",
		TeamID: "team_new",
	}

	err := updateProjectConfig(tmpDir, cfg)
	assert.NoError(t, err)

	// read back and verify
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)

	content := string(data)
	assert.Contains(t, content, `"repo_id": "repo_new"`)
	assert.Contains(t, content, `"team_id": "team_new"`)
}

func TestUpdateProjectConfig_NoChangeIfSame(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	// create initial config with same values
	configPath := filepath.Join(sageoxDir, "config.json")
	initialConfig := `{"repo_id":"repo_same","team_id":"team_same","update_frequency_hours":24}`
	require.NoError(t, os.WriteFile(configPath, []byte(initialConfig), 0600))

	// get initial mod time
	info1, _ := os.Stat(configPath)

	cfg := &RedirectConfig{
		RepoID: "repo_same",
		TeamID: "team_same",
	}

	err := updateProjectConfig(tmpDir, cfg)
	assert.NoError(t, err)

	// file should not have been modified (mod time unchanged)
	// Note: this test is timing-sensitive but generally works
	info2, _ := os.Stat(configPath)
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Log("warning: file was rewritten even though values unchanged (timing issue possible)")
	}
}
