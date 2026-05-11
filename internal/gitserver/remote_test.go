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

// Per ox-eeqi, RefreshRemoteCredentials no longer embeds the PAT into the
// origin URL. It strips any embedded PAT and installs the ox credential
// helper. The tests below assert the new behavior:
//
//   - embedded PAT in origin → stripped + helper installed
//   - bare origin           → helper installed (idempotent)
//   - SSH / file:// / non-oauth2 → untouched

func TestRefreshRemoteCredentials_StalePAT(t *testing.T) {
	ep := "https://sageox.ai"
	oldToken := "old-pat-token-12345"
	newToken := "new-pat-token-67890"

	dir := setupGitRepoWithRemote(t, "https://oauth2:"+oldToken+"@git.sageox.ai/team/ledger.git")
	setupTestCredentials(t, ep, newToken, "https://git.sageox.ai")

	err := RefreshRemoteCredentials(dir, ep)
	require.NoError(t, err)

	// New behavior: origin is now bare; credentials live in the store, not the URL.
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	require.NoError(t, err)
	assert.NotContains(t, string(out), oldToken, "old token should be stripped from URL")
	assert.NotContains(t, string(out), newToken, "new token must NOT be embedded — that's the leak we're closing")
	assert.Contains(t, string(out), "https://git.sageox.ai/team/ledger.git",
		"bare URL expected after migration")

	// Helper must be configured for the matching host.
	helperVal, err := readGitConfig(dir, "credential.https://git.sageox.ai.helper")
	require.NoError(t, err)
	assert.NotEmpty(t, helperVal, "credential helper should be installed for git.sageox.ai")
}

func TestRefreshRemoteCredentials_CurrentPAT(t *testing.T) {
	ep := "https://sageox.ai"
	token := "current-pat-token"

	dir := setupGitRepoWithRemote(t, "https://oauth2:"+token+"@git.sageox.ai/team/ledger.git")
	setupTestCredentials(t, ep, token, "https://git.sageox.ai")

	err := RefreshRemoteCredentials(dir, ep)
	require.NoError(t, err)

	// Even when the embedded token matches the store, the new behavior strips
	// it — the goal is "no token in .git/config", period. Idempotent on re-run.
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	require.NoError(t, err)
	assert.NotContains(t, string(out), token, "embedded token must be removed")
	helperVal, err := readGitConfig(dir, "credential.https://git.sageox.ai.helper")
	require.NoError(t, err)
	assert.NotEmpty(t, helperVal)
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

	// Bare URLs stay bare. The helper handles auth at fetch/push time.
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	require.NoError(t, err)
	assert.NotContains(t, string(out), "some-token", "URL must NOT carry the token")
	assert.NotContains(t, string(out), "oauth2:", "URL must NOT carry userinfo at all")

	// And the helper must be installed for this host.
	helperVal, err := readGitConfig(dir, "credential.https://git.sageox.ai.helper")
	require.NoError(t, err)
	assert.NotEmpty(t, helperVal)
}

func TestRefreshRemoteCredentials_NoCredentials(t *testing.T) {
	// Per ox-eeqi: missing credentials are not a hard error at refresh time.
	// The helper returns nothing when called, git prompts (or falls back),
	// and the user gets a clearer error from the actual operation. What the
	// refresh MUST do is strip the embedded PAT so we close the leak.
	dir := setupGitRepoWithRemote(t, "https://oauth2:old-token@git.sageox.ai/team/ledger.git")

	credDir := t.TempDir()
	prev := TestSetConfigDirOverride(credDir)
	prevForce := TestSetForceFileStorage(true)
	t.Cleanup(func() {
		TestSetConfigDirOverride(prev)
		TestSetForceFileStorage(prevForce)
	})

	err := RefreshRemoteCredentials(dir, "https://sageox.ai")
	require.NoError(t, err)

	// embedded PAT must have been stripped regardless of credentials presence
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	require.NoError(t, err)
	assert.NotContains(t, string(out), "old-token",
		"embedded PAT must be stripped even when no store credentials exist — closing the leak doesn't depend on having a replacement token")
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
	require.NoError(t, err, "expired creds no longer fail refresh; daemon renewal handles them")

	// New behavior strips the embedded PAT regardless of whether the store
	// credentials are expired — the leak we close (URL-embedded token) is
	// independent of the renewal cycle.
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	require.NoError(t, err)
	assert.NotContains(t, string(out), "old-token")
	assert.NotContains(t, string(out), "new-but-expired-token",
		"expired tokens must never be embedded — but they were never going to be under the new model anyway")
}

