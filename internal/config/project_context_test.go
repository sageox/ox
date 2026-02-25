package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -----------------------------------------------------------------------------
// TestLoadProjectContext
// -----------------------------------------------------------------------------

func TestLoadProjectContext_ValidProjectRoot(t *testing.T) {
	t.Parallel()
	tmpDir := CreateInitializedProject(t)

	// create project config
	cfg := &ProjectConfig{
		Org:      "test-org",
		Team:     "test-team",
		Project:  "test-project",
		Endpoint: "https://staging.sageox.ai",
	}
	require.NoError(t, SaveProjectConfig(tmpDir, cfg))

	// create local config
	localCfg := &LocalConfig{
		Ledger: &LedgerConfig{
			Path: "/custom/ledger/path",
		},
	}
	require.NoError(t, SaveLocalConfig(tmpDir, localCfg))

	// load project context
	ctx, err := LoadProjectContext(tmpDir)
	require.NoError(t, err)
	require.NotNil(t, ctx)

	// verify config was loaded
	assert.Equal(t, "test-org", ctx.Config().Org)
	assert.Equal(t, "test-team", ctx.Config().Team)
	assert.Equal(t, "test-project", ctx.Config().Project)
	assert.Equal(t, "https://staging.sageox.ai", ctx.Config().Endpoint)

	// verify local config was loaded
	require.NotNil(t, ctx.LocalConfig().Ledger)
	assert.Equal(t, "/custom/ledger/path", ctx.LocalConfig().Ledger.Path)
}

func TestLoadProjectContext_EmptyProjectRoot(t *testing.T) {
	t.Parallel()

	ctx, err := LoadProjectContext("")
	assert.Error(t, err, "expected error for empty project root")
	assert.Nil(t, ctx)
	assert.Contains(t, err.Error(), "project root cannot be empty")
}

func TestLoadProjectContext_NonExistentProjectRoot(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	// do not create any config files, LoadProjectContext should return defaults
	ctx, err := LoadProjectContext(tmpDir)
	require.NoError(t, err, "should not error for non-existent config")
	require.NotNil(t, ctx)

	// should have default project config
	assert.Equal(t, defaultUpdateFrequencyHours, ctx.Config().UpdateFrequencyHours)

	// local config should be empty but not nil
	require.NotNil(t, ctx.LocalConfig())
	assert.Nil(t, ctx.LocalConfig().Ledger)
}

func TestLoadProjectContext_OnlyProjectConfigExists(t *testing.T) {
	t.Parallel()
	tmpDir := CreateInitializedProject(t)

	// create only project config, no local config
	cfg := &ProjectConfig{
		Org:     "only-org",
		Team:    "only-team",
		Project: "only-project",
	}
	require.NoError(t, SaveProjectConfig(tmpDir, cfg))

	// load project context
	ctx, err := LoadProjectContext(tmpDir)
	require.NoError(t, err)
	require.NotNil(t, ctx)

	// verify project config was loaded
	assert.Equal(t, "only-org", ctx.Config().Org)

	// local config should be empty but not nil
	require.NotNil(t, ctx.LocalConfig())
	assert.Nil(t, ctx.LocalConfig().Ledger)
	assert.Empty(t, ctx.LocalConfig().TeamContexts)
}

// -----------------------------------------------------------------------------
// TestProjectContext_Endpoint
// -----------------------------------------------------------------------------

func TestProjectContext_Endpoint_FromProjectConfig(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	// create project config with custom endpoint
	cfg := &ProjectConfig{
		Endpoint: "https://custom.sageox.ai",
	}
	require.NoError(t, SaveProjectConfig(tmpDir, cfg))

	ctx, err := LoadProjectContext(tmpDir)
	require.NoError(t, err)

	// should return endpoint from project config
	assert.Equal(t, "https://custom.sageox.ai", ctx.Endpoint())
}

