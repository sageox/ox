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

func TestLoadLocalConfig_EmptyProjectRoot(t *testing.T) {
	_, err := LoadLocalConfig("")
	assert.Error(t, err, "expected error for empty project root")
}

func TestLoadLocalConfig_NonExistent(t *testing.T) {
	tmpDir := t.TempDir()

	cfg, err := LoadLocalConfig(tmpDir)
	require.NoError(t, err, "unexpected error")

	require.NotNil(t, cfg, "expected non-nil config")

	assert.Nil(t, cfg.Ledger, "expected nil ledger for non-existent config")

	assert.Empty(t, cfg.TeamContexts, "expected empty team contexts for non-existent config")
}

func TestSaveLocalConfig_EmptyProjectRoot(t *testing.T) {
	cfg := &LocalConfig{}
	err := SaveLocalConfig("", cfg)
	assert.Error(t, err, "expected error for empty project root")
}

func TestSaveLocalConfig_NilConfig(t *testing.T) {
	tmpDir := t.TempDir()
	err := SaveLocalConfig(tmpDir, nil)
	assert.Error(t, err, "expected error for nil config")
}

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
	require.False(t, os.IsNotExist(err), "config file was not created")

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

func TestDefaultSageoxSiblingDir(t *testing.T) {
	tests := []struct {
		name        string
		repoName    string
		projectRoot string
		want        string
	}{
		{
			name:        "standard case",
			repoName:    "my-project",
			projectRoot: "/home/user/code/my-project",
			want:        "/home/user/code/my-project_sageox",
		},
		{
			name:        "repo with spaces",
			repoName:    "my project",
			projectRoot: "/home/user/code/my-project",
			want:        "/home/user/code/my_project_sageox",
		},
		{
			name:        "repo with slashes",
			repoName:    "org/repo",
			projectRoot: "/home/user/code/repo",
			want:        "/home/user/code/org_repo_sageox",
		},
		{
			name:        "empty project root",
			repoName:    "my-project",
			projectRoot: "",
			want:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DefaultSageoxSiblingDir(tt.repoName, tt.projectRoot)
			assert.Equal(t, tt.want, got, "DefaultSageoxSiblingDir(%q, %q)", tt.repoName, tt.projectRoot)
		})
	}
}

func TestDefaultLedgerPath(t *testing.T) {
	tests := []struct {
		name        string
		repoName    string
		projectRoot string
		endpointURL string
		want        string
	}{
		{
			name:        "production endpoint",
			repoName:    "my-project",
			projectRoot: "/home/user/code/my-project",
			endpointURL: "https://api.sageox.ai",
			want:        "/home/user/code/my-project_sageox/sageox.ai/ledger",
		},
		{
			name:        "staging endpoint",
			repoName:    "my-project",
			projectRoot: "/home/user/code/my-project",
			endpointURL: "https://staging.sageox.ai",
			want:        "/home/user/code/my-project_sageox/staging.sageox.ai/ledger",
		},
		{
			name:        "localhost endpoint",
			repoName:    "my-project",
			projectRoot: "/home/user/code/my-project",
			endpointURL: "http://localhost:8080",
			want:        "/home/user/code/my-project_sageox/localhost/ledger",
		},
		{
			name:        "repo with spaces",
			repoName:    "my project",
			projectRoot: "/home/user/code/my-project",
			endpointURL: "https://sageox.ai",
			want:        "/home/user/code/my_project_sageox/sageox.ai/ledger",
		},
		{
			name:        "repo with slashes",
			repoName:    "org/repo",
			projectRoot: "/home/user/code/repo",
			endpointURL: "https://sageox.ai",
			want:        "/home/user/code/org_repo_sageox/sageox.ai/ledger",
		},
		{
			name:        "empty project root",
			repoName:    "my-project",
			projectRoot: "",
			endpointURL: "https://sageox.ai",
			want:        "",
		},
		{
			name:        "empty endpoint defaults to production",
			repoName:    "my-project",
			projectRoot: "/home/user/code/my-project",
			endpointURL: "",
			want:        "/home/user/code/my-project_sageox/sageox.ai/ledger",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DefaultLedgerPath(tt.repoName, tt.projectRoot, tt.endpointURL)
			assert.Equal(t, tt.want, got, "DefaultLedgerPath(%q, %q, %q)", tt.repoName, tt.projectRoot, tt.endpointURL)
		})
	}
}

