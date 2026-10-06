package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/storage/filesystem/dotgit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/index"
)

// useLedgerDataDir gives the manager a ledger index dir so a build gets past
// "no ledger index dir available" and reaches the git walk.
func useLedgerDataDir(t *testing.T, s *SyncScheduler) {
	t.Helper()
	s.codedb.ledgerDataDir = filepath.Join(t.TempDir(), "ledger")
}

// failIndexWith makes every ledger build fail inside the git object walk.
func failIndexWith(t *testing.T, s *SyncScheduler, err error) {
	t.Helper()
	useLedgerDataDir(t, s)
	s.codedb.ledgerIndexRepoFn = func(context.Context, *codedb.DB, string, index.IndexOptions) error {
		return fmt.Errorf("process default branch: %w", err)
	}
}

// TestLedgerIndexRebuild_RepoChangedFailureNotCharged reproduces #1155: a pull or
// repack replaces pack files mid-walk, the build fails with object/packfile not
// found, and the scheduler must treat that as "retry soon" instead of recording
// the failed fingerprint and charging the full cooldown.
// Failure prevented: a failed 20 minute build marks the ledger index up to date
// and the next attempt waits an hour, so the index stays stale for hours.
func TestLedgerIndexRebuild_RepoChangedFailureNotCharged(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{name: "object not found", err: plumbing.ErrObjectNotFound},
		{name: "packfile not found", err: dotgit.ErrPackfileNotFound},
		{name: "wrapped packfile not found", err: fmt.Errorf("diff tree: from: %w", dotgit.ErrPackfileNotFound)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, _, _ := newLedgerStormScheduler(t)
			failIndexWith(t, s, tt.err)

			s.triggerLedgerIndexRebuild(context.Background())
			waitLedgerBuildIdle(t, s)

			s.mu.Lock()
			defer s.mu.Unlock()
			assert.Empty(t, s.lastLedgerSha, "fingerprint must not advance after a repository-changed failure")
			assert.True(t, s.lastLedgerBuildDone.IsZero(), "cooldown must not be charged after a repository-changed failure")
		})
	}
}

// TestLedgerIndexRebuild_RealFailureStillChargesCooldown pins the #1144 behavior
// for failures that are not a repository-changed race.
func TestLedgerIndexRebuild_RealFailureStillChargesCooldown(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	t.Parallel()

	s, _, _ := newLedgerStormScheduler(t)
	failIndexWith(t, s, errors.New("disk full"))

	s.triggerLedgerIndexRebuild(context.Background())
	waitLedgerBuildIdle(t, s)

	s.mu.Lock()
	defer s.mu.Unlock()
	require.NotEmpty(t, s.lastLedgerSha)
	assert.False(t, s.lastLedgerBuildDone.IsZero())
}

// TestLedgerIndexRebuild_RetriesAreBoundedPerHour proves a ledger that keeps
// racing its pull cannot become a retry storm: the retry delay gates each attempt,
// at most LedgerIndexMaxRetriesPerHour retries run, and the next failure charges
// the full cooldown like any other.
// Failure prevented: a repository-changed retry loop burning a core indefinitely.
func TestLedgerIndexRebuild_RetriesAreBoundedPerHour(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	t.Parallel()

	s, _, builds := newLedgerStormScheduler(t)
	failIndexWith(t, s, plumbing.ErrObjectNotFound)
	ctx := context.Background()
	maxRetries := s.config.LedgerIndexMaxRetriesPerHour
	require.Positive(t, maxRetries)

	s.triggerLedgerIndexRebuild(ctx)
	waitLedgerBuildIdle(t, s)
	require.Equal(t, int32(1), builds.Load())

	// the retry delay holds an immediate re-trigger back
	s.triggerLedgerIndexRebuild(ctx)
	assert.Equal(t, int32(1), builds.Load(), "retry delay must gate the next attempt")

	// every later tick, with the delay elapsed, retries until the budget is spent
	for i := 0; i < maxRetries+3; i++ {
		s.mu.Lock()
		s.ledgerRetryNotBefore = time.Time{}
		s.mu.Unlock()
		s.triggerLedgerIndexRebuild(ctx)
		waitLedgerBuildIdle(t, s)
	}

	// first build + maxRetries retries; the failure after the budget charges the cooldown
	assert.Equal(t, int32(1+maxRetries), builds.Load())
	s.mu.Lock()
	defer s.mu.Unlock()
	assert.NotEmpty(t, s.lastLedgerSha, "exhausted budget falls back to the normal failure handling")
	assert.False(t, s.lastLedgerBuildDone.IsZero())
}

