package gitserver

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupGitRepoWithRemote creates a temp git repo with a remote URL set.
func setupGitRepoWithRemote(t *testing.T, remoteURL string) string {
	t.Helper()
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init", "--initial-branch=main", dir},
		{"git", "-C", dir, "remote", "add", "origin", remoteURL},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "cmd %v failed: %s", args, output)
	}
	return dir
}

// setupTestCredentials writes test credentials to a temp dir and sets the override.
func setupTestCredentials(t *testing.T, ep string, token string, serverURL string) {
	t.Helper()
	credDir := t.TempDir()

	prev := TestSetConfigDirOverride(credDir)
	prevForce := TestSetForceFileStorage(true)
	t.Cleanup(func() {
		TestSetConfigDirOverride(prev)
		TestSetForceFileStorage(prevForce)
	})

	creds := GitCredentials{
		Token:     token,
		ServerURL: serverURL,
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	data, err := json.Marshal(creds)
	require.NoError(t, err)

	// write to the endpoint-specific file (must match getEndpointCredentialsPath layout)
	slug := endpointSlug(ep)
	sageoxDir := filepath.Join(credDir, "sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0o755))
	credFile := filepath.Join(sageoxDir, "git-credentials-"+slug+".json")
	require.NoError(t, os.WriteFile(credFile, data, 0o600))
}

func TestRefreshRemoteCredentials_StalePAT(t *testing.T) {
	ep := "https://sageox.ai"
	oldToken := "old-pat-token-12345"
	newToken := "new-pat-token-67890"

	dir := setupGitRepoWithRemote(t, "https://oauth2:"+oldToken+"@git.sageox.ai/team/ledger.git")
	setupTestCredentials(t, ep, newToken, "https://git.sageox.ai")

	err := RefreshRemoteCredentials(dir, ep)
	require.NoError(t, err)

	// verify remote was updated
	cmd := exec.Command("git", "-C", dir, "remote", "get-url", "origin")
	output, err := cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(output), newToken)
	assert.NotContains(t, string(output), oldToken)
}

func TestRefreshRemoteCredentials_CurrentPAT(t *testing.T) {
	ep := "https://sageox.ai"
	token := "current-pat-token"

	dir := setupGitRepoWithRemote(t, "https://oauth2:"+token+"@git.sageox.ai/team/ledger.git")
	setupTestCredentials(t, ep, token, "https://git.sageox.ai")

	err := RefreshRemoteCredentials(dir, ep)
	require.NoError(t, err)

	// verify remote unchanged
	cmd := exec.Command("git", "-C", dir, "remote", "get-url", "origin")
	output, err := cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(output), token)
}

func TestRefreshRemoteCredentials_SSHRemote(t *testing.T) {
	ep := "https://sageox.ai"
	dir := setupGitRepoWithRemote(t, "git@git.sageox.ai:team/ledger.git")
	setupTestCredentials(t, ep, "some-token", "https://git.sageox.ai")

	err := RefreshRemoteCredentials(dir, ep)
	require.NoError(t, err)

	// verify remote unchanged (SSH, no PAT to refresh)
	cmd := exec.Command("git", "-C", dir, "remote", "get-url", "origin")
	output, err := cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(output), "git@git.sageox.ai")
}

func TestRefreshRemoteCredentials_BareHTTPS(t *testing.T) {
	ep := "https://sageox.ai"
	dir := setupGitRepoWithRemote(t, "https://git.sageox.ai/team/ledger.git")
	setupTestCredentials(t, ep, "some-token", "https://git.sageox.ai")

	err := RefreshRemoteCredentials(dir, ep)
	require.NoError(t, err)

	// bare HTTPS URL should get credentials inserted when host matches
	cmd := exec.Command("git", "-C", dir, "remote", "get-url", "origin")
	output, err := cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(output), "oauth2:some-token@git.sageox.ai")
}

func TestRefreshRemoteCredentials_NoCredentials(t *testing.T) {
	dir := setupGitRepoWithRemote(t, "https://oauth2:old-token@git.sageox.ai/team/ledger.git")

	// set up empty credential dir (no credentials file)
	credDir := t.TempDir()
	prev := TestSetConfigDirOverride(credDir)
	prevForce := TestSetForceFileStorage(true)
	t.Cleanup(func() {
		TestSetConfigDirOverride(prev)
		TestSetForceFileStorage(prevForce)
	})

	err := RefreshRemoteCredentials(dir, "https://sageox.ai")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "credentials")

	// verify remote was NOT modified on error
	cmd := exec.Command("git", "-C", dir, "remote", "get-url", "origin")
	output, err := cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(output), "old-token")
}

func TestRefreshRemoteCredentials_NonOauth2Username(t *testing.T) {
	ep := "https://sageox.ai"
	dir := setupGitRepoWithRemote(t, "https://deploy-token:some-token@git.sageox.ai/team/ledger.git")
	setupTestCredentials(t, ep, "different-token", "https://git.sageox.ai")

	err := RefreshRemoteCredentials(dir, ep)
	require.NoError(t, err)

	// verify remote unchanged (non-oauth2 username, not ox-managed)
	cmd := exec.Command("git", "-C", dir, "remote", "get-url", "origin")
	output, err := cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(output), "deploy-token:some-token")
}