func TestLegacyLedgerPath(t *testing.T) {
	tests := []struct {
		name        string
		repoName    string
		projectRoot string
		want        string
	}{
		{
			name:        "standard case",
			repoName:    "my-project",
			projectRoot: "/home/user/code/my-project",
			want:        "/home/user/code/my-project_sageox_ledger",
		},
		{
			name:        "repo with spaces",
			repoName:    "my project",
			projectRoot: "/home/user/code/my-project",
			want:        "/home/user/code/my_project_sageox_ledger",
		},
		{
			name:        "empty project root",
			repoName:    "my-project",
			projectRoot: "",
			want:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LegacyLedgerPath(tt.repoName, tt.projectRoot)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDefaultTeamContextPath(t *testing.T) {
	// use temp home for test isolation
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("OX_XDG_DISABLE", "") // XDG is now the default
	t.Setenv("XDG_DATA_HOME", filepath.Join(tempHome, ".local", "share"))

	t.Run("production endpoint returns namespaced path", func(t *testing.T) {
		got := DefaultTeamContextPath("team_abc123", "https://api.sageox.ai")
		// XDG mode: path should be in $XDG_DATA_HOME/sageox/sageox.ai/teams/
		assert.Contains(t, got, "team_abc123")
		assert.Contains(t, got, filepath.Join("sageox", "sageox.ai", "teams"))
	})

	t.Run("empty team id returns empty", func(t *testing.T) {
		got := DefaultTeamContextPath("", "https://api.sageox.ai")
		assert.Equal(t, "", got)
	})

	t.Run("non-production endpoint returns namespaced path", func(t *testing.T) {
		got := DefaultTeamContextPath("team_abc123", "https://staging.sageox.ai")
		// XDG mode: path should be in $XDG_DATA_HOME/sageox/staging.sageox.ai/teams/
		assert.Contains(t, got, "team_abc123")
		assert.Contains(t, got, "staging.sageox.ai")
		assert.Contains(t, got, "teams")
	})

	t.Run("localhost with port returns namespaced path", func(t *testing.T) {
		got := DefaultTeamContextPath("team_abc123", "http://localhost:8080")
		// XDG mode: path should be in $XDG_DATA_HOME/sageox/localhost/teams/ (port stripped)
		assert.Contains(t, got, "team_abc123")
		assert.Contains(t, got, filepath.Join("localhost", "teams"))
	})

	t.Run("empty endpoint panics", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("DefaultTeamContextPath should panic when endpoint is empty")
			}
		}()
		DefaultTeamContextPath("team_abc123", "")
	})
}

func TestDefaultTeamContextPath_LegacyMode(t *testing.T) {
	// use temp home for test isolation
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("OX_XDG_DISABLE", "1") // legacy mode uses ~/.sageox/

	t.Run("production endpoint returns namespaced path", func(t *testing.T) {
		got := DefaultTeamContextPath("team_abc123", "https://api.sageox.ai")
		// legacy mode: path should be in ~/.sageox/data/sageox.ai/teams/
		assert.Contains(t, got, "team_abc123")
		assert.Contains(t, got, filepath.Join(".sageox", "data", "sageox.ai", "teams"))
	})

	t.Run("empty endpoint panics", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("DefaultTeamContextPath should panic when endpoint is empty")
			}
		}()
		DefaultTeamContextPath("team_abc123", "")
	})
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
			want:        "/home/user/code/sageox_team_team_abc123_context",
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

func TestLocalConfig_GetLedgerPath(t *testing.T) {
	projectRoot := "/home/user/code/my-project"
	repoName := "my-project"
	endpointURL := "https://api.sageox.ai"

	t.Run("nil config", func(t *testing.T) {
		var cfg *LocalConfig
		got := cfg.GetLedgerPath(repoName, projectRoot, endpointURL)
		want := DefaultLedgerPath(repoName, projectRoot, endpointURL)
		assert.Equal(t, want, got)
	})

	t.Run("empty ledger", func(t *testing.T) {
		cfg := &LocalConfig{}
		got := cfg.GetLedgerPath(repoName, projectRoot, endpointURL)
		want := DefaultLedgerPath(repoName, projectRoot, endpointURL)
		assert.Equal(t, want, got)
	})

	t.Run("configured ledger", func(t *testing.T) {
		cfg := &LocalConfig{
			Ledger: &LedgerConfig{Path: "/custom/ledger/path"},
		}
		got := cfg.GetLedgerPath(repoName, projectRoot, endpointURL)
		assert.Equal(t, "/custom/ledger/path", got)
	})
}

