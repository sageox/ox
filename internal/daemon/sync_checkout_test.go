package daemon

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncScheduler_PullTeamContexts_EmptyPath(t *testing.T) {
	// isolate from real credentials
	prevConfigDir := gitserver.TestSetConfigDirOverride(t.TempDir())
	prevForceFile := gitserver.TestSetForceFileStorage(true)
	t.Cleanup(func() {
		gitserver.TestSetConfigDirOverride(prevConfigDir)
		gitserver.TestSetForceFileStorage(prevForceFile)
	})

	// create temp project with team context with empty path
	tmpDir := t.TempDir()
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	require.NoError(t, os.MkdirAll(sageoxDir, 0755))

	// write config with empty path
	configContent := `
[[team_contexts]]
team_id = "test-team"
team_name = "Test Team"
path = ""
`
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.local.toml"), []byte(configContent), 0644))

	// write project config with fake endpoint to prevent real API calls
	require.NoError(t, os.WriteFile(filepath.Join(sageoxDir, "config.json"),
		[]byte(`{"endpoint":"https://fake.test.invalid"}`), 0644))

	cfg := DefaultConfig()
	cfg.ProjectRoot = tmpDir
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)
	scheduler.pullTeamContexts(context.Background())

	// should have status entry marked as not existing
	status := scheduler.TeamContextStatus()
	require.Len(t, status, 1)
	assert.Equal(t, "test-team", status[0].TeamID)
	assert.False(t, status[0].Exists)
	assert.Equal(t, "no path configured", status[0].LastErr)
}

// Test SyncScheduler.Checkout with existing repo
func TestSyncScheduler_Checkout_AlreadyExists(t *testing.T) {
	// create temp git repo
	tmpDir := t.TempDir()
	gitDir := filepath.Join(tmpDir, ".git")
	require.NoError(t, os.MkdirAll(gitDir, 0755))

	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	result, err := scheduler.Checkout(CheckoutPayload{
		RepoPath: tmpDir,
		CloneURL: "https://example.com/repo.git",
		RepoType: "ledger",
	}, nil)

	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.True(t, result.AlreadyExists)
	assert.False(t, result.Cloned)
	assert.Equal(t, tmpDir, result.Path)
}

