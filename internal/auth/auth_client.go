package auth

import (
	"log/slog"
	"strings"
	"sync"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
)

// AuthClient provides authentication operations with configurable storage location.
// Use NewAuthClient() for production or NewAuthClientWithDir() for testing.
type AuthClient struct {
	configDir string // base config directory (e.g., ~/.config or test temp dir)
	endpoint  string // API endpoint URL

	// knownEndpoints tracks endpoints seen in previous loads within this
	// client's lifetime. Used to detect when another process overwrites
	// auth.json and removes credentials. Instance-scoped so parallel tests
	// with isolated AuthClients don't cross-contaminate.
	knownEndpointsMu sync.Mutex
	knownEndpoints   map[string]struct{}
}

// NewAuthClient creates an AuthClient using the default config directory and endpoint.
func NewAuthClient() *AuthClient {
	return &AuthClient{
		configDir:      "", // empty means use default from config.GetUserConfigDir()
		endpoint:       endpoint.Get(),
		knownEndpoints: make(map[string]struct{}),
	}
}

// NewAuthClientWithDir creates an AuthClient with a custom config directory.
// Primarily used for testing to isolate each test's auth storage.
func NewAuthClientWithDir(configDir string) *AuthClient {
	return &AuthClient{
		configDir:      configDir,
		endpoint:       endpoint.Get(),
		knownEndpoints: make(map[string]struct{}),
	}
}

// WithEndpoint returns a new AuthClient using the specified API endpoint.
// Trailing slashes are stripped to prevent double slashes in URL paths.
func (c *AuthClient) WithEndpoint(ep string) *AuthClient {
	return &AuthClient{
		configDir:      c.configDir,
		endpoint:       strings.TrimSuffix(ep, "/"),
		knownEndpoints: make(map[string]struct{}),
	}
}

// getConfigDir returns the effective config directory.
func (c *AuthClient) getConfigDir() string {
	if c.configDir != "" {
		return c.configDir
	}
	return config.GetUserConfigDir()
}

// Endpoint returns the configured API endpoint.
func (c *AuthClient) Endpoint() string {
	return c.endpoint
}

// checkKnownEndpoints compares the loaded store against endpoints this client
// has previously seen. Logs an ERROR if any previously-known endpoint has
// disappeared — this indicates another process may have overwritten auth.json.
func (c *AuthClient) checkKnownEndpoints(store *AuthStore) {
	c.knownEndpointsMu.Lock()
	defer c.knownEndpointsMu.Unlock()

	if c.knownEndpoints == nil {
		c.knownEndpoints = make(map[string]struct{})
	}

	for ep := range c.knownEndpoints {
		if _, ok := store.Tokens[ep]; !ok {
			slog.Error("auth: credential disappeared between loads",
				"endpoint", ep,
				"remaining_endpoints", len(store.Tokens),
				"action", "investigate — another process may have overwritten auth.json")
		}
	}

	for ep := range store.Tokens {
		c.knownEndpoints[ep] = struct{}{}
	}
}

// defaultClient is the package-level client for backward compatibility.
var defaultClient = NewAuthClient()
