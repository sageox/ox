package gitutil

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePushClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakePushClock() *fakePushClock {
	return &fakePushClock{now: time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)}
}

func (c *fakePushClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakePushClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Failure prevented: a wedge that never backs off hammers the remote and the
// repair every tick; one that never caps strands a ledger a human already fixed.
func TestPushBreaker_BackoffDoublesCapsAndClears(t *testing.T) {
	t.Parallel()

	clock := newFakePushClock()
	b := newPushBreaker(clock.Now)
	repo := filepath.Join(t.TempDir(), "ledger")

	_, wedged := b.blockedUntil(repo)
	require.False(t, wedged, "a fresh repo is never wedged")

	wantBackoffs := []time.Duration{
		5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute,
		time.Hour, time.Hour, time.Hour,
	}
	for i, want := range wantBackoffs {
		until := b.trip(repo)
		assert.Equal(t, clock.Now().Add(want), until, "trip %d", i+1)

		got, wedged := b.blockedUntil(repo)
		assert.True(t, wedged, "trip %d: open immediately", i+1)
		assert.Equal(t, until, got)

		clock.Advance(want - time.Second)
		_, wedged = b.blockedUntil(repo)
		assert.True(t, wedged, "trip %d: still open one second before the window ends", i+1)

		clock.Advance(time.Second)
		_, wedged = b.blockedUntil(repo)
		assert.False(t, wedged, "trip %d: the probe push is allowed once the window ends", i+1)
	}

	b.clear(repo)
	assert.Equal(t, clock.Now().Add(5*time.Minute), b.trip(repo), "a successful push restarts the backoff at the base")
}

func TestPushBreaker_KeyIsTheCleanedAbsolutePath(t *testing.T) {
	t.Parallel()

	clock := newFakePushClock()
	b := newPushBreaker(clock.Now)
	dir := t.TempDir()
	b.trip(dir)

	for _, alias := range []string{dir, dir + string(filepath.Separator), filepath.Join(dir, "sub", "..")} {
		_, wedged := b.blockedUntil(alias)
		assert.True(t, wedged, "%q names the same repo", alias)
	}
	_, wedged := b.blockedUntil(filepath.Join(dir, "other"))
	assert.False(t, wedged, "a different repo is independent")
}

// rejectingRemote is a bare remote whose pre-receive hook behaves like GitLab's
// LFS check while rejectFlag exists, and counts every push that reaches it.
type rejectingRemote struct {
	repo       string
	rejectFlag string
	hitLog     string
}

func newRejectingRemote(t *testing.T) *rejectingRemote {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("pre-receive hook fixture needs a POSIX shell")
	}
	repo, bare := initBareRemoteRepo(t)
	r := &rejectingRemote{
		repo:       repo,
		rejectFlag: filepath.Join(t.TempDir(), "reject"),
		hitLog:     filepath.Join(t.TempDir(), "hits"),
	}
	hook := "#!/bin/sh\necho hit >> '" + r.hitLog + "'\n" +
		"if [ -f '" + r.rejectFlag + "' ]; then\n" +
		"  echo 'remote: GitLab: LFS objects are missing. Ensure LFS is properly set up or try a manual \"git lfs push --all\".' >&2\n" +
		"  exit 1\nfi\nexit 0\n"
	require.NoError(t, os.MkdirAll(filepath.Join(bare, "hooks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bare, "hooks", "pre-receive"), []byte(hook), 0o755))
	r.reject(t, true)
	addCommit(t, repo, "a.txt", "hello", "add a")
	return r
}

func (r *rejectingRemote) reject(t *testing.T, on bool) {
	t.Helper()
	if on {
		require.NoError(t, os.WriteFile(r.rejectFlag, nil, 0o644))
		return
	}
	require.NoError(t, os.Remove(r.rejectFlag))
}

// pushesSeen is how many pushes actually reached the remote.
func (r *rejectingRemote) pushesSeen(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(r.hitLog)
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(t, err)
	return strings.Count(string(data), "hit")
}

// Failure prevented: ~250 LFS repairs a day (each ~50 s under the ledger lock)
// because nothing remembered that the last repair had already failed. The
// observable difference is the remote's own push counter: a second pusher inside
// the backoff never reaches the remote and never reruns the repair.
func TestPushWithRetry_WedgeBreakerStopsRepeatRejectionsAndRepair(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git remote with a pre-receive hook")
	}

	tests := []struct {
		name          string
		reconcile     func(calls *int) func(string) (bool, error)
		wantWedged    bool
		wantReconcile int // calls during the FIRST push
	}{
		{
			name: "repair errors",
			reconcile: func(calls *int) func(string) (bool, error) {
				return func(string) (bool, error) {
					*calls++
					return false, errors.New("prepare missing artifact metadata: disagrees")
				}
			},
			wantWedged:    true,
			wantReconcile: 1,
		},
		{
			name: "repair finds nothing to change",
			reconcile: func(calls *int) func(string) (bool, error) {
				return func(string) (bool, error) { *calls++; return false, nil }
			},
			wantWedged:    true,
			wantReconcile: 1,
		},
		{
			// nothing was tried, so nothing is remembered: another pusher that
			// does carry a repair must still get its chance
			name:          "no repair available",
			reconcile:     func(*int) func(string) (bool, error) { return nil },
			wantWedged:    false,
			wantReconcile: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remote := newRejectingRemote(t)
			clock := newFakePushClock()
			breaker := newPushBreaker(clock.Now)
			var logBuf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
			var reconcileCalls, prePushCalls int
			opts := PushOpts{
				MaxRetries:   3,
				OpTimeout:    20 * time.Second,
				Logger:       logger,
				breaker:      breaker,
				ReconcileLFS: tt.reconcile(&reconcileCalls),
				PrePush:      func(string) error { prePushCalls++; return nil },
			}

			err := PushWithRetry(context.Background(), remote.repo, opts)
			require.Error(t, err)
			assert.Equal(t, tt.wantWedged, errors.Is(err, ErrPushWedged), "first push: %v", err)
			assert.Contains(t, err.Error(), "LFS objects are missing", "the caller still sees the real rejection")
			assert.Equal(t, tt.wantReconcile, reconcileCalls)
			assert.Equal(t, 1, remote.pushesSeen(t))
			_, open := breaker.blockedUntil(remote.repo)
			assert.Equal(t, tt.wantWedged, open)

			err = PushWithRetry(context.Background(), remote.repo, opts)
			require.Error(t, err)
			if !tt.wantWedged {
				assert.Equal(t, 2, remote.pushesSeen(t), "nothing remembered: the second pusher reaches the remote")
				return
			}
			assert.ErrorIs(t, err, ErrPushWedged)
			assert.Equal(t, 1, remote.pushesSeen(t), "second push inside the backoff must not run git")
			assert.Equal(t, tt.wantReconcile, reconcileCalls, "and must not rerun the repair")
			assert.Equal(t, 1, prePushCalls, "and must not do pre-push setup such as credential refresh")
			assert.Equal(t, 1, strings.Count(logBuf.String(), "ledger push wedged: LFS objects missing"),
				"exactly one warning when the breaker opens, nothing per skipped push")
			assert.Contains(t, logBuf.String(), "until=")
		})
	}
}

// Probe, re-trip, and recovery against one remote: the backoff doubles when the
// probe fails and a successful push clears the wedge entirely.
func TestPushWithRetry_WedgeBreakerProbesDoublesAndRecovers(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git remote with a pre-receive hook")
	}
	remote := newRejectingRemote(t)
	clock := newFakePushClock()
	breaker := newPushBreaker(clock.Now)
	opts := PushOpts{
		MaxRetries:   3,
		OpTimeout:    20 * time.Second,
		Logger:       slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		breaker:      breaker,
		ReconcileLFS: func(string) (bool, error) { return false, nil },
	}

	require.ErrorIs(t, PushWithRetry(context.Background(), remote.repo, opts), ErrPushWedged)
	first, _ := breaker.blockedUntil(remote.repo)
	assert.Equal(t, clock.Now().Add(5*time.Minute), first)

	clock.Advance(5 * time.Minute)
	require.ErrorIs(t, PushWithRetry(context.Background(), remote.repo, opts), ErrPushWedged)
	assert.Equal(t, 2, remote.pushesSeen(t), "the probe after the window really hits the remote")
	second, _ := breaker.blockedUntil(remote.repo)
	assert.Equal(t, clock.Now().Add(10*time.Minute), second, "a failed probe doubles the backoff")

	clock.Advance(10 * time.Minute)
	remote.reject(t, false)
	require.NoError(t, PushWithRetry(context.Background(), remote.repo, opts))
	_, open := breaker.blockedUntil(remote.repo)
	assert.False(t, open, "a successful push clears the wedge")

	// and the next wedge starts over at the base, not at the doubled value
	addCommit(t, remote.repo, "b.txt", "more", "add b")
	remote.reject(t, true)
	require.ErrorIs(t, PushWithRetry(context.Background(), remote.repo, opts), ErrPushWedged)
	third, _ := breaker.blockedUntil(remote.repo)
	assert.Equal(t, clock.Now().Add(5*time.Minute), third)
}

