package gitserver

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

// setupTestDir creates a temporary directory and overrides the config path.
// Also forces file storage to ensure deterministic tests.
func setupTestDir(t *testing.T) string {
	tempDir := t.TempDir()

	// save original override values
	originalConfigOverride := configDirOverride
	originalForceFile := forceFileStorage

	// set overrides for this test
	configDirOverride = tempDir
	forceFileStorage = true

	// restore on cleanup
	t.Cleanup(func() {
		configDirOverride = originalConfigOverride
		forceFileStorage = originalForceFile
	})

	return tempDir
}

// createTestCredentials returns test credentials for testing
func createTestCredentials(expiresIn time.Duration) GitCredentials {
	return GitCredentials{
		Token:     "glpat-test-token-12345",
		ServerURL: "https://git.example.com",
		Username:  "testuser",
		ExpiresAt: time.Now().Add(expiresIn),
		Repos: map[string]RepoEntry{
			"team-alpha": {
				Name: "team-alpha",
				Type: "team-context",
				URL:  "https://git.example.com/teams/alpha.git",
			},
		},
	}
}

func TestLoadCredentials_NoFile(t *testing.T) {
	setupTestDir(t)

	creds, err := LoadCredentialsForEndpoint("")
	require.NoError(t, err)
	assert.Nil(t, creds)
}

func TestSaveAndLoadCredentials(t *testing.T) {
	setupTestDir(t)

	// create test credentials
	originalCreds := createTestCredentials(1 * time.Hour)

	// save credentials
	require.NoError(t, SaveCredentialsForEndpoint("", originalCreds))

	// retrieve credentials
	retrievedCreds, err := LoadCredentialsForEndpoint("")
	require.NoError(t, err)
	require.NotNil(t, retrievedCreds)

	// verify all fields match
	assert.Equal(t, originalCreds.Token, retrievedCreds.Token)
	assert.Equal(t, originalCreds.ServerURL, retrievedCreds.ServerURL)
	assert.Equal(t, originalCreds.Username, retrievedCreds.Username)
	assert.Len(t, retrievedCreds.Repos, len(originalCreds.Repos))

	teamAlpha := retrievedCreds.GetRepo("team-alpha")
	require.NotNil(t, teamAlpha)
	assert.Equal(t, originalCreds.Repos["team-alpha"].URL, teamAlpha.URL)

	// verify ExpiresAt (allow small difference for JSON marshaling precision)
	timeDiff := retrievedCreds.ExpiresAt.Sub(originalCreds.ExpiresAt)
	assert.True(t, timeDiff <= time.Second && timeDiff >= -time.Second, "ExpiresAt = %v, want %v (diff: %v)", retrievedCreds.ExpiresAt, originalCreds.ExpiresAt, timeDiff)
}

func TestSaveCredentials_AtomicWrite(t *testing.T) {
	setupTestDir(t)

	creds := createTestCredentials(1 * time.Hour)

	// save credentials
	require.NoError(t, SaveCredentialsForEndpoint("", creds))

	// verify temp file was cleaned up
	credsPath, err := getCredentialsFilePath()
	require.NoError(t, err)

	tempPath := credsPath + ".tmp"
	_, err = os.Stat(tempPath)
	assert.True(t, os.IsNotExist(err), "temp file still exists at %s", tempPath)

	// verify final file exists
	_, err = os.Stat(credsPath)
	assert.NoError(t, err, "git credentials file does not exist at %s", credsPath)
}

func TestSaveCredentials_Permissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping permission test on Windows")
	}

	setupTestDir(t)

	creds := createTestCredentials(1 * time.Hour)

	// save credentials
	require.NoError(t, SaveCredentialsForEndpoint("", creds))

	// verify file permissions
	credsPath, err := getCredentialsFilePath()
	require.NoError(t, err)

	info, err := os.Stat(credsPath)
	require.NoError(t, err)

	mode := info.Mode().Perm()
	expectedMode := os.FileMode(0600)
	assert.Equal(t, expectedMode, mode)

	// verify directory permissions
	configDir := filepath.Dir(credsPath)
	dirInfo, err := os.Stat(configDir)
	require.NoError(t, err)

	dirMode := dirInfo.Mode().Perm()
	expectedDirMode := os.FileMode(0700)
	assert.Equal(t, expectedDirMode, dirMode)
}

