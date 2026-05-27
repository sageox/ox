package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Note: these tests use t.Setenv which is incompatible with t.Parallel
// (the runtime panics if both are used). Env-token resolution is inherently
// process-global, so serial execution is the correct semantics.

// TestTokenFromEnv_OXTokenSet — Failure prevented: OX_TOKEN env doesn't produce a
// usable StoredToken, breaking CI/CD and headless agents.
func TestTokenFromEnv_OXTokenSet(t *testing.T) {
	t.Setenv(EnvVarToken, "oxp_test")
	t.Setenv(EnvVarTokenAlias, "")

	tok := tokenFromEnv("https://api.sageox.ai/")
	require.NotNil(t, tok)
	assert.Equal(t, "oxp_test", tok.AccessToken)
	assert.Equal(t, "Bearer", tok.TokenType)
	assert.Equal(t, "*", tok.Scope)
	assert.Empty(t, tok.RefreshToken)
	assert.Empty(t, tok.SessionToken)
	assert.True(t, tok.ExpiresAt.After(time.Now()), "env token expiry must be in the future")
}

// TestTokenFromEnv_AliasOnly — Failure prevented: SAGEOX_TOKEN alias silently
// drops, breaking back-compat with users who adopted the pre-rename name.
func TestTokenFromEnv_AliasOnly(t *testing.T) {
	t.Setenv(EnvVarToken, "")
	t.Setenv(EnvVarTokenAlias, "oxp_test_alias")

	tok := tokenFromEnv("https://api.sageox.ai/")
	require.NotNil(t, tok)
	assert.Equal(t, "oxp_test_alias", tok.AccessToken)
}

// TestTokenFromEnv_PrimaryWinsOverAlias — Failure prevented: resolution order
// flips, OX_TOKEN gets shadowed by stale SAGEOX_TOKEN, user can't override.
func TestTokenFromEnv_PrimaryWinsOverAlias(t *testing.T) {
	t.Setenv(EnvVarToken, "oxp_primary")
	t.Setenv(EnvVarTokenAlias, "oxp_alias")

	tok := tokenFromEnv("https://api.sageox.ai/")
	require.NotNil(t, tok)
	assert.Equal(t, "oxp_primary", tok.AccessToken, "OX_TOKEN must win over SAGEOX_TOKEN")
}

// TestTokenFromEnv_NeitherSet — Failure prevented: nil-return contract broken,
// callers that fall through to disk lookup are skipped.
func TestTokenFromEnv_NeitherSet(t *testing.T) {
	t.Setenv(EnvVarToken, "")
	t.Setenv(EnvVarTokenAlias, "")

	assert.Nil(t, tokenFromEnv("https://api.sageox.ai/"))
}

// TestGetTokenForEndpoint_EnvOverridesDisk — Failure prevented: env token
// ignored when disk has a token, defeating the override use case.
func TestGetTokenForEndpoint_EnvOverridesDisk(t *testing.T) {
	t.Setenv(EnvVarToken, "")
	t.Setenv(EnvVarTokenAlias, "")

	client := NewTestClient(t)
	disk := createTestTokenForTest(1 * time.Hour)
	disk.AccessToken = "disk-token"
	require.NoError(t, client.SaveToken(disk))

	// without env, disk wins
	got, err := client.GetToken()
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "disk-token", got.AccessToken)

	// with env, env wins
	t.Setenv(EnvVarToken, "env-token")
	got, err = client.GetToken()
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "env-token", got.AccessToken)
	assert.Equal(t, "*", got.Scope)
}

// TestGetTokenForEndpoint_EnvEmptyFallsBackToDisk — Failure prevented:
// empty env var treated as a token, blanking out legitimate disk auth.
func TestGetTokenForEndpoint_EnvEmptyFallsBackToDisk(t *testing.T) {
	t.Setenv(EnvVarToken, "")
	t.Setenv(EnvVarTokenAlias, "")

	client := NewTestClient(t)
	disk := createTestTokenForTest(1 * time.Hour)
	disk.AccessToken = "disk-only"
	require.NoError(t, client.SaveToken(disk))

	got, err := client.GetToken()
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "disk-only", got.AccessToken)
}

// TestPackageAndClientAgree — Failure prevented: package-level
// GetTokenForEndpoint and AuthClient.GetTokenForEndpoint diverge on env-token
// resolution, leading to inconsistent auth behavior across call sites.
func TestPackageAndClientAgree(t *testing.T) {
	t.Setenv(EnvVarToken, "oxp_agree")
	t.Setenv(EnvVarTokenAlias, "")

	ep := "https://api.sageox.ai/"

	pkgTok, err := GetTokenForEndpoint(ep)
	require.NoError(t, err)
	require.NotNil(t, pkgTok)

	client := NewTestClient(t)
	cliTok, err := client.GetTokenForEndpoint(ep)
	require.NoError(t, err)
	require.NotNil(t, cliTok)

	assert.Equal(t, pkgTok.AccessToken, cliTok.AccessToken)
	assert.Equal(t, pkgTok.Scope, cliTok.Scope)
	assert.Equal(t, pkgTok.TokenType, cliTok.TokenType)
}
