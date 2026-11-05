package identity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- readGHHostsConfig ---
// Prevents losing GitHub token when hosts.yml exists but has no github.com entry

func TestReadGHHostsConfig(t *testing.T) {
	t.Run("reads github.com token", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		ghDir := filepath.Join(dir, "gh")
		require.NoError(t, os.MkdirAll(ghDir, 0o755))

		content := `github.com:
  user: testuser
  oauth_token: ghp_test123
  git_protocol: https
`
		require.NoError(t, os.WriteFile(filepath.Join(ghDir, "hosts.yml"), []byte(content), 0o644))

		assert.Equal(t, "ghp_test123", readGHHostsConfig())
	})

	t.Run("returns empty when no github.com entry", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		ghDir := filepath.Join(dir, "gh")
		require.NoError(t, os.MkdirAll(ghDir, 0o755))

		content := `gitlab.com:
  user: testuser
  oauth_token: glpat_test
`
		require.NoError(t, os.WriteFile(filepath.Join(ghDir, "hosts.yml"), []byte(content), 0o644))

		assert.Empty(t, readGHHostsConfig())
	})

	t.Run("returns empty when file missing", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		assert.Empty(t, readGHHostsConfig())
	})

	t.Run("returns empty on invalid yaml", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		ghDir := filepath.Join(dir, "gh")
		require.NoError(t, os.MkdirAll(ghDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(ghDir, "hosts.yml"), []byte("{{{{"), 0o644))

		assert.Empty(t, readGHHostsConfig())
	})
}

// --- readGLabConfig ---
// Prevents losing GitLab token when config exists but has no gitlab.com host

func TestReadGLabConfig(t *testing.T) {
	t.Run("reads gitlab.com token", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		glabDir := filepath.Join(dir, "glab-cli")
		require.NoError(t, os.MkdirAll(glabDir, 0o755))

		content := `hosts:
  gitlab.com:
    token: glpat_test456
    api_host: gitlab.com
    user: testuser
`
		require.NoError(t, os.WriteFile(filepath.Join(glabDir, "config.yml"), []byte(content), 0o644))

		assert.Equal(t, "glpat_test456", readGLabConfig())
	})

	t.Run("returns empty when no gitlab.com host", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		glabDir := filepath.Join(dir, "glab-cli")
		require.NoError(t, os.MkdirAll(glabDir, 0o755))

		content := `hosts:
  gitlab.internal.com:
    token: glpat_internal
`
		require.NoError(t, os.WriteFile(filepath.Join(glabDir, "config.yml"), []byte(content), 0o644))

		assert.Empty(t, readGLabConfig())
	})

	t.Run("returns empty when file missing", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		assert.Empty(t, readGLabConfig())
	})
}

// --- readTeaConfig ---
// Credentials must match the configured instance, not the default login.

func TestReadTeaConfig(t *testing.T) {
	t.Run("matches instance URL", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		teaDir := filepath.Join(dir, "tea")
		require.NoError(t, os.MkdirAll(teaDir, 0o755))

		content := `logins:
  - name: corp
    url: https://gitea.corp.com
    token: tea_token_123
    default: false
  - name: personal
    url: https://gitea.home.net
    token: tea_token_456
    default: true
`
		require.NoError(t, os.WriteFile(filepath.Join(teaDir, "config.yml"), []byte(content), 0o644))

		assert.Equal(t, "tea_token_123", readTeaConfig("https://gitea.corp.com"))
		assert.Equal(t, "tea_token_123", readTeaConfig("https://GITEA.CORP.COM/"))
		assert.Empty(t, readTeaConfig("http://gitea.corp.com"))
		assert.Empty(t, readTeaConfig("https://gitea.corp.com:3000"))
		assert.Empty(t, readTeaConfig("https://gitea.corp.com/other"))
	})

	t.Run("does not use default login when no match", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		teaDir := filepath.Join(dir, "tea")
		require.NoError(t, os.MkdirAll(teaDir, 0o755))

		content := `logins:
  - name: personal
    url: https://gitea.home.net
    token: tea_default_tok
    default: true
`
		require.NoError(t, os.WriteFile(filepath.Join(teaDir, "config.yml"), []byte(content), 0o644))

		assert.Empty(t, readTeaConfig("https://unknown.host.com"))
	})

	t.Run("returns empty when no match and no default", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		teaDir := filepath.Join(dir, "tea")
		require.NoError(t, os.MkdirAll(teaDir, 0o755))

		content := `logins:
  - name: corp
    url: https://gitea.corp.com
    token: tea_token_123
    default: false
`
		require.NoError(t, os.WriteFile(filepath.Join(teaDir, "config.yml"), []byte(content), 0o644))

		assert.Empty(t, readTeaConfig("https://other.host.com"))
	})

	t.Run("returns empty when config missing", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		assert.Empty(t, readTeaConfig("https://any.host.com"))
	})
}

// --- configured tea instance selection ---

