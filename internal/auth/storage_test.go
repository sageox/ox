package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestTokenForTest returns a test token for testing (not saved)
func createTestTokenForTest(expiresIn time.Duration) *StoredToken {
	return &StoredToken{
		AccessToken:  "test-access-token",
		RefreshToken: "test-refresh-token",
		ExpiresAt:    time.Now().Add(expiresIn),
		TokenType:    "Bearer",
		Scope:        "openid profile email",
		UserInfo: UserInfo{
			UserID: "user123",
			Email:  "user@example.com",
			Name:   "Test User",
		},
	}
}

func TestGetToken_NoFile(t *testing.T) {
	t.Parallel()

	client := NewTestClient(t)

	token, err := client.GetToken()
	require.NoError(t, err)
	assert.Nil(t, token, "want nil when no token file exists")
}

func TestSaveAndGetToken(t *testing.T) {
	t.Parallel()

	client := NewTestClient(t)

	// create test token
	originalToken := createTestTokenForTest(1 * time.Hour)

	// save token
	require.NoError(t, client.SaveToken(originalToken))

	// retrieve token
	retrievedToken, err := client.GetToken()
	require.NoError(t, err)
	require.NotNil(t, retrievedToken, "expected token")

	// verify all fields match
	assert.Equal(t, originalToken.AccessToken, retrievedToken.AccessToken)
	assert.Equal(t, originalToken.RefreshToken, retrievedToken.RefreshToken)
	assert.Equal(t, originalToken.TokenType, retrievedToken.TokenType)
	assert.Equal(t, originalToken.Scope, retrievedToken.Scope)

	// verify ExpiresAt (allow small difference for JSON marshaling precision)
	timeDiff := retrievedToken.ExpiresAt.Sub(originalToken.ExpiresAt)
	assert.True(t, timeDiff <= time.Second && timeDiff >= -time.Second,
		"ExpiresAt = %v, want %v (diff: %v)", retrievedToken.ExpiresAt, originalToken.ExpiresAt, timeDiff)

	// verify UserInfo
	assert.Equal(t, originalToken.UserInfo.UserID, retrievedToken.UserInfo.UserID)
	assert.Equal(t, originalToken.UserInfo.Email, retrievedToken.UserInfo.Email)
	assert.Equal(t, originalToken.UserInfo.Name, retrievedToken.UserInfo.Name)
}

func TestSaveToken_AtomicWrite(t *testing.T) {
	t.Parallel()

	client := NewTestClient(t)

	token := createTestTokenForTest(1 * time.Hour)

	// save token
	require.NoError(t, client.SaveToken(token))

	// verify temp file was cleaned up
	authPath, err := client.GetAuthFilePath()
	require.NoError(t, err)

	tempPath := authPath + ".tmp"
	_, err = os.Stat(tempPath)
	assert.True(t, os.IsNotExist(err), "temp file still exists at %s", tempPath)

	// verify final file exists
	_, err = os.Stat(authPath)
	assert.NoError(t, err, "auth file does not exist at %s", authPath)
}

func TestSaveToken_Permissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping permission test on Windows")
	}

	t.Parallel()

	client := NewTestClient(t)

	token := createTestTokenForTest(1 * time.Hour)

	// save token
	require.NoError(t, client.SaveToken(token))

	// verify file permissions
	authPath, err := client.GetAuthFilePath()
	require.NoError(t, err)

	info, err := os.Stat(authPath)
	require.NoError(t, err)

	mode := info.Mode().Perm()
	expectedMode := os.FileMode(0600)
	assert.Equal(t, expectedMode, mode)

	// verify directory permissions
	configDir := filepath.Dir(authPath)
	dirInfo, err := os.Stat(configDir)
	require.NoError(t, err)

	dirMode := dirInfo.Mode().Perm()
	expectedDirMode := os.FileMode(0700)
	assert.Equal(t, expectedDirMode, dirMode)
}

