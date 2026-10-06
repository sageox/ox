package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// holdLedgerLikeAPull plays the daemon's pull-rebase: it takes the repo lock
// and, while holding it, leaves git's own index.lock in the clone. It returns
// once the holder is inside the critical section; the channel yields the
// instant the holder released both.
func holdLedgerLikeAPull(t *testing.T, ledgerPath string, hold time.Duration) <-chan time.Time {
	t.Helper()
	inside := make(chan struct{})
	released := make(chan time.Time, 1)
	indexLock := filepath.Join(ledgerPath, ".git", "index.lock")
	go func() {
		_ = gitutil.WithRepoLock(t.Context(), ledgerPath, func() error {
			_ = os.WriteFile(indexLock, nil, 0o644)
			close(inside)
			time.Sleep(hold)
			_ = os.Remove(indexLock)
			return nil
		})
		released <- time.Now()
	}()
	<-inside
	return released
}

// A draft commit that starts while the daemon's pull holds the ledger must wait
// for it and then commit, instead of colliding on .git/index.lock.
//
// Failure prevented (#1190): a hook-spawned draft commit met the pull-rebase's
// index.lock, which pushed the pull into rebase --abort and a stale wedge.
func TestCommitDraftLocally_WaitsForRepoLock(t *testing.T) {
	const session = "2026-01-01T00-00-testuser-OxLockWait"
	f := newDraftLedgerFixture(t)
	f.writeDraft(t, session, 1)

	released := holdLedgerLikeAPull(t, f.ledgerPath, 400*time.Millisecond)
	err := commitDraftLocally(f.ledgerPath, session)
	committedAt := time.Now()

	require.NoError(t, err, "draft commit must wait out the lock holder, not hit index.lock")
	assert.False(t, committedAt.Before(<-released), "commit must land after the holder released the lock")
	assert.Contains(t, runGit(t, f.ledgerPath, "log", "-1", "--format=%s"), "session-draft: "+session)
}