func TestSaveCredentials_UpdatesExisting(t *testing.T) {
	setupTestDir(t)

	// save first credentials
	creds1 := createTestCredentials(1 * time.Hour)
	creds1.Token = "token1"
	require.NoError(t, SaveCredentialsForEndpoint("", creds1))

	// save second credentials (should overwrite)
	creds2 := createTestCredentials(2 * time.Hour)
	creds2.Token = "token2"
	require.NoError(t, SaveCredentialsForEndpoint("", creds2))

	// verify second credentials are retrieved
	retrievedCreds, err := LoadCredentialsForEndpoint("")
	require.NoError(t, err)

	assert.Equal(t, "token2", retrievedCreds.Token)
}

func TestRemoveCredentials(t *testing.T) {
	setupTestDir(t)

	// save credentials
	creds := createTestCredentials(1 * time.Hour)
	require.NoError(t, SaveCredentialsForEndpoint("", creds))

	// verify credentials exist
	retrievedCreds, err := LoadCredentialsForEndpoint("")
	require.NoError(t, err)
	require.NotNil(t, retrievedCreds, "credentials should exist before removal")

	// remove credentials
	require.NoError(t, RemoveCredentials())

	// verify credentials no longer exist
	retrievedCreds, err = LoadCredentialsForEndpoint("")
	require.NoError(t, err)
	assert.Nil(t, retrievedCreds)
}

func TestRemoveCredentials_NoFile(t *testing.T) {
	setupTestDir(t)

	// attempt to remove non-existent credentials
	assert.NoError(t, RemoveCredentials())
}

func TestGitCredentials_IsExpired(t *testing.T) {
	tests := []struct {
		name      string
		expiresIn time.Duration
		zeroTime  bool
		want      bool
	}{
		{
			name:      "expired credentials",
			expiresIn: -1 * time.Hour,
			want:      true,
		},
		{
			name:      "valid credentials far in future",
			expiresIn: 1 * time.Hour,
			want:      false,
		},
		{
			name:      "credentials expiring soon",
			expiresIn: 30 * time.Second,
			want:      false,
		},
		{
			name:     "no expiration set (zero time)",
			zeroTime: true,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var creds GitCredentials
			if tt.zeroTime {
				creds = GitCredentials{Token: "test"}
			} else {
				creds = createTestCredentials(tt.expiresIn)
			}
			got := creds.IsExpired()
			assert.Equal(t, tt.want, got, "expiresAt: %v, now: %v", creds.ExpiresAt, time.Now())
		})
	}
}

func TestLoadCredentials_CorruptedFile(t *testing.T) {
	setupTestDir(t)

	// write corrupted JSON to credentials file
	credsPath, err := getCredentialsFilePath()
	require.NoError(t, err)

	configDir := filepath.Dir(credsPath)
	require.NoError(t, os.MkdirAll(configDir, 0700))

	corruptedData := []byte("{ invalid json }")
	require.NoError(t, os.WriteFile(credsPath, corruptedData, 0600))

	// attempt to read corrupted file
	creds, err := LoadCredentialsForEndpoint("")
	assert.Error(t, err)
	assert.Nil(t, creds)
}

func TestLoadCredentials_EmptyFile(t *testing.T) {
	setupTestDir(t)

	// write empty file
	credsPath, err := getCredentialsFilePath()
	require.NoError(t, err)

	configDir := filepath.Dir(credsPath)
	require.NoError(t, os.MkdirAll(configDir, 0700))
	require.NoError(t, os.WriteFile(credsPath, []byte(""), 0600))

	// attempt to read empty file
	creds, err := LoadCredentialsForEndpoint("")
	assert.Error(t, err)
	assert.Nil(t, creds)
}

func TestSaveCredentials_JSONFormat(t *testing.T) {
	setupTestDir(t)

	creds := createTestCredentials(1 * time.Hour)

	require.NoError(t, SaveCredentialsForEndpoint("", creds))

	// read raw file contents
	credsPath, err := getCredentialsFilePath()
	require.NoError(t, err)

	data, err := os.ReadFile(credsPath)
	require.NoError(t, err)

	// verify it's valid JSON
	var result map[string]interface{}
	assert.NoError(t, json.Unmarshal(data, &result))

	// verify it's indented (contains newlines for formatting)
	assert.True(t, len(data) > 0 && contains(data, '\n'), "saved JSON is not indented")
}

