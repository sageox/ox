package auth

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// NewTestClient creates an AuthClient with isolated storage for parallel testing.
// Each test gets its own temp directory, enabling t.Parallel().
// Uses a nested path (tempDir/sageox) to match production structure where
// configDir is ~/.config/sageox and auth.json is at configDir/auth.json.
// This allows permission tests to verify directory creation with 0700.
func NewTestClient(t *testing.T) *AuthClient {
	t.Helper()
	return NewAuthClientWithDir(filepath.Join(t.TempDir(), "sageox"))
}

// CreateTestTokenForClient creates a test token and saves it to the given client
func CreateTestTokenForClient(t *testing.T, client *AuthClient, expiresIn time.Duration) *StoredToken {
	t.Helper()
	token := &StoredToken{
		AccessToken:  "test-access-token",
		RefreshToken: "test-refresh-token",
		ExpiresAt:    time.Now().Add(expiresIn),
		TokenType:    "Bearer",
		Scope:        "user:profile sageox:write",
		UserInfo: UserInfo{
			UserID: "user-123",
			Email:  "test@example.com",
			Name:   "Test User",
		},
	}
	require.NoError(t, client.SaveToken(token), "failed to save test token")
	return token
}

// createTestToken returns a test token for testing (not saved).
func createTestToken(expiresIn time.Duration) *StoredToken {
	return &StoredToken{
		AccessToken:  "test-access-token",
		RefreshToken: "test-refresh-token",
		ExpiresAt:    time.Now().Add(expiresIn),
		TokenType:    "Bearer",
		Scope:        "user:profile sageox:write",
		UserInfo: UserInfo{
			UserID: "user-123",
			Email:  "test@example.com",
			Name:   "Test User",
		},
	}
}
