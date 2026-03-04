package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/fileutil"
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
	ExpiresAt    time.Time `json:"expires_at"`
	TokenType    string    `json:"token_type"`
	Scope        string    `json:"scope"`
	UserInfo     UserInfo  `json:"user_info"`
}

// AuthStore holds tokens for multiple API endpoints
type AuthStore struct {
	// Tokens maps API endpoint URLs to their authentication tokens
	// e.g., "https://api.sageox.ai/" -> token, "https://sageox.walmart.com/" -> token
	Tokens map[string]*StoredToken `json:"tokens"`
}

// configDirOverride allows tests to override the config directory
var configDirOverride string

// GetAuthFilePath returns the path to the auth token file.
//
// Path Resolution (via internal/paths package):
//
//	Default:           ~/.sageox/config/auth.json
//	With OX_XDG_ENABLE: $XDG_CONFIG_HOME/sageox/auth.json
//
// See internal/paths/doc.go for architecture rationale.
func GetAuthFilePath() (string, error) {
	// allow tests to override config directory
	if configDirOverride != "" {
		return filepath.Join(configDirOverride, "sageox", "auth.json"), nil
	}

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

// loadAuthStore loads the entire auth store from disk
func loadAuthStore() (*AuthStore, error) {
	authPath, err := GetAuthFilePath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(authPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &AuthStore{Tokens: make(map[string]*StoredToken)}, nil
		}
		return nil, fmt.Errorf("failed to read auth file: %w", err)
	}

	// try new multi-endpoint format first
	var store AuthStore
	if err := json.Unmarshal(data, &store); err == nil && store.Tokens != nil {
		normalizeTokenKeys(&store)
		return &store, nil
	}

	// migrate from legacy single-token format
	var legacyToken StoredToken
	if err := json.Unmarshal(data, &legacyToken); err != nil {
		return nil, fmt.Errorf("failed to parse auth file: %w", err)
	}

	// migrate legacy token to new format using default endpoint
	store = AuthStore{
		Tokens: map[string]*StoredToken{
			endpoint.Get(): &legacyToken,
		},
	}

	// save migrated format (best effort)
	_ = saveAuthStore(&store)

	return &store, nil
}

// saveAuthStore saves the entire auth store to disk
func saveAuthStore(store *AuthStore) error {
	authPath, err := GetAuthFilePath()
	if err != nil {
		return err
	}

	// SECURITY: directory permission 0700 = owner-only access.
	// Auth tokens are sensitive credentials; prevent other users on shared
	// systems from reading them.
	configDir := filepath.Dir(authPath)
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// SECURITY: atomic write with fsync prevents partial/corrupt token files.
	if err := fileutil.AtomicWriteJSON(authPath, store, 0600); err != nil {
		return fmt.Errorf("failed to save auth store: %w", err)
	}

	return nil
}

// GetToken loads the authentication token for the current API endpoint
func GetToken() (*StoredToken, error) {
	return GetTokenForEndpoint(endpoint.Get())
}