// empty endpoint derives endpoint from remote URL host, then loads credentials
func TestRefreshRemoteCredentials_EmptyEndpoint_DerivesFromRemote(t *testing.T) {
	// use example.invalid so no real credentials can match
	dir := setupGitRepoWithRemote(t, "https://oauth2:some-token@git.example.invalid/team/ledger.git")

	// set up empty credential dir — no credentials for example.invalid
	credDir := t.TempDir()
	prev := TestSetConfigDirOverride(credDir)
	prevForce := TestSetForceFileStorage(true)
	t.Cleanup(func() {
		TestSetConfigDirOverride(prev)
		TestSetForceFileStorage(prevForce)
	})

	// empty endpoint → derives "https://example.invalid" from remote
	// no credentials stored → still strips the PAT (closing the leak doesn't
	// require having a replacement token).
	err := RefreshRemoteCredentials(dir, "")
	require.NoError(t, err)

	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	require.NoError(t, err)
	assert.NotContains(t, string(out), "some-token",
		"PAT must be stripped even when no replacement creds are available")
}

// empty endpoint with matching credentials installs the helper and strips
// the embedded PAT.
func TestRefreshRemoteCredentials_EmptyEndpoint_WithCredentials(t *testing.T) {
	oldToken := "old-token-abc"
	newToken := "new-token-xyz"
	dir := setupGitRepoWithRemote(t, "https://oauth2:"+oldToken+"@git.sageox.ai/team/ledger.git")

	// derived endpoint: git.sageox.ai → sageox.ai
	setupTestCredentials(t, "https://sageox.ai", newToken, "https://git.sageox.ai")

	err := RefreshRemoteCredentials(dir, "")
	require.NoError(t, err)

	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	require.NoError(t, err)
	assert.NotContains(t, string(out), oldToken)
	assert.NotContains(t, string(out), newToken,
		"new token MUST NOT be embedded — that's the entire migration")
	helperVal, err := readGitConfig(dir, "credential.https://git.sageox.ai.helper")
	require.NoError(t, err)
	assert.NotEmpty(t, helperVal)
}

// local file:// remotes must never have credentials injected
func TestRefreshRemoteCredentials_LocalRemote_NoOp(t *testing.T) {
	base := t.TempDir()
	bareDir := filepath.Join(base, "bare.git")
	cmd := exec.Command("git", "init", "--bare", bareDir)
	require.NoError(t, cmd.Run())

	dir := setupGitRepoWithRemote(t, "file://"+bareDir)
	setupTestCredentials(t, "https://sageox.ai", "some-token", "https://git.sageox.ai")

	err := RefreshRemoteCredentials(dir, "https://sageox.ai")
	require.NoError(t, err)

	// remote URL must remain file:// — never corrupted with credentials
	out := exec.Command("git", "-C", dir, "remote", "get-url", "origin")
	output, err := out.Output()
	require.NoError(t, err)
	assert.Equal(t, "file://"+bareDir+"\n", string(output))
}

// bare local path remotes must never have credentials injected
func TestRefreshRemoteCredentials_BarePathRemote_NoOp(t *testing.T) {
	base := t.TempDir()
	bareDir := filepath.Join(base, "bare.git")
	cmd := exec.Command("git", "init", "--bare", bareDir)
	require.NoError(t, cmd.Run())

	dir := setupGitRepoWithRemote(t, bareDir)
	setupTestCredentials(t, "https://sageox.ai", "some-token", "https://git.sageox.ai")

	err := RefreshRemoteCredentials(dir, "https://sageox.ai")
	require.NoError(t, err)

	out := exec.Command("git", "-C", dir, "remote", "get-url", "origin")
	output, err := out.Output()
	require.NoError(t, err)
	assert.Equal(t, bareDir+"\n", string(output))
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
	// Per ox-eeqi, the round-trip is "strip → install helper → leave bare."
	// PATs no longer get re-inserted into URLs on login. They live in the
	// credential store; git asks the helper for them at fetch/push time.
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

	// refresh credentials (simulates ox login or daemon sync). The URL
	// MUST stay bare; the credential helper MUST be installed.
	err = RefreshRemoteCredentials(dir, ep)
	require.NoError(t, err)

	cmd = exec.Command("git", "-C", dir, "remote", "get-url", "origin")
	output, err = cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, "https://git.sageox.ai/team/ledger.git\n", string(output),
		"URL stays bare after refresh; helper resolves auth at git invocation time")
	helperVal, err := readGitConfig(dir, "credential.https://git.sageox.ai.helper")
	require.NoError(t, err)
	assert.NotEmpty(t, helperVal, "helper must be installed after refresh")
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

func TestEndpointFromRemoteURL(t *testing.T) {
	tests := []struct {
		name      string
		remoteURL string
		want      string
	}{
		{"git prefix stripped", "https://oauth2:TOKEN@git.sageox.ai/repo.git", "https://sageox.ai"},
		{"staging with git prefix", "https://oauth2:TOKEN@git.test.sageox.ai/repo.git", "https://test.sageox.ai"},
		{"no git prefix", "https://oauth2:TOKEN@sageox.ai/repo.git", "https://sageox.ai"},
		{"bare HTTPS", "https://git.sageox.ai/repo.git", "https://sageox.ai"},
		{"localhost", "http://localhost:3000/repo.git", "http://localhost"},
		{"empty", "", ""},
		{"file URL", "file:///tmp/repo.git", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, endpointFromRemoteURL(tt.remoteURL))
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