func TestLocalConfig_SetLedgerPath(t *testing.T) {
	cfg := &LocalConfig{}
	cfg.SetLedgerPath("/new/ledger/path")

	require.NotNil(t, cfg.Ledger, "expected ledger to be created")
	assert.Equal(t, "/new/ledger/path", cfg.Ledger.Path)
}

func TestLocalConfig_UpdateLedgerLastSync(t *testing.T) {
	t.Run("no-ops when ledger is nil", func(t *testing.T) {
		cfg := &LocalConfig{}
		cfg.UpdateLedgerLastSync()
		assert.Nil(t, cfg.Ledger, "should not create lossy LedgerConfig when nil")
	})

	t.Run("updates existing ledger and preserves path", func(t *testing.T) {
		cfg := &LocalConfig{
			Ledger: &LedgerConfig{Path: "/some/path"},
		}
		before := time.Now().UTC()
		cfg.UpdateLedgerLastSync()
		after := time.Now().UTC()

		require.True(t, cfg.Ledger.HasLastSync(), "expected last_sync to be set")
		assert.Equal(t, "/some/path", cfg.Ledger.Path, "path must be preserved")
		assert.True(t, !cfg.Ledger.LastSync.Before(before) && !cfg.Ledger.LastSync.After(after),
			"last_sync %v not in range [%v, %v]", cfg.Ledger.LastSync, before, after)
	})
}

func TestLocalConfig_GetTeamContext(t *testing.T) {
	cfg := &LocalConfig{
		TeamContexts: []TeamContext{
			{TeamID: "team_a", TeamName: "Team A", Path: "/path/a"},
			{TeamID: "team_b", TeamName: "Team B", Path: "/path/b"},
		},
	}

	t.Run("found", func(t *testing.T) {
		tc := cfg.GetTeamContext("team_a")
		require.NotNil(t, tc, "expected to find team context")
		assert.Equal(t, "Team A", tc.TeamName)
	})

	t.Run("not found", func(t *testing.T) {
		tc := cfg.GetTeamContext("team_c")
		assert.Nil(t, tc, "expected nil for non-existent team")
	})

	t.Run("nil config", func(t *testing.T) {
		var nilCfg *LocalConfig
		tc := nilCfg.GetTeamContext("team_a")
		assert.Nil(t, tc, "expected nil for nil config")
	})
}

func TestLocalConfig_GetTeamContextPath(t *testing.T) {
	endpoint := "https://api.sageox.ai"

	t.Run("configured path", func(t *testing.T) {
		cfg := &LocalConfig{
			TeamContexts: []TeamContext{
				{TeamID: "team_a", Path: "/custom/path"},
			},
		}
		got := cfg.GetTeamContextPath("team_a", endpoint)
		assert.Equal(t, "/custom/path", got)
	})

	t.Run("default path for production", func(t *testing.T) {
		cfg := &LocalConfig{}
		got := cfg.GetTeamContextPath("team_a", endpoint)
		want := DefaultTeamContextPath("team_a", endpoint)
		assert.Equal(t, want, got)
	})

	t.Run("default path for non-production", func(t *testing.T) {
		cfg := &LocalConfig{}
		stagingEndpoint := "https://staging.sageox.ai"
		got := cfg.GetTeamContextPath("team_a", stagingEndpoint)
		want := DefaultTeamContextPath("team_a", stagingEndpoint)
		assert.Equal(t, want, got)
		assert.Contains(t, got, "staging.sageox.ai")
	})
}