func TestSaveToken_UpdatesExistingToken(t *testing.T) {
	t.Parallel()

	client := NewTestClient(t)

	// save first token
	token1 := createTestTokenForTest(1 * time.Hour)
	token1.AccessToken = "token1"
	require.NoError(t, client.SaveToken(token1))

	// save second token (should overwrite)
	token2 := createTestTokenForTest(2 * time.Hour)
	token2.AccessToken = "token2"
	require.NoError(t, client.SaveToken(token2))

	// verify second token is retrieved
	retrievedToken, err := client.GetToken()
	require.NoError(t, err)

	assert.Equal(t, "token2", retrievedToken.AccessToken)
}

func TestRemoveToken(t *testing.T) {
	t.Parallel()

	client := NewTestClient(t)

	// save token
	token := createTestTokenForTest(1 * time.Hour)
	require.NoError(t, client.SaveToken(token))

	// verify token exists
	retrievedToken, err := client.GetToken()
	require.NoError(t, err)
	require.NotNil(t, retrievedToken, "token should exist before removal")

	// remove token
	require.NoError(t, client.RemoveToken())

	// verify token no longer exists
	retrievedToken, err = client.GetToken()
	require.NoError(t, err)
	assert.Nil(t, retrievedToken, "want nil after removal")
}

func TestRemoveToken_NoFile(t *testing.T) {
	t.Parallel()

	client := NewTestClient(t)

	// attempt to remove non-existent token
	err := client.RemoveToken()
	assert.NoError(t, err, "want nil when file doesn't exist")
}

func TestIsExpired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		expiresIn     time.Duration
		bufferSeconds int
		want          bool
	}{
		{
			name:          "expired token",
			expiresIn:     -1 * time.Hour,
			bufferSeconds: 0,
			want:          true,
		},
		{
			name:          "valid token far in future",
			expiresIn:     1 * time.Hour,
			bufferSeconds: 0,
			want:          false,
		},
		{
			name:          "token expiring within buffer",
			expiresIn:     30 * time.Second,
			bufferSeconds: 60,
			want:          true,
		},
		{
			name:          "token expiring outside buffer",
			expiresIn:     120 * time.Second,
			bufferSeconds: 60,
			want:          false,
		},
		{
			name:          "negative buffer treated as zero",
			expiresIn:     1 * time.Hour,
			bufferSeconds: -100,
			want:          false,
		},
		{
			name:          "expired token with buffer",
			expiresIn:     -1 * time.Hour,
			bufferSeconds: 60,
			want:          true,
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			token := createTestTokenForTest(tt.expiresIn)
			got := token.IsExpired(tt.bufferSeconds)
			assert.Equal(t, tt.want, got, "IsExpired(%d) (expiresAt: %v, now: %v)",
				tt.bufferSeconds, token.ExpiresAt, time.Now())
		})
	}
}

func TestIsAuthenticated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setupToken  func(t *testing.T, client *AuthClient)
		want        bool
		wantErr     bool
		description string
	}{
		{
			name: "no token",
			setupToken: func(t *testing.T, client *AuthClient) {
				// no token setup
			},
			want:        false,
			wantErr:     false,
			description: "returns false when no token file exists",
		},
		{
			name: "valid token",
			setupToken: func(t *testing.T, client *AuthClient) {
				token := createTestTokenForTest(1 * time.Hour)
				require.NoError(t, client.SaveToken(token))
			},
			want:        true,
			wantErr:     false,
			description: "returns true for valid non-expired token",
		},
		{
			name: "expired token",
			setupToken: func(t *testing.T, client *AuthClient) {
				token := createTestTokenForTest(-1 * time.Hour)
				require.NoError(t, client.SaveToken(token))
			},
			want:        false,
			wantErr:     false,
			description: "returns false for expired token",
		},
		{
			name: "token expiring soon",
			setupToken: func(t *testing.T, client *AuthClient) {
				token := createTestTokenForTest(30 * time.Second)
				require.NoError(t, client.SaveToken(token))
			},
			want:        true,
			wantErr:     false,
			description: "returns true for token expiring soon (no buffer)",
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := NewTestClient(t)
			tt.setupToken(t, client)

			got, err := client.IsAuthenticated()
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got, tt.description)
		})
	}
}

