package gitserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Credential file-based storage tests ---

func TestSaveCredentialsForEndpoint_FileStorage(t *testing.T) {
	tmpDir := t.TempDir()
	prev := TestSetConfigDirOverride(tmpDir)
	defer TestSetConfigDirOverride(prev)
	prevForce := TestSetForceFileStorage(true)
	defer TestSetForceFileStorage(prevForce)

	creds := GitCredentials{
		Token:     "test-token-123",
		ServerURL: "https://git.example.com",
		Username:  "testuser",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos: map[string]RepoEntry{
			"team_abc": {Name: "Test Repo", Type: "team-context", URL: "https://git.example.com/repo.git", TeamID: "team_abc"},
		},
	}

	err := SaveCredentialsForEndpoint("https://example.com", creds)
	require.NoError(t, err)

	credsPath, err := getEndpointCredentialsPath("https://example.com")
	require.NoError(t, err)
	info, err := os.Stat(credsPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	loaded, err := LoadCredentialsForEndpoint("https://example.com")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, "test-token-123", loaded.Token)
	assert.Equal(t, 1, len(loaded.Repos))
}

func TestSaveCredentialsForEndpoint_OverwriteExisting(t *testing.T) {
	tmpDir := t.TempDir()
	prev := TestSetConfigDirOverride(tmpDir)
	defer TestSetConfigDirOverride(prev)
	prevForce := TestSetForceFileStorage(true)
	defer TestSetForceFileStorage(prevForce)

	ep := "https://overwrite-test.sageox.ai"
	require.NoError(t, SaveCredentialsForEndpoint(ep, GitCredentials{Token: "old-token"}))
	require.NoError(t, SaveCredentialsForEndpoint(ep, GitCredentials{Token: "new-token", ServerURL: "https://updated.com"}))

	loaded, err := LoadCredentialsForEndpoint(ep)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, "new-token", loaded.Token)
}

func TestLoadCredentialsForEndpoint_CorruptedJSON(t *testing.T) {
	tmpDir := t.TempDir()
	prev := TestSetConfigDirOverride(tmpDir)
	defer TestSetConfigDirOverride(prev)
	prevForce := TestSetForceFileStorage(true)
	defer TestSetForceFileStorage(prevForce)

	credsPath, err := getEndpointCredentialsPath("https://corrupt.sageox.ai")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(credsPath), 0700))
	require.NoError(t, os.WriteFile(credsPath, []byte("not-json{{{"), 0600))

	_, err = LoadCredentialsForEndpoint("https://corrupt.sageox.ai")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

func TestLoadCredentialsForEndpoint_MissingFile(t *testing.T) {
	tmpDir := t.TempDir()
	prev := TestSetConfigDirOverride(tmpDir)
	defer TestSetConfigDirOverride(prev)
	prevForce := TestSetForceFileStorage(true)
	defer TestSetForceFileStorage(prevForce)

	loaded, err := LoadCredentialsForEndpoint("https://nonexistent.sageox.ai")
	assert.NoError(t, err)
	assert.Nil(t, loaded)
}

// --- isKeyringAvailable CI detection ---

func TestIsKeyringAvailable_CIEnvVars(t *testing.T) {
	for _, envVar := range []string{"CI", "CONTINUOUS_INTEGRATION", "GITHUB_ACTIONS", "GITLAB_CI", "JENKINS_URL", "BUILDKITE"} {
		t.Run(envVar, func(t *testing.T) {
			t.Setenv(envVar, "true")
			assert.False(t, isKeyringAvailable())
		})
	}
}

func TestIsKeyringAvailable_CredentialsFileEnv(t *testing.T) {
	t.Setenv("OX_GIT_CREDENTIALS_FILE", "/tmp/test-creds.json")
	assert.False(t, isKeyringAvailable())
}

func TestIsKeyringAvailable_ForceFileStorage(t *testing.T) {
	prevForce := TestSetForceFileStorage(true)
	defer TestSetForceFileStorage(prevForce)
	assert.False(t, isKeyringAvailable())
}

// --- RemoveCredentials ---

func TestRemoveCredentialsForEndpoint_FileCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	prev := TestSetConfigDirOverride(tmpDir)
	defer TestSetConfigDirOverride(prev)
	prevForce := TestSetForceFileStorage(true)
	defer TestSetForceFileStorage(prevForce)

	ep := "https://remove-test.sageox.ai"
	require.NoError(t, SaveCredentialsForEndpoint(ep, GitCredentials{Token: "removeme"}))
	require.NoError(t, RemoveCredentialsForEndpoint(ep))

	loaded, err := LoadCredentialsForEndpoint(ep)
	require.NoError(t, err)
	assert.Nil(t, loaded)
}

