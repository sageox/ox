package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wedgedLedger returns a clone whose remote declines every push the way GitLab
// declines a pack with missing LFS objects, with the process-wide push breaker
// already tripped for it (by a real PushWithRetry whose repair changed nothing).
// hits counts the pushes that actually reached the remote.
func wedgedLedger(t *testing.T) (ledger string, hits func() int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("pre-receive hook fixture needs a POSIX shell")
	}
	base := t.TempDir()
	bare := filepath.Join(base, "remote.git")
	ledger = filepath.Join(base, "ledger")
	wedgeGit(t, base, "init", "--quiet", "--bare", bare)
	wedgeGit(t, base, "clone", "--quiet", "file://"+bare, ledger)
	wedgeGit(t, ledger, "config", "user.email", "test@test.local")
	wedgeGit(t, ledger, "config", "user.name", "Test")
	wedgeCommitFile(t, ledger, "init.txt", "init", "init")
	wedgeGit(t, ledger, "push", "--quiet")

	hitLog := filepath.Join(base, "hits")
	hook := "#!/bin/sh\necho hit >> '" + hitLog + "'\n" +
		"echo 'remote: GitLab: LFS objects are missing. Ensure LFS is properly set up or try a manual \"git lfs push --all\".' >&2\nexit 1\n"
	require.NoError(t, os.MkdirAll(filepath.Join(bare, "hooks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bare, "hooks", "pre-receive"), []byte(hook), 0o755))
	hits = func() int {
		data, err := os.ReadFile(hitLog)
		if os.IsNotExist(err) {
			return 0
		}
		require.NoError(t, err)
		return strings.Count(string(data), "hit")
	}

	wedgeCommitFile(t, ledger, "murmur.txt", "m", "murmur: wedge fixture")
	err := gitutil.PushWithRetry(context.Background(), ledger, gitutil.PushOpts{
		MaxRetries:        1,
		OpTimeout:         20 * time.Second,
		Logger:            slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		SuspendWhenWedged: true, // the daemon's pushers all set this
		ReconcileLFS:      func(string) (bool, error) { return false, nil },
	})
	require.ErrorIs(t, err, gitutil.ErrPushWedged, "fixture must trip the shared breaker")
	_, wedged := gitutil.PushWedgedUntil(ledger)
	require.True(t, wedged)
	require.Equal(t, 1, hits())
	return ledger, hits
}

func wedgeGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

func wedgeCommitFile(t *testing.T, repo, name, content, msg string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644))
	wedgeGit(t, repo, "add", name)
	wedgeGit(t, repo, "commit", "--quiet", "--no-verify", "-m", msg)
}

// Failure prevented: the murmur and session-draft pushers kept pushing a wedged
// ledger every sync tick (~60 s), each time paying a rejected push and logging a
// Warn. They commit locally already, so skipping loses nothing.
//
// Observable differences: the remote's push counter and the warning count. The
// healthy-ledger control proves the same call path still pushes and still warns
// about an ordinary failure.
func TestPushers_SkipWedgedLedgerQuietly(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git remote with a pre-receive hook")
	}
	tests := []struct {
		name        string
		subject     string
		push        func(s *SyncScheduler, ctx context.Context, ledger string)
		failMessage string
	}{
		{
			name:        "murmur pusher",
			subject:     "murmur: x",
			push:        func(s *SyncScheduler, ctx context.Context, ledger string) { s.pushMurmurCommits(ctx, ledger) },
			failMessage: "murmur push failed",
		},
		{
			name:        "session draft pusher",
			subject:     "session-draft: x",
			push:        func(s *SyncScheduler, ctx context.Context, ledger string) { s.pushSessionDraftCommits(ctx, ledger) },
			failMessage: "session-draft push failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logBuf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			t.Run("wedged ledger: no git, no warning", func(t *testing.T) {
				ledger, hits := wedgedLedger(t)
				mock := &mockGitRunner{output: tt.subject}
				cfg := DefaultConfig()
				cfg.ProjectRoot, cfg.LedgerPath = ledger, ledger
				s := NewSyncScheduler(cfg, logger, WithGitRunner(mock))

				for i := 0; i < 5; i++ {
					tt.push(s, ctx, ledger)
				}

				assert.Zero(t, mock.calls.Load(), "a wedged ledger must not even be inspected")
				assert.Equal(t, 1, hits(), "no push may reach the remote while wedged")
				assert.NotContains(t, logBuf.String(), tt.failMessage)
			})

			t.Run("control: healthy ledger still reaches the push and warns on failure", func(t *testing.T) {
				healthy := t.TempDir() // not a repo: the push fails for an ordinary reason
				mock := &mockGitRunner{output: tt.subject}
				cfg := DefaultConfig()
				cfg.ProjectRoot, cfg.LedgerPath = healthy, healthy
				s := NewSyncScheduler(cfg, logger, WithGitRunner(mock))

				tt.push(s, ctx, healthy)

				assert.Equal(t, int64(1), mock.calls.Load())
				assert.Contains(t, logBuf.String(), tt.failMessage, "an ordinary push failure keeps its warning")
			})
		})
	}
}

