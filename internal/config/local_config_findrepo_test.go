package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSaveAndLoadLocalConfig(t *testing.T) {
	tmpDir := CreateInitializedProject(t)
	now := time.Now().UTC().Truncate(time.Second)

	original := &LocalConfig{
		Ledger: &LedgerConfig{
			Path:     "/path/to/ledger",
			LastSync: now,
		},
		TeamContexts: []TeamContext{
			{
				TeamID:   "team_abc123",
				TeamName: "Team Alpha",
				Path:     "/path/to/team-alpha",
				LastSync: now,
			},
			{
				TeamID:   "team_def456",
				TeamName: "Team Beta",
				Path:     "/path/to/team-beta",
				// LastSync left as zero value
			},
		},
	}

	// save config
	require.NoError(t, SaveLocalConfig(tmpDir, original), "save failed")

	// verify file exists
	configPath := filepath.Join(tmpDir, sageoxDir, localConfigFilename)
	_, err := os.Stat(configPath)
	require.NoError(t, err, "config file was not created")

	// load config
	loaded, err := LoadLocalConfig(tmpDir)
	require.NoError(t, err, "load failed")

	// verify ledger
	require.NotNil(t, loaded.Ledger, "expected ledger to be set")
	assert.Equal(t, original.Ledger.Path, loaded.Ledger.Path, "ledger path")
	require.True(t, loaded.Ledger.HasLastSync(), "expected ledger last_sync to be set")
	assert.True(t, loaded.Ledger.LastSync.Equal(now), "ledger last_sync: got %v, want %v", loaded.Ledger.LastSync, now)

	// verify team contexts
	require.Len(t, loaded.TeamContexts, 2, "team contexts count")

	tc1 := loaded.TeamContexts[0]
	assert.Equal(t, "team_abc123", tc1.TeamID, "team 1 id")
	assert.Equal(t, "Team Alpha", tc1.TeamName, "team 1 name")
	assert.Equal(t, "/path/to/team-alpha", tc1.Path, "team 1 path")
	assert.True(t, tc1.HasLastSync(), "expected team 1 last_sync to be set")

	tc2 := loaded.TeamContexts[1]
	assert.Equal(t, "team_def456", tc2.TeamID, "team 2 id")
	assert.False(t, tc2.HasLastSync(), "expected team 2 last_sync to be zero (unset)")
}

func TestLegacyTeamContextPath(t *testing.T) {
	tests := []struct {
		name        string
		teamID      string
		projectRoot string
		want        string
	}{
		{
			name:        "standard case",
			teamID:      "team_abc123",
			projectRoot: "/home/user/code/my-project",
			want:        filepath.FromSlash("/home/user/code/sageox_team_team_abc123_context"),
		},
		{
			name:        "empty team id",
			teamID:      "",
			projectRoot: "/home/user/code/my-project",
			want:        "",
		},
		{
			name:        "empty project root",
			teamID:      "team_abc123",
			projectRoot: "",
			want:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LegacyTeamContextPath(tt.teamID, tt.projectRoot)
			assert.Equal(t, tt.want, got, "LegacyTeamContextPath(%q, %q)", tt.teamID, tt.projectRoot)
		})
	}
}

// -----------------------------------------------------------------------------
// FindRepoTeamContext Tests
// -----------------------------------------------------------------------------

func TestFindRepoTeamContext_WithLocalConfig(t *testing.T) {
	// when [[team_contexts]] is populated in config.local.toml, use it
	tmpDir := CreateInitializedProjectWithConfig(t, &ProjectConfig{
		TeamID:   "team_abc",
		TeamName: "Team ABC",
		Endpoint: "https://sageox.ai",
	})

	localCfg := &LocalConfig{
		TeamContexts: []TeamContext{
			{TeamID: "team_abc", TeamName: "Team ABC", Path: "/some/path/team_abc"},
		},
	}
	require.NoError(t, SaveLocalConfig(tmpDir, localCfg))

	tc := FindRepoTeamContext(tmpDir)
	require.NotNil(t, tc, "expected team context from local config")
	assert.Equal(t, "team_abc", tc.TeamID)
	assert.Equal(t, "/some/path/team_abc", tc.Path)
}