func TestRemoveCredentials_FileCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	prev := TestSetConfigDirOverride(tmpDir)
	defer TestSetConfigDirOverride(prev)
	prevForce := TestSetForceFileStorage(true)
	defer TestSetForceFileStorage(prevForce)

	credsPath, err := getCredentialsFilePath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(credsPath), 0700))
	data, _ := json.Marshal(GitCredentials{Token: "old"})
	require.NoError(t, os.WriteFile(credsPath, data, 0600))

	require.NoError(t, RemoveCredentials())
	_, err = os.Stat(credsPath)
	assert.True(t, os.IsNotExist(err))
}

func TestRemoveCredentialsFromFile_NoFile(t *testing.T) {
	tmpDir := t.TempDir()
	prev := TestSetConfigDirOverride(tmpDir)
	defer TestSetConfigDirOverride(prev)
	assert.NoError(t, removeCredentialsFromFile())
}

func TestRemoveCredentialsFromFile_WithFile(t *testing.T) {
	tmpDir := t.TempDir()
	prev := TestSetConfigDirOverride(tmpDir)
	defer TestSetConfigDirOverride(prev)

	credsPath, err := getCredentialsFilePath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(credsPath), 0700))
	require.NoError(t, os.WriteFile(credsPath, []byte(`{"token":"t"}`), 0600))

	assert.NoError(t, removeCredentialsFromFile())
	_, err = os.Stat(credsPath)
	assert.True(t, os.IsNotExist(err))
}

// --- validateCheckoutPath ---

func TestValidateCheckoutPath_FileExists(t *testing.T) {
	t.Parallel()
	filePath := filepath.Join(t.TempDir(), "somefile")
	require.NoError(t, os.WriteFile(filePath, []byte("data"), 0644))

	err := validateCheckoutPath(filePath)
	assert.ErrorIs(t, err, ErrPathExists)
	assert.Contains(t, err.Error(), "file")
}

func TestValidateCheckoutPath_NonEmptyDir(t *testing.T) {
	t.Parallel()
	dirPath := filepath.Join(t.TempDir(), "nonempty")
	require.NoError(t, os.MkdirAll(dirPath, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dirPath, "f.txt"), []byte("x"), 0644))

	err := validateCheckoutPath(dirPath)
	assert.ErrorIs(t, err, ErrPathExists)
}

func TestValidateCheckoutPath_EmptyDir(t *testing.T) {
	t.Parallel()
	emptyDir := filepath.Join(t.TempDir(), "emptydir")
	require.NoError(t, os.MkdirAll(emptyDir, 0755))
	assert.NoError(t, validateCheckoutPath(emptyDir))
}

func TestValidateCheckoutPath_NonExistent(t *testing.T) {
	t.Parallel()
	assert.NoError(t, validateCheckoutPath(filepath.Join(t.TempDir(), "nope")))
}

// --- CacheFilesTracked ---

func TestCacheFilesTracked_NoTrackedFiles(t *testing.T) {
	t.Parallel()
	repoPath := makeCovTestRepo(t)
	cacheDir := filepath.Join(repoPath, ".sageox", "cache")
	require.NoError(t, os.MkdirAll(cacheDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "test.db"), []byte("data"), 0644))
	assert.False(t, CacheFilesTracked(repoPath))
}

func TestCacheFilesTracked_WithTrackedFiles(t *testing.T) {
	t.Parallel()
	repoPath := makeCovTestRepo(t)
	cacheDir := filepath.Join(repoPath, ".sageox", "cache")
	require.NoError(t, os.MkdirAll(cacheDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "test.db"), []byte("data"), 0644))
	runGit(t, repoPath, "add", ".sageox/cache/test.db")
	runGit(t, repoPath, "commit", "-m", "track cache file")
	assert.True(t, CacheFilesTracked(repoPath))
}

func TestCacheFilesTracked_NoSageoxDir(t *testing.T) {
	t.Parallel()
	assert.False(t, CacheFilesTracked(makeCovTestRepo(t)))
}

// --- GetBareRemoteURL ---

func TestGetBareRemoteURL_StripsCredentials(t *testing.T) {
	t.Parallel()
	repoPath := makeCovTestRepo(t)
	runGit(t, repoPath, "remote", "add", "origin", "https://oauth2:secret@git.example.com/org/repo.git")

	bareURL, err := GetBareRemoteURL(repoPath)
	require.NoError(t, err)
	assert.Equal(t, "https://git.example.com/org/repo.git", bareURL)
}

