package daemon

import (
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// newDiscoveryTestScheduler creates a SyncScheduler for credential refresh testing.
func newDiscoveryTestScheduler(t *testing.T) *SyncScheduler {
	t.Helper()
	cfg := DefaultConfig()
	cfg.ProjectRoot = t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := NewSyncScheduler(cfg, logger)
	s.issues = NewIssueTracker()
	return s
}

func TestRefreshCredentials_DedupWithinWindow(t *testing.T) {
	s := newDiscoveryTestScheduler(t)

	// first call sets the timestamp
	s.refreshCredentialsIfNeeded()

	s.mu.Lock()
	firstStamp := s.lastCredentialRefresh
	s.mu.Unlock()
	assert.False(t, firstStamp.IsZero(), "first call should set lastCredentialRefresh")

	// second call within 5min window should be a no-op (dedup)
	s.refreshCredentialsIfNeeded()

	s.mu.Lock()
	secondStamp := s.lastCredentialRefresh
	s.mu.Unlock()
	assert.Equal(t, firstStamp, secondStamp,
		"second call within dedup window should not update timestamp")
}

func TestRefreshCredentials_AllowsAfterWindow(t *testing.T) {
	s := newDiscoveryTestScheduler(t)

	// set timestamp to 6 minutes ago (beyond 5min dedup window)
	s.mu.Lock()
	s.lastCredentialRefresh = time.Now().Add(-6 * time.Minute)
	oldStamp := s.lastCredentialRefresh
	s.mu.Unlock()

	// call should proceed past the dedup check and update the timestamp
	s.refreshCredentialsIfNeeded()

	s.mu.Lock()
	newStamp := s.lastCredentialRefresh
	s.mu.Unlock()
	assert.True(t, newStamp.After(oldStamp),
		"call after dedup window should update timestamp")
}

func TestRefreshCredentials_ConcurrentCalls(t *testing.T) {
	s := newDiscoveryTestScheduler(t)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.refreshCredentialsIfNeeded()
		}()
	}
	wg.Wait()

	// verify no race: timestamp should be set exactly once
	s.mu.Lock()
	stamp := s.lastCredentialRefresh
	s.mu.Unlock()
	assert.False(t, stamp.IsZero(), "timestamp should be set after concurrent calls")
}