func TestGetToken_CorruptedFile(t *testing.T) {
	t.Parallel()

	client := NewTestClient(t)

	// write corrupted JSON to auth file
	authPath, err := client.GetAuthFilePath()
	require.NoError(t, err)

	configDir := filepath.Dir(authPath)
	require.NoError(t, os.MkdirAll(configDir, 0700))

	corruptedData := []byte("{ invalid json }")
	require.NoError(t, os.WriteFile(authPath, corruptedData, 0600))

	// attempt to read corrupted file
	token, err := client.GetToken()
	assert.Error(t, err, "want error for corrupted JSON")
	assert.Nil(t, token, "want nil for corrupted JSON")
}

func TestGetToken_EmptyFile(t *testing.T) {
	t.Parallel()

	client := NewTestClient(t)

	// write empty file
	authPath, err := client.GetAuthFilePath()
	require.NoError(t, err)

	configDir := filepath.Dir(authPath)
	require.NoError(t, os.MkdirAll(configDir, 0700))

	require.NoError(t, os.WriteFile(authPath, []byte(""), 0600))

	// attempt to read empty file
	token, err := client.GetToken()
	assert.Error(t, err, "want error for empty file")
	assert.Nil(t, token, "want nil for empty file")
}

func TestSaveToken_JSONFormat(t *testing.T) {
	t.Parallel()

	client := NewTestClient(t)

	token := createTestTokenForTest(1 * time.Hour)

	require.NoError(t, client.SaveToken(token))

	// read raw file contents
	authPath, err := client.GetAuthFilePath()
	require.NoError(t, err)

	data, err := os.ReadFile(authPath)
	require.NoError(t, err)

	// verify it's valid JSON
	var result map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &result), "saved file contains invalid JSON")

	// verify it's indented (contains newlines for formatting)
	// MarshalIndent doesn't add trailing newline, so just check for internal newlines
	assert.True(t, len(data) > 0 && contains(data, '\n'), "saved JSON is not indented")
}

// contains checks if byte slice contains a byte
func contains(data []byte, b byte) bool {
	for _, c := range data {
		if c == b {
			return true
		}
	}
	return false
}

func TestStoredToken_IsExpired_EdgeCases(t *testing.T) {
	t.Parallel()

	// test with exact expiration time
	now := time.Now()
	token := &StoredToken{
		ExpiresAt: now,
	}

	// at exact expiration with no buffer, should be expired
	assert.True(t, token.IsExpired(0), "want true for token expiring at exactly now")

	// just before expiration
	token.ExpiresAt = now.Add(1 * time.Millisecond)
	assert.False(t, token.IsExpired(0), "want false for token expiring 1ms in future")

	// just after expiration
	token.ExpiresAt = now.Add(-1 * time.Millisecond)
	assert.True(t, token.IsExpired(0), "want true for token expired 1ms ago")
}

// --- Normalization tests ---

func TestNormalizeTokenKeys_NilStore(t *testing.T) {
	t.Parallel()
	// should not panic
	normalizeTokenKeys(nil)
	normalizeTokenKeys(&AuthStore{Tokens: nil})
}

func TestNormalizeTokenKeys_AlreadyNormalized(t *testing.T) {
	t.Parallel()

	token := createTestTokenForTest(1 * time.Hour)
	store := &AuthStore{
		Tokens: map[string]*StoredToken{
			"https://sageox.ai": token,
		},
	}

	normalizeTokenKeys(store)

	assert.Len(t, store.Tokens, 1)
	assert.NotNil(t, store.Tokens["https://sageox.ai"])
}

func TestNormalizeTokenKeys_SinglePrefixedKey(t *testing.T) {
	t.Parallel()

	token := createTestTokenForTest(1 * time.Hour)
	token.AccessToken = "my-token"
	store := &AuthStore{
		Tokens: map[string]*StoredToken{
			"https://api.sageox.ai": token,
		},
	}

	normalizeTokenKeys(store)

	assert.Len(t, store.Tokens, 1)
	assert.Nil(t, store.Tokens["https://api.sageox.ai"], "prefixed key should be removed")
	retrieved := store.Tokens["https://sageox.ai"]
	require.NotNil(t, retrieved, "normalized key should exist")
	assert.Equal(t, "my-token", retrieved.AccessToken)
}