func TestGetBareRemoteURL_BareHTTPS(t *testing.T) {
	t.Parallel()
	repoPath := makeCovTestRepo(t)
	runGit(t, repoPath, "remote", "add", "origin", "https://git.example.com/org/repo.git")

	bareURL, err := GetBareRemoteURL(repoPath)
	require.NoError(t, err)
	assert.Equal(t, "https://git.example.com/org/repo.git", bareURL)
}

func TestGetBareRemoteURL_NoRemote(t *testing.T) {
	t.Parallel()
	_, err := GetBareRemoteURL(makeCovTestRepo(t))
	assert.Error(t, err)
}

// --- writeAskpassScript ---

func TestWriteAskpassScript_CreatesExecutableFile(t *testing.T) {
	t.Parallel()
	path, err := writeAskpassScript("my-secret-token")
	require.NoError(t, err)
	defer os.Remove(path)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), info.Mode().Perm())
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(content), "#!/bin/sh")
}

func TestWriteAskpassScript_EscapesSingleQuotes(t *testing.T) {
	t.Parallel()
	path, err := writeAskpassScript("token'with'quotes")
	require.NoError(t, err)
	defer os.Remove(path)

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(content), `'\''`)
}

func TestWriteAskpassScript_OutputsToken(t *testing.T) {
	t.Parallel()
	token := "test-pat-abc123"
	path, err := writeAskpassScript(token)
	require.NoError(t, err)
	defer os.Remove(path)

	out, err := exec.Command("sh", path).Output()
	require.NoError(t, err)
	assert.Equal(t, token+"\n", string(out))
}

// --- getEndpointCredentialsPath ---

func TestGetEndpointCredentialsPath_EmptyEndpoint(t *testing.T) {
	tmpDir := t.TempDir()
	prev := TestSetConfigDirOverride(tmpDir)
	defer TestSetConfigDirOverride(prev)

	path, err := getEndpointCredentialsPath("")
	require.NoError(t, err)
	assert.Contains(t, path, "git-credentials.json")
	assert.NotContains(t, path, "git-credentials-.json")
}

func TestGetEndpointCredentialsPath_WithEndpoint(t *testing.T) {
	tmpDir := t.TempDir()
	prev := TestSetConfigDirOverride(tmpDir)
	defer TestSetConfigDirOverride(prev)

	path, err := getEndpointCredentialsPath("https://test.sageox.ai")
	require.NoError(t, err)
	assert.Contains(t, path, "git-credentials-test.sageox.ai.json")
}

func TestGetEndpointCredentialsPath_DefaultPath(t *testing.T) {
	prevDir := TestSetConfigDirOverride("")
	defer TestSetConfigDirOverride(prevDir)

	path, err := getEndpointCredentialsPath("https://test.sageox.ai")
	require.NoError(t, err)
	assert.Contains(t, path, "git-credentials-test.sageox.ai.json")
}

// --- getCredentialsFilePath ---

func TestGetCredentialsFilePath_ConfigDirOverride(t *testing.T) {
	tmpDir := t.TempDir()
	prev := TestSetConfigDirOverride(tmpDir)
	defer TestSetConfigDirOverride(prev)

	path, err := getCredentialsFilePath()
	require.NoError(t, err)
	assert.Contains(t, path, filepath.Join(tmpDir, "sageox", "git-credentials.json"))
}

func TestGetCredentialsFilePath_DefaultPath(t *testing.T) {
	prevDir := TestSetConfigDirOverride("")
	defer TestSetConfigDirOverride(prevDir)

	path, err := getCredentialsFilePath()
	require.NoError(t, err)
	assert.NotEmpty(t, path)
	assert.Contains(t, path, "git-credentials")
}

// --- CloneFromURLWithEndpoint ---

func TestCloneFromURLWithEndpoint_EmptyURL(t *testing.T) {
	t.Parallel()
	assert.ErrorIs(t, CloneFromURLWithEndpoint(context.TODO(), "", "/tmp/test", "https://sageox.ai", nil), ErrEmptyURL)
}

func TestCloneFromURLWithEndpoint_NoCreds(t *testing.T) {
	tmpDir := t.TempDir()
	prev := TestSetConfigDirOverride(tmpDir)
	defer TestSetConfigDirOverride(prev)
	prevForce := TestSetForceFileStorage(true)
	defer TestSetForceFileStorage(prevForce)

	err := CloneFromURLWithEndpoint(context.TODO(), "https://git.example.com/repo.git", "/tmp/test", "https://no-creds.sageox.ai", nil)
	assert.ErrorIs(t, err, ErrNoCredentials)
}

// --- pickProbeRepoURL ---

