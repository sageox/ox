package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildStatusJSON_WithRepoDetail(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	localCfg := &config.LocalConfig{
		Ledger: &config.LedgerConfig{Path: tmpDir},
	}

	repoDetail := &api.RepoDetailResponse{
		Visibility:  "private",
		AccessLevel: "member",
	}

	output := buildStatusJSON(
		false, nil, "test.sageox.ai", "/tmp/auth.json", false,
		"/tmp/config", "/tmp/cwd", "/tmp/cwd/.sageox", false,
		localCfg, "", repoDetail,
	)

	require.NotNil(t, output.Ledger, "ledger section should be populated when localCfg.Ledger has a path")
	assert.Equal(t, "private", output.Ledger.Visibility)
	assert.Equal(t, "member", output.Ledger.AccessLevel)
	assert.True(t, output.Ledger.Configured)
	assert.Equal(t, tmpDir, output.Ledger.Path)
}

func TestBuildStatusJSON_WithoutRepoDetail(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	localCfg := &config.LocalConfig{
		Ledger: &config.LedgerConfig{Path: tmpDir},
	}

	output := buildStatusJSON(
		false, nil, "test.sageox.ai", "/tmp/auth.json", false,
		"/tmp/config", "/tmp/cwd", "/tmp/cwd/.sageox", false,
		localCfg, "", nil,
	)

	require.NotNil(t, output.Ledger, "ledger section should be populated even without repoDetail")
	assert.Empty(t, output.Ledger.Visibility, "visibility should be empty when repoDetail is nil")
	assert.Empty(t, output.Ledger.AccessLevel, "access_level should be empty when repoDetail is nil")
	assert.True(t, output.Ledger.Configured)
}

func TestBuildStatusJSON_ViewerAccess(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	localCfg := &config.LocalConfig{
		Ledger: &config.LedgerConfig{Path: tmpDir},
	}

	repoDetail := &api.RepoDetailResponse{
		Visibility:  "public",
		AccessLevel: "viewer",
	}

	output := buildStatusJSON(
		false, nil, "test.sageox.ai", "/tmp/auth.json", false,
		"/tmp/config", "/tmp/cwd", "/tmp/cwd/.sageox", false,
		localCfg, "", repoDetail,
	)

	require.NotNil(t, output.Ledger)
	assert.Equal(t, "public", output.Ledger.Visibility)
	assert.Equal(t, "viewer", output.Ledger.AccessLevel)
}

func TestBuildStatusJSON_NoLedgerConfig(t *testing.T) {
	t.Parallel()

	// no ledger configured means repoDetail fields have no place to land
	localCfg := &config.LocalConfig{}

	repoDetail := &api.RepoDetailResponse{
		Visibility:  "private",
		AccessLevel: "member",
	}

	output := buildStatusJSON(
		false, nil, "test.sageox.ai", "/tmp/auth.json", false,
		"/tmp/config", "/tmp/cwd", "/tmp/cwd/.sageox", false,
		localCfg, "", repoDetail,
	)

	assert.Nil(t, output.Ledger, "ledger section should be nil when no ledger config exists")
}

func TestBuildStatusJSON_NilLocalConfig(t *testing.T) {
	t.Parallel()

	output := buildStatusJSON(
		false, nil, "test.sageox.ai", "/tmp/auth.json", false,
		"/tmp/config", "/tmp/cwd", "/tmp/cwd/.sageox", false,
		nil, "", nil,
	)

	assert.Nil(t, output.Ledger, "ledger section should be nil when localCfg is nil")
	assert.Nil(t, output.TeamContexts, "team_contexts should be nil when localCfg is nil")
}

func TestBuildStatusJSON_LedgerPathNotExists(t *testing.T) {
	t.Parallel()

	// path that doesn't exist triggers "not found" error in getGitRepoStatus
	localCfg := &config.LocalConfig{
		Ledger: &config.LedgerConfig{Path: "/nonexistent/path/ledger"},
	}

	repoDetail := &api.RepoDetailResponse{
		Visibility:  "private",
		AccessLevel: "viewer",
	}

	output := buildStatusJSON(
		false, nil, "test.sageox.ai", "/tmp/auth.json", false,
		"/tmp/config", "/tmp/cwd", "/tmp/cwd/.sageox", false,
		localCfg, "", repoDetail,
	)

	require.NotNil(t, output.Ledger)
	assert.False(t, output.Ledger.Exists, "ledger should not exist for nonexistent path")
	assert.Equal(t, "not found", output.Ledger.Error)
	// visibility/access are still populated from repoDetail regardless of local state
	assert.Equal(t, "private", output.Ledger.Visibility)
	assert.Equal(t, "viewer", output.Ledger.AccessLevel)
}

