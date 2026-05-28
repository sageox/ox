package auth

import (
	"os"
	"time"

	"github.com/sageox/ox/internal/endpoint"
)

// EnvVarToken is the environment variable for supplying a SageOx access token
// out-of-band (CI/CD, headless agents, ephemeral containers). When set, it takes
// precedence over any token stored on disk.
const EnvVarToken = "SAGEOX_TOKEN"

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

// envTokenEndpoint returns the single endpoint an env-supplied token is allowed
// to target in this process. SAGEOX_TOKEN is not self-describing client-side, so we
// bind it to the explicit endpoint selection surface only:
//   - SAGEOX_ENDPOINT when set
//   - production by default
//
// We intentionally do NOT inherit endpoint.Get() here because that function can
// fall back to "the only logged-in endpoint", which would let a prod SAGEOX_TOKEN
// silently ride along to a different host if disk auth happened to be sparse.
func envTokenEndpoint() string {
	if ep := os.Getenv(endpoint.EnvVar); ep != "" {
		return endpoint.NormalizeEndpoint(ep)
	}
	return endpoint.Default
}

// tokenFromEnv returns a StoredToken populated from SAGEOX_TOKEN when the
// requested endpoint matches envTokenEndpoint(). Returns nil when the env var
// is unset, or when the request is for a different endpoint.
func tokenFromEnv(ep string) *StoredToken {
	val := os.Getenv(EnvVarToken)
	if val == "" {
		return nil
	}
	if requested := endpoint.NormalizeEndpoint(ep); requested != "" && requested != envTokenEndpoint() {
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