func TestPickProbeRepoURL_EmptyRepos(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", pickProbeRepoURL(&GitCredentials{Repos: map[string]RepoEntry{}}))
}

func TestPickProbeRepoURL_RepoWithEmptyURL(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", pickProbeRepoURL(&GitCredentials{
		Repos: map[string]RepoEntry{"team_1": {URL: ""}},
	}))
}

func TestPickProbeRepoURL_WithValidRepo(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "https://git.example.com/a.git", pickProbeRepoURL(&GitCredentials{
		Repos: map[string]RepoEntry{"team_1": {URL: "https://git.example.com/a.git"}},
	}))
}

// --- buildProbeURL ---

func TestBuildProbeURL_NoScheme(t *testing.T) {
	t.Parallel()
	result, err := buildProbeURL("git.example.com/repo.git")
	require.NoError(t, err)
	assert.Contains(t, result, "https://")
	assert.Contains(t, result, "oauth2@")
}

func TestBuildProbeURL_PreservesPath(t *testing.T) {
	t.Parallel()
	result, err := buildProbeURL("https://git.example.com/org/repo.git")
	require.NoError(t, err)
	assert.Equal(t, "https://oauth2@git.example.com/org/repo.git", result)
}

// --- CredentialStatus ---

func TestCredentialStatus_FormatExpiry_Boundaries(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{"negative_expired", -1 * time.Hour, "expired"},
		{"seconds", 30 * time.Second, "30s"},
		{"minutes", 45 * time.Minute, "45m"},
		{"hours", 5 * time.Hour, "5h"},
		{"days", 48 * time.Hour, "2d"},
		{"sub_second", 500 * time.Millisecond, "0s"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, CredentialStatus{TimeUntilExpiry: tt.duration}.FormatExpiry())
		})
	}
}

// --- CheckCredentialStatusForEndpoint ---

func TestCheckCredentialStatusForEndpoint_ExpiredCreds(t *testing.T) {
	tmpDir := t.TempDir()
	prev := TestSetConfigDirOverride(tmpDir)
	defer TestSetConfigDirOverride(prev)
	prevForce := TestSetForceFileStorage(true)
	defer TestSetForceFileStorage(prevForce)

	ep := "https://expired-extra.sageox.ai"
	require.NoError(t, SaveCredentialsForEndpoint(ep, GitCredentials{Token: "t", ExpiresAt: time.Now().Add(-1 * time.Hour)}))
	status := CheckCredentialStatusForEndpoint(ep)
	assert.False(t, status.Valid)
	assert.Equal(t, "expired", status.Reason)
}

func TestCheckCredentialStatusForEndpoint_ExpiringSoon(t *testing.T) {
	tmpDir := t.TempDir()
	prev := TestSetConfigDirOverride(tmpDir)
	defer TestSetConfigDirOverride(prev)
	prevForce := TestSetForceFileStorage(true)
	defer TestSetForceFileStorage(prevForce)

	ep := "https://expiring-extra.sageox.ai"
	require.NoError(t, SaveCredentialsForEndpoint(ep, GitCredentials{Token: "t", ExpiresAt: time.Now().Add(30 * time.Minute)}))
	status := CheckCredentialStatusForEndpoint(ep)
	assert.False(t, status.Valid)
	assert.Equal(t, "expiring soon", status.Reason)
}

func TestCheckCredentialStatusForEndpoint_ValidWithRepoCount(t *testing.T) {
	tmpDir := t.TempDir()
	prev := TestSetConfigDirOverride(tmpDir)
	defer TestSetConfigDirOverride(prev)
	prevForce := TestSetForceFileStorage(true)
	defer TestSetForceFileStorage(prevForce)

	ep := "https://valid-extra.sageox.ai"
	require.NoError(t, SaveCredentialsForEndpoint(ep, GitCredentials{
		Token: "t", ExpiresAt: time.Now().Add(24 * time.Hour),
		Repos: map[string]RepoEntry{"team_1": {TeamID: "team_1"}, "team_2": {TeamID: "team_2"}},
	}))
	status := CheckCredentialStatusForEndpoint(ep)
	assert.True(t, status.Valid)
	assert.Equal(t, 2, status.RepoCount)
}

// --- EnsureValidCredentialsForEndpoint ---

func TestEnsureValidCredentialsForEndpoint_FetchError(t *testing.T) {
	tmpDir := t.TempDir()
	prev := TestSetConfigDirOverride(tmpDir)
	defer TestSetConfigDirOverride(prev)
	prevForce := TestSetForceFileStorage(true)
	defer TestSetForceFileStorage(prevForce)

	_, err := EnsureValidCredentialsForEndpoint("https://fetch-err.sageox.ai", func() (*GitCredentials, error) {
		return nil, fmt.Errorf("API unavailable")
	})
	assert.Error(t, err)
}