// GetTokenForEndpoint loads the authentication token for a specific API endpoint
func GetTokenForEndpoint(ep string) (*StoredToken, error) {
	ep = endpoint.NormalizeEndpoint(ep)

	store, err := loadAuthStore()
	if err != nil {
		return nil, err
	}

	token, exists := store.Tokens[ep]
	if !exists {
		return nil, nil // not authenticated for this endpoint
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

// SaveToken saves the authentication token for the current API endpoint
func SaveToken(token *StoredToken) error {
	return SaveTokenForEndpoint(endpoint.Get(), token)
}

// SaveTokenForEndpoint saves the authentication token for a specific API endpoint
func SaveTokenForEndpoint(ep string, token *StoredToken) error {
	ep = endpoint.NormalizeEndpoint(ep)

	store, err := loadAuthStore()
	if err != nil {
		return err
	}

	if store.Tokens == nil {
		store.Tokens = make(map[string]*StoredToken)
	}

	store.Tokens[ep] = token
	return saveAuthStore(store)
}

// RemoveToken deletes the authentication token for the current API endpoint
func RemoveToken() error {
	return RemoveTokenForEndpoint(endpoint.Get())
}

// RemoveTokenForEndpoint deletes the authentication token for a specific API endpoint
func RemoveTokenForEndpoint(ep string) error {
	ep = endpoint.NormalizeEndpoint(ep)

	store, err := loadAuthStore()
	if err != nil {
		return err
	}

	if store.Tokens == nil {
		return nil // nothing to remove
	}

	delete(store.Tokens, ep)
	return saveAuthStore(store)
}

// ListEndpoints returns all endpoints that have stored tokens
func ListEndpoints() ([]string, error) {
	store, err := loadAuthStore()
	if err != nil {
		return nil, err
	}

	endpoints := make([]string, 0, len(store.Tokens))
	for ep := range store.Tokens {
		endpoints = append(endpoints, endpoint.NormalizeEndpoint(ep))
	}
	return endpoints, nil
}

// GetLoggedInEndpoints returns all endpoints with valid (non-expired) tokens.
// Returns nil if no valid tokens exist.
func GetLoggedInEndpoints() []string {
	store, err := loadAuthStore()
	if err != nil || store == nil {
		return nil
	}

	var endpoints []string
	now := time.Now()
	for ep, token := range store.Tokens {
		if token != nil && token.AccessToken != "" && token.ExpiresAt.After(now) {
			endpoints = append(endpoints, endpoint.NormalizeEndpoint(ep))
		}
	}
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

// IsAuthenticated checks if a valid non-expired token exists for the current endpoint
func IsAuthenticated() (bool, error) {
	return IsAuthenticatedForEndpoint(endpoint.Get())
}

// IsAuthenticatedForEndpoint checks if a valid non-expired token exists for a specific endpoint
func IsAuthenticatedForEndpoint(endpoint string) (bool, error) {
	token, err := GetTokenForEndpoint(endpoint)
	if err != nil {
		return false, err
	}

	if token == nil {
		return false, nil
	}

	// check if token is expired
	if time.Now().After(token.ExpiresAt) {
		return false, nil
	}

	return true, nil
}

// --- AuthClient Methods ---
// These methods allow using custom config directories for testing isolation.

// GetAuthFilePath returns the path to the auth token file for this client.
func (c *AuthClient) GetAuthFilePath() (string, error) {
	configDir := c.getConfigDir()
	if configDir == "" {
		return "", fmt.Errorf("failed to get config directory")
	}

	// auth.json is stored in {configDir}/auth.json
	// configDir is already the sageox config directory (e.g., ~/.config/sageox)
	return filepath.Join(configDir, "auth.json"), nil
}

// loadAuthStore loads the entire auth store from disk for this client
func (c *AuthClient) loadAuthStore() (*AuthStore, error) {
	authPath, err := c.GetAuthFilePath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(authPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &AuthStore{Tokens: make(map[string]*StoredToken)}, nil
		}
		return nil, fmt.Errorf("failed to read auth file: %w", err)
	}

	// try new multi-endpoint format first
	var store AuthStore
	if err := json.Unmarshal(data, &store); err == nil && store.Tokens != nil {
		normalizeTokenKeys(&store)
		return &store, nil
	}

	// migrate from legacy single-token format
	var legacyToken StoredToken
	if err := json.Unmarshal(data, &legacyToken); err != nil {
		return nil, fmt.Errorf("failed to parse auth file: %w", err)
	}

	// migrate legacy token to new format using client's endpoint
	ep := c.endpoint
	if ep == "" {
		ep = endpoint.Get()
	}
	store = AuthStore{
		Tokens: map[string]*StoredToken{
			ep: &legacyToken,
		},
	}

	// save migrated format (best effort)
	_ = c.saveAuthStore(&store)

	return &store, nil
}

// saveAuthStore saves the entire auth store to disk for this client
func (c *AuthClient) saveAuthStore(store *AuthStore) error {
	authPath, err := c.GetAuthFilePath()
	if err != nil {
		return err
	}

	// SECURITY: directory permission 0700 = owner-only access.
	configDir := filepath.Dir(authPath)
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// SECURITY: atomic write with fsync prevents partial/corrupt token files.
	if err := fileutil.AtomicWriteJSON(authPath, store, 0600); err != nil {
		return fmt.Errorf("failed to save auth store: %w", err)
	}

	return nil
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

	store, err := c.loadAuthStore()
	if err != nil {
		return nil, err
	}

	token, exists := store.Tokens[ep]
	if !exists {
		return nil, nil // not authenticated for this endpoint
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

	store, err := c.loadAuthStore()
	if err != nil {
		return err
	}

	if store.Tokens == nil {
		store.Tokens = make(map[string]*StoredToken)
	}

	store.Tokens[ep] = token
	return c.saveAuthStore(store)
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

	store, err := c.loadAuthStore()
	if err != nil {
		return err
	}

	if store.Tokens == nil {
		return nil // nothing to remove
	}

	delete(store.Tokens, ep)
	return c.saveAuthStore(store)
}

// IsAuthenticated checks if a valid non-expired token exists for this client's endpoint
func (c *AuthClient) IsAuthenticated() (bool, error) {
	ep := c.endpoint
	if ep == "" {
		ep = endpoint.Get()
	}
	return c.IsAuthenticatedForEndpoint(ep)
}

// IsAuthenticatedForEndpoint checks if a valid non-expired token exists for a specific endpoint
func (c *AuthClient) IsAuthenticatedForEndpoint(ep string) (bool, error) {
	token, err := c.GetTokenForEndpoint(ep)
	if err != nil {
		return false, err
	}

	if token == nil {
		return false, nil
	}

	// check if token is expired
	if time.Now().After(token.ExpiresAt) {
		return false, nil
	}

	return true, nil
}