// Test SyncScheduler.Checkout with non-git directory (self-healing)
func TestSyncScheduler_Checkout_ExistsButNotGit(t *testing.T) {
	// create temp directory without .git - simulates corrupt/incomplete clone
	parentDir := t.TempDir()
	repoDir := filepath.Join(parentDir, "repo")
	require.NoError(t, os.MkdirAll(repoDir, 0755))

	// create a file in the directory to verify backup works
	testFile := filepath.Join(repoDir, "testfile.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("test"), 0644))

	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	// self-healing: should move directory aside and attempt clone
	// clone will fail due to untrusted host, but directory should be backed up
	result, err := scheduler.Checkout(CheckoutPayload{
		RepoPath: repoDir,
		CloneURL: "https://example.com/repo.git",
		RepoType: "ledger",
	}, nil)

	assert.Error(t, err)
	assert.Nil(t, result)
	// error should be from clone attempt (untrusted host), not "not a git repository"
	assert.Contains(t, err.Error(), "untrusted git host")

	// verify original directory was moved to backup (self-healing)
	backups, _ := filepath.Glob(filepath.Join(parentDir, "repo.bak.*"))
	assert.Len(t, backups, 1, "expected backup directory to be created")
	if len(backups) > 0 {
		// verify backup contains our test file
		backupTestFile := filepath.Join(backups[0], "testfile.txt")
		_, err := os.Stat(backupTestFile)
		assert.NoError(t, err, "backup should contain original files")
	}
}

// Test SyncScheduler.Checkout queues when all clone slots are busy (doesn't error)
func TestSyncScheduler_Checkout_ConcurrentClonesQueue(t *testing.T) {
	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	// fill all clone slots to simulate busy state
	for range maxConcurrentClones {
		scheduler.cloneSem <- struct{}{}
	}

	// hook fires just before the goroutine tries to acquire the semaphore,
	// so we know deterministically that it's about to block
	reachedSem := make(chan struct{})
	scheduler.onBeforeCloneSem = func() { close(reachedSem) }

	// use temp directory to pass path validation
	tmpDir := t.TempDir()
	repoPath := filepath.Join(tmpDir, "nonexistent-repo")

	// Checkout should block (not error) when all slots are busy.
	// Launch in goroutine and verify it doesn't return an error after we free a slot.
	done := make(chan struct{})
	var result *CheckoutResult
	var err error
	go func() {
		result, err = scheduler.Checkout(CheckoutPayload{
			RepoPath: repoPath,
			CloneURL: "http://127.0.0.1:1/repo.git", // local URL that fails instantly (no network round trip)
			RepoType: "ledger",
		}, nil)
		close(done)
	}()

	// wait for goroutine to reach the semaphore (deterministic, no sleep)
	select {
	case <-reachedSem:
		// goroutine is now about to block on the full semaphore
	case <-time.After(5 * time.Second):
		t.Fatal("Checkout goroutine did not reach semaphore in time")
	}

	select {
	case <-done:
		t.Fatal("Checkout should be blocking on semaphore, not returning immediately")
	default:
		// expected: still blocked
	}

	// free one slot — Checkout should proceed (and fail on clone, which is expected)
	<-scheduler.cloneSem

	select {
	case <-done:
		// Checkout completed (will error on actual clone, but NOT "another checkout in progress")
		if err != nil {
			assert.NotContains(t, err.Error(), "another checkout operation is in progress",
				"should never get the old contention error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Checkout did not unblock after freeing a clone slot")
	}

	// drain remaining slots
	for range maxConcurrentClones - 1 {
		<-scheduler.cloneSem
	}
	_ = result
}

// Test SyncScheduler.Checkout rejects path traversal attempts
func TestSyncScheduler_Checkout_PathTraversal(t *testing.T) {
	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	testCases := []struct {
		name     string
		repoPath string
	}{
		{"parent directory traversal", "/home/user/../../../etc/passwd"},
		{"embedded traversal", "/home/user/repos/../../../etc/passwd"},
		{"relative path", "relative/path/to/repo"},
		{"empty path", ""},
		{"double dot only", ".."},
		{"traversal at end", "/home/user/.."},
		{"outside home and tmp", "/etc/sageox/repo"},
		{"system directory", "/usr/local/repo"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := scheduler.Checkout(CheckoutPayload{
				RepoPath: tc.repoPath,
				CloneURL: "https://example.com/repo.git",
				RepoType: "ledger",
			}, nil)

			assert.Error(t, err)
			assert.Nil(t, result)
			assert.ErrorIs(t, err, ErrInvalidRepoPath)
		})
	}
}

// Test SyncScheduler.Checkout rejects local paths (SSRF protection)
func TestSyncScheduler_Checkout_Clone_LocalPathRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	// check if git is available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// create bare repo to clone from
	bareDir := t.TempDir()
	initBareCmd := exec.Command("git", "init", "--bare")
	initBareCmd.Dir = bareDir
	require.NoError(t, initBareCmd.Run())

	targetDir := filepath.Join(t.TempDir(), "cloned-repo")

	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	// SSRF protection: local paths are rejected
	result, err := scheduler.Checkout(CheckoutPayload{
		RepoPath: targetDir,
		CloneURL: bareDir, // local path - should be rejected
		RepoType: "ledger",
	}, nil)

	// local paths are now rejected as SSRF protection
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "invalid clone URL")
}