// Failure prevented: PushWithRetry returned nil — success — when its last
// attempt was spent on a rejected push that the repair reported fixing. Callers
// prune the only copy of a session's source content on success.
func TestPushWithRetry_ExhaustedAfterRepairIsAFailureNotASuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git remote with a pre-receive hook")
	}
	remote := newRejectingRemote(t)
	breaker := newPushBreaker(newFakePushClock().Now)
	err := PushWithRetry(context.Background(), remote.repo, PushOpts{
		MaxRetries:   1, // the retry the repair earned never runs
		OpTimeout:    20 * time.Second,
		Logger:       slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		breaker:      breaker,
		ReconcileLFS: func(string) (bool, error) { return true, nil },
	})

	require.Error(t, err, "a push that never succeeded must not report success")
	assert.ErrorIs(t, err, ErrPushWedged)
	assert.Equal(t, 1, remote.pushesSeen(t))
}

// The process-wide breaker is what every pusher shares, and PushWedgedUntil is
// the read-only accessor status surfaces and finalize use.
func TestPushWedgedUntil_ReportsTheSharedBreaker(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git remote with a pre-receive hook")
	}
	remote := newRejectingRemote(t)
	t.Cleanup(func() { defaultPushBreaker.clear(remote.repo) })

	_, wedged := PushWedgedUntil(remote.repo)
	require.False(t, wedged)

	err := PushWithRetry(context.Background(), remote.repo, PushOpts{
		MaxRetries:        1,
		OpTimeout:         20 * time.Second,
		Logger:            slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		SuspendWhenWedged: true,
		ReconcileLFS:      func(string) (bool, error) { return false, nil },
	})
	require.ErrorIs(t, err, ErrPushWedged)

	until, wedged := PushWedgedUntil(remote.repo)
	assert.True(t, wedged)
	assert.WithinDuration(t, time.Now().Add(5*time.Minute), until, 10*time.Second)
}