func TestNormalizeTokenKeys_CollisionKeepsNewerToken(t *testing.T) {
	t.Parallel()

	olderToken := createTestTokenForTest(1 * time.Hour)
	olderToken.AccessToken = "older"

	newerToken := createTestTokenForTest(10 * time.Hour)
	newerToken.AccessToken = "newer"

	store := &AuthStore{
		Tokens: map[string]*StoredToken{
			"https://api.sageox.ai": olderToken,
			"https://www.sageox.ai": newerToken,
		},
	}

	normalizeTokenKeys(store)

	assert.Len(t, store.Tokens, 1)
	retrieved := store.Tokens["https://sageox.ai"]
	require.NotNil(t, retrieved, "normalized key should exist")
	assert.Equal(t, "newer", retrieved.AccessToken, "should keep the token with later ExpiresAt")
}

func TestNormalizeTokenKeys_CollisionWithExistingNormalized(t *testing.T) {
	t.Parallel()

	// key already exists at normalized form, plus a prefixed duplicate
	existingToken := createTestTokenForTest(10 * time.Hour)
	existingToken.AccessToken = "existing"

	prefixedToken := createTestTokenForTest(1 * time.Hour)
	prefixedToken.AccessToken = "prefixed"

	store := &AuthStore{
		Tokens: map[string]*StoredToken{
			"https://sageox.ai":     existingToken,
			"https://api.sageox.ai": prefixedToken,
		},
	}

	normalizeTokenKeys(store)

	assert.Len(t, store.Tokens, 1)
	retrieved := store.Tokens["https://sageox.ai"]
	require.NotNil(t, retrieved)
	assert.Equal(t, "existing", retrieved.AccessToken, "should keep existing token (newer ExpiresAt)")
}

func TestGetTokenForEndpoint_NormalizesPrefixedInput(t *testing.T) {
	t.Parallel()

	client := NewTestClient(t)

	// save token under normalized key
	token := createTestTokenForTest(1 * time.Hour)
	token.AccessToken = "saved-token"
	require.NoError(t, client.SaveTokenForEndpoint("https://sageox.ai", token))

	// retrieve with prefixed endpoint
	retrieved, err := client.GetTokenForEndpoint("https://api.sageox.ai")
	require.NoError(t, err)
	require.NotNil(t, retrieved, "should find token via prefixed endpoint")
	assert.Equal(t, "saved-token", retrieved.AccessToken)
}

func TestSaveTokenForEndpoint_NormalizesPrefixedInput(t *testing.T) {
	t.Parallel()

	client := NewTestClient(t)

	// save with prefixed endpoint
	token := createTestTokenForTest(1 * time.Hour)
	token.AccessToken = "prefixed-save"
	require.NoError(t, client.SaveTokenForEndpoint("https://api.sageox.ai", token))

	// verify on-disk key is normalized by reading raw JSON
	authPath, err := client.GetAuthFilePath()
	require.NoError(t, err)
	data, err := os.ReadFile(authPath)
	require.NoError(t, err)

	var rawStore struct {
		Tokens map[string]json.RawMessage `json:"tokens"`
	}
	require.NoError(t, json.Unmarshal(data, &rawStore))

	_, hasPrefixed := rawStore.Tokens["https://api.sageox.ai"]
	assert.False(t, hasPrefixed, "prefixed key should not be on disk")

	_, hasNormalized := rawStore.Tokens["https://sageox.ai"]
	assert.True(t, hasNormalized, "normalized key should be on disk")
}

func TestRemoveTokenForEndpoint_NormalizesPrefixedInput(t *testing.T) {
	t.Parallel()

	client := NewTestClient(t)

	// save under normalized key
	token := createTestTokenForTest(1 * time.Hour)
	require.NoError(t, client.SaveTokenForEndpoint("https://sageox.ai", token))

	// remove using prefixed endpoint
	require.NoError(t, client.RemoveTokenForEndpoint("https://www.sageox.ai"))

	// verify token is gone
	retrieved, err := client.GetTokenForEndpoint("https://sageox.ai")
	require.NoError(t, err)
	assert.Nil(t, retrieved, "token should be removed")
}