func TestEnsureValidCredentialsForEndpoint_SuccessfulRefresh(t *testing.T) {
	tmpDir := t.TempDir()
	prev := TestSetConfigDirOverride(tmpDir)
	defer TestSetConfigDirOverride(prev)
	prevForce := TestSetForceFileStorage(true)
	defer TestSetForceFileStorage(prevForce)

	status, err := EnsureValidCredentialsForEndpoint("https://refresh-ok.sageox.ai", func() (*GitCredentials, error) {
		return &GitCredentials{Token: "fresh", ExpiresAt: time.Now().Add(24 * time.Hour)}, nil
	})
	assert.NoError(t, err)
	assert.True(t, status.Valid)
}

func TestEnsureValidCredentialsForEndpoint_AlreadyValid(t *testing.T) {
	tmpDir := t.TempDir()
	prev := TestSetConfigDirOverride(tmpDir)
	defer TestSetConfigDirOverride(prev)
	prevForce := TestSetForceFileStorage(true)
	defer TestSetForceFileStorage(prevForce)

	ep := "https://already-valid.sageox.ai"
	require.NoError(t, SaveCredentialsForEndpoint(ep, GitCredentials{Token: "t", ExpiresAt: time.Now().Add(24 * time.Hour)}))

	status, err := EnsureValidCredentialsForEndpoint(ep, func() (*GitCredentials, error) {
		t.Fatal("fetcher should not be called when creds are valid")
		return nil, nil
	})
	assert.NoError(t, err)
	assert.True(t, status.Valid)
}

func TestRefreshCredentialsForEndpoint_FetcherReturnsNilCreds(t *testing.T) {
	tmpDir := t.TempDir()
	prev := TestSetConfigDirOverride(tmpDir)
	defer TestSetConfigDirOverride(prev)
	prevForce := TestSetForceFileStorage(true)
	defer TestSetForceFileStorage(prevForce)

	result := RefreshCredentialsForEndpoint("https://nil-creds.sageox.ai", func() (*GitCredentials, error) {
		return nil, nil
	}, true)
	assert.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "nil credentials")
}

func TestRefreshCredentialsForEndpoint_SaveSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	prev := TestSetConfigDirOverride(tmpDir)
	defer TestSetConfigDirOverride(prev)
	prevForce := TestSetForceFileStorage(true)
	defer TestSetForceFileStorage(prevForce)

	result := RefreshCredentialsForEndpoint("https://save-ok.sageox.ai", func() (*GitCredentials, error) {
		return &GitCredentials{Token: "new", ExpiresAt: time.Now().Add(24 * time.Hour)}, nil
	}, true)
	assert.True(t, result.Refreshed)
	assert.NoError(t, result.Error)
}

// --- GetStorageBackend ---

func TestGetStorageBackend_FileMode(t *testing.T) {
	prevForce := TestSetForceFileStorage(true)
	defer TestSetForceFileStorage(prevForce)
	assert.Equal(t, "file", GetStorageBackend())
}

// --- commitAndPushAgentsMD ---

func TestCommitAndPushAgentsMD_NoRemote(t *testing.T) {
	t.Parallel()
	repoPath := makeCovTestRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repoPath, "AGENTS.md"), []byte("# Test\n"), 0644))

	err := commitAndPushAgentsMD(context.Background(), repoPath)
	require.NoError(t, err)

	out, err := exec.Command("git", "-C", repoPath, "log", "--oneline", "-1").Output()
	require.NoError(t, err)
	assert.Contains(t, string(out), "AGENTS.md")
}

func TestCommitAndPushAgentsMD_WithRemote_PushFails(t *testing.T) {
	t.Parallel()
	repoPath := makeCovTestRepo(t)
	runGit(t, repoPath, "remote", "add", "origin", "https://git.example.com/fake/repo.git")
	require.NoError(t, os.WriteFile(filepath.Join(repoPath, "AGENTS.md"), []byte("# Test\n"), 0644))

	err := commitAndPushAgentsMD(context.Background(), repoPath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "push")
}

func TestCommitAgentsMD_AlreadyCommitted(t *testing.T) {
	t.Parallel()
	repoPath := makeCovTestRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repoPath, "AGENTS.md"), []byte("# Test\n"), 0644))
	runGit(t, repoPath, "add", "AGENTS.md")
	runGit(t, repoPath, "commit", "-m", "add AGENTS.md")

	assert.NoError(t, commitAgentsMD(context.Background(), repoPath))
}

