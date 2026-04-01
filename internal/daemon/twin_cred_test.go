//go:build slow

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createGiteaToken creates a new API token via Gitea API and returns its value.
func createGiteaToken(t *testing.T, g *giteaFixture, tokenName string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	body, _ := json.Marshal(map[string]interface{}{
		"name":   tokenName,
		"scopes": []string{"all"},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		g.httpURL+"/api/v1/users/"+g.adminUser+"/tokens", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(g.adminUser, g.adminPass)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))

	token, _ := result["sha1"].(string)
	if token == "" {
		token, _ = result["token"].(string)
	}
	require.NotEmpty(t, token, "API token should not be empty")
	return token
}

// deleteGiteaToken deletes an API token by name via Gitea API.
func deleteGiteaToken(t *testing.T, g *giteaFixture, tokenName string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		g.httpURL+"/api/v1/users/"+g.adminUser+"/tokens/"+tokenName, nil)
	require.NoError(t, err)
	req.SetBasicAuth(g.adminUser, g.adminPass)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	// 204 No Content = success, 404 = already gone
	require.True(t, resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound,
		"delete token: unexpected status %d", resp.StatusCode)
}

// --- A. PAT liveness with valid token ---

// TestPATLiveness_ValidToken_RealServer verifies that GIT_ASKPASS credential
// injection works against a real HTTP git server with a private repo.
// Failure prevented: GIT_ASKPASS script or GIT_CONFIG_COUNT env vars not
// functioning correctly against real HTTP server authentication.
func TestPATLiveness_ValidToken_RealServer(t *testing.T) {
	g := getSharedGitea(t)
	repoName := "twin-cred-liveness-valid"
	g.createPrivateRepo(t, repoName)

	token := createGiteaToken(t, g, "liveness-valid-token")
	t.Cleanup(func() { deleteGiteaToken(t, g, "liveness-valid-token") })

	repoURL := fmt.Sprintf("http://localhost:%s/%s/%s.git", giteaHostPort, g.adminUser, repoName)

	creds := &gitserver.GitCredentials{
		Token:     token,
		ServerURL: g.httpURL,
		Repos: map[string]gitserver.RepoEntry{
			"test": {URL: repoURL, Name: repoName},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := gitserver.ValidatePATLiveness(ctx, creds)
	assert.True(t, result.Valid, "valid token should pass liveness check, reason: %s", result.Reason)
	assert.False(t, result.Skipped)
}

// --- B. PAT liveness with revoked token ---

// TestPATLiveness_RevokedToken_RealServer verifies that a revoked token is
// correctly detected as invalid by the liveness check against a private repo.
// Failure prevented: Gitea's revoked-token error format not matching our
// "401"/"403"/"authentication failed" detection patterns.
func TestPATLiveness_RevokedToken_RealServer(t *testing.T) {
	g := getSharedGitea(t)
	repoName := "twin-cred-liveness-revoked"
	g.createPrivateRepo(t, repoName)

	tokenName := "liveness-revoke-token"
	token := createGiteaToken(t, g, tokenName)

	repoURL := fmt.Sprintf("http://localhost:%s/%s/%s.git", giteaHostPort, g.adminUser, repoName)
	creds := &gitserver.GitCredentials{
		Token:     token,
		ServerURL: g.httpURL,
		Repos: map[string]gitserver.RepoEntry{
			"test": {URL: repoURL, Name: repoName},
		},
	}

	// verify it works first
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	result := gitserver.ValidatePATLiveness(ctx, creds)
	cancel()
	require.True(t, result.Valid, "token should be valid before revocation")

	// revoke the token
	deleteGiteaToken(t, g, tokenName)

	// now it should fail
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	result = gitserver.ValidatePATLiveness(ctx2, creds)
	assert.False(t, result.Valid, "revoked token should fail liveness check")
	assert.False(t, result.Skipped, "should not be skipped")
	assert.NotEmpty(t, result.Reason, "should have a failure reason")
}

// --- C. PAT liveness with bogus token ---

// TestPATLiveness_InvalidToken_RealServer verifies that a completely bogus
// token string is correctly detected as invalid against a private repo.
// Failure prevented: bogus token error from Gitea not recognized as auth failure,
// classified as "network error" instead and returning Skipped=true.
func TestPATLiveness_InvalidToken_RealServer(t *testing.T) {
	g := getSharedGitea(t)
	repoName := "twin-cred-liveness-bogus"
	g.createPrivateRepo(t, repoName)

	repoURL := fmt.Sprintf("http://localhost:%s/%s/%s.git", giteaHostPort, g.adminUser, repoName)
	creds := &gitserver.GitCredentials{
		Token:     "completely-bogus-token-value-that-does-not-exist",
		ServerURL: g.httpURL,
		Repos: map[string]gitserver.RepoEntry{
			"test": {URL: repoURL, Name: repoName},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := gitserver.ValidatePATLiveness(ctx, creds)
	assert.False(t, result.Valid, "bogus token should fail liveness check")
	assert.False(t, result.Skipped, "should not be skipped — it's an auth failure, not network")
}

// --- D. oauth2:TOKEN URL format works for real git operations ---

// TestGitOperations_OAuth2URL_RealServer verifies that the oauth2:TOKEN@host
// URL format used by RefreshRemoteCredentials works for clone, fetch, and push
// against real Gitea.
// Failure prevented: URL construction in RefreshRemoteCredentials producing
// URLs that git doesn't accept or that Gitea rejects.
func TestGitOperations_OAuth2URL_RealServer(t *testing.T) {
	g := getSharedGitea(t)
	repoName := "twin-cred-oauth2-url"
	g.createPrivateRepo(t, repoName)

	token := createGiteaToken(t, g, "oauth2-url-token")
	t.Cleanup(func() { deleteGiteaToken(t, g, "oauth2-url-token") })

	// this is the exact URL format RefreshRemoteCredentials constructs
	oauth2URL := fmt.Sprintf("http://oauth2:%s@localhost:%s/%s/%s.git",
		token, giteaHostPort, g.adminUser, repoName)

	// clone with oauth2 URL
	cloneDir := filepath.Join(t.TempDir(), "oauth2-clone")
	cmd := exec.Command("git", "clone", oauth2URL, cloneDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git clone with oauth2 URL failed: %s", string(out))

	gitConfig(t, cloneDir)

	// commit and push
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "test.txt"), []byte("oauth2 test\n"), 0o644))
	cmd = exec.Command("git", "-C", cloneDir, "add", "test.txt")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git add failed: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "test oauth2 push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit failed: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git push with oauth2 URL failed: %s", string(out))

	// fetch also works
	cmd = exec.Command("git", "-C", cloneDir, "fetch", "origin")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git fetch with oauth2 URL failed: %s", string(out))
}

// --- E. Credential rotation: stale token → fresh token ---

// TestCredentialRotation_StaleToFresh_RealServer verifies the full credential
// rotation lifecycle against a private repo: clone with token A, revoke token A,
// operations fail, update remote to token B, operations succeed again.
// Failure prevented: credential refresh not actually fixing broken auth state,
// or remote URL manipulation corrupting the URL format.
func TestCredentialRotation_StaleToFresh_RealServer(t *testing.T) {
	g := getSharedGitea(t)
	repoName := "twin-cred-rotation"
	g.createPrivateRepo(t, repoName)

	tokenA := createGiteaToken(t, g, "rotation-token-a")
	tokenB := createGiteaToken(t, g, "rotation-token-b")
	t.Cleanup(func() {
		deleteGiteaToken(t, g, "rotation-token-a")
		deleteGiteaToken(t, g, "rotation-token-b")
	})

	// clone with token A
	urlWithA := fmt.Sprintf("http://oauth2:%s@localhost:%s/%s/%s.git",
		tokenA, giteaHostPort, g.adminUser, repoName)
	cloneDir := filepath.Join(t.TempDir(), "rotation-clone")
	cmd := exec.Command("git", "clone", urlWithA, cloneDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "clone with token A failed: %s", string(out))

	gitConfig(t, cloneDir)

	// verify ls-remote works with token A
	cmd = exec.Command("git", "-C", cloneDir, "ls-remote", "--heads", "origin")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "ls-remote with token A should work: %s", string(out))

	// revoke token A
	deleteGiteaToken(t, g, "rotation-token-a")

	// ls-remote with stale token A should fail on a private repo
	cmd = exec.Command("git", "-C", cloneDir, "ls-remote", "--heads", "origin")
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err = cmd.CombinedOutput()
	require.Error(t, err, "ls-remote with revoked token should fail on private repo: %s", string(out))

	// simulate credential refresh: update remote URL to token B
	urlWithB := fmt.Sprintf("http://oauth2:%s@localhost:%s/%s/%s.git",
		tokenB, giteaHostPort, g.adminUser, repoName)
	cmd = exec.Command("git", "-C", cloneDir, "remote", "set-url", "origin", urlWithB)
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "set-url to token B failed: %s", string(out))

	// ls-remote with token B should succeed
	cmd = exec.Command("git", "-C", cloneDir, "ls-remote", "--heads", "origin")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "ls-remote with fresh token B should work: %s", string(out))

	// push also works with new token
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "rotated.txt"), []byte("after rotation\n"), 0o644))
	cmd = exec.Command("git", "-C", cloneDir, "add", "rotated.txt")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git add failed: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "commit with rotated creds")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit failed: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "push with token B should work: %s", string(out))
}