func TestProjectContext_Endpoint_NilConfig(t *testing.T) {
	t.Parallel()

	// construct ProjectContext directly with nil config
	ctx := &ProjectContext{
		root:   "/some/path",
		config: nil,
	}

	// should return empty string when config is nil
	assert.Empty(t, ctx.Endpoint())
}

func TestProjectContext_Endpoint_FallbackToDefault(t *testing.T) {
	// cannot use t.Parallel() with t.Setenv()
	tmpDir := t.TempDir()

	// create project config without endpoint
	cfg := &ProjectConfig{
		Org:  "test-org",
		Team: "test-team",
	}
	require.NoError(t, SaveProjectConfig(tmpDir, cfg))

	// clear any env var
	t.Setenv("SAGEOX_ENDPOINT", "")

	ctx, err := LoadProjectContext(tmpDir)
	require.NoError(t, err)

	// should fall back to default production endpoint via GetEndpoint()
	assert.Equal(t, "https://sageox.ai", ctx.Endpoint())
}

func TestProjectContext_Endpoint_EnvVarOverride(t *testing.T) {
	// cannot use t.Parallel() with t.Setenv()
	tmpDir := t.TempDir()

	// create project config with endpoint
	cfg := &ProjectConfig{
		Endpoint: "https://config.sageox.ai",
	}
	require.NoError(t, SaveProjectConfig(tmpDir, cfg))

	// set env var - but GetEndpoint prefers config.Endpoint first
	t.Setenv("SAGEOX_ENDPOINT", "https://env.sageox.ai")

	ctx, err := LoadProjectContext(tmpDir)
	require.NoError(t, err)

	// GetEndpoint respects config.Endpoint first, not env var
	// (env var is checked by endpoint.Get(), but config.GetEndpoint prefers config.Endpoint)
	assert.Equal(t, "https://config.sageox.ai", ctx.Endpoint())
}

// -----------------------------------------------------------------------------
// TestProjectContext_PathHelpers
// -----------------------------------------------------------------------------

func TestProjectContext_TeamContextDir(t *testing.T) {
	// cannot use t.Parallel() with t.Setenv()
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tempHome, ".local", "share"))
	t.Setenv("OX_XDG_DISABLE", "") // XDG mode

	tmpDir := t.TempDir()
	cfg := &ProjectConfig{
		Endpoint: "https://api.sageox.ai",
	}
	require.NoError(t, SaveProjectConfig(tmpDir, cfg))

	ctx, err := LoadProjectContext(tmpDir)
	require.NoError(t, err)

	teamDir := ctx.TeamContextDir("team_abc123")
	assert.Contains(t, teamDir, "team_abc123")
	assert.Contains(t, teamDir, "sageox.ai")
	assert.Contains(t, teamDir, "teams")
}

func TestProjectContext_TeamContextDir_NonProduction(t *testing.T) {
	// cannot use t.Parallel() with t.Setenv()
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tempHome, ".local", "share"))
	t.Setenv("OX_XDG_DISABLE", "")

	tmpDir := t.TempDir()
	cfg := &ProjectConfig{
		Endpoint: "https://staging.sageox.ai",
	}
	require.NoError(t, SaveProjectConfig(tmpDir, cfg))

	ctx, err := LoadProjectContext(tmpDir)
	require.NoError(t, err)

	teamDir := ctx.TeamContextDir("team_staging")
	assert.Contains(t, teamDir, "team_staging")
	assert.Contains(t, teamDir, "staging.sageox.ai")
}

func TestProjectContext_LedgerDir(t *testing.T) {
	// cannot use t.Parallel() with t.Setenv()
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tempHome, ".local", "share"))
	t.Setenv("OX_XDG_DISABLE", "")

	tmpDir := t.TempDir()
	cfg := &ProjectConfig{
		Endpoint: "https://api.sageox.ai",
	}
	require.NoError(t, SaveProjectConfig(tmpDir, cfg))

	ctx, err := LoadProjectContext(tmpDir)
	require.NoError(t, err)

	ledgerDir := ctx.LedgerDir("repo_01abc123")
	assert.Contains(t, ledgerDir, "repo_01abc123")
	assert.Contains(t, ledgerDir, "sageox.ai")
	assert.Contains(t, ledgerDir, "ledgers")
}