func TestGetStorageBackend_ForcedFile(t *testing.T) {
	setupTestDir(t)

	// with forceFileStorage=true (set by setupTestDir), should return "file"
	backend := GetStorageBackend()
	assert.Equal(t, "file", backend)
}

func TestGetCredentialsFilePath_EnvOverride(t *testing.T) {
	tempDir := t.TempDir()
	customPath := filepath.Join(tempDir, "custom-creds.json")

	// set env var
	originalEnv := os.Getenv("OX_GIT_CREDENTIALS_FILE")
	t.Setenv("OX_GIT_CREDENTIALS_FILE", customPath)
	t.Cleanup(func() {
		if originalEnv != "" {
			os.Setenv("OX_GIT_CREDENTIALS_FILE", originalEnv)
		} else {
			os.Unsetenv("OX_GIT_CREDENTIALS_FILE")
		}
	})

	// verify path uses env var
	path, err := getCredentialsFilePath()
	require.NoError(t, err)
	assert.Equal(t, customPath, path)
}

func TestTestSetForceFileStorage(t *testing.T) {
	// save original state
	original := forceFileStorage

	// test setting to true
	prev := TestSetForceFileStorage(true)
	assert.Equal(t, original, prev)
	assert.True(t, forceFileStorage)

	// test setting to false
	prev = TestSetForceFileStorage(false)
	assert.True(t, prev)
	assert.False(t, forceFileStorage)

	// restore
	forceFileStorage = original
}

func TestGitCredentials_GetRepo(t *testing.T) {
	creds := createTestCredentials(1 * time.Hour)

	// test getting existing repo
	repo := creds.GetRepo("team-alpha")
	assert.NotNil(t, repo)

	// test getting non-existent repo
	repo = creds.GetRepo("non-existent")
	assert.Nil(t, repo)

	// test with nil repos map
	emptyCreds := GitCredentials{Token: "test"}
	repo = emptyCreds.GetRepo("anything")
	assert.Nil(t, repo)
}

func TestGitCredentials_AddRepo(t *testing.T) {
	creds := GitCredentials{Token: "test"}

	// add first repo (should initialize map, keyed by TeamID)
	creds.AddRepo(RepoEntry{
		Name:   "Alpha Team",
		Type:   "team-context",
		URL:    "https://git.example.com/teams/alpha.git",
		TeamID: "team_alpha",
	})

	require.NotNil(t, creds.Repos)
	assert.Len(t, creds.Repos, 1)

	// add second repo
	creds.AddRepo(RepoEntry{
		Name:   "Beta Team",
		Type:   "team-context",
		URL:    "https://git.example.com/teams/beta.git",
		TeamID: "team_beta",
	})

	assert.Len(t, creds.Repos, 2)

	// update existing repo (same TeamID overwrites)
	creds.AddRepo(RepoEntry{
		Name:   "Alpha Team",
		Type:   "team-context",
		URL:    "https://git.example.com/teams/alpha-updated.git",
		TeamID: "team_alpha",
	})

	assert.Len(t, creds.Repos, 2)
	repo := creds.GetRepo("team_alpha")
	require.NotNil(t, repo)
	assert.Equal(t, "https://git.example.com/teams/alpha-updated.git", repo.URL)
	assert.Equal(t, "Alpha Team", repo.Name, "display name preserved")
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

// --- Per-Endpoint Credential Tests ---

func TestEndpointSlug(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		expected string
	}{
		{
			name:     "localhost with port",
			endpoint: "http://localhost:3000",
			expected: "localhost",
		},
		{
			name:     "localhost without port",
			endpoint: "http://localhost",
			expected: "localhost",
		},
		{
			name:     "127.0.0.1 with port",
			endpoint: "http://127.0.0.1:3000",
			expected: "localhost",
		},
		{
			name:     "test.sageox.ai",
			endpoint: "https://test.sageox.ai",
			expected: "test.sageox.ai",
		},
		{
			name:     "sageox.ai production",
			endpoint: "https://sageox.ai",
			expected: "sageox.ai",
		},
		{
			name:     "empty endpoint",
			endpoint: "",
			expected: "",
		},
		{
			name:     "bare hostname treated as valid",
			endpoint: "not-a-url",
			expected: "not-a-url", // bare strings are treated as hostnames
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := endpointSlug(tc.endpoint)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestKeyringUserForEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		expected string
	}{
		{
			name:     "empty endpoint uses default",
			endpoint: "",
			expected: "git-credentials",
		},
		{
			name:     "localhost endpoint",
			endpoint: "http://localhost:3000",
			expected: "git-credentials:localhost",
		},
		{
			name:     "test.sageox.ai endpoint",
			endpoint: "https://test.sageox.ai",
			expected: "git-credentials:test.sageox.ai",
		},
		{
			name:     "production endpoint",
			endpoint: "https://sageox.ai",
			expected: "git-credentials:sageox.ai",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := keyringUserForEndpoint(tc.endpoint)
			assert.Equal(t, tc.expected, result)
		})
	}
}