// Failure prevented: GitHub sync counted a wedged ledger as a GitHub failure,
// and once failCount passed 5 it suspended itself permanently — long after the
// ledger recovered. The data is committed locally either way.
func TestGitHubSync_WedgedLedgerIsNotAGitHubFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		pushErr      error
		wantFailures int
		wantLastErr  bool
	}{
		{name: "wedged ledger", pushErr: fmt.Errorf("git push failed (not retryable): rejected: %w", gitutil.ErrPushWedged)},
		{name: "ordinary push failure still counts", pushErr: errors.New("git push failed: network down"), wantFailures: 1, wantLastErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logBuf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
			m := NewGitHubSyncManager(t.TempDir(), nil, logger)

			for i := 0; i < 10; i++ {
				m.handlePushError(tt.pushErr)
			}

			m.mu.Lock()
			defer m.mu.Unlock()
			if tt.wantFailures == 0 {
				assert.Zero(t, m.failCount, "ten wedged pushes must not suspend GitHub sync")
				assert.NoError(t, m.lastErr)
				assert.NotContains(t, logBuf.String(), "github sync failed")
				return
			}
			assert.Equal(t, 10, m.failCount)
			assert.Error(t, m.lastErr)
		})
	}
}

// Failure prevented: "sync in backoff, skipping" logged at Warn on every
// scheduler tick (~777 a day). It should announce entering backoff, announce
// again when another failure extends it, and otherwise stay quiet — and a
// workspace that recovered and later backs off again must announce itself anew.
func TestShouldSyncOrBypass_WarnsOnEnteringBackoffNotEveryTick(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	s := NewSyncScheduler(DefaultConfig(), logger)
	warnings := func() int { return strings.Count(logBuf.String(), "sync in backoff, skipping") }
	tick := func(n int) {
		for i := 0; i < n; i++ {
			require.False(t, s.shouldSyncOrBypass("ledger", false), "in backoff the tick must skip")
		}
	}

	s.workspaceRegistry.RecordSyncFailure("ledger")
	tick(25)
	assert.Equal(t, 1, warnings(), "entering backoff warns once, not per tick")

	s.workspaceRegistry.RecordSyncFailure("ledger")
	tick(25)
	assert.Equal(t, 2, warnings(), "another failure changes the picture and warns again")

	// recovery, then a fresh backoff
	s.workspaceRegistry.ClearSyncFailures("ledger")
	require.True(t, s.shouldSyncOrBypass("ledger", false))
	s.workspaceRegistry.RecordSyncFailure("ledger")
	tick(25)
	assert.Equal(t, 3, warnings(), "a new backoff after recovery announces itself again")

	// an explicit user sync clears it, so the next backoff is new too
	require.True(t, s.shouldSyncOrBypass("ledger", true))
	s.workspaceRegistry.RecordSyncFailure("ledger")
	tick(5)
	assert.Equal(t, 4, warnings())
}
