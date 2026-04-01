package identity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Resolve (top-level wrapper) ---
// Prevents Resolve() from diverging from ResolveWithConfig(nil)

func TestResolve(t *testing.T) {
	result, err := Resolve()
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.NotNil(t, result.Primary)
}

// --- ResolveWithConfig primary-only mode ---
// Prevents skipping provider collection when primary-only is requested

func TestResolveWithConfig_PrimaryOnly(t *testing.T) {
	result, err := ResolveWithConfig(&Config{Collection: "primary-only"})
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.NotNil(t, result.Primary)
}

// --- readAWSCredentials (file-based) ---
// Prevents credential file parsing failures when profile selection or INI format varies

func TestReadAWSCredentials(t *testing.T) {
	t.Run("reads default profile", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("HOME", dir)
		t.Setenv("AWS_ACCESS_KEY_ID", "")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "")
		t.Setenv("AWS_PROFILE", "")

		awsDir := filepath.Join(dir, ".aws")
		require.NoError(t, os.MkdirAll(awsDir, 0o755))

		content := `[default]
aws_access_key_id = AKIAIOSFODNN7EXAMPLE
aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
`
		require.NoError(t, os.WriteFile(filepath.Join(awsDir, "credentials"), []byte(content), 0o644))

		ak, sk := readAWSCredentials()
		assert.Equal(t, "AKIAIOSFODNN7EXAMPLE", ak)
		assert.Equal(t, "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", sk)
	})

	t.Run("reads named profile from AWS_PROFILE", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("HOME", dir)
		t.Setenv("AWS_ACCESS_KEY_ID", "")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "")
		t.Setenv("AWS_PROFILE", "staging")

		awsDir := filepath.Join(dir, ".aws")
		require.NoError(t, os.MkdirAll(awsDir, 0o755))

		content := `[default]
aws_access_key_id = AKIADEFAULT
aws_secret_access_key = default_secret

[staging]
aws_access_key_id = AKIASTAGING
aws_secret_access_key = staging_secret
`
		require.NoError(t, os.WriteFile(filepath.Join(awsDir, "credentials"), []byte(content), 0o644))

		ak, sk := readAWSCredentials()
		assert.Equal(t, "AKIASTAGING", ak)
		assert.Equal(t, "staging_secret", sk)
	})

	t.Run("returns empty when profile not found", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("HOME", dir)
		t.Setenv("AWS_ACCESS_KEY_ID", "")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "")
		t.Setenv("AWS_PROFILE", "nonexistent")

		awsDir := filepath.Join(dir, ".aws")
		require.NoError(t, os.MkdirAll(awsDir, 0o755))

		content := `[default]
aws_access_key_id = AKIADEFAULT
aws_secret_access_key = default_secret
`
		require.NoError(t, os.WriteFile(filepath.Join(awsDir, "credentials"), []byte(content), 0o644))

		ak, sk := readAWSCredentials()
		assert.Empty(t, ak)
		assert.Empty(t, sk)
	})

	t.Run("returns empty when file missing", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("AWS_ACCESS_KEY_ID", "")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "")

		ak, sk := readAWSCredentials()
		assert.Empty(t, ak)
		assert.Empty(t, sk)
	})
}

// --- getGitHubIdentity / getGitLabIdentity / getBitbucketIdentity / getAzureDevOpsIdentity ---
// Prevents silent nil returns when tokens are missing (should return error)

func TestProviderIdentity_NoTokenErrors(t *testing.T) {
	t.Run("github returns error without token", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "")
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv("PATH", "") // prevent gh CLI subprocess

		_, err := getGitHubIdentity()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no GitHub token")
	})

	t.Run("gitlab returns error without token", func(t *testing.T) {
		t.Setenv("GITLAB_TOKEN", "")
		t.Setenv("GITLAB_PRIVATE_TOKEN", "")
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		_, err := getGitLabIdentity()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no GitLab token")
	})

	t.Run("bitbucket returns error without token", func(t *testing.T) {
		t.Setenv("BITBUCKET_TOKEN", "")
		t.Setenv("BITBUCKET_ACCESS_TOKEN", "")

		_, err := getBitbucketIdentity()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no Bitbucket token")
	})

	t.Run("azure devops returns error without token", func(t *testing.T) {
		t.Setenv("AZURE_DEVOPS_EXT_PAT", "")
		t.Setenv("SYSTEM_ACCESSTOKEN", "")
		t.Setenv("HOME", t.TempDir())

		_, err := getAzureDevOpsIdentity()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no Azure DevOps token")
	})

	t.Run("gitea returns error without token", func(t *testing.T) {
		t.Setenv("GITEA_TOKEN", "")
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		_, err := getGiteaIdentity("https://gitea.example.com")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no Gitea token")
	})
}

// --- Identity / ResolvedIdentities struct edge cases ---

func TestIdentity_ZeroValue(t *testing.T) {
	t.Parallel()

	// zero-value Identity should not panic when accessed
	var id Identity
	assert.Empty(t, id.UserID)
	assert.Empty(t, id.Source)
	assert.False(t, id.Verified)
}

func TestResolvedIdentities_ZeroValue(t *testing.T) {
	t.Parallel()

	var ri ResolvedIdentities
	assert.Nil(t, ri.Primary)
	assert.Nil(t, ri.GitHub)
}