// TestLedgerIndexRebuild_YieldsToLedgerMutation proves the build never starts
// while the pull cycle, gc, or another ledger git writer is mid-flight, and that
// yielding charges nothing so the next tick builds as soon as the ledger is idle.
// Failure prevented: a walk that begins while pull --rebase or a gc swap replaces
// pack files, and dies with object/packfile not found after minutes of work.
func TestLedgerIndexRebuild_YieldsToLedgerMutation(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	t.Parallel()

	tests := []struct {
		name  string
		begin func(s *SyncScheduler) (end func())
	}{
		{
			name: "pull in progress",
			begin: func(s *SyncScheduler) func() {
				s.mu.Lock()
				s.pullInProgress = true
				s.mu.Unlock()
				return func() {
					s.mu.Lock()
					s.pullInProgress = false
					s.mu.Unlock()
				}
			},
		},
		{
			name: "gc in progress",
			begin: func(s *SyncScheduler) func() {
				atomic.StoreInt32(&s.gcInProgress, 1)
				return func() { atomic.StoreInt32(&s.gcInProgress, 0) }
			},
		},
		{
			name: "ledger git lock held",
			begin: func(s *SyncScheduler) func() {
				s.ledgerMu.Lock()
				return s.ledgerMu.Unlock
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, _, builds := newLedgerStormScheduler(t)
			ctx := context.Background()

			end := tt.begin(s)
			s.triggerLedgerIndexRebuild(ctx)
			assert.Equal(t, int32(0), builds.Load(), "build must not start during a ledger mutation")
			s.mu.Lock()
			assert.Empty(t, s.lastLedgerSha)
			assert.True(t, s.lastLedgerBuildDone.IsZero(), "yielding must not charge the cooldown")
			s.mu.Unlock()

			end()
			s.triggerLedgerIndexRebuild(ctx)
			waitLedgerBuildIdle(t, s)
			assert.Equal(t, int32(1), builds.Load(), "build runs on the next tick once the ledger is idle")
		})
	}
}

// TestLedgerIndexRebuild_YieldsToWorktreeIndex proves the ledger build does not
// start while a worktree index owns the codedb, which is the SQLITE_BUSY window.
// Failure prevented: the ledger build losing the sqlite write lock to a worktree
// index and failing immediately, then waiting out a full cooldown.
func TestLedgerIndexRebuild_YieldsToWorktreeIndex(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	t.Parallel()

	s, _, builds := newLedgerStormScheduler(t)
	ctx := context.Background()

	s.codedb.mu.Lock()
	s.codedb.indexing = true
	s.codedb.mu.Unlock()

	s.triggerLedgerIndexRebuild(ctx)
	waitLedgerBuildIdle(t, s)
	assert.Equal(t, int32(0), builds.Load())
	s.mu.Lock()
	assert.Empty(t, s.lastLedgerSha)
	assert.True(t, s.lastLedgerBuildDone.IsZero(), "yielding must not charge the cooldown")
	s.mu.Unlock()

	s.codedb.mu.Lock()
	s.codedb.indexing = false
	s.codedb.mu.Unlock()

	s.triggerLedgerIndexRebuild(ctx)
	waitLedgerBuildIdle(t, s)
	assert.Equal(t, int32(1), builds.Load())
}

// TestBuildLedgerIndex_ClassifiesOutcome pins the contract the scheduler relies on.
func TestBuildLedgerIndex_ClassifiesOutcome(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	t.Parallel()

	tests := []struct {
		name        string
		indexErr    error
		wantChanged bool
		wantErr     bool
	}{
		{name: "object not found", indexErr: plumbing.ErrObjectNotFound, wantChanged: true, wantErr: true},
		{name: "packfile not found", indexErr: dotgit.ErrPackfileNotFound, wantChanged: true, wantErr: true},
		{name: "real failure", indexErr: errors.New("disk full"), wantErr: true},
		{name: "success", indexErr: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, ledgerDir, _ := newLedgerStormScheduler(t)
			useLedgerDataDir(t, s)
			s.codedb.ledgerIndexRepoFn = func(context.Context, *codedb.DB, string, index.IndexOptions) error {
				return tt.indexErr
			}

			err := s.codedb.BuildLedgerIndex(context.Background(), ledgerDir)
			assert.Equal(t, tt.wantErr, err != nil)
			assert.Equal(t, tt.wantChanged, errors.Is(err, ErrLedgerRepoChanged))
			assert.False(t, errors.Is(err, ErrLedgerBuildYielded))
		})
	}
}
