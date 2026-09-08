package auth

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
)

// ErrEnvTokenMalformed reports that SAGEOX_TOKEN was set and bound to the
// requested endpoint, but its value failed the local format check — so no
// credential was resolved at all.
//
// Callers that render or explain auth state should branch on this with
// errors.Is: "the credential you named is unusable" and "you have no
// credential" call for different advice, and conflating them sends a CI
// operator to `ox login` when the fix is to re-copy a service token.
var ErrEnvTokenMalformed = errors.New("SAGEOX_TOKEN is set but its value failed a local format check")

// UserInfo contains user information from the authentication provider
type UserInfo struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
}

// StoredToken represents the authentication token stored locally
type StoredToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	// SessionToken is a Better Auth compatibility field. Better Auth servers may issue
	// a single long-lived session token that serves as BOTH the access token AND the
	// refresh token — i.e. access_token == refresh_token == session_token is a valid
	// and intentional state. When the server omits refresh_token but provides
	// session_token, EffectiveRefreshToken uses session_token so the refresh flow
	// works correctly. Do NOT treat access_token == refresh_token as a bug.
	// See PR #299 for the original fix, and refresh.go:effectiveRefreshToken.
	SessionToken string    `json:"session_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	TokenType    string    `json:"token_type"`
	Scope        string    `json:"scope"`
	UserInfo     UserInfo  `json:"user_info"`
}

// EffectiveRefreshToken returns the token to use for refresh grant requests.
//
// Better Auth compatibility: some server configurations issue a session_token that
// acts as a long-lived refresh credential instead of (or in addition to) a standard
// OAuth2 refresh_token. In these configurations access_token == refresh_token is
// normal and expected — the opaque session token can legitimately be sent as the
// refresh_token in a grant_type=refresh_token request.
//
// When the session expires server-side the refresh will return 401/authentication_required,
// which is correct — the user must re-login. This is not a client bug.
func (t *StoredToken) EffectiveRefreshToken() string {
	if t.RefreshToken != "" {
		return t.RefreshToken
	}
	return t.SessionToken
}

// AuthStore holds tokens for multiple API endpoints
type AuthStore struct {
	// Tokens maps API endpoint URLs to their authentication tokens
	// e.g., "https://api.sageox.ai/" -> token, "https://sageox.walmart.com/" -> token
	Tokens map[string]*StoredToken `json:"tokens"`
}

// GetAuthFilePath returns the path to the auth token file.
//
// Path Resolution (via internal/paths package):
//
//	Default:           $XDG_CONFIG_HOME/sageox/auth.json (typically ~/.config/sageox/auth.json)
//	With OX_XDG_DISABLE: ~/.sageox/config/auth.json
//
// See internal/paths/doc.go for architecture rationale.
func GetAuthFilePath() (string, error) {
	authPath := paths.AuthFile()
	if authPath == "" {
		return "", fmt.Errorf("failed to get auth file path")
	}

	return authPath, nil
}

// normalizeTokenKeys re-keys any tokens whose key differs from the normalized form.
// Fixes existing auth.json files that were saved with prefixed endpoint URLs.
// When multiple keys normalize to the same endpoint, keeps the token with the
// latest ExpiresAt to avoid silent data loss.
func normalizeTokenKeys(store *AuthStore) {
	if store == nil || store.Tokens == nil {
		return
	}

	for key, token := range store.Tokens {
		normalized := endpoint.NormalizeEndpoint(key)
		if normalized != key {
			delete(store.Tokens, key)
			// collision: keep the token that expires later
			if existing, ok := store.Tokens[normalized]; ok {
				if existing != nil && (token == nil || existing.ExpiresAt.After(token.ExpiresAt)) {
					continue // existing token is newer, skip
				}
			}
			store.Tokens[normalized] = token
		}
	}
}

// GetToken loads the raw stored token for the current endpoint.
// Internal use only (refresh logic, display/identity checks).
// Callers making API requests MUST use EnsureValidToken(300) or
// EnsureValidTokenForEndpoint(ep, 300) instead — raw tokens may be expired.
func GetToken() (*StoredToken, error) {
	return GetTokenForEndpoint(endpoint.Get())
}

// envTokenOrError resolves SAGEOX_TOKEN for ep ahead of any disk lookup.
//
// Three outcomes, and the middle one is the whole point:
//   - (token, nil)  — a usable env credential; use it, skip auth.json.
//   - (nil, err)    — the operator set SAGEOX_TOKEN for THIS endpoint and the
//     value is unusable. Fail closed. Falling through to auth.json would
//     authenticate as whoever is logged in on this machine instead of the
//     principal the operator named, silently re-attributing every API call,
//     ledger write, and murmur — while status still shows a green check.
//   - (nil, nil)    — no env token applies (unset, or bound to another
//     endpoint); the caller should read auth.json as usual.
func envTokenOrError(ep string) (*StoredToken, error) {
	tok, state := EnvTokenFor(ep)
	if state == EnvTokenMalformed {
		// Never interpolate the value: this error is printed to terminals and
		// carried into logs, and a value that failed a checksum is still a
		// secret (it may be a correct token with one character lost).
		return nil, fmt.Errorf("%w (endpoint %s): refusing to fall back to a stored login — re-copy the token (a truncated paste is the usual cause), or unset %s to use your `ox login` credential",
			ErrEnvTokenMalformed, ep, EnvVarToken)
	}
	return tok, nil
}

// GetTokenForEndpoint loads the raw stored token for a specific endpoint.
// Internal use only (refresh logic, display/identity checks).
// Callers making API requests MUST use EnsureValidTokenForEndpoint(ep, 300)
// instead — raw tokens may be expired.
func GetTokenForEndpoint(ep string) (*StoredToken, error) {
	ep = endpoint.NormalizeEndpoint(ep)

	envTok, envErr := envTokenOrError(ep)
	if envErr != nil {
		return nil, envErr
	}
	if envTok != nil {
		return envTok, nil
	}

	authPath, err := GetAuthFilePath()
	if err != nil {
		return nil, err
	}

	var token *StoredToken
	err = withAuthFileRLocked(authPath, func(h *authFileHandle) error {
		store, loadErr := h.load()
		if loadErr != nil {
			return loadErr
		}
		t, exists := store.Tokens[ep]
		if exists {
			token = t
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return token, nil
}

// GetUserID returns the authenticated user's unique ID for the given endpoint.
// Returns empty string if not authenticated or on error.
func GetUserID(ep string) string {
	var token *StoredToken
	var err error
	if ep != "" {
		token, err = GetTokenForEndpoint(ep)
	} else {
		token, err = GetToken()
	}
	if err != nil || token == nil {
		return ""
	}
	return token.UserInfo.UserID
}

// GetUsername returns the authenticated user's display name for a given endpoint.
// Prefers email, falls back to name, then empty string.
func GetUsername(ep string) string {
	var token *StoredToken
	var err error
	if ep != "" {
		token, err = GetTokenForEndpoint(ep)
	} else {
		token, err = GetToken()
	}
	if err != nil || token == nil {
		return ""
	}
	if token.UserInfo.Email != "" {
		return token.UserInfo.Email
	}
	return token.UserInfo.Name
}

// SaveToken saves the authentication token for the current API endpoint
func SaveToken(token *StoredToken) error {
	return SaveTokenForEndpoint(endpoint.Get(), token)
}

// SaveTokenForEndpoint saves the authentication token for a specific API endpoint
func SaveTokenForEndpoint(ep string, token *StoredToken) error {
	ep = endpoint.NormalizeEndpoint(ep)

	authPath, err := GetAuthFilePath()
	if err != nil {
		return err
	}

	return withAuthFileLocked(authPath, func(h *authFileHandle) error {
		store, loadErr := h.load()
		if loadErr != nil {
			return loadErr
		}
		if store.Tokens == nil {
			store.Tokens = make(map[string]*StoredToken)
		}
		store.Tokens[ep] = token
		return h.save(store)
	})
}

// RemoveToken deletes the authentication token for the current API endpoint
func RemoveToken() error {
	return RemoveTokenForEndpoint(endpoint.Get())
}

// RemoveTokenForEndpoint deletes the authentication token for a specific API endpoint
func RemoveTokenForEndpoint(ep string) error {
	ep = endpoint.NormalizeEndpoint(ep)

	authPath, err := GetAuthFilePath()
	if err != nil {
		return err
	}

	return withAuthFileLocked(authPath, func(h *authFileHandle) error {
		store, loadErr := h.load()
		if loadErr != nil {
			return loadErr
		}
		if store.Tokens == nil {
			return nil // nothing to remove
		}
		delete(store.Tokens, ep)
		return h.save(store)
	})
}

// ListEndpoints returns all endpoints that have stored tokens
func ListEndpoints() ([]string, error) {
	authPath, err := GetAuthFilePath()
	if err != nil {
		return nil, err
	}

	var endpoints []string
	err = withAuthFileRLocked(authPath, func(h *authFileHandle) error {
		store, loadErr := h.load()
		if loadErr != nil {
			return loadErr
		}
		endpoints = make([]string, 0, len(store.Tokens))
		for ep := range store.Tokens {
			endpoints = append(endpoints, endpoint.NormalizeEndpoint(ep))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return endpoints, nil
}

// GetLoggedInEndpoints returns all endpoints with valid (non-expired) tokens.
// Returns nil if no valid tokens exist.
func GetLoggedInEndpoints() []string {
	var endpoints []string

	// An env-supplied token (SAGEOX_TOKEN — a PAT in CI, a cloud agent, or a
	// container) is a real, usable credential; it simply never touches
	// auth.json. Reading only the file made `ox status` print "not logged in"
	// in a shell where every API call succeeded — the most misleading thing a
	// status command can do, and the reason a working PAT read as a broken
	// one. EnvTokenFor() owns the endpoint-binding and format rules (see
	// env_token.go); we ask it rather than re-deriving them here.
	//
	// A malformed env value adds nothing here: this list means "endpoints with
	// a usable credential", and the malformed case has none. Renderers must ask
	// EnvTokenFor(envTokenEndpoint()) directly rather than inferring the env
	// state from this list, or a rejected token with no disk login behind it
	// reads as a plain "not logged in".
	envEP := envTokenEndpoint()
	_, envState := EnvTokenFor(envEP)
	envAdded := envState == EnvTokenValid
	if envAdded {
		endpoints = append(endpoints, envEP)
	}

	authPath, err := GetAuthFilePath()
	if err != nil {
		return endpoints
	}

	_ = withAuthFileRLocked(authPath, func(h *authFileHandle) error {
		store, loadErr := h.load()
		if loadErr != nil {
			return loadErr
		}
		now := time.Now()
		for ep, token := range store.Tokens {
			normalized := endpoint.NormalizeEndpoint(ep)
			// The env token SHADOWS a disk token for the same endpoint
			// (GetTokenForEndpoint returns the env one first), so listing both
			// would report two logins where the process can only ever use one.
			// Test envAdded, never len(endpoints): the length also becomes
			// non-zero after the first DISK endpoint is appended, so under Go's
			// randomized map order an unrelated endpoint could suppress this
			// one — a different answer from `ox status` run to run.
			if normalized == envEP && envAdded {
				continue
			}
			if token != nil && token.AccessToken != "" && token.ExpiresAt.After(now) {
				endpoints = append(endpoints, normalized)
			}
		}
		return nil
	})
	return endpoints
}

// IsExpired checks if the token is expired or will expire within the buffer seconds
func (t *StoredToken) IsExpired(bufferSeconds int) bool {
	if bufferSeconds < 0 {
		bufferSeconds = 0
	}
	threshold := time.Now().Add(time.Duration(bufferSeconds) * time.Second)
	// !Before, not After: a token whose expiry instant equals the threshold is
	// EXPIRED. Strict After() treated the exact-expiry instant as still valid,
	// which is the wrong direction for a credential check — and it made the
	// boundary test fail whenever both time.Now() calls landed inside one clock
	// tick, which is exactly when the equality case arises.
	return !threshold.Before(t.ExpiresAt)
}

// Auth validation functions (IsAuthenticated, IsAuthenticatedForEndpoint,
// IsAuthCredentialValid, IsAuthCredentialValidForEndpoint, ValidateTokenServerSide)
// are defined in validate.go

// --- AuthClient Methods ---
// These methods allow using custom config directories for testing isolation.

// GetAuthFilePath returns the path to the auth token file for this client.
func (c *AuthClient) GetAuthFilePath() (string, error) {
	configDir := c.getConfigDir()
	if configDir == "" {
		return "", fmt.Errorf("failed to get config directory")
	}

	return filepath.Join(configDir, "auth.json"), nil
}

// GetToken loads the authentication token for this client's endpoint
func (c *AuthClient) GetToken() (*StoredToken, error) {
	ep := c.endpoint
	if ep == "" {
		ep = endpoint.Get()
	}
	return c.GetTokenForEndpoint(ep)
}

// GetTokenForEndpoint loads the authentication token for a specific API endpoint
func (c *AuthClient) GetTokenForEndpoint(ep string) (*StoredToken, error) {
	ep = endpoint.NormalizeEndpoint(ep)

	// Same fail-closed contract as the package-level function — the two must
	// never disagree about which principal is in play (see TestPackageAndClientAgree).
	envTok, envErr := envTokenOrError(ep)
	if envErr != nil {
		return nil, envErr
	}
	if envTok != nil {
		return envTok, nil
	}

	authPath, err := c.GetAuthFilePath()
	if err != nil {
		return nil, err
	}

	var token *StoredToken
	legacyOpt := withLegacyMigrationEndpoint(c.endpoint)
	trackerOpt := withEndpointTracker(c)
	err = withAuthFileRLocked(authPath, func(h *authFileHandle) error {
		store, loadErr := h.load()
		if loadErr != nil {
			return loadErr
		}
		t, exists := store.Tokens[ep]
		if exists {
			token = t
		}
		return nil
	}, legacyOpt, trackerOpt)
	if err != nil {
		return nil, err
	}

	return token, nil
}

// SaveToken saves the authentication token for this client's endpoint
func (c *AuthClient) SaveToken(token *StoredToken) error {
	ep := c.endpoint
	if ep == "" {
		ep = endpoint.Get()
	}
	return c.SaveTokenForEndpoint(ep, token)
}

// SaveTokenForEndpoint saves the authentication token for a specific API endpoint
func (c *AuthClient) SaveTokenForEndpoint(ep string, token *StoredToken) error {
	ep = endpoint.NormalizeEndpoint(ep)

	authPath, err := c.GetAuthFilePath()
	if err != nil {
		return err
	}

	legacyOpt := withLegacyMigrationEndpoint(c.endpoint)
	trackerOpt := withEndpointTracker(c)
	return withAuthFileLocked(authPath, func(h *authFileHandle) error {
		store, loadErr := h.load()
		if loadErr != nil {
			return loadErr
		}
		if store.Tokens == nil {
			store.Tokens = make(map[string]*StoredToken)
		}
		store.Tokens[ep] = token
		return h.save(store)
	}, legacyOpt, trackerOpt)
}

// RemoveToken deletes the authentication token for this client's endpoint
func (c *AuthClient) RemoveToken() error {
	ep := c.endpoint
	if ep == "" {
		ep = endpoint.Get()
	}
	return c.RemoveTokenForEndpoint(ep)
}

// RemoveTokenForEndpoint deletes the authentication token for a specific API endpoint
func (c *AuthClient) RemoveTokenForEndpoint(ep string) error {
	ep = endpoint.NormalizeEndpoint(ep)

	authPath, err := c.GetAuthFilePath()
	if err != nil {
		return err
	}

	legacyOpt := withLegacyMigrationEndpoint(c.endpoint)
	trackerOpt := withEndpointTracker(c)
	return withAuthFileLocked(authPath, func(h *authFileHandle) error {
		store, loadErr := h.load()
		if loadErr != nil {
			return loadErr
		}
		if store.Tokens == nil {
			return nil // nothing to remove
		}
		delete(store.Tokens, ep)
		return h.save(store)
	}, legacyOpt, trackerOpt)
}

// IsAuthenticated checks if a locally valid token exists for this client's endpoint.
// AuthClient is used for test isolation — uses local-only check (no server call).
func (c *AuthClient) IsAuthenticated() (bool, error) {
	ep := c.endpoint
	if ep == "" {
		ep = endpoint.Get()
	}
	return c.IsAuthCredentialValidForEndpoint(ep)
}

// IsAuthCredentialValidForEndpoint checks if a locally valid token exists for a
// specific endpoint. Does NOT contact the server. For test isolation only.
func (c *AuthClient) IsAuthCredentialValidForEndpoint(ep string) (bool, error) {
	token, err := c.EnsureValidTokenForEndpoint(ep, 0)
	if err != nil {
		// Same reasoning as the package-level IsAuthCredentialValidForEndpoint
		// in validate.go, and deliberately the same shape: a named-but-unusable
		// credential is a different fact from no credential, and only that one
		// is worth reporting. Everything else still degrades to "not
		// authenticated, no error". The twins must not diverge — a client-scoped
		// caller that reports a different verdict than the package-level one is
		// a bug report nobody can reproduce.
		//
		// Branch on THIS error rather than re-reading the token:
		// EnsureValidTokenForEndpoint returns GetTokenForEndpoint's error
		// verbatim, so re-calling would only add a file-lock round trip on the
		// failure path. That verbatim pass-through is load-bearing — if it ever
		// starts wrapping or swallowing, the malformed-env-token case in
		// TestAuthClientAuthChecks_MalformedEnvTokenSurfacesReason goes red.
		if errors.Is(err, ErrEnvTokenMalformed) {
			return false, err
		}
		raw, _ := c.GetTokenForEndpoint(ep)
		if raw != nil {
			return false, fmt.Errorf("token refresh failed: %w", err)
		}
		return false, nil
	}

	if token == nil {
		return false, nil
	}

	return true, nil
}
