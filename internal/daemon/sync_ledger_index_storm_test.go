package daemon

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newLedgerStormScheduler wires a scheduler to a real ledger git repo and counts
// full index builds through the ledger test hook.
func newLedgerStormScheduler(t *testing.T) (*SyncScheduler, string, *atomic.Int32) {
	t.Helper()

	ledgerDir := t.TempDir()
	initLedgerGitRepo(t, ledgerDir)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := DefaultConfig()
	cfg.ProjectRoot = ledgerDir
	s := NewSyncScheduler(cfg, logger)

	builds := &atomic.Int32{}
	s.codedb = NewCodeDBManager(ledgerDir, logger, nil)
	s.codedb.ledgerTestHook = func() { builds.Add(1) }
	s.workspaceRegistry.ledger = &WorkspaceState{ID: "ledger", Type: WorkspaceTypeLedger, Path: ledgerDir, Exists: true}
	return s, ledgerDir, builds
}

// waitLedgerBuildIdle blocks until no ledger build goroutine is in flight.
func waitLedgerBuildIdle(t *testing.T, s *SyncScheduler) {
	t.Helper()
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.ledgerBuildRunning
	}, 5*time.Second, 5*time.Millisecond)
}

// TestLedgerIndexRebuild_StormCoalesced reproduces the production storm: every
// ledger or team-context commit used to force a full rebuild on the next tick.
// Failure prevented: daemon pinned at 50-75% CPU rebuilding a 39k-commit index
// dozens of times a day.
func TestLedgerIndexRebuild_StormCoalesced(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	t.Parallel()

	tests := []struct {
		name         string
		ledgerCommit int
		teamCommits  int
		wantBuilds   int32
	}{
		{name: "no changes after first build", wantBuilds: 1},
		{name: "five ledger commits inside the cooldown", ledgerCommit: 5, wantBuilds: 1},
		{name: "team context commits never rebuild the ledger index", teamCommits: 5, wantBuilds: 1},
		{name: "mixed commits inside the cooldown", ledgerCommit: 3, teamCommits: 3, wantBuilds: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, ledgerDir, builds := newLedgerStormScheduler(t)
			teamDir := t.TempDir()
			initLedgerGitRepo(t, teamDir)
			s.workspaceRegistry.workspaces["team-a"] = &WorkspaceState{ID: "team-a", Type: WorkspaceTypeTeamContext, Path: teamDir, Exists: true}
			ctx := context.Background()

			s.triggerLedgerIndexRebuild(ctx)
			waitLedgerBuildIdle(t, s)

			for i := 0; i < tt.ledgerCommit; i++ {
				addCommit(t, ledgerDir, "ledger churn")
				s.triggerLedgerIndexRebuild(ctx)
				waitLedgerBuildIdle(t, s)
			}
			for i := 0; i < tt.teamCommits; i++ {
				addCommit(t, teamDir, "team churn")
				s.triggerLedgerIndexRebuild(ctx)
				waitLedgerBuildIdle(t, s)
			}

			assert.Equal(t, tt.wantBuilds, builds.Load())
		})
	}
}

// TestLedgerIndexRebuild_SingleFlight verifies a trigger that arrives while a
// build is running never starts a second build, and that exactly one follow-up
// build runs (with the latest fingerprint) once the cooldown has elapsed.
// Failure prevented: overlapping builds piling up and multiplying memory use.
func TestLedgerIndexRebuild_SingleFlight(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	t.Parallel()

	s, ledgerDir, builds := newLedgerStormScheduler(t)
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	s.codedb.ledgerTestHook = func() {
		builds.Add(1)
		started <- struct{}{}
		<-release
	}
	ctx := context.Background()

	s.triggerLedgerIndexRebuild(ctx)
	<-started

	// three newer fingerprints arrive mid-build
	for i := 0; i < 3; i++ {
		addCommit(t, ledgerDir, "during build")
		s.triggerLedgerIndexRebuild(ctx)
	}
	assert.Equal(t, int32(1), builds.Load(), "no concurrent build while one is running")

	release <- struct{}{}
	waitLedgerBuildIdle(t, s)

	// cooldown still active: nothing new starts
	s.triggerLedgerIndexRebuild(ctx)
	assert.Equal(t, int32(1), builds.Load(), "cooldown defers the follow-up")

	// cooldown elapsed: exactly one follow-up, repeated ticks do not add more
	s.mu.Lock()
	s.lastLedgerBuildDone = time.Now().Add(-2 * s.config.LedgerIndexMinInterval)
	s.mu.Unlock()
	s.triggerLedgerIndexRebuild(ctx)
	<-started
	s.triggerLedgerIndexRebuild(ctx)
	assert.Equal(t, int32(2), builds.Load())
	release <- struct{}{}
	waitLedgerBuildIdle(t, s)

	s.triggerLedgerIndexRebuild(ctx)
	assert.Equal(t, int32(2), builds.Load(), "up to date: no further build")
}