// Failure prevented: the breaker is in-memory and process-wide, so a user-
// initiated caller that fixes the problem and retries in the same process (the
// CLI's session stop and doctor retries) was refused with "ledger push wedged"
// without ever reaching the remote. Only long-lived callers that opt in via
// SuspendWhenWedged get the breaker; for everyone else a retry always pushes.
//
// Observable difference: the remote's push counter and the shared breaker state.
func TestPushWithRetry_WithoutOptInNeverSuspends(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git remote with a pre-receive hook")
	}
	remote := newRejectingRemote(t)
	t.Cleanup(func() { defaultPushBreaker.clear(remote.repo) })
	opts := PushOpts{
		MaxRetries:   1,
		OpTimeout:    20 * time.Second,
		Logger:       slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		ReconcileLFS: func(string) (bool, error) { return false, nil },
	}

	for attempt := 1; attempt <= 3; attempt++ {
		err := PushWithRetry(context.Background(), remote.repo, opts)
		require.Error(t, err)
		assert.False(t, errors.Is(err, ErrPushWedged), "attempt %d must report the real rejection", attempt)
		assert.Contains(t, err.Error(), "LFS objects are missing")
		assert.Equal(t, attempt, remote.pushesSeen(t), "every attempt reaches the remote")
	}
	_, wedged := PushWedgedUntil(remote.repo)
	assert.False(t, wedged, "a caller that did not opt in leaves no breaker state behind")

	// the user fixes the remote and retries in the same process: it must go through
	remote.reject(t, false)
	require.NoError(t, PushWithRetry(context.Background(), remote.repo, opts))
}