func TestCreateAgentsMD_NewRepo(t *testing.T) {
	t.Parallel()
	repoPath := makeCovTestRepo(t)
	require.NoError(t, CreateAgentsMD(context.Background(), repoPath, &AgentsMDOptions{
		RepoType: "team-context", TeamID: "team_test123",
	}))

	content, err := os.ReadFile(filepath.Join(repoPath, "AGENTS.md"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "Team Context")
}

func TestCreateAgentsMD_SkipsExistingFile(t *testing.T) {
	t.Parallel()
	repoPath := makeCovTestRepo(t)
	agentsPath := filepath.Join(repoPath, "AGENTS.md")
	require.NoError(t, os.WriteFile(agentsPath, []byte("existing content"), 0644))

	require.NoError(t, CreateAgentsMD(context.Background(), repoPath, nil))

	content, err := os.ReadFile(agentsPath)
	require.NoError(t, err)
	assert.Equal(t, "existing content", string(content))
}

func TestGenerateAgentsMDContent_Defaults(t *testing.T) {
	t.Parallel()
	content := generateAgentsMDContent(nil)
	assert.Contains(t, content, "Ledger")
	assert.Contains(t, content, "(not linked)")
}

func TestGenerateAgentsMDContent_TeamContext(t *testing.T) {
	t.Parallel()
	content := generateAgentsMDContent(&AgentsMDOptions{RepoType: "team-context", TeamID: "team_xyz"})
	assert.Contains(t, content, "Team Context")
	assert.Contains(t, content, "team_xyz")
}

// --- ValidatePATLiveness ---

func TestValidatePATLiveness_NilCreds_Extra(t *testing.T) {
	t.Parallel()
	result := ValidatePATLiveness(context.Background(), nil)
	assert.True(t, result.Skipped)
}

func TestValidatePATLiveness_NoRepos_Extra(t *testing.T) {
	t.Parallel()
	result := ValidatePATLiveness(context.Background(), &GitCredentials{Token: "t", Repos: map[string]RepoEntry{}})
	assert.True(t, result.Skipped)
}

func TestValidatePATLiveness_WithFakeRepo(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result := ValidatePATLiveness(ctx, &GitCredentials{
		Token: "fake-token",
		Repos: map[string]RepoEntry{"team_1": {URL: "https://localhost:1/nonexistent.git"}},
	})
	assert.True(t, result.Skipped || !result.Valid)
}

// --- extractPATFromRemote ---

func TestExtractPATFromRemote_OauthURL(t *testing.T) {
	t.Parallel()
	repoPath := makeCovTestRepo(t)
	runGit(t, repoPath, "remote", "add", "origin", "https://oauth2:my-secret@git.example.com/repo.git")

	pat, _, err := extractPATFromRemote(repoPath)
	require.NoError(t, err)
	assert.Equal(t, "my-secret", pat)
}

func TestExtractPATFromRemote_BareURL(t *testing.T) {
	t.Parallel()
	repoPath := makeCovTestRepo(t)
	runGit(t, repoPath, "remote", "add", "origin", "https://git.example.com/repo.git")

	pat, url, err := extractPATFromRemote(repoPath)
	require.NoError(t, err)
	assert.Equal(t, "", pat)
	assert.Equal(t, "https://git.example.com/repo.git", url)
}

func TestExtractPATFromRemote_NonOauth2User(t *testing.T) {
	t.Parallel()
	repoPath := makeCovTestRepo(t)
	runGit(t, repoPath, "remote", "add", "origin", "https://deploy-token:secret@git.example.com/repo.git")

	pat, _, err := extractPATFromRemote(repoPath)
	require.NoError(t, err)
	assert.Equal(t, "", pat)
}

func TestExtractPATFromRemote_NoRemote(t *testing.T) {
	t.Parallel()
	_, _, err := extractPATFromRemote(makeCovTestRepo(t))
	assert.Error(t, err)
}

func TestExtractPATFromRemote_SSHRemote(t *testing.T) {
	t.Parallel()
	repoPath := makeCovTestRepo(t)
	runGit(t, repoPath, "remote", "add", "origin", "git@github.com:org/repo.git")

	pat, url, err := extractPATFromRemote(repoPath)
	require.NoError(t, err)
	assert.Equal(t, "", pat)
	assert.Equal(t, "git@github.com:org/repo.git", url)
}

func TestExtractPATFromRemote_UserWithoutPassword(t *testing.T) {
	t.Parallel()
	repoPath := makeCovTestRepo(t)
	runGit(t, repoPath, "remote", "add", "origin", "https://oauth2@git.example.com/repo.git")

	pat, _, err := extractPATFromRemote(repoPath)
	require.NoError(t, err)
	assert.Equal(t, "", pat)
}

// --- StripRemoteCredentials ---

func TestStripRemoteCredentials_RemovesOauth2(t *testing.T) {
	t.Parallel()
	repoPath := makeCovTestRepo(t)
	runGit(t, repoPath, "remote", "add", "origin", "https://oauth2:token123@git.example.com/repo.git")

	require.NoError(t, StripRemoteCredentials(repoPath))
	out, err := exec.Command("git", "-C", repoPath, "remote", "get-url", "origin").Output()
	require.NoError(t, err)
	assert.Equal(t, "https://git.example.com/repo.git", strings.TrimSpace(string(out)))
}

func TestStripRemoteCredentials_NoOpForBareURL(t *testing.T) {
	t.Parallel()
	repoPath := makeCovTestRepo(t)
	runGit(t, repoPath, "remote", "add", "origin", "https://git.example.com/repo.git")
	require.NoError(t, StripRemoteCredentials(repoPath))
}

func TestStripRemoteCredentials_NoOpForSSH(t *testing.T) {
	t.Parallel()
	repoPath := makeCovTestRepo(t)
	runGit(t, repoPath, "remote", "add", "origin", "git@github.com:org/repo.git")
	require.NoError(t, StripRemoteCredentials(repoPath))
}

func TestStripRemoteCredentials_NoOpForDeployToken(t *testing.T) {
	t.Parallel()
	repoPath := makeCovTestRepo(t)
	runGit(t, repoPath, "remote", "add", "origin", "https://deploy:token@git.example.com/repo.git")
	require.NoError(t, StripRemoteCredentials(repoPath))

	out, err := exec.Command("git", "-C", repoPath, "remote", "get-url", "origin").Output()
	require.NoError(t, err)
	assert.Equal(t, "https://deploy:token@git.example.com/repo.git", strings.TrimSpace(string(out)))
}

// --- RefreshRemoteCredentials (file-based creds, unique names) ---

func TestRefreshRemoteCredentials_BareURLNoFileCreds(t *testing.T) {
	tmpDir := t.TempDir()
	prev := TestSetConfigDirOverride(tmpDir)
	defer TestSetConfigDirOverride(prev)
	prevForce := TestSetForceFileStorage(true)
	defer TestSetForceFileStorage(prevForce)

	repoPath := makeCovTestRepo(t)
	runGit(t, repoPath, "remote", "add", "origin", "https://git.example.com/repo.git")
	assert.NoError(t, RefreshRemoteCredentials(repoPath, "https://no-creds-file.sageox.ai"))
}

func TestRefreshRemoteCredentials_UpdatesStaleFileCreds(t *testing.T) {
	tmpDir := t.TempDir()
	prev := TestSetConfigDirOverride(tmpDir)
	defer TestSetConfigDirOverride(prev)
	prevForce := TestSetForceFileStorage(true)
	defer TestSetForceFileStorage(prevForce)

	ep := "https://stale-file.sageox.ai"
	require.NoError(t, SaveCredentialsForEndpoint(ep, GitCredentials{
		Token: "new-file-token", ServerURL: "https://git.example.com",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}))

	repoPath := makeCovTestRepo(t)
	runGit(t, repoPath, "remote", "add", "origin", "https://oauth2:stale-token@git.example.com/repo.git")

	require.NoError(t, RefreshRemoteCredentials(repoPath, ep))
	out, err := exec.Command("git", "-C", repoPath, "remote", "get-url", "origin").Output()
	require.NoError(t, err)
	assert.Contains(t, string(out), "new-file-token")
}

func TestRefreshRemoteCredentials_EmptyServerURLSkipsHostCheck(t *testing.T) {
	tmpDir := t.TempDir()
	prev := TestSetConfigDirOverride(tmpDir)
	defer TestSetConfigDirOverride(prev)
	prevForce := TestSetForceFileStorage(true)
	defer TestSetForceFileStorage(prevForce)

	ep := "https://noserver-file.sageox.ai"
	require.NoError(t, SaveCredentialsForEndpoint(ep, GitCredentials{
		Token: "updated-token", ServerURL: "", ExpiresAt: time.Now().Add(24 * time.Hour),
	}))

	repoPath := makeCovTestRepo(t)
	runGit(t, repoPath, "remote", "add", "origin", "https://oauth2:old-token@git.example.com/repo.git")

	require.NoError(t, RefreshRemoteCredentials(repoPath, ep))
	out, err := exec.Command("git", "-C", repoPath, "remote", "get-url", "origin").Output()
	require.NoError(t, err)
	assert.Contains(t, string(out), "updated-token")
}

// --- EnsureCheckoutGitignore ---

func TestEnsureCheckoutGitignore_CreatesFreshGitignore(t *testing.T) {
	t.Parallel()
	repoPath := makeCovTestRepo(t)
	sageoxDir := filepath.Join(repoPath, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	require.NoError(t, EnsureCheckoutGitignore(repoPath))

	content, err := os.ReadFile(filepath.Join(sageoxDir, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "*")
	assert.Contains(t, string(content), "!.gitignore")
	assert.Contains(t, string(content), "!sync.manifest")
}

func TestEnsureCheckoutGitignore_AppendsToExisting(t *testing.T) {
	t.Parallel()
	repoPath := makeCovTestRepo(t)
	sageoxDir := filepath.Join(repoPath, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, ".gitignore"), []byte("# custom\n*\n"), 0644))

	require.NoError(t, EnsureCheckoutGitignore(repoPath))

	content, err := os.ReadFile(filepath.Join(sageoxDir, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "# custom")
	assert.Contains(t, string(content), "!.gitignore")
	assert.Contains(t, string(content), "!sync.manifest")
}

func TestCommitCheckoutGitignore_CommitsFile(t *testing.T) {
	t.Parallel()
	repoPath := makeCovTestRepo(t)
	sageoxDir := filepath.Join(repoPath, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, ".gitignore"), []byte("*\n!.gitignore\n"), 0644))

	require.NoError(t, EnsureCheckoutGitignore(repoPath))

	out, err := exec.Command("git", "-C", repoPath, "ls-files", ".sageox/.gitignore").Output()
	require.NoError(t, err)
	assert.Contains(t, string(out), ".sageox/.gitignore")
}