// --- F. Verify stripped credentials URL still fails (sanity) ---

// TestStrippedCredentials_PushFails_RealServer verifies that
// StripRemoteCredentials actually prevents git auth on a private repo,
// confirming the PAT was the sole authentication mechanism.
// Failure prevented: git caching credentials in memory/helper, making
// StripRemoteCredentials appear to work while auth still succeeds.
func TestStrippedCredentials_PushFails_RealServer(t *testing.T) {
	g := getSharedGitea(t)
	repoName := "twin-cred-strip"
	g.createPrivateRepo(t, repoName)

	token := createGiteaToken(t, g, "strip-test-token")
	t.Cleanup(func() { deleteGiteaToken(t, g, "strip-test-token") })

	oauth2URL := fmt.Sprintf("http://oauth2:%s@localhost:%s/%s/%s.git",
		token, giteaHostPort, g.adminUser, repoName)

	cloneDir := filepath.Join(t.TempDir(), "strip-clone")
	cmd := exec.Command("git", "clone", oauth2URL, cloneDir)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "clone failed: %s", string(out))

	gitConfig(t, cloneDir)

	// strip credentials from remote URL
	err = gitserver.StripRemoteCredentials(cloneDir)
	require.NoError(t, err)

	// verify remote URL no longer has credentials
	cmd = exec.Command("git", "-C", cloneDir, "remote", "get-url", "origin")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err)
	remoteURL := string(out)
	assert.NotContains(t, remoteURL, token, "remote URL should not contain token after strip")
	assert.NotContains(t, remoteURL, "oauth2", "remote URL should not contain oauth2 after strip")

	// push should now fail (no credentials, private repo)
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "fail.txt"), []byte("will fail\n"), 0o644))
	cmd = exec.Command("git", "-C", cloneDir, "add", "fail.txt")
	_, _ = cmd.CombinedOutput()
	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "should fail to push")
	_, _ = cmd.CombinedOutput()

	cmd = exec.Command("git", "-C", cloneDir, "push")
	cmd.Env = append(cmd.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		// disable credential helpers so cached credentials from the initial
		// clone don't mask the stripped URL — tests the URL itself, not the cache
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0=",
	)
	_, err = cmd.CombinedOutput()
	require.Error(t, err, "push without credentials should fail on private repo")
}