func TestFindRepoTeamContext_FallbackToConfigJSON(t *testing.T) {
	// BUG REGRESSION: when daemon hasn't synced yet (no [[team_contexts]] in
	// config.local.toml), FindRepoTeamContext should fall back to computing the
	// path from config.json's team_id + endpoint. Previously returned nil, which
	// broke team context features until daemon ran.
	tempHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tempHome, ".local", "share"))

	tmpDir := CreateInitializedProjectWithConfig(t, &ProjectConfig{
		TeamID:   "team_fallback",
		TeamName: "Fallback Team",
		Endpoint: "https://sageox.ai",
	})

	// compute expected path and create the directory (simulates daemon having cloned it)
	expectedPath := DefaultTeamContextPath("team_fallback", "https://sageox.ai")
	require.NotEmpty(t, expectedPath)
	require.NoError(t, os.MkdirAll(expectedPath, 0755))

	// no config.local.toml at all — daemon hasn't written anything
	tc := FindRepoTeamContext(tmpDir)
	require.NotNil(t, tc, "expected fallback team context from config.json")
	assert.Equal(t, "team_fallback", tc.TeamID)
	assert.Equal(t, "Fallback Team", tc.TeamName)
	assert.Equal(t, expectedPath, tc.Path)
}

func TestFindRepoTeamContext_FallbackRequiresDirOnDisk(t *testing.T) {
	// fallback should NOT return a team context if the directory doesn't exist
	tempHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tempHome, ".local", "share"))

	tmpDir := CreateInitializedProjectWithConfig(t, &ProjectConfig{
		TeamID:   "team_nodir",
		TeamName: "No Dir Team",
		Endpoint: "https://sageox.ai",
	})

	tc := FindRepoTeamContext(tmpDir)
	assert.Nil(t, tc, "expected nil when team context dir doesn't exist on disk")
}

func TestFindRepoTeamContext_NoProjectConfig(t *testing.T) {
	tmpDir := CreateInitializedProject(t)

	tc := FindRepoTeamContext(tmpDir)
	assert.Nil(t, tc, "expected nil with no project config and no local config")
}

func TestFindRepoTeamContext_MultiTeam_MatchesCorrect(t *testing.T) {
	// regression: with multiple teams in config.local.toml, must return the
	// repo's own team (by ProjectConfig.TeamID), not TeamContexts[0]
	tmpDir := CreateInitializedProjectWithConfig(t, &ProjectConfig{
		TeamID:   "team_abc",
		TeamName: "Team ABC",
		Endpoint: "https://sageox.ai",
	})
	localCfg := &LocalConfig{
		TeamContexts: []TeamContext{
			{TeamID: "team_xyz", TeamName: "Team XYZ", Path: "/path/xyz"},
			{TeamID: "team_abc", TeamName: "Team ABC", Path: "/path/abc"},
			{TeamID: "team_def", TeamName: "Team DEF", Path: "/path/def"},
		},
	}
	require.NoError(t, SaveLocalConfig(tmpDir, localCfg))

	tc := FindRepoTeamContext(tmpDir)
	require.NotNil(t, tc, "expected team context for repo's team")
	assert.Equal(t, "team_abc", tc.TeamID, "must return repo's own team, not TeamContexts[0]")
	assert.Equal(t, "/path/abc", tc.Path)
}

func TestFindRepoTeamContext_MultiTeam_NoMatchReturnsNil(t *testing.T) {
	// regression: when repo's TeamID doesn't match any local config entry and
	// no computed-path dir exists, must return nil — never return TeamContexts[0]
	tmpDir := CreateInitializedProjectWithConfig(t, &ProjectConfig{
		TeamID:   "team_missing",
		TeamName: "Missing Team",
		Endpoint: "https://sageox.ai",
	})
	localCfg := &LocalConfig{
		TeamContexts: []TeamContext{
			{TeamID: "team_xyz", TeamName: "Team XYZ", Path: "/path/xyz"},
			{TeamID: "team_def", TeamName: "Team DEF", Path: "/path/def"},
		},
	}
	require.NoError(t, SaveLocalConfig(tmpDir, localCfg))

	tc := FindRepoTeamContext(tmpDir)
	assert.Nil(t, tc, "must return nil when repo's team not found, not a random team")
}

func TestFindRepoTeamContext_NoProjectConfig_WithTeams_ReturnsNil(t *testing.T) {
	// regression: with no config.json but teams in config.local.toml,
	// must return nil — never guess from TeamContexts[0]
	tmpDir := CreateInitializedProject(t)
	localCfg := &LocalConfig{
		TeamContexts: []TeamContext{
			{TeamID: "team_xyz", TeamName: "Team XYZ", Path: "/path/xyz"},
			{TeamID: "team_abc", TeamName: "Team ABC", Path: "/path/abc"},
		},
	}
	require.NoError(t, SaveLocalConfig(tmpDir, localCfg))

	tc := FindRepoTeamContext(tmpDir)
	assert.Nil(t, tc, "must return nil without project config, not TeamContexts[0]")
}

func TestCreateTeamSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests skipped on Windows")
	}

	// set up isolated home directory
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("SAGEOX_ENDPOINT", "")

	repoName := "test-repo"

	t.Run("creates symlink successfully", func(t *testing.T) {
		projectRoot := t.TempDir()
		teamID := "team_test123"
		ep := "https://api.sageox.ai"

		err := CreateTeamSymlink(repoName, projectRoot, teamID, ep)
		require.NoError(t, err)

		// verify symlink was created
		symlinkPath := DefaultTeamSymlinkPath(repoName, projectRoot, ep, teamID)
		info, err := os.Lstat(symlinkPath)
		require.NoError(t, err, "symlink should exist")
		assert.True(t, info.Mode()&os.ModeSymlink != 0, "should be a symlink")

		// verify symlink target
		target, err := os.Readlink(symlinkPath)
		require.NoError(t, err)
		assert.Contains(t, target, teamID, "target should contain team ID")
	})

	t.Run("idempotent - same target", func(t *testing.T) {
		projectRoot := t.TempDir()
		teamID := "team_idem"
		ep := "https://api.sageox.ai"

		// create first time
		err := CreateTeamSymlink(repoName, projectRoot, teamID, ep)
		require.NoError(t, err)

		// create again - should succeed (idempotent)
		err = CreateTeamSymlink(repoName, projectRoot, teamID, ep)
		require.NoError(t, err)

		// verify still exists
		symlinkPath := DefaultTeamSymlinkPath(repoName, projectRoot, ep, teamID)
		info, err := os.Lstat(symlinkPath)
		require.NoError(t, err)
		assert.True(t, info.Mode()&os.ModeSymlink != 0, "should still be a symlink")
	})

	t.Run("recreates symlink if pointing to wrong target", func(t *testing.T) {
		projectRoot := t.TempDir()
		teamID := "team_recreate"
		ep := "https://api.sageox.ai"

		symlinkPath := DefaultTeamSymlinkPath(repoName, projectRoot, ep, teamID)

		// create parent directory
		require.NoError(t, os.MkdirAll(filepath.Dir(symlinkPath), 0755))

		// create symlink pointing to wrong location
		wrongTarget := "/some/wrong/path"
		require.NoError(t, os.Symlink(wrongTarget, symlinkPath))

		// call CreateTeamSymlink - should recreate with correct target
		err := CreateTeamSymlink(repoName, projectRoot, teamID, ep)
		require.NoError(t, err)

		// verify it now points to correct target
		target, err := os.Readlink(symlinkPath)
		require.NoError(t, err)
		assert.NotEqual(t, wrongTarget, target, "should no longer point to wrong target")
		assert.Contains(t, target, teamID, "should point to team context dir")
	})

	t.Run("error if path exists and is not symlink", func(t *testing.T) {
		projectRoot := t.TempDir()
		teamID := "team_file_exists"
		ep := "https://api.sageox.ai"

		symlinkPath := DefaultTeamSymlinkPath(repoName, projectRoot, ep, teamID)

		// create parent directory and a regular file at symlink location
		require.NoError(t, os.MkdirAll(filepath.Dir(symlinkPath), 0755))
		require.NoError(t, os.WriteFile(symlinkPath, []byte("not a symlink"), 0644))

		err := CreateTeamSymlink(repoName, projectRoot, teamID, ep)
		require.Error(t, err, "should error when path exists and is not a symlink")
		assert.Contains(t, err.Error(), "not a symlink")
	})

	t.Run("error on empty project root", func(t *testing.T) {
		err := CreateTeamSymlink(repoName, "", "team_abc", "https://api.sageox.ai")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "project root cannot be empty")
	})

	t.Run("error on empty team id", func(t *testing.T) {
		err := CreateTeamSymlink(repoName, t.TempDir(), "", "https://api.sageox.ai")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "team ID cannot be empty")
	})

	t.Run("staging endpoint creates namespaced symlink", func(t *testing.T) {
		projectRoot := t.TempDir()
		teamID := "team_staging"
		ep := "https://staging.sageox.ai"

		err := CreateTeamSymlink(repoName, projectRoot, teamID, ep)
		require.NoError(t, err)

		symlinkPath := DefaultTeamSymlinkPath(repoName, projectRoot, ep, teamID)
		assert.Contains(t, symlinkPath, "staging.sageox.ai")

		info, err := os.Lstat(symlinkPath)
		require.NoError(t, err)
		assert.True(t, info.Mode()&os.ModeSymlink != 0)
	})
}
