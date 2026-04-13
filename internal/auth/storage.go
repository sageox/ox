package auth

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
)

// UserInfo contains user information from the authentication provider
type UserInfo struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
}

// StoredToken represents the authentication token stored locally
type StoredToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
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

// GetTokenForEndpoint loads the raw stored token for a specific endpoint.
// Internal use only (refresh logic, display/identity checks).
// Callers making API requests MUST use EnsureValidTokenForEndpoint(ep, 300)
// instead — raw tokens may be expired.
func GetTokenForEndpoint(ep string) (*StoredToken, error) {
	ep = endpoint.NormalizeEndpoint(ep)

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
	authPath, err := GetAuthFilePath()
	if err != nil {
		return nil
	}

	var endpoints []string
	_ = withAuthFileRLocked(authPath, func(h *authFileHandle) error {
		store, loadErr := h.load()
		if loadErr != nil {
			return loadErr
		}
		now := time.Now()
		for ep, token := range store.Tokens {
			if token != nil && token.AccessToken != "" && token.ExpiresAt.After(now) {
				endpoints = append(endpoints, endpoint.NormalizeEndpoint(ep))
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
	return threshold.After(t.ExpiresAt)
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