func TestExtractGiteaInstanceURL_TeaConfig(t *testing.T) {
	t.Setenv("GITEA_TOKEN", "")
	t.Setenv("GITEA_URL", "")

	t.Run("uses configured URL for matching host", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		teaDir := filepath.Join(dir, "tea")
		require.NoError(t, os.MkdirAll(teaDir, 0o755))

		content := `logins:
  - url: https://git.example.com:3000/gitea/
    token: matched_token
`
		require.NoError(t, os.WriteFile(filepath.Join(teaDir, "config.yml"), []byte(content), 0o644))

		instanceURL := extractGiteaInstanceURL([]string{"git.example.com:3000/gitea/user/repo"})
		assert.Equal(t, "https://git.example.com:3000/gitea", instanceURL)
		assert.Equal(t, "matched_token", getGiteaToken(instanceURL))
	})

	t.Run("returns empty for non-matching host", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)

		teaDir := filepath.Join(dir, "tea")
		require.NoError(t, os.MkdirAll(teaDir, 0o755))

		content := `logins:
  - url: https://gitea.example.com
    token: some_token
`
		require.NoError(t, os.WriteFile(filepath.Join(teaDir, "config.yml"), []byte(content), 0o644))

		assert.Empty(t, extractGiteaInstanceURL([]string{"other.host.com/gitea/repo"}))
	})

	t.Run("returns empty when config missing", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		assert.Empty(t, extractGiteaInstanceURL([]string{"gitea.example.com/user/repo"}))
	})
}

// --- token env var functions ---
// Prevents breaking CI/CD flows that rely on specific env var names

func TestGetGitHubToken_EnvVars(t *testing.T) {
	t.Run("GITHUB_TOKEN takes precedence", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "gh_tok_1")
		t.Setenv("GH_TOKEN", "gh_tok_2")
		// clear config path so file-based lookup fails
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		assert.Equal(t, "gh_tok_1", GetGitHubToken())
	})

	t.Run("GH_TOKEN used when GITHUB_TOKEN empty", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "gh_tok_2")
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		assert.Equal(t, "gh_tok_2", GetGitHubToken())
	})
}

func TestGetGitLabToken_EnvVars(t *testing.T) {
	t.Run("GITLAB_TOKEN takes precedence", func(t *testing.T) {
		t.Setenv("GITLAB_TOKEN", "gl_tok_1")
		t.Setenv("GITLAB_PRIVATE_TOKEN", "gl_tok_2")
		assert.Equal(t, "gl_tok_1", getGitLabToken())
	})

	t.Run("GITLAB_PRIVATE_TOKEN used as fallback", func(t *testing.T) {
		t.Setenv("GITLAB_TOKEN", "")
		t.Setenv("GITLAB_PRIVATE_TOKEN", "gl_tok_2")
		assert.Equal(t, "gl_tok_2", getGitLabToken())
	})
}

func TestGetBitbucketToken_EnvVars(t *testing.T) {
	t.Run("BITBUCKET_TOKEN takes precedence", func(t *testing.T) {
		t.Setenv("BITBUCKET_TOKEN", "bb_tok_1")
		t.Setenv("BITBUCKET_ACCESS_TOKEN", "bb_tok_2")
		assert.Equal(t, "bb_tok_1", getBitbucketToken())
	})

	t.Run("BITBUCKET_ACCESS_TOKEN used as fallback", func(t *testing.T) {
		t.Setenv("BITBUCKET_TOKEN", "")
		t.Setenv("BITBUCKET_ACCESS_TOKEN", "bb_tok_2")
		assert.Equal(t, "bb_tok_2", getBitbucketToken())
	})

	t.Run("returns empty when neither set", func(t *testing.T) {
		t.Setenv("BITBUCKET_TOKEN", "")
		t.Setenv("BITBUCKET_ACCESS_TOKEN", "")
		assert.Empty(t, getBitbucketToken())
	})
}

func TestGetAzureDevOpsToken_EnvVars(t *testing.T) {
	t.Run("AZURE_DEVOPS_EXT_PAT takes precedence", func(t *testing.T) {
		t.Setenv("AZURE_DEVOPS_EXT_PAT", "az_pat_1")
		t.Setenv("SYSTEM_ACCESSTOKEN", "az_sys_1")
		assert.Equal(t, "az_pat_1", getAzureDevOpsToken())
	})

	t.Run("SYSTEM_ACCESSTOKEN used as fallback", func(t *testing.T) {
		t.Setenv("AZURE_DEVOPS_EXT_PAT", "")
		t.Setenv("SYSTEM_ACCESSTOKEN", "az_sys_1")
		t.Setenv("HOME", t.TempDir()) // prevent reading real azure config
		assert.Equal(t, "az_sys_1", getAzureDevOpsToken())
	})
}

func TestGetGiteaToken_EnvVars(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	t.Run("GITEA_TOKEN requires matching GITEA_URL", func(t *testing.T) {
		t.Setenv("GITEA_TOKEN", "tea_env_tok")
		t.Setenv("GITEA_URL", "https://git.example.com/")
		assert.Equal(t, "tea_env_tok", getGiteaToken("https://git.example.com"))
		assert.Empty(t, getGiteaToken("https://attacker.example"))
		assert.Empty(t, getGiteaToken("http://git.example.com"))
		assert.Empty(t, getGiteaToken("https://git.example.com:3000"))
		assert.Empty(t, getGiteaToken("https://git.example.com/other"))
		assert.Empty(t, getGiteaToken(""))
	})

	t.Run("unscoped GITEA_TOKEN is ignored", func(t *testing.T) {
		t.Setenv("GITEA_TOKEN", "tea_env_tok")
		t.Setenv("GITEA_URL", "")
		assert.Empty(t, getGiteaToken("https://git.example.com"))
	})

	t.Run("falls back to tea config", func(t *testing.T) {
		t.Setenv("GITEA_TOKEN", "")
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		assert.Empty(t, getGiteaToken("https://gitea.example.com"))
	})
}

// --- readAzureCliConfig edge case ---
// Prevents crash on invalid YAML in azure config

func TestReadAzureCliConfig_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	azureDir := filepath.Join(dir, ".azure")
	require.NoError(t, os.MkdirAll(azureDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(azureDir, "config"), []byte("{{{{"), 0o644))

	assert.Empty(t, readAzureCliConfig())
}