func TestProjectContext_LedgerDir_NonProduction(t *testing.T) {
	// cannot use t.Parallel() with t.Setenv()
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tempHome, ".local", "share"))
	t.Setenv("OX_XDG_DISABLE", "")

	tmpDir := t.TempDir()
	cfg := &ProjectConfig{
		Endpoint: "https://staging.sageox.ai",
	}
	require.NoError(t, SaveProjectConfig(tmpDir, cfg))

	ctx, err := LoadProjectContext(tmpDir)
	require.NoError(t, err)

	ledgerDir := ctx.LedgerDir("repo_staging123")
	assert.Contains(t, ledgerDir, "repo_staging123")
	assert.Contains(t, ledgerDir, "staging.sageox.ai")
}

func TestProjectContext_TeamsDataDir(t *testing.T) {
	// cannot use t.Parallel() with t.Setenv()
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tempHome, ".local", "share"))
	t.Setenv("OX_XDG_DISABLE", "")

	tmpDir := t.TempDir()
	cfg := &ProjectConfig{
		Endpoint: "https://api.sageox.ai",
	}
	require.NoError(t, SaveProjectConfig(tmpDir, cfg))

	ctx, err := LoadProjectContext(tmpDir)
	require.NoError(t, err)

	teamsDir := ctx.TeamsDataDir()
	assert.Contains(t, teamsDir, "sageox.ai")
	assert.Contains(t, teamsDir, "teams")
}

func TestProjectContext_SiblingDir(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	// create project at tmpDir/my-project
	projectDir := filepath.Join(tmpDir, "my-project")
	require.NoError(t, os.MkdirAll(projectDir, 0755))

	cfg := &ProjectConfig{
		Org: "test-org",
	}
	require.NoError(t, SaveProjectConfig(projectDir, cfg))

	ctx, err := LoadProjectContext(projectDir)
	require.NoError(t, err)

	siblingDir := ctx.SiblingDir()
	assert.Equal(t, filepath.Join(tmpDir, "my-project_sageox"), siblingDir)
}

func TestProjectContext_DefaultLedgerPath(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	// create project at tmpDir/test-repo
	projectDir := filepath.Join(tmpDir, "test-repo")
	require.NoError(t, os.MkdirAll(projectDir, 0755))

	cfg := &ProjectConfig{
		Endpoint: "https://api.sageox.ai",
		RepoID:   "repo_01jfk3mab",
	}
	require.NoError(t, SaveProjectConfig(projectDir, cfg))

	ctx, err := LoadProjectContext(projectDir)
	require.NoError(t, err)

	ledgerPath := ctx.DefaultLedgerPath()
	assert.Contains(t, ledgerPath, "sageox.ai")
	assert.Contains(t, ledgerPath, "ledgers")
	assert.Contains(t, ledgerPath, "repo_01jfk3mab")
}

func TestProjectContext_DefaultLedgerPath_NonProduction(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	projectDir := filepath.Join(tmpDir, "staging-repo")
	require.NoError(t, os.MkdirAll(projectDir, 0755))

	cfg := &ProjectConfig{
		Endpoint: "https://staging.sageox.ai",
		RepoID:   "repo_staging123",
	}
	require.NoError(t, SaveProjectConfig(projectDir, cfg))

	ctx, err := LoadProjectContext(projectDir)
	require.NoError(t, err)

	ledgerPath := ctx.DefaultLedgerPath()
	assert.Contains(t, ledgerPath, "staging.sageox.ai")
	assert.Contains(t, ledgerPath, "ledgers")
	assert.Contains(t, ledgerPath, "repo_staging123")
}