func TestRefreshRemoteCredentials_DifferentHost(t *testing.T) {
	ep := "https://sageox.ai"
	dir := setupGitRepoWithRemote(t, "https://oauth2:old-token@git.other-server.com/team/ledger.git")
	setupTestCredentials(t, ep, "new-token", "https://git.sageox.ai")

	err := RefreshRemoteCredentials(dir, ep)
	require.NoError(t, err)

	// verify remote unchanged (different host, not our repo)
	cmd := exec.Command("git", "-C", dir, "remote", "get-url", "origin")
	output, err := cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(output), "old-token")
}

func TestRefreshRemoteCredentials_ExpiredCredentials(t *testing.T) {
	ep := "https://sageox.ai"
	dir := setupGitRepoWithRemote(t, "https://oauth2:old-token@git.sageox.ai/team/ledger.git")

	// set up credentials that are expired
	credDir := t.TempDir()
	prev := TestSetConfigDirOverride(credDir)
	prevForce := TestSetForceFileStorage(true)
	t.Cleanup(func() {
		TestSetConfigDirOverride(prev)
		TestSetForceFileStorage(prevForce)
	})

	creds := GitCredentials{
		Token:     "new-but-expired-token",
		ServerURL: "https://git.sageox.ai",
		Username:  "oauth2",
		ExpiresAt: time.Now().Add(-1 * time.Hour), // expired 1 hour ago
	}
	data, err := json.Marshal(creds)
	require.NoError(t, err)

	slug := endpointSlug(ep)
	sageoxDir := filepath.Join(credDir, "sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0o755))
	credFile := filepath.Join(sageoxDir, "git-credentials-"+slug+".json")
	require.NoError(t, os.WriteFile(credFile, data, 0o600))

	err = RefreshRemoteCredentials(dir, ep)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expired")

	// verify remote was NOT updated with expired credentials
	cmd := exec.Command("git", "-C", dir, "remote", "get-url", "origin")
	output, err := cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(output), "old-token")
	assert.NotContains(t, string(output), "new-but-expired-token")
}

func TestStripRemoteCredentials_OauthURL(t *testing.T) {
	dir := setupGitRepoWithRemote(t, "https://oauth2:secret-token@git.sageox.ai/team/ledger.git")

	err := StripRemoteCredentials(dir)
	require.NoError(t, err)

	cmd := exec.Command("git", "-C", dir, "remote", "get-url", "origin")
	output, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, "https://git.sageox.ai/team/ledger.git\n", string(output))
}

func TestStripRemoteCredentials_SSHNoOp(t *testing.T) {
	dir := setupGitRepoWithRemote(t, "git@git.sageox.ai:team/ledger.git")

	err := StripRemoteCredentials(dir)
	require.NoError(t, err)

	cmd := exec.Command("git", "-C", dir, "remote", "get-url", "origin")
	output, err := cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(output), "git@git.sageox.ai")
}

func TestStripRemoteCredentials_BareURLNoOp(t *testing.T) {
	dir := setupGitRepoWithRemote(t, "https://git.sageox.ai/team/ledger.git")

	err := StripRemoteCredentials(dir)
	require.NoError(t, err)

	cmd := exec.Command("git", "-C", dir, "remote", "get-url", "origin")
	output, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, "https://git.sageox.ai/team/ledger.git\n", string(output))
}

func TestStripRemoteCredentials_NonOauth2NoOp(t *testing.T) {
	dir := setupGitRepoWithRemote(t, "https://deploy-token:some-token@git.sageox.ai/team/ledger.git")

	err := StripRemoteCredentials(dir)
	require.NoError(t, err)

	// non-oauth2 credentials are not ox-managed, should be left alone
	cmd := exec.Command("git", "-C", dir, "remote", "get-url", "origin")
	output, err := cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(output), "deploy-token:some-token")
}

func TestStripThenRefresh_RoundTrip(t *testing.T) {
	ep := "https://sageox.ai"
	token := "active-pat-token"
	originalURL := "https://oauth2:" + token + "@git.sageox.ai/team/ledger.git"

	dir := setupGitRepoWithRemote(t, originalURL)
	setupTestCredentials(t, ep, token, "https://git.sageox.ai")

	// strip credentials (simulates ox logout)
	err := StripRemoteCredentials(dir)
	require.NoError(t, err)

	cmd := exec.Command("git", "-C", dir, "remote", "get-url", "origin")
	output, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, "https://git.sageox.ai/team/ledger.git\n", string(output), "PAT should be stripped after logout")

	// refresh credentials (simulates ox login or daemon sync)
	err = RefreshRemoteCredentials(dir, ep)
	require.NoError(t, err)

	cmd = exec.Command("git", "-C", dir, "remote", "get-url", "origin")
	output, err = cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(output), token, "PAT should be re-inserted after login")
	assert.Contains(t, string(output), "oauth2:"+token+"@", "should use oauth2 username")
}

func TestSanitizeRemoteURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"strips oauth2 credentials", "https://oauth2:secret-token@git.sageox.ai/repo.git", "https://git.sageox.ai/repo.git"},
		{"strips any credentials", "https://user:pass@host.com/repo.git", "https://host.com/repo.git"},
		{"bare URL unchanged", "https://git.sageox.ai/repo.git", "https://git.sageox.ai/repo.git"},
		{"SSH unchanged", "git@github.com:org/repo.git", "git@github.com:org/repo.git"},
		{"empty string", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SanitizeRemoteURL(tt.input))
		})
	}
}

func TestExtractHost(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://git.sageox.ai/repo.git", "git.sageox.ai"},
		{"https://git.sageox.ai:8443/repo.git", "git.sageox.ai"},
		{"git.sageox.ai", "git.sageox.ai"},
		{"http://localhost:3000/repo.git", "localhost"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, extractHost(tt.input))
		})
	}
}