// --- EnsureGitignoreBeforeCommit ---

func TestEnsureGitignoreBeforeCommit_EmptyPath(t *testing.T) {
	t.Parallel()
	EnsureGitignoreBeforeCommit("") // should not panic
}

func TestEnsureGitignoreBeforeCommit_WithCacheTracked(t *testing.T) {
	t.Parallel()
	repoPath := makeCovTestRepo(t)
	sageoxDir := filepath.Join(repoPath, ".sageox")
	cacheDir := filepath.Join(sageoxDir, "cache")
	require.NoError(t, os.MkdirAll(cacheDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "state.json"), []byte("{}"), 0644))
	runGit(t, repoPath, "add", ".sageox/cache/state.json")
	runGit(t, repoPath, "commit", "-m", "add cache file")

	EnsureGitignoreBeforeCommit(repoPath)

	// cache file should still exist on disk
	_, err := os.Stat(filepath.Join(cacheDir, "state.json"))
	assert.NoError(t, err)
}

func TestEnsureGitignoreBeforeCommit_RemovesLegacyCodedb(t *testing.T) {
	t.Parallel()
	repoPath := makeCovTestRepo(t)
	require.NoError(t, os.MkdirAll(filepath.Join(repoPath, ".sageox"), 0755))
	codedbDir := filepath.Join(repoPath, "codedb")
	require.NoError(t, os.MkdirAll(codedbDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(codedbDir, "index.db"), []byte("data"), 0644))

	EnsureGitignoreBeforeCommit(repoPath)
	_, err := os.Stat(codedbDir)
	assert.True(t, os.IsNotExist(err))
}

// --- URL parsing error paths ---

func TestExtractHost_InvalidURL(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", extractHost("://"))
}

func TestSanitizeURL_Unparseable(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "://bad", sanitizeURL("://bad"))
}

func TestSanitizeRemoteURL_Unparseable(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "://bad", SanitizeRemoteURL("://bad"))
}

func TestRepoNameFromURL_ParseError(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", repoNameFromURL("://bad-url"))
}

func TestBuildAuthURL_ParseError(t *testing.T) {
	t.Parallel()
	_, err := BuildAuthURL("://bad-url", &GitCredentials{Token: "tok"})
	assert.Error(t, err)
}

// --- RepoEntry.StableID ---

func TestRepoEntry_StableID(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "team_abc123", RepoEntry{TeamID: "team_abc123"}.StableID())
}

func TestRepoEntry_StableID_Empty(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", RepoEntry{Name: "No Team"}.StableID())
}

// --- helpers ---

func makeCovTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitkeep"), []byte(""), 0644))
	runGit(t, dir, "add", ".gitkeep")
	runGit(t, dir, "commit", "-m", "initial commit")
	return dir
}