func TestLocalConfig_SetTeamContext(t *testing.T) {
	t.Run("add new", func(t *testing.T) {
		cfg := &LocalConfig{}
		cfg.SetTeamContext("team_a", "Team A", "/path/a")

		require.Len(t, cfg.TeamContexts, 1, "team contexts count")
		assert.Equal(t, "team_a", cfg.TeamContexts[0].TeamID, "team id")
		assert.Equal(t, "Team A", cfg.TeamContexts[0].TeamName, "team name")
		assert.Equal(t, "/path/a", cfg.TeamContexts[0].Path, "path")
	})

	t.Run("update existing", func(t *testing.T) {
		cfg := &LocalConfig{
			TeamContexts: []TeamContext{
				{TeamID: "team_a", TeamName: "Old Name", Path: "/old/path"},
			},
		}
		cfg.SetTeamContext("team_a", "New Name", "/new/path")

		require.Len(t, cfg.TeamContexts, 1, "team contexts count")
		assert.Equal(t, "New Name", cfg.TeamContexts[0].TeamName, "team name")
		assert.Equal(t, "/new/path", cfg.TeamContexts[0].Path, "path")
	})
}

func TestLocalConfig_UpdateTeamContextLastSync(t *testing.T) {
	now := time.Now().UTC()
	cfg := &LocalConfig{
		TeamContexts: []TeamContext{
			{TeamID: "team_a", TeamName: "Team A", Path: "/path/a"},
		},
	}

	cfg.UpdateTeamContextLastSync("team_a")

	tc := cfg.GetTeamContext("team_a")
	require.True(t, tc.HasLastSync(), "expected last_sync to be set")
	assert.True(t, !tc.LastSync.Before(now), "last_sync %v is before %v", tc.LastSync, now)

	// update non-existent team should not panic
	cfg.UpdateTeamContextLastSync("team_nonexistent")
}

func TestLocalConfig_RemoveTeamContext(t *testing.T) {
	cfg := &LocalConfig{
		TeamContexts: []TeamContext{
			{TeamID: "team_a", TeamName: "Team A", Path: "/path/a"},
			{TeamID: "team_b", TeamName: "Team B", Path: "/path/b"},
		},
	}

	cfg.RemoveTeamContext("team_a")

	require.Len(t, cfg.TeamContexts, 1, "team contexts count")
	assert.Equal(t, "team_b", cfg.TeamContexts[0].TeamID, "remaining team id")

	// remove non-existent should not panic
	cfg.RemoveTeamContext("team_nonexistent")
	assert.Len(t, cfg.TeamContexts, 1, "count after removing non-existent")
}

func TestSanitizeRepoName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple", "my-project", "my-project"},
		{"with spaces", "my project", "my_project"},
		{"with slashes", "org/repo", "org_repo"},
		{"with special chars", "my:project*name", "my_project_name"},
		{"empty", "", "unknown"},
		{"only special chars", "***", "unknown"},
		{"leading dots", "...name", "name"},
		{"trailing underscores", "name___", "name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeRepoName(tt.input)
			assert.Equal(t, tt.want, got, "sanitizeRepoName(%q)", tt.input)
		})
	}
}

func TestLocalConfigFilePermissions(t *testing.T) {
	tmpDir := CreateInitializedProject(t)

	cfg := &LocalConfig{
		Ledger: &LedgerConfig{Path: "/path/to/ledger"},
	}

	require.NoError(t, SaveLocalConfig(tmpDir, cfg), "save failed")

	configPath := filepath.Join(tmpDir, sageoxDir, localConfigFilename)
	info, err := os.Stat(configPath)
	require.NoError(t, err, "stat failed")

	// verify file permissions are 0600 (owner read/write only)
	perm := info.Mode().Perm()
	assert.Equal(t, os.FileMode(0600), perm, "file permissions")
}

