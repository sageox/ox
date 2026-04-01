package daemon

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncScheduler_NoLedgerPath(t *testing.T) {
	cfg := DefaultConfig()
	cfg.LedgerPath = "" // no ledger configured
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scheduler := NewSyncScheduler(cfg, logger)

	// should not panic, just return early
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	scheduler.pullChanges(ctx)
	scheduler.syncAll(ctx)
}

func TestIsClonePermanentError(t *testing.T) {
	permanent := []struct {
		name string
		msg  string
	}{
		{"auth failed", "fatal: Authentication failed for 'https://git.example.com/repo.git'"},
		{"permission denied", "Permission denied (publickey)"},
		{"could not read username", "fatal: could not read Username for 'https://git.example.com': terminal prompts disabled"},
		{"invalid credentials", "remote: invalid credentials"},
		{"repo not found", "fatal: repository not found"},
		{"not a git repo", "fatal: 'https://example.com/bad' does not appear to be a git repository"},
		{"http 401", "git clone failed: HTTP 401"},
		{"http 403", "git clone failed: HTTP 403"},
		{"http 404", "git clone failed: HTTP 404"},
		{"invalid clone url", "invalid clone URL: scheme must be https"},
	}

	for _, tt := range permanent {
		t.Run(tt.name, func(t *testing.T) {
			assert.True(t, isClonePermanentError(tt.msg), "expected permanent for: %s", tt.msg)
		})
	}

	transient := []struct {
		name string
		msg  string
	}{
		{"network timeout", "fatal: unable to access 'https://git.example.com/repo.git': Connection timed out"},
		{"dns failure", "fatal: unable to access: Could not resolve host: git.example.com"},
		{"connection reset", "fatal: the remote end hung up unexpectedly"},
		{"server 500", "error: RPC failed; HTTP 500 curl 22"},
		{"generic error", "git clone failed: exit status 128"},
	}

	for _, tt := range transient {
		t.Run(tt.name, func(t *testing.T) {
			assert.False(t, isClonePermanentError(tt.msg), "expected transient for: %s", tt.msg)
		})
	}
}

func TestClonePermanentBackoffMax(t *testing.T) {
	// permanent errors should have a much shorter max backoff than transient
	assert.Less(t, clonePermanentBackoffMax, cloneBackoffMax)
	assert.Equal(t, 5*time.Minute, clonePermanentBackoffMax)
}

func TestSyncBackoff_LedgerFetchFailure(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmpDir := t.TempDir()
	ledgerDir := filepath.Join(tmpDir, "ledger")

	// init repo with bogus remote so fetch fails
	require.NoError(t, exec.Command("git", "init", ledgerDir).Run())
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "config", "user.email", "test@test.com").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "config", "user.name", "Test").Run())
	require.NoError(t, os.WriteFile(filepath.Join(ledgerDir, "README.md"), []byte("test"), 0644))
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "add", ".").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "commit", "-m", "initial").Run())
	require.NoError(t, exec.Command("git", "-C", ledgerDir, "remote", "add", "origin", "https://127.0.0.1:1/nonexistent.git").Run())

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	cfg.SyncIntervalRead = 1 * time.Second
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	ctx := context.Background()

	// first attempt: should fail (fetch fails on bogus remote)
	err := scheduler.doPull(ctx, nil, false, true)
	assert.Error(t, err, "first pull should fail")

	// verify backoff was recorded
	failures, nextRetry := scheduler.workspaceRegistry.GetSyncRetryInfo("ledger")
	assert.Equal(t, 1, failures)
	assert.False(t, nextRetry.IsZero(), "next retry should be set")
	assert.True(t, nextRetry.After(time.Now()), "next retry should be in the future")

	// second attempt: should be skipped due to backoff (no error, just skipped)
	err = scheduler.doPull(ctx, nil, false, true)
	assert.NoError(t, err, "second pull should be skipped by backoff (returns nil)")
}

func TestSyncBackoff_ClearsOnSuccess(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tmpDir := t.TempDir()
	ledgerDir := filepath.Join(tmpDir, "ledger")
	require.NoError(t, os.MkdirAll(ledgerDir, 0755))
	setupGitRepo(t, ledgerDir)

	cfg := DefaultConfig()
	cfg.LedgerPath = ledgerDir
	cfg.SyncIntervalRead = 1 * time.Second
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	// artificially set failure state
	scheduler.workspaceRegistry.RecordSyncFailure("ledger")
	failures, _ := scheduler.workspaceRegistry.GetSyncRetryInfo("ledger")
	assert.Equal(t, 1, failures)

	// use forceSync=true to bypass backoff (simulates on-demand sync clearing backoff)
	ctx := context.Background()
	err := scheduler.doPull(ctx, nil, true, true)
	assert.NoError(t, err)

	// verify doPull's success path cleared the failure state
	failures, _ = scheduler.workspaceRegistry.GetSyncRetryInfo("ledger")
	assert.Equal(t, 0, failures)
}

func TestExponentialBackoff(t *testing.T) {
	base := time.Minute
	maxBack := 30 * time.Minute

	tests := []struct {
		failures int
		expected time.Duration
	}{
		{0, 1 * time.Minute},
		{1, 1 * time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{4, 8 * time.Minute},
		{5, 16 * time.Minute},
		{6, 30 * time.Minute},   // 32min capped to 30
		{7, 30 * time.Minute},   // still capped
		{100, 30 * time.Minute}, // extreme value, still capped
	}
	for _, tt := range tests {
		got := exponentialBackoff(tt.failures, base, maxBack)
		assert.Equal(t, tt.expected, got, "failures=%d", tt.failures)
	}
}

func TestSyncBackoff_SeparateAPIAndGitKeys(t *testing.T) {
	// verify that API failures don't block git sync and vice versa
	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	// record API failure
	scheduler.workspaceRegistry.RecordSyncFailure("ledger-api")
	assert.False(t, scheduler.workspaceRegistry.ShouldSync("ledger-api"), "API should be in backoff")
	assert.True(t, scheduler.workspaceRegistry.ShouldSync("ledger"), "git sync should NOT be in backoff")

	// record git sync failure
	scheduler.workspaceRegistry.RecordSyncFailure("ledger")
	assert.False(t, scheduler.workspaceRegistry.ShouldSync("ledger"), "git sync should be in backoff")
	assert.False(t, scheduler.workspaceRegistry.ShouldSync("ledger-api"), "API should still be in backoff")

	// clear git sync — API should still be backed off
	scheduler.workspaceRegistry.ClearSyncFailures("ledger")
	assert.True(t, scheduler.workspaceRegistry.ShouldSync("ledger"), "git sync should be clear")
	assert.False(t, scheduler.workspaceRegistry.ShouldSync("ledger-api"), "API should still be in backoff")
}

func TestSyncBackoff_ForceBypassClearsState(t *testing.T) {
	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewSyncScheduler(cfg, logger)

	// put ledger into backoff
	scheduler.workspaceRegistry.RecordSyncFailure("ledger")
	scheduler.workspaceRegistry.RecordSyncFailure("ledger")
	assert.False(t, scheduler.workspaceRegistry.ShouldSync("ledger"))

	// forceSync=true should clear and proceed
	assert.True(t, scheduler.shouldSyncOrBypass("ledger", true))
	// state should be cleared
	failures, _ := scheduler.workspaceRegistry.GetSyncRetryInfo("ledger")
	assert.Equal(t, 0, failures)
}