func TestBuildStatusJSON_AuthenticatedWithToken(t *testing.T) {
	t.Parallel()

	expires := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	token := &auth.StoredToken{
		AccessToken: "test-token",
		ExpiresAt:   expires,
		UserInfo: auth.UserInfo{
			UserID: "user_123",
			Email:  "person@example.com",
			Name:   "Person A",
		},
	}

	output := buildStatusJSON(
		true, token, "test.sageox.ai", "/tmp/auth.json", true,
		"/tmp/config", "/tmp/cwd", "/tmp/cwd/.sageox", true,
		nil, "", nil,
	)

	require.NotNil(t, output.Auth)
	assert.True(t, output.Auth.Authenticated)
	assert.Equal(t, "Person A", output.Auth.User)
	assert.Equal(t, "person@example.com", output.Auth.Email)
	assert.Equal(t, &expires, output.Auth.ExpiresAt)
}

func TestBuildStatusJSON_ProjectInitialized(t *testing.T) {
	t.Parallel()

	output := buildStatusJSON(
		false, nil, "test.sageox.ai", "/tmp/auth.json", false,
		"/tmp/config", "/tmp/cwd", "/tmp/cwd/.sageox", true,
		nil, "", nil,
	)

	require.NotNil(t, output.Project)
	assert.True(t, output.Project.Initialized)
	assert.Equal(t, "/tmp/cwd/.sageox", output.Project.ConfigPath)
}

func TestBuildStatusJSON_ProjectNotInitialized(t *testing.T) {
	t.Parallel()

	output := buildStatusJSON(
		false, nil, "test.sageox.ai", "/tmp/auth.json", false,
		"/tmp/config", "/tmp/cwd", "/tmp/cwd/.sageox", false,
		nil, "", nil,
	)

	require.NotNil(t, output.Project)
	assert.False(t, output.Project.Initialized)
	assert.Empty(t, output.Project.ConfigPath, "config path should be empty when not initialized")
}

func TestBuildStatusJSON_VisibilityAccessLevelCombinations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		visibility  string
		accessLevel string
	}{
		{
			name:        "private repo with member access",
			visibility:  "private",
			accessLevel: "member",
		},
		{
			name:        "private repo with viewer access",
			visibility:  "private",
			accessLevel: "viewer",
		},
		{
			name:        "public repo with member access",
			visibility:  "public",
			accessLevel: "member",
		},
		{
			name:        "public repo with viewer access",
			visibility:  "public",
			accessLevel: "viewer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			localCfg := &config.LocalConfig{
				Ledger: &config.LedgerConfig{Path: tmpDir},
			}
			repoDetail := &api.RepoDetailResponse{
				Visibility:  tt.visibility,
				AccessLevel: tt.accessLevel,
			}

			output := buildStatusJSON(
				false, nil, "test.sageox.ai", "/tmp/auth.json", false,
				"/tmp/config", "/tmp/cwd", "/tmp/cwd/.sageox", false,
				localCfg, "", repoDetail,
			)

			require.NotNil(t, output.Ledger)
			assert.Equal(t, tt.visibility, output.Ledger.Visibility)
			assert.Equal(t, tt.accessLevel, output.Ledger.AccessLevel)
		})
	}
}

func TestShortenPathViaSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require Developer Mode on Windows")
	}
	t.Parallel()

	target := filepath.Join(t.TempDir(), "data", "ledger")
	require.NoError(t, os.MkdirAll(target, 0755))

	projectRoot := filepath.Join(t.TempDir(), "project")
	sageoxDir := filepath.Join(projectRoot, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	require.NoError(t, os.Symlink(target, filepath.Join(sageoxDir, "ledger")))

	tests := []struct {
		name       string
		root       string
		fullPath   string
		candidates []string
		want       string
	}{
		{
			name:       "symlink matches",
			root:       projectRoot,
			fullPath:   target,
			candidates: []string{".sageox/ledger"},
			want:       ".sageox/ledger",
		},
		{
			name:       "no match returns full path",
			root:       projectRoot,
			fullPath:   "/some/other/path",
			candidates: []string{".sageox/ledger"},
			want:       "/some/other/path",
		},
		{
			name:       "empty root returns full path",
			root:       "",
			fullPath:   target,
			candidates: []string{".sageox/ledger"},
			want:       target,
		},
		{
			name:       "empty fullPath returns empty",
			root:       projectRoot,
			fullPath:   "",
			candidates: []string{".sageox/ledger"},
			want:       "",
		},
		{
			name:       "nonexistent symlink returns full path",
			root:       projectRoot,
			fullPath:   target,
			candidates: []string{".sageox/teams/primary"},
			want:       target,
		},
		{
			name:       "first matching candidate wins",
			root:       projectRoot,
			fullPath:   target,
			candidates: []string{".sageox/teams/primary", ".sageox/ledger"},
			want:       ".sageox/ledger",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shortenPathViaSymlink(tt.root, tt.fullPath, tt.candidates...)
			assert.Equal(t, tt.want, got)
		})
	}
}
