package daemon

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test isValidRepoPath validates correct paths
func TestIsValidRepoPath(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	require.NoError(t, err)

	tmpDir := os.TempDir()

	testCases := []struct {
		name     string
		path     string
		expected bool
	}{
		// valid paths
		{"home directory path", filepath.Join(homeDir, "repos", "myrepo"), true},
		{"temp directory path", filepath.Join(tmpDir, "test-repo"), true},
		{"nested home path", filepath.Join(homeDir, ".sageox", "data", "ledger"), true},
		{"/tmp path", "/tmp/test-repo", true},
		{"/private/tmp path", "/private/tmp/test-repo", true},
		{"nested /tmp path", "/tmp/myproject_sageox/ledger", true},

		// invalid paths
		{"empty path", "", false},
		{"relative path", "relative/path", false},
		{"path with traversal", filepath.Join(homeDir, "..", "etc"), false},
		{"system path", "/etc/passwd", false},
		{"var path", "/var/log/test", false},
		{"usr path", "/usr/local/bin", false},
		{"root path", "/", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := isValidRepoPath(tc.path)
			assert.Equal(t, tc.expected, result, "path: %s", tc.path)
		})
	}
}

// TestSanitizeGitOutput tests credential sanitization in git command output
func TestSanitizeGitOutput(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "oauth2 token in clone URL",
			input:    "fatal: could not read from remote 'https://oauth2:ghp_abc123xyz789@github.com/org/repo.git'",
			expected: "fatal: could not read from remote 'https://oauth2:***@github.com/org/repo.git'",
		},
		{
			name:     "oauth2 token in error message",
			input:    "Cloning into 'repo'...\nfatal: Authentication failed for 'https://oauth2:secret_token_here@git.sageox.io/team/repo.git'",
			expected: "Cloning into 'repo'...\nfatal: Authentication failed for 'https://oauth2:***@git.sageox.io/team/repo.git'",
		},
		{
			name:     "multiple tokens in output",
			input:    "remote: oauth2:token1@host.com\nremote: oauth2:token2@host.com",
			expected: "remote: oauth2:***@host.com\nremote: oauth2:***@host.com",
		},
		{
			name:     "no credentials - unchanged",
			input:    "fatal: repository 'https://github.com/org/repo.git' not found",
			expected: "fatal: repository 'https://github.com/org/repo.git' not found",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "long token",
			input:    "https://oauth2:glpat-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx@gitlab.com/repo.git",
			expected: "https://oauth2:***@gitlab.com/repo.git",
		},
		{
			name:     "token with special characters (no @)",
			input:    "https://oauth2:abc123!#$%^&*()_+-=xyz@host.com/repo.git",
			expected: "https://oauth2:***@host.com/repo.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := gitutil.SanitizeOutput(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestInjectGitCredentials tests credential injection into git URLs
func TestInjectGitCredentials(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		username string
		password string
		expected string
	}{
		{
			name:     "https URL with credentials",
			url:      "https://github.com/org/repo.git",
			username: "oauth2",
			password: "token123",
			expected: "https://oauth2:token123@github.com/org/repo.git",
		},
		{
			name:     "https URL with port",
			url:      "https://git.example.com:8443/repo.git",
			username: "oauth2",
			password: "token123",
			expected: "https://oauth2:token123@git.example.com:8443/repo.git",
		},
		{
			name:     "http localhost URL with credentials",
			url:      "http://localhost:8929/org/repo.git",
			username: "oauth2",
			password: "token123",
			expected: "http://oauth2:token123@localhost:8929/org/repo.git",
		},
		{
			name:     "http localhost without port",
			url:      "http://localhost/org/repo.git",
			username: "oauth2",
			password: "token123",
			expected: "http://oauth2:token123@localhost/org/repo.git",
		},
		{
			name:     "http 127.0.0.1 URL with credentials",
			url:      "http://127.0.0.1:8929/org/repo.git",
			username: "oauth2",
			password: "token123",
			expected: "http://oauth2:token123@127.0.0.1:8929/org/repo.git",
		},
		{
			name:     "http external host - unchanged (security)",
			url:      "http://external-host.com/repo.git",
			username: "oauth2",
			password: "token123",
			expected: "http://external-host.com/repo.git",
		},
		{
			name:     "http .local domain - unchanged (security)",
			url:      "http://gitlab.local:8929/repo.git",
			username: "oauth2",
			password: "token123",
			expected: "http://gitlab.local:8929/repo.git",
		},
		{
			name:     "empty username - unchanged",
			url:      "https://github.com/org/repo.git",
			username: "",
			password: "token123",
			expected: "https://github.com/org/repo.git",
		},
		{
			name:     "empty password - unchanged",
			url:      "https://github.com/org/repo.git",
			username: "oauth2",
			password: "",
			expected: "https://github.com/org/repo.git",
		},
		{
			name:     "ssh URL - unchanged",
			url:      "git@github.com:org/repo.git",
			username: "oauth2",
			password: "token123",
			expected: "git@github.com:org/repo.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := injectGitCredentials(tt.url, tt.username, tt.password)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRemoteRefCheck_NoChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	require.NoError(t, os.MkdirAll(repoDir, 0755))
	setupGitRepo(t, repoDir)

	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	// local and remote are in sync — should return true (skip)
	ctx := context.Background()
	assert.True(t, scheduler.remoteRefCheck(ctx, repoDir), "should skip when remote matches local")
}

func TestRemoteRefCheck_WithChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	require.NoError(t, os.MkdirAll(repoDir, 0755))
	setupGitRepo(t, repoDir)

	// push a new commit to the bare remote from a separate clone
	bareDir := repoDir + ".bare"
	tempClone := filepath.Join(tmpDir, "temp-clone")
	require.NoError(t, exec.Command("git", "clone", bareDir, tempClone).Run())

	configCmd := exec.Command("git", "-C", tempClone, "config", "user.email", "test@test.com")
	require.NoError(t, configCmd.Run())
	configCmd2 := exec.Command("git", "-C", tempClone, "config", "user.name", "Test")
	require.NoError(t, configCmd2.Run())

	require.NoError(t, os.WriteFile(filepath.Join(tempClone, "new-file.txt"), []byte("new content"), 0644))
	require.NoError(t, exec.Command("git", "-C", tempClone, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", tempClone, "commit", "-m", "new commit").Run())
	require.NoError(t, exec.Command("git", "-C", tempClone, "push", "origin", "HEAD:main").Run())

	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	// remote has a new commit — should return false (proceed with fetch)
	ctx := context.Background()
	assert.False(t, scheduler.remoteRefCheck(ctx, repoDir), "should not skip when remote has new commits")
}

func TestRemoteRefCheck_ErrorFallsThrough(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	require.NoError(t, os.MkdirAll(repoDir, 0755))

	// init repo with a bogus remote that will fail ls-remote
	require.NoError(t, exec.Command("git", "init", repoDir).Run())
	require.NoError(t, exec.Command("git", "-C", repoDir, "remote", "add", "origin", "https://127.0.0.1:1/nonexistent.git").Run())

	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	// ls-remote should fail — function should return false (fall through to fetch)
	ctx := context.Background()
	assert.False(t, scheduler.remoteRefCheck(ctx, repoDir), "should fall through on ls-remote error")
}