// TestIsValidCloneURL tests SSRF protection for clone URLs
func TestIsValidCloneURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
		errMsg  string
	}{
		// valid URLs - trusted hosts (daemon path: SageOx-controlled only)
		{
			name:    "git.sageox.io https",
			url:     "https://git.sageox.io/team/repo.git",
			wantErr: false,
		},
		{
			name:    "git.sageox.ai https",
			url:     "https://git.sageox.ai/team/repo.git",
			wantErr: false,
		},
		{
			name:    "subdomain of trusted host - sageox.ai",
			url:     "https://test.sageox.ai/team/repo.git",
			wantErr: false, // matches .sageox.ai
		},
		// The daemon's auto-clone path was narrowed in SECREVIEW follow-up: github.com
		// and gitlab.com are no longer trusted destinations for daemon-initiated clones.
		// A compromised cloud API can therefore no longer direct the daemon to clone
		// arbitrary GitHub/GitLab repos as "team contexts." If you're tempted to add
		// these back, route through a separate CLI-side allow-list instead.
		{
			name:    "github.com https - REJECTED on daemon path (was previously trusted)",
			url:     "https://github.com/org/repo.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
		{
			name:    "gitlab.com https - REJECTED on daemon path (was previously trusted)",
			url:     "https://gitlab.com/org/repo.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
		{
			name:    "subdomain of github - also REJECTED on daemon path",
			url:     "https://api.github.com/repos/org/repo.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
		{
			name:    "subdomain of gitlab - also REJECTED on daemon path",
			url:     "https://enterprise.gitlab.com/org/repo.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},

		// invalid URLs - wrong schemes (SSRF vectors)
		{
			name:    "file:// scheme - local file access",
			url:     "file:///etc/passwd",
			wantErr: true,
			errMsg:  "URL has no host", // file:// URLs have empty host
		},
		{
			name:    "file:// scheme - windows path",
			url:     "file:///C:/Windows/System32/config/SAM",
			wantErr: true,
			errMsg:  "URL has no host", // file:// URLs have empty host
		},
		{
			name:    "git:// scheme - unauthenticated",
			url:     "git://github.com/org/repo.git",
			wantErr: true,
			errMsg:  "only https:// URLs are supported",
		},
		{
			name:    "ssh:// scheme",
			url:     "ssh://git@github.com/org/repo.git",
			wantErr: true,
			errMsg:  "only https:// URLs are supported",
		},
		{
			name:    "http:// scheme - insecure for remote hosts",
			url:     "http://github.com/org/repo.git",
			wantErr: true,
			errMsg:  "only https:// URLs are supported for remote hosts",
		},

		// valid URLs - local development (http:// allowed for localhost only)
		{
			name:    "http:// localhost - devcontainer support",
			url:     "http://localhost/repo.git",
			wantErr: false,
		},
		{
			name:    "http:// localhost with port - devcontainer support",
			url:     "http://localhost:8929/team/repo.git",
			wantErr: false,
		},
		{
			name:    "http:// 127.0.0.1 - devcontainer support",
			url:     "http://127.0.0.1/repo.git",
			wantErr: false,
		},
		{
			name:    "http:// 127.0.0.1 with port - devcontainer support",
			url:     "http://127.0.0.1:8929/repo.git",
			wantErr: false,
		},

		// http:// should fail for all other hosts (even local networks)
		{
			name:    "http:// .local domain - blocked",
			url:     "http://gitlab.local:8929/team/repo.git",
			wantErr: true,
			errMsg:  "only https:// URLs are supported for remote hosts",
		},
		{
			name:    "http:// 192.168.x.x - blocked",
			url:     "http://192.168.1.100:8929/repo.git",
			wantErr: true,
			errMsg:  "only https:// URLs are supported for remote hosts",
		},
		{
			name:    "http:// external host - blocked",
			url:     "http://evil-server.com/repo.git",
			wantErr: true,
			errMsg:  "only https:// URLs are supported for remote hosts",
		},
		{
			name:    "http:// gitlab.com - blocked (use https)",
			url:     "http://gitlab.com/org/repo.git",
			wantErr: true,
			errMsg:  "only https:// URLs are supported for remote hosts",
		},
		{
			name:    "ftp:// scheme",
			url:     "ftp://evil.com/malware.git",
			wantErr: true,
			errMsg:  "only https:// URLs are supported",
		},

		// invalid URLs - https on untrusted hosts (even local ones need http for dev)
		// Note: https:// to local hosts still fails because they're not in trustedGitHosts
		// Use http:// for local development instead
		{
			name:    "https localhost - not in trusted hosts",
			url:     "https://localhost/repo.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
		{
			name:    "https 127.0.0.1 - not in trusted hosts",
			url:     "https://127.0.0.1/repo.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
		{
			name:    "https 192.168.x.x - not in trusted hosts",
			url:     "https://192.168.1.1/repo.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
		{
			name:    "https 10.x.x.x - not in trusted hosts",
			url:     "https://10.0.0.1/repo.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
		{
			name:    "https 172.16.x.x - not in trusted hosts",
			url:     "https://172.16.0.1/repo.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
		{
			name:    "internal hostname",
			url:     "https://internal-git.corp/repo.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
		{
			name:    "arbitrary external host",
			url:     "https://evil-server.com/malware.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
		{
			name:    "typosquatting - githubcom",
			url:     "https://githubcom.evil.com/repo.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
		{
			name:    "typosquatting - github-com",
			url:     "https://github-com.evil.com/repo.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},

		// edge cases
		{
			name:    "empty URL",
			url:     "",
			wantErr: true,
			errMsg:  "clone URL is empty",
		},
		{
			name:    "malformed URL",
			url:     "not-a-valid-url",
			wantErr: true,
			errMsg:  "URL has no host", // parsed as path with no scheme/host
		},
		{
			name:    "URL with credentials on trusted host",
			url:     "https://user:pass@git.sageox.ai/team/repo.git",
			wantErr: false,
		},
		{
			name:    "URL with port on trusted host",
			url:     "https://git.sageox.ai:443/team/repo.git",
			wantErr: false,
		},
		{
			name:    "URL with port on untrusted host",
			url:     "https://evil.com:8080/repo.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
		{
			name:    "AWS metadata endpoint",
			url:     "https://169.254.169.254/latest/meta-data/",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
		{
			name:    "IPv6 localhost",
			url:     "https://[::1]/repo.git",
			wantErr: true,
			errMsg:  "untrusted git host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := isValidCloneURL(tt.url)
			if tt.wantErr {
				require.Error(t, err, "expected error for URL: %s", tt.url)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				require.NoError(t, err, "unexpected error for URL: %s", tt.url)
			}
		})
	}
}

// TestSyncScheduler_Checkout_SSRF_Prevention tests that Checkout rejects unsafe URLs
func TestSyncScheduler_Checkout_SSRF_Prevention(t *testing.T) {
	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	unsafeURLs := []struct {
		name string
		url  string
	}{
		{"file URL", "file:///etc/passwd"},
		{"localhost", "https://localhost/repo.git"},
		{"internal IP", "https://192.168.1.1/repo.git"},
		{"arbitrary host", "https://evil.com/repo.git"},
		{"git protocol", "git://github.com/repo.git"},
	}

	// use temp dir for the repo path to avoid path validation issues
	tmpDir := t.TempDir()

	for _, tc := range unsafeURLs {
		t.Run(tc.name, func(t *testing.T) {
			result, err := scheduler.Checkout(CheckoutPayload{
				RepoPath: filepath.Join(tmpDir, "repo"),
				CloneURL: tc.url,
				RepoType: "ledger",
			}, nil)

			require.Error(t, err)
			assert.Nil(t, result)
			assert.Contains(t, err.Error(), "invalid clone URL")
		})
	}
}

func TestWorkspaceRegistry_SyncBackoff(t *testing.T) {
	// unit test the backoff math
	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	// initially should allow sync
	assert.True(t, scheduler.workspaceRegistry.ShouldSync("ledger"))

	// record failures and verify exponential backoff
	scheduler.workspaceRegistry.RecordSyncFailure("ledger")
	assert.False(t, scheduler.workspaceRegistry.ShouldSync("ledger"), "should be in backoff after 1 failure")

	failures, nextRetry := scheduler.workspaceRegistry.GetSyncRetryInfo("ledger")
	assert.Equal(t, 1, failures)
	assert.True(t, nextRetry.After(time.Now()))

	// clear should reset
	scheduler.workspaceRegistry.ClearSyncFailures("ledger")
	assert.True(t, scheduler.workspaceRegistry.ShouldSync("ledger"), "should allow sync after clear")

	failures, _ = scheduler.workspaceRegistry.GetSyncRetryInfo("ledger")
	assert.Equal(t, 0, failures)
}

func TestSyncBackoffMax(t *testing.T) {
	assert.Equal(t, 30*time.Minute, syncBackoffMax)
}
