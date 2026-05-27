package auth

import (
	"os"
	"time"
)

// EnvVarToken is the primary environment variable for supplying an ox access token
// out-of-band (CI/CD, headless agents, ephemeral containers). When set, it takes
// precedence over any token stored on disk.
const EnvVarToken = "OX_TOKEN"

// EnvVarTokenAlias is the back-compat alias for EnvVarToken. Kept for users who
// adopted SAGEOX_TOKEN before the ryan/ox-session-start work was lost. Resolution
// order: OX_TOKEN wins, then SAGEOX_TOKEN.
const EnvVarTokenAlias = "SAGEOX_TOKEN"

// envTokenTTL is the synthetic rolling expiry stamped on env-sourced tokens.
// Env tokens have no refresh credential — the server returning 401 is the source
// of truth for invalidation. A 24h rolling TTL keeps IsExpired() honest without
// triggering refresh paths.
const envTokenTTL = 24 * time.Hour

// isEnvToken reports whether the given token was sourced from the environment.
// Env tokens have no refresh credential and the server returning 401 is the
// source of truth for invalidation — callers must never attempt to refresh one.
func isEnvToken(ep string, token *StoredToken) bool {
	if token == nil {
		return false
	}
	if token.RefreshToken != "" || token.SessionToken != "" {
		return false
	}
	envTok := tokenFromEnv(ep)
	if envTok == nil {
		return false
	}
	return token.AccessToken == envTok.AccessToken
}

// tokenFromEnv returns a StoredToken populated from OX_TOKEN (preferred) or
// SAGEOX_TOKEN (alias). Returns nil when neither is set.
//
// The ep parameter is currently unused — it is kept for API symmetry with
// GetTokenForEndpoint and reserved for future per-endpoint variants
// (e.g. OX_TOKEN_<HOST>). Callers should pass the normalized endpoint anyway.
func tokenFromEnv(ep string) *StoredToken {
	_ = ep // reserved for future per-endpoint env tokens
	val := os.Getenv(EnvVarToken)
	if val == "" {
		val = os.Getenv(EnvVarTokenAlias)
	}
	if val == "" {
		return nil
	}
	return &StoredToken{
		AccessToken:  val,
		RefreshToken: "",
		SessionToken: "",
		ExpiresAt:    time.Now().Add(envTokenTTL),
		TokenType:    "Bearer",
		Scope:        "*",
		// UserInfo is zero-valued — filled lazily on first server response that
		// includes claims.
	}
}
