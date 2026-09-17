package session

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestWithPublicationLockRunsFnAndCreatesLockDir prevents a regression where
// the cache directory for publication locks is never created, which would
// make every real caller fail on a fresh ledger clone that has no
// .sageox/cache/session-publication-locks directory yet.
func TestWithPublicationLockRunsFnAndCreatesLockDir(t *testing.T) {
	ledger := t.TempDir()
	called := false
	err := WithPublicationLock(context.Background(), ledger, "session-a", func() error {
		called = true
		return nil
	})
	require.NoError(t, err)
	require.True(t, called, "fn must run when the lock is acquired")
	require.DirExists(t, filepath.Join(ledger, ".sageox", "cache", "session-publication-locks"))
}

// TestWithPublicationLockRejectsInvalidSessionName prevents a path-traversal
// or empty-name lock file from ever being created (e.g. "../../etc" or "").
func TestWithPublicationLockRejectsInvalidSessionName(t *testing.T) {
	ledger := t.TempDir()
	for _, name := range []string{"", ".", "..", "a/b", "a\\b"} {
		err := WithPublicationLock(context.Background(), ledger, name, func() error {
			t.Fatalf("fn must not run for invalid session name %q", name)
			return nil
		})
		require.Error(t, err, "name %q must be rejected", name)
	}
}

// TestWithPublicationLockPropagatesFnError ensures a failure inside the
// critical section is surfaced to the caller rather than swallowed by the
// locking wrapper.
func TestWithPublicationLockPropagatesFnError(t *testing.T) {
	ledger := t.TempDir()
	sentinel := os.ErrClosed
	err := WithPublicationLock(context.Background(), ledger, "session-a", func() error {
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)
}

// TestWithPublicationLockSerializesConcurrentCallers proves the lock actually
// excludes concurrent transcript-replacement + derived-artifact writers for
// the same session name — the whole point of this helper. Without real
// mutual exclusion, two publishers racing on the same session could
// interleave writes to a session's derived artifacts.
func TestWithPublicationLockSerializesConcurrentCallers(t *testing.T) {
	ledger := t.TempDir()
	var mu sync.Mutex
	active := 0
	maxActive := 0
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := WithPublicationLock(context.Background(), ledger, "shared-session", func() error {
				mu.Lock()
				active++
				if active > maxActive {
					maxActive = active
				}
				mu.Unlock()
				time.Sleep(10 * time.Millisecond)
				mu.Lock()
				active--
				mu.Unlock()
				return nil
			})
			require.NoError(t, err)
		}()
	}
	wg.Wait()
	require.Equal(t, 1, maxActive, "publication lock must serialize concurrent callers for the same session")
}

// TestWithPublicationLockAllowsDifferentSessionsConcurrently proves the lock
// is scoped per session name, not a single global lock that would needlessly
// serialize unrelated sessions' publications.
func TestWithPublicationLockAllowsDifferentSessionsConcurrently(t *testing.T) {
	ledger := t.TempDir()
	release := make(chan struct{})
	started := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := WithPublicationLock(context.Background(), ledger, "session-one", func() error {
			close(started)
			<-release
			return nil
		})
		require.NoError(t, err)
	}()

	<-started
	done := make(chan struct{})
	go func() {
		err := WithPublicationLock(context.Background(), ledger, "session-two", func() error {
			return nil
		})
		require.NoError(t, err)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("lock for a different session name must not block behind an unrelated session's lock")
	}
	close(release)
	wg.Wait()
}