func TestHasLastSync(t *testing.T) {
	t.Run("ledger nil", func(t *testing.T) {
		var lc *LedgerConfig
		assert.False(t, lc.HasLastSync(), "expected false for nil ledger config")
	})

	t.Run("ledger zero time", func(t *testing.T) {
		lc := &LedgerConfig{Path: "/some/path"}
		assert.False(t, lc.HasLastSync(), "expected false for zero time")
	})

	t.Run("ledger with time", func(t *testing.T) {
		lc := &LedgerConfig{Path: "/some/path", LastSync: time.Now()}
		assert.True(t, lc.HasLastSync(), "expected true for set time")
	})

	t.Run("team context nil", func(t *testing.T) {
		var tc *TeamContext
		assert.False(t, tc.HasLastSync(), "expected false for nil team context")
	})

	t.Run("team context zero time", func(t *testing.T) {
		tc := &TeamContext{TeamID: "team_a"}
		assert.False(t, tc.HasLastSync(), "expected false for zero time")
	})

	t.Run("team context with time", func(t *testing.T) {
		tc := &TeamContext{TeamID: "team_a", LastSync: time.Now()}
		assert.True(t, tc.HasLastSync(), "expected true for set time")
	})
}

// -----------------------------------------------------------------------------
// Team Symlink Tests
// -----------------------------------------------------------------------------

func TestDefaultTeamSymlinkPath(t *testing.T) {
	tests := []struct {
		name        string
		repoName    string
		projectRoot string
		endpointURL string
		teamID      string
		want        string
		wantEmpty   bool
	}{
		{
			name:        "production endpoint",
			repoName:    "my-project",
			projectRoot: "/home/user/code/my-project",
			endpointURL: "https://api.sageox.ai",
			teamID:      "team_abc123",
			want:        "/home/user/code/my-project_sageox/sageox.ai/teams/team_abc123",
		},
		{
			name:        "staging endpoint",
			repoName:    "my-project",
			projectRoot: "/home/user/code/my-project",
			endpointURL: "https://staging.sageox.ai",
			teamID:      "team_abc123",
			want:        "/home/user/code/my-project_sageox/staging.sageox.ai/teams/team_abc123",
		},
		{
			name:        "localhost with port",
			repoName:    "my-project",
			projectRoot: "/home/user/code/my-project",
			endpointURL: "http://localhost:8080",
			teamID:      "team_abc123",
			want:        "/home/user/code/my-project_sageox/localhost/teams/team_abc123",
		},
		{
			name:        "empty project root",
			repoName:    "my-project",
			projectRoot: "",
			endpointURL: "https://api.sageox.ai",
			teamID:      "team_abc123",
			wantEmpty:   true,
		},
		{
			name:        "empty team id",
			repoName:    "my-project",
			projectRoot: "/home/user/code/my-project",
			endpointURL: "https://api.sageox.ai",
			teamID:      "",
			wantEmpty:   true,
		},
		{
			name:        "team id with special chars",
			repoName:    "my-project",
			projectRoot: "/home/user/code/my-project",
			endpointURL: "https://api.sageox.ai",
			teamID:      "team/with:special*chars",
			want:        "/home/user/code/my-project_sageox/sageox.ai/teams/team_with_special_chars",
		},
		{
			name:        "empty endpoint defaults to production",
			repoName:    "my-project",
			projectRoot: "/home/user/code/my-project",
			endpointURL: "",
			teamID:      "team_abc123",
			want:        "/home/user/code/my-project_sageox/sageox.ai/teams/team_abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DefaultTeamSymlinkPath(tt.repoName, tt.projectRoot, tt.endpointURL, tt.teamID)

			if tt.wantEmpty {
				assert.Empty(t, got, "expected empty path")
				return
			}

			assert.Equal(t, tt.want, got)
		})
	}
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
		assert.Error(t, err, "should error when path exists and is not a symlink")
		assert.Contains(t, err.Error(), "not a symlink")
	})

	t.Run("error on empty project root", func(t *testing.T) {
		err := CreateTeamSymlink(repoName, "", "team_abc", "https://api.sageox.ai")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "project root cannot be empty")
	})

	t.Run("error on empty team id", func(t *testing.T) {
		err := CreateTeamSymlink(repoName, t.TempDir(), "", "https://api.sageox.ai")
		assert.Error(t, err)
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

func TestRemoveTeamSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests skipped on Windows")
	}

	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	repoName := "test-repo"

	t.Run("removes existing symlink", func(t *testing.T) {
		projectRoot := t.TempDir()
		teamID := "team_remove"
		ep := "https://api.sageox.ai"

		// create symlink first
		require.NoError(t, CreateTeamSymlink(repoName, projectRoot, teamID, ep))

		// verify it exists
		symlinkPath := DefaultTeamSymlinkPath(repoName, projectRoot, ep, teamID)
		_, err := os.Lstat(symlinkPath)
		require.NoError(t, err, "symlink should exist before removal")

		// remove it
		err = RemoveTeamSymlink(repoName, projectRoot, teamID, ep)
		require.NoError(t, err)

		// verify it's gone
		_, err = os.Lstat(symlinkPath)
		assert.True(t, os.IsNotExist(err), "symlink should be removed")
	})

	t.Run("succeeds if symlink does not exist", func(t *testing.T) {
		projectRoot := t.TempDir()
		err := RemoveTeamSymlink(repoName, projectRoot, "nonexistent_team", "https://api.sageox.ai")
		assert.NoError(t, err, "should not error for non-existent symlink")
	})

	t.Run("error if path is not a symlink", func(t *testing.T) {
		projectRoot := t.TempDir()
		teamID := "team_not_symlink"
		ep := "https://api.sageox.ai"

		symlinkPath := DefaultTeamSymlinkPath(repoName, projectRoot, ep, teamID)

		// create parent directory and a regular file
		require.NoError(t, os.MkdirAll(filepath.Dir(symlinkPath), 0755))
		require.NoError(t, os.WriteFile(symlinkPath, []byte("file content"), 0644))

		err := RemoveTeamSymlink(repoName, projectRoot, teamID, ep)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not a symlink")
	})

	t.Run("nil-safe for empty inputs", func(t *testing.T) {
		assert.NoError(t, RemoveTeamSymlink(repoName, "", "team", "ep"), "empty project root")
		assert.NoError(t, RemoveTeamSymlink(repoName, "/tmp", "", "ep"), "empty team id")
	})

	t.Run("cleans up empty parent directories", func(t *testing.T) {
		projectRoot := t.TempDir()
		teamID := "team_cleanup"
		ep := "https://api.sageox.ai"

		// create symlink
		require.NoError(t, CreateTeamSymlink(repoName, projectRoot, teamID, ep))

		// remove symlink
		require.NoError(t, RemoveTeamSymlink(repoName, projectRoot, teamID, ep))

		// verify parent directories were cleaned up - path is <repo>_sageox/sageox.ai/teams
		siblingDir := DefaultSageoxSiblingDir(repoName, projectRoot)
		teamsDir := filepath.Join(siblingDir, "sageox.ai", "teams")
		_, err := os.Stat(teamsDir)
		assert.True(t, os.IsNotExist(err), "empty teams directory should be cleaned up")
	})
}

func TestUpdateTeamSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests skipped on Windows")
	}

	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	repoName := "test-repo"

	t.Run("updates symlink to new endpoint", func(t *testing.T) {
		projectRoot := t.TempDir()
		teamID := "team_update"
		oldEndpoint := "https://api.sageox.ai"
		newEndpoint := "https://staging.sageox.ai"

		// create with old endpoint
		require.NoError(t, CreateTeamSymlink(repoName, projectRoot, teamID, oldEndpoint))

		// update to new endpoint
		err := UpdateTeamSymlink(repoName, projectRoot, teamID, oldEndpoint, newEndpoint)
		require.NoError(t, err)

		// verify old symlink path is gone
		oldSymlinkPath := DefaultTeamSymlinkPath(repoName, projectRoot, oldEndpoint, teamID)
		_, err = os.Lstat(oldSymlinkPath)
		assert.True(t, os.IsNotExist(err), "old symlink path should be removed")

		// verify new symlink exists
		newSymlinkPath := DefaultTeamSymlinkPath(repoName, projectRoot, newEndpoint, teamID)
		info, err := os.Lstat(newSymlinkPath)
		require.NoError(t, err, "new symlink should exist")
		assert.True(t, info.Mode()&os.ModeSymlink != 0)
	})

	t.Run("error on empty project root", func(t *testing.T) {
		err := UpdateTeamSymlink(repoName, "", "team_abc", "old", "https://api.sageox.ai")
		assert.Error(t, err)
	})

	t.Run("error on empty team id", func(t *testing.T) {
		err := UpdateTeamSymlink(repoName, t.TempDir(), "", "old", "https://api.sageox.ai")
		assert.Error(t, err)
	})
}