func TestProjectContext_DefaultLedgerPath_EmptyRepoID(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	projectDir := filepath.Join(tmpDir, "no-repo-id")
	require.NoError(t, os.MkdirAll(projectDir, 0755))

	cfg := &ProjectConfig{
		Endpoint: "https://sageox.ai",
	}
	require.NoError(t, SaveProjectConfig(projectDir, cfg))

	ctx, err := LoadProjectContext(projectDir)
	require.NoError(t, err)

	// no repo ID means empty ledger path
	ledgerPath := ctx.DefaultLedgerPath()
	assert.Empty(t, ledgerPath)
}

// -----------------------------------------------------------------------------
// TestProjectContext_Accessors
// -----------------------------------------------------------------------------

func TestProjectContext_Root(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	cfg := &ProjectConfig{Org: "test"}
	require.NoError(t, SaveProjectConfig(tmpDir, cfg))

	ctx, err := LoadProjectContext(tmpDir)
	require.NoError(t, err)

	assert.Equal(t, tmpDir, ctx.Root())
}

func TestProjectContext_Config(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	cfg := &ProjectConfig{
		Org:       "accessor-org",
		Team:      "accessor-team",
		Project:   "accessor-project",
		ProjectID: "prj_12345",
		RepoID:    "repo_abc123",
	}
	require.NoError(t, SaveProjectConfig(tmpDir, cfg))

	ctx, err := LoadProjectContext(tmpDir)
	require.NoError(t, err)

	config := ctx.Config()
	require.NotNil(t, config)
	assert.Equal(t, "accessor-org", config.Org)
	assert.Equal(t, "accessor-team", config.Team)
	assert.Equal(t, "accessor-project", config.Project)
	assert.Equal(t, "prj_12345", config.ProjectID)
	assert.Equal(t, "repo_abc123", config.RepoID)
}

func TestProjectContext_LocalConfig(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	// create project config first
	cfg := &ProjectConfig{Org: "test"}
	require.NoError(t, SaveProjectConfig(tmpDir, cfg))

	// create local config
	localCfg := &LocalConfig{
		Ledger: &LedgerConfig{
			Path: "/path/to/ledger",
		},
		TeamContexts: []TeamContext{
			{TeamID: "team_1", TeamName: "Team One", Path: "/path/team1"},
		},
	}
	require.NoError(t, SaveLocalConfig(tmpDir, localCfg))

	ctx, err := LoadProjectContext(tmpDir)
	require.NoError(t, err)

	local := ctx.LocalConfig()
	require.NotNil(t, local)
	require.NotNil(t, local.Ledger)
	assert.Equal(t, "/path/to/ledger", local.Ledger.Path)
	require.Len(t, local.TeamContexts, 1)
	assert.Equal(t, "team_1", local.TeamContexts[0].TeamID)
}

func TestProjectContext_RepoName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		projectPath string
		want        string
	}{
		{
			name:        "simple name",
			projectPath: "my-project",
			want:        "my-project",
		},
		{
			name:        "nested path",
			projectPath: "deeply/nested/awesome-repo",
			want:        "awesome-repo",
		},
		{
			name:        "path with special chars",
			projectPath: "org_repo-v2",
			want:        "org_repo-v2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tmpDir := t.TempDir()

			projectDir := filepath.Join(tmpDir, tt.projectPath)
			require.NoError(t, os.MkdirAll(projectDir, 0755))

			cfg := &ProjectConfig{Org: "test"}
			require.NoError(t, SaveProjectConfig(projectDir, cfg))

			ctx, err := LoadProjectContext(projectDir)
			require.NoError(t, err)

			assert.Equal(t, tt.want, ctx.RepoName())
		})
	}
}