// TestMultiEndpointCredentials_Isolation verifies that credentials for different
// endpoints are stored and loaded independently - the core multi-endpoint behavior.
func TestMultiEndpointCredentials_Isolation(t *testing.T) {
	setupTestDir(t)

	// credentials for localhost (devcontainer)
	localCreds := GitCredentials{
		Token:     "glpat-local-token",
		ServerURL: "http://localhost:8929",
		Username:  "localuser",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos: map[string]RepoEntry{
			"team-local": {
				Name: "team-local",
				Type: "team-context",
				URL:  "http://localhost:8929/teams/local.git",
			},
		},
	}

	// credentials for test.sageox.ai
	testCreds := GitCredentials{
		Token:     "glpat-test-token",
		ServerURL: "https://git.test.sageox.ai",
		Username:  "testuser",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos: map[string]RepoEntry{
			"team-test": {
				Name: "team-test",
				Type: "team-context",
				URL:  "https://git.test.sageox.ai/teams/test.git",
			},
		},
	}

	// save credentials for both endpoints
	require.NoError(t, SaveCredentialsForEndpoint("http://localhost:3000", localCreds))
	require.NoError(t, SaveCredentialsForEndpoint("https://test.sageox.ai", testCreds))

	// load localhost credentials - should get local creds
	loadedLocal, err := LoadCredentialsForEndpoint("http://localhost:3000")
	require.NoError(t, err)
	require.NotNil(t, loadedLocal)
	assert.Equal(t, "glpat-local-token", loadedLocal.Token)
	assert.Equal(t, "http://localhost:8929", loadedLocal.ServerURL)
	assert.Contains(t, loadedLocal.Repos, "team-local")
	assert.NotContains(t, loadedLocal.Repos, "team-test")

	// load test.sageox.ai credentials - should get test creds
	loadedTest, err := LoadCredentialsForEndpoint("https://test.sageox.ai")
	require.NoError(t, err)
	require.NotNil(t, loadedTest)
	assert.Equal(t, "glpat-test-token", loadedTest.Token)
	assert.Equal(t, "https://git.test.sageox.ai", loadedTest.ServerURL)
	assert.Contains(t, loadedTest.Repos, "team-test")
	assert.NotContains(t, loadedTest.Repos, "team-local")
}

// TestMultiEndpointCredentials_NoCredentialsForEndpoint verifies behavior when
// credentials exist for one endpoint but not another.
func TestMultiEndpointCredentials_NoCredentialsForEndpoint(t *testing.T) {
	setupTestDir(t)

	// save credentials only for test.sageox.ai
	testCreds := createTestCredentials(24 * time.Hour)
	testCreds.ServerURL = "https://git.test.sageox.ai"
	require.NoError(t, SaveCredentialsForEndpoint("https://test.sageox.ai", testCreds))

	// try to load credentials for localhost - should return nil, not test creds
	localCreds, err := LoadCredentialsForEndpoint("http://localhost:3000")
	require.NoError(t, err)
	assert.Nil(t, localCreds, "should not return credentials for different endpoint")
}