func TestIsAuthenticatedForEndpoint_NormalizesPrefixedInput(t *testing.T) {
	t.Parallel()

	client := NewTestClient(t)

	// save valid token under normalized key
	token := createTestTokenForTest(1 * time.Hour)
	require.NoError(t, client.SaveTokenForEndpoint("https://sageox.ai", token))

	// check auth with prefixed endpoint
	authed, err := client.IsAuthenticatedForEndpoint("https://app.sageox.ai")
	require.NoError(t, err)
	assert.True(t, authed, "should find token via prefixed endpoint")
}

func TestLoadAuthStore_NormalizesPrefixedKeysOnDisk(t *testing.T) {
	t.Parallel()

	client := NewTestClient(t)

	// write raw auth.json with prefixed keys directly
	authPath, err := client.GetAuthFilePath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(authPath), 0700))

	rawStore := AuthStore{
		Tokens: map[string]*StoredToken{
			"https://api.sageox.ai": {
				AccessToken: "api-token",
				ExpiresAt:   time.Now().Add(1 * time.Hour),
			},
		},
	}
	data, err := json.MarshalIndent(rawStore, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(authPath, data, 0600))

	// load via client (should normalize keys)
	retrieved, err := client.GetTokenForEndpoint("https://sageox.ai")
	require.NoError(t, err)
	require.NotNil(t, retrieved, "should find token under normalized key")
	assert.Equal(t, "api-token", retrieved.AccessToken)
}

// --- Multi-endpoint tests ---

func TestMultiEndpointTokens(t *testing.T) {
	t.Parallel()

	client := NewTestClient(t)

	endpoint1 := "https://api.example.com/"
	endpoint2 := "https://api.other.com/"

	// save tokens for different endpoints
	token1 := createTestTokenForTest(1 * time.Hour)
	token1.AccessToken = "token-for-endpoint1"
	require.NoError(t, client.SaveTokenForEndpoint(endpoint1, token1))

	token2 := createTestTokenForTest(2 * time.Hour)
	token2.AccessToken = "token-for-endpoint2"
	require.NoError(t, client.SaveTokenForEndpoint(endpoint2, token2))

	// verify tokens are isolated
	retrieved1, err := client.GetTokenForEndpoint(endpoint1)
	require.NoError(t, err)
	assert.Equal(t, "token-for-endpoint1", retrieved1.AccessToken)

	retrieved2, err := client.GetTokenForEndpoint(endpoint2)
	require.NoError(t, err)
	assert.Equal(t, "token-for-endpoint2", retrieved2.AccessToken)

	// verify removing one doesn't affect the other
	require.NoError(t, client.RemoveTokenForEndpoint(endpoint1))

	retrieved1, err = client.GetTokenForEndpoint(endpoint1)
	require.NoError(t, err)
	assert.Nil(t, retrieved1)

	// endpoint2 should still exist
	retrieved2, err = client.GetTokenForEndpoint(endpoint2)
	require.NoError(t, err)
	assert.NotNil(t, retrieved2, "endpoint2 token should still exist after removing endpoint1")
}

func TestWithEndpoint(t *testing.T) {
	t.Parallel()

	baseClient := NewTestClient(t)
	customEndpoint := "https://custom.api.example.com/"

	// create client with custom endpoint
	customClient := baseClient.WithEndpoint(customEndpoint)

	// save token via custom client
	token := createTestTokenForTest(1 * time.Hour)
	token.AccessToken = "custom-endpoint-token"
	require.NoError(t, customClient.SaveToken(token))

	// verify we can retrieve it via the custom client
	retrieved, err := customClient.GetToken()
	require.NoError(t, err)
	assert.Equal(t, "custom-endpoint-token", retrieved.AccessToken)

	// verify baseClient doesn't see it (different endpoint)
	baseToken, err := baseClient.GetToken()
	require.NoError(t, err)
	assert.Nil(t, baseToken, "baseClient should not see custom endpoint token")
}

// --- P0: Tests for silent customer breakage scenarios ---

// These use setupTestDir (not t.Parallel) because GetLoggedInEndpoints and
// GetUserID are package-level functions that use configDirOverride.

