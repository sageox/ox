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

// TestTokenFromEnv_SageOxTokenSet — Failure prevented: SAGEOX_TOKEN env doesn't produce a
// usable StoredToken, breaking CI/CD and headless agents.
func TestTokenFromEnv_SageOxTokenSet(t *testing.T) {
	t.Setenv(EnvVarToken, "oxp_test")

	tok := tokenFromEnv("https://api.sageox.ai/")
	require.NotNil(t, tok)
	assert.Equal(t, "oxp_test", tok.AccessToken)
	assert.Equal(t, "Bearer", tok.TokenType)
	assert.Equal(t, "*", tok.Scope)
	assert.Empty(t, tok.RefreshToken)
	assert.Empty(t, tok.SessionToken)
	assert.True(t, tok.ExpiresAt.After(time.Now()), "env token expiry must be in the future")
}

// TestTokenFromEnv_LegacyOxTokenIgnored — Failure prevented: removed OX_TOKEN
// support accidentally lingers and keeps a second customer-facing env var alive.
func TestTokenFromEnv_LegacyOxTokenIgnored(t *testing.T) {
	t.Setenv(EnvVarToken, "")
	t.Setenv("OX_TOKEN", "oxp_legacy")

	assert.Nil(t, tokenFromEnv("https://api.sageox.ai/"))
}

// TestTokenFromEnv_NeitherSet — Failure prevented: nil-return contract broken,
// callers that fall through to disk lookup are skipped.
func TestTokenFromEnv_NeitherSet(t *testing.T) {
	t.Setenv(EnvVarToken, "")
	t.Setenv("OX_TOKEN", "")

	assert.Nil(t, tokenFromEnv("https://api.sageox.ai/"))
}

// TestTokenFromEnv_MismatchedEndpointIgnored — Failure prevented: a single
// env token silently applies to every endpoint lookup and gets sent to the
// wrong host in multi-endpoint setups.
func TestTokenFromEnv_MismatchedEndpointIgnored(t *testing.T) {
	t.Setenv(EnvVarToken, "oxp_prod")
	t.Setenv("SAGEOX_ENDPOINT", "")

	assert.Nil(t, tokenFromEnv("https://staging.sageox.ai/"))
}

// TestTokenFromEnv_ExplicitEndpointSelection — Failure prevented: staging or
// self-hosted users set SAGEOX_TOKEN but, without binding it to the selected
// endpoint, either hit production implicitly or fan the token out everywhere.
func TestTokenFromEnv_ExplicitEndpointSelection(t *testing.T) {
	t.Setenv(EnvVarToken, "oxp_stage")
	t.Setenv("SAGEOX_ENDPOINT", "https://staging.sageox.ai")

	tok := tokenFromEnv("https://staging.sageox.ai/")
	require.NotNil(t, tok)
	assert.Equal(t, "oxp_stage", tok.AccessToken)
	assert.Nil(t, tokenFromEnv("https://sageox.ai/"))
}

// TestGetTokenForEndpoint_EnvOverridesDisk — Failure prevented: env token
// ignored when disk has a token, defeating the override use case.
func TestGetTokenForEndpoint_EnvOverridesDisk(t *testing.T) {
	t.Setenv(EnvVarToken, "")
	t.Setenv("OX_TOKEN", "")

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
	t.Setenv("OX_TOKEN", "")

	client := NewTestClient(t)
	disk := createTestTokenForTest(1 * time.Hour)
	disk.AccessToken = "disk-only"
	require.NoError(t, client.SaveToken(disk))

	got, err := client.GetToken()
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "disk-only", got.AccessToken)
}

// TestGetTokenForEndpoint_MismatchedEnvFallsBackToDisk — Failure prevented:
// exporting SAGEOX_TOKEN for one endpoint hijacks token lookups for a different
// endpoint that already has valid disk credentials.
func TestGetTokenForEndpoint_MismatchedEnvFallsBackToDisk(t *testing.T) {
	t.Setenv(EnvVarToken, "env-prod")
	t.Setenv("SAGEOX_ENDPOINT", "")

	client := NewTestClient(t)
	disk := createTestTokenForTest(1 * time.Hour)
	disk.AccessToken = "disk-staging"
	require.NoError(t, client.SaveTokenForEndpoint("https://staging.sageox.ai", disk))

	got, err := client.GetTokenForEndpoint("https://staging.sageox.ai")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "disk-staging", got.AccessToken)
}

// TestPackageAndClientAgree — Failure prevented: package-level
// GetTokenForEndpoint and AuthClient.GetTokenForEndpoint diverge on env-token
// resolution, leading to inconsistent auth behavior across call sites.
func TestPackageAndClientAgree(t *testing.T) {
	t.Setenv(EnvVarToken, "oxp_agree")

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