// TestMultiEndpointCredentials_UpdateOneEndpoint verifies that updating credentials
// for one endpoint doesn't affect another endpoint's credentials.
func TestMultiEndpointCredentials_UpdateOneEndpoint(t *testing.T) {
	setupTestDir(t)

	// save initial credentials for both endpoints
	localCreds := GitCredentials{
		Token:     "glpat-local-v1",
		ServerURL: "http://localhost:8929",
		Username:  "localuser",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	testCreds := GitCredentials{
		Token:     "glpat-test-v1",
		ServerURL: "https://git.test.sageox.ai",
		Username:  "testuser",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}

	require.NoError(t, SaveCredentialsForEndpoint("http://localhost:3000", localCreds))
	require.NoError(t, SaveCredentialsForEndpoint("https://test.sageox.ai", testCreds))

	// update localhost credentials
	localCreds.Token = "glpat-local-v2"
	require.NoError(t, SaveCredentialsForEndpoint("http://localhost:3000", localCreds))

	// verify localhost was updated
	loadedLocal, err := LoadCredentialsForEndpoint("http://localhost:3000")
	require.NoError(t, err)
	require.NotNil(t, loadedLocal)
	assert.Equal(t, "glpat-local-v2", loadedLocal.Token)

	// verify test.sageox.ai was NOT affected
	loadedTest, err := LoadCredentialsForEndpoint("https://test.sageox.ai")
	require.NoError(t, err)
	require.NotNil(t, loadedTest)
	assert.Equal(t, "glpat-test-v1", loadedTest.Token)
}

// TestMultiEndpointCredentials_FileNaming verifies that credential files are
// created with correct names for each endpoint.
func TestMultiEndpointCredentials_FileNaming(t *testing.T) {
	tempDir := setupTestDir(t)

	// save credentials for different endpoints
	require.NoError(t, SaveCredentialsForEndpoint("http://localhost:3000", createTestCredentials(1*time.Hour)))
	require.NoError(t, SaveCredentialsForEndpoint("https://test.sageox.ai", createTestCredentials(1*time.Hour)))
	require.NoError(t, SaveCredentialsForEndpoint("https://sageox.ai", createTestCredentials(1*time.Hour)))

	// verify files exist with correct names
	sageoxDir := filepath.Join(tempDir, "sageox")
	files, err := os.ReadDir(sageoxDir)
	require.NoError(t, err)

	var fileNames []string
	for _, f := range files {
		fileNames = append(fileNames, f.Name())
	}

	assert.Contains(t, fileNames, "git-credentials-localhost.json")
	assert.Contains(t, fileNames, "git-credentials-test.sageox.ai.json")
	assert.Contains(t, fileNames, "git-credentials-sageox.ai.json")
}

// TestMultiEndpointCredentials_DaemonSwitchProject simulates the real-world scenario
// where a daemon switches between projects that use different endpoints.
func TestMultiEndpointCredentials_DaemonSwitchProject(t *testing.T) {
	setupTestDir(t)

	// setup: project A uses localhost, project B uses test.sageox.ai
	// both have different team contexts

	// save credentials as if daemon for project A fetched them
	projectACreds := GitCredentials{
		Token:     "glpat-projectA-token",
		ServerURL: "http://localhost:8929",
		Username:  "devuser",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos: map[string]RepoEntry{
			"team_abc123": {
				Name: "team_abc123",
				Type: "team-context",
				URL:  "http://localhost:8929/teams/abc123.git",
			},
		},
	}
	require.NoError(t, SaveCredentialsForEndpoint("http://localhost:3000", projectACreds))

	// save credentials as if daemon for project B fetched them
	projectBCreds := GitCredentials{
		Token:     "glpat-projectB-token",
		ServerURL: "https://git.test.sageox.ai",
		Username:  "produser",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos: map[string]RepoEntry{
			"team_xyz789": {
				Name: "team_xyz789",
				Type: "team-context",
				URL:  "https://git.test.sageox.ai/teams/xyz789.git",
			},
		},
	}
	require.NoError(t, SaveCredentialsForEndpoint("https://test.sageox.ai", projectBCreds))

	// simulate daemon for project A loading its credentials
	daemonACreds, err := LoadCredentialsForEndpoint("http://localhost:3000")
	require.NoError(t, err)
	require.NotNil(t, daemonACreds)

	// daemon A should see team_abc123 (its team) but NOT team_xyz789 (project B's team)
	assert.Contains(t, daemonACreds.Repos, "team_abc123")
	assert.NotContains(t, daemonACreds.Repos, "team_xyz789")

	// simulate daemon for project B loading its credentials
	daemonBCreds, err := LoadCredentialsForEndpoint("https://test.sageox.ai")
	require.NoError(t, err)
	require.NotNil(t, daemonBCreds)

	// daemon B should see team_xyz789 (its team) but NOT team_abc123 (project A's team)
	assert.Contains(t, daemonBCreds.Repos, "team_xyz789")
	assert.NotContains(t, daemonBCreds.Repos, "team_abc123")
}