func TestProjectContext_RepoID(t *testing.T) {
	t.Parallel()

	t.Run("with repo ID", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		cfg := &ProjectConfig{
			RepoID: "repo_01jfk3mab123",
		}
		require.NoError(t, SaveProjectConfig(tmpDir, cfg))

		ctx, err := LoadProjectContext(tmpDir)
		require.NoError(t, err)

		assert.Equal(t, "repo_01jfk3mab123", ctx.RepoID())
	})

	t.Run("without repo ID", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		cfg := &ProjectConfig{
			Org: "test",
		}
		require.NoError(t, SaveProjectConfig(tmpDir, cfg))

		ctx, err := LoadProjectContext(tmpDir)
		require.NoError(t, err)

		assert.Empty(t, ctx.RepoID())
	})

	t.Run("nil config", func(t *testing.T) {
		t.Parallel()
		ctx := &ProjectContext{
			root:   "/some/path",
			config: nil,
		}
		assert.Empty(t, ctx.RepoID())
	})
}

// -----------------------------------------------------------------------------
// Edge Cases and Error Handling
// -----------------------------------------------------------------------------

func TestProjectContext_PathHelpers_WithEmptyEndpoint(t *testing.T) {
	// cannot use t.Parallel() with t.Setenv()
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tempHome, ".local", "share"))
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("SAGEOX_ENDPOINT", "") // ensure no env override

	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "empty-endpoint-repo")
	require.NoError(t, os.MkdirAll(projectDir, 0755))

	// config with no endpoint set but with repo ID
	cfg := &ProjectConfig{
		Org:    "test",
		RepoID: "repo_test_endpoint",
	}
	require.NoError(t, SaveProjectConfig(projectDir, cfg))

	ctx, err := LoadProjectContext(projectDir)
	require.NoError(t, err)

	// endpoint should fall back to production
	assert.Equal(t, "https://sageox.ai", ctx.Endpoint())

	// path helpers should work with production endpoint
	teamsDir := ctx.TeamsDataDir()
	assert.Contains(t, teamsDir, "sageox.ai")

	teamDir := ctx.TeamContextDir("team_test")
	assert.Contains(t, teamDir, "team_test")
	assert.Contains(t, teamDir, "sageox.ai")

	ledgerDir := ctx.LedgerDir("repo_test")
	assert.Contains(t, ledgerDir, "repo_test")
	assert.Contains(t, ledgerDir, "sageox.ai")

	ledgerPath := ctx.DefaultLedgerPath()
	assert.Contains(t, ledgerPath, "sageox.ai")
	assert.Contains(t, ledgerPath, "repo_test_endpoint")
}

func TestProjectContext_LocalhostEndpoint(t *testing.T) {
	// cannot use t.Parallel() with t.Setenv()
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tempHome, ".local", "share"))
	t.Setenv("OX_XDG_DISABLE", "")

	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "localhost-repo")
	require.NoError(t, os.MkdirAll(projectDir, 0755))

	cfg := &ProjectConfig{
		Endpoint: "http://localhost:8080",
		RepoID:   "repo_localhost_test",
	}
	require.NoError(t, SaveProjectConfig(projectDir, cfg))

	ctx, err := LoadProjectContext(projectDir)
	require.NoError(t, err)

	// localhost endpoint paths should use "localhost" slug (port stripped)
	teamsDir := ctx.TeamsDataDir()
	assert.Contains(t, teamsDir, "localhost")
	assert.NotContains(t, teamsDir, "8080") // port should be stripped

	ledgerPath := ctx.DefaultLedgerPath()
	assert.Contains(t, ledgerPath, "localhost")
	assert.NotContains(t, ledgerPath, "8080")
	assert.Contains(t, ledgerPath, "repo_localhost_test")
}

func TestProjectContext_ConfigDefaults(t *testing.T) {
	t.Parallel()
	tmpDir := CreateInitializedProject(t)

	// create minimal config - defaults should be applied
	minimalConfig := `{"org":"minimal-org"}`
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, sageoxDir, projectConfigFilename),
		[]byte(minimalConfig),
		0600,
	))

	ctx, err := LoadProjectContext(tmpDir)
	require.NoError(t, err)

	// verify defaults were applied
	assert.Equal(t, defaultUpdateFrequencyHours, ctx.Config().UpdateFrequencyHours)
	assert.Equal(t, "minimal-org", ctx.Config().Org)
}