func TestGetLoggedInEndpoints_EmptyAccessToken(t *testing.T) {
	setupTestDir(t)

	// save a token with empty AccessToken — should NOT appear in logged-in endpoints
	emptyToken := &StoredToken{
		AccessToken:  "",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	require.NoError(t, SaveTokenForEndpoint("https://sageox.ai", emptyToken))

	// save a valid token
	validToken := &StoredToken{
		AccessToken:  "valid-token",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	require.NoError(t, SaveTokenForEndpoint("https://staging.sageox.ai", validToken))

	endpoints := GetLoggedInEndpoints()
	assert.Len(t, endpoints, 1, "only the valid token should be returned")
	assert.Contains(t, endpoints, "https://staging.sageox.ai")
}

func TestGetLoggedInEndpoints_MixedValidity(t *testing.T) {
	setupTestDir(t)

	// expired token
	expired := &StoredToken{
		AccessToken: "expired",
		ExpiresAt:   time.Now().Add(-time.Hour),
	}
	require.NoError(t, SaveTokenForEndpoint("https://expired.sageox.ai", expired))

	// valid token
	valid := &StoredToken{
		AccessToken: "valid",
		ExpiresAt:   time.Now().Add(time.Hour),
	}
	require.NoError(t, SaveTokenForEndpoint("https://valid.sageox.ai", valid))

	endpoints := GetLoggedInEndpoints()
	assert.Len(t, endpoints, 1)
	assert.Contains(t, endpoints, "https://valid.sageox.ai")
}

func TestGetUserID_WithUserInfo(t *testing.T) {
	setupTestDir(t)

	token := &StoredToken{
		AccessToken: "tok",
		ExpiresAt:   time.Now().Add(time.Hour),
		UserInfo:    UserInfo{UserID: "user-abc"},
	}
	require.NoError(t, SaveToken(token))

	uid := GetUserID("")
	assert.Equal(t, "user-abc", uid)
}

func TestGetUserID_EmptyUserInfo(t *testing.T) {
	setupTestDir(t)

	token := &StoredToken{
		AccessToken: "tok",
		ExpiresAt:   time.Now().Add(time.Hour),
		UserInfo:    UserInfo{},
	}
	require.NoError(t, SaveToken(token))

	uid := GetUserID("")
	assert.Equal(t, "", uid, "empty UserInfo should return empty user ID")
}

func TestGetUserID_NoToken(t *testing.T) {
	setupTestDir(t)
	uid := GetUserID("")
	assert.Equal(t, "", uid, "no token should return empty user ID")
}

func TestLoadAuthStore_LegacyMigration(t *testing.T) {
	t.Parallel()
	client := NewTestClient(t)

	// write legacy single-token format directly to auth.json
	legacyToken := StoredToken{
		AccessToken:  "legacy-access",
		RefreshToken: "legacy-refresh",
		ExpiresAt:    time.Now().Add(time.Hour),
		TokenType:    "Bearer",
	}
	data, err := json.Marshal(legacyToken)
	require.NoError(t, err)

	authPath, err := client.GetAuthFilePath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(authPath), 0700))
	require.NoError(t, os.WriteFile(authPath, data, 0600))

	// loadAuthStore should migrate to multi-endpoint format
	store, err := client.loadAuthStore()
	require.NoError(t, err)
	require.NotNil(t, store)
	require.NotNil(t, store.Tokens)
	assert.Len(t, store.Tokens, 1, "legacy token should be migrated to single entry")

	// verify the token is accessible
	for _, token := range store.Tokens {
		assert.Equal(t, "legacy-access", token.AccessToken)
		assert.Equal(t, "legacy-refresh", token.RefreshToken)
	}
}

func TestLoadAuthStore_TruncatedJSON(t *testing.T) {
	t.Parallel()
	client := NewTestClient(t)

	// write truncated JSON (simulating crash during write)
	authPath, err := client.GetAuthFilePath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(authPath), 0700))
	require.NoError(t, os.WriteFile(authPath, []byte(`{"tokens": {"https://sage`), 0600))

	_, err = client.loadAuthStore()
	require.Error(t, err, "truncated JSON should return error")
	assert.Contains(t, err.Error(), "failed to parse auth file")
}
