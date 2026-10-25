package daemon

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initLedgerGitRepo creates a git repo with an initial commit and returns the HEAD sha.
func initLedgerGitRepo(t *testing.T, dir string) string {
	t.Helper()
	require.NoError(t, exec.Command("git", "init", "-b", "main", dir).Run())
	require.NoError(t, exec.Command("git", "-C", dir, "config", "user.email", "test@test.com").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "config", "user.name", "Test").Run())
	require.NoError(t, exec.Command("git", "-C", dir, "commit", "--allow-empty", "-m", "init").Run())
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	return string(out[:len(out)-1]) // trim newline
}

// addCommit adds an empty commit and returns the new HEAD sha.
func addCommit(t *testing.T, dir, msg string) string {
	t.Helper()
	require.NoError(t, exec.Command("git", "-C", dir, "commit", "--allow-empty", "-m", msg).Run())
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	return string(out[:len(out)-1])
}

// --- D. Sync scheduler ledger index trigger tests ---

// TestTriggerLedgerIndexRebuild_ShaChanged verifies that when the ledger HEAD changes,
// ledger index rebuild fires.
// Failure prevented: ledger updates don't propagate to ledger index.
func TestTriggerLedgerIndexRebuild_ShaChanged(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	t.Parallel()

	ledgerDir := t.TempDir()
	initLedgerGitRepo(t, ledgerDir)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := DefaultConfig()
	cfg.ProjectRoot = ledgerDir
	s := NewSyncScheduler(cfg, logger)

	// wire up codedb manager and workspace registry with ledger
	cdbMgr := NewCodeDBManager(ledgerDir, logger, nil)
	s.codedb = cdbMgr

	ledgerIndexCalls := make(chan string, 10)
	cdbMgr.ledgerTestHook = func() {
		ledgerIndexCalls <- "called"
	}

	s.workspaceRegistry.ledger = &WorkspaceState{
		ID:     "ledger",
		Type:   WorkspaceTypeLedger,
		Path:   ledgerDir,
		Exists: true,
	}

	ctx := context.Background()

	// first call — initial sha, should trigger rebuild
	s.triggerLedgerIndexRebuild(ctx)
	select {
	case <-ledgerIndexCalls:
	case <-time.After(5 * time.Second):
		t.Fatal("ledger index build was not triggered")
	}
	// lastLedgerSha is set asynchronously after BuildLedgerIndex returns
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.lastLedgerSha != ""
	}, 5*time.Second, 10*time.Millisecond, "lastLedgerSha should be set after first trigger")

	s.mu.Lock()
	firstSha := s.lastLedgerSha
	s.mu.Unlock()

	// add a new commit — sha changes; pretend the cooldown after the first build elapsed
	addCommit(t, ledgerDir, "second commit")
	s.mu.Lock()
	s.lastLedgerBuildDone = time.Now().Add(-2 * s.config.LedgerIndexMinInterval)
	s.mu.Unlock()

	// second call — different sha, should trigger rebuild
	s.triggerLedgerIndexRebuild(ctx)
	select {
	case <-ledgerIndexCalls:
	case <-time.After(5 * time.Second):
		t.Fatal("second ledger index build was not triggered")
	}
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.lastLedgerSha != firstSha
	}, 5*time.Second, 10*time.Millisecond, "lastLedgerSha should update when HEAD changes")
}

// TestTriggerLedgerIndexRebuild_SameSha_NoRebuild verifies that identical SHA doesn't
// trigger redundant rebuilds.
// Failure prevented: quiet repos burning CPU on redundant ledger index rebuilds every tick.
func TestTriggerLedgerIndexRebuild_SameSha_NoRebuild(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	t.Parallel()

	ledgerDir := t.TempDir()
	initLedgerGitRepo(t, ledgerDir)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := DefaultConfig()
	cfg.ProjectRoot = ledgerDir
	s := NewSyncScheduler(cfg, logger)

	cdbMgr := NewCodeDBManager(ledgerDir, logger, nil)
	s.codedb = cdbMgr

	var buildCount int
	cdbMgr.ledgerTestHook = func() {
		buildCount++
	}

	s.workspaceRegistry.ledger = &WorkspaceState{
		ID:     "ledger",
		Type:   WorkspaceTypeLedger,
		Path:   ledgerDir,
		Exists: true,
	}

	ctx := context.Background()

	// first call — triggers build (initial sha)
	s.triggerLedgerIndexRebuild(ctx)
	// wait for async sha update
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.lastLedgerSha != ""
	}, 5*time.Second, 10*time.Millisecond)

	s.mu.Lock()
	firstSha := s.lastLedgerSha
	s.mu.Unlock()

	// second call — same sha, should NOT trigger
	s.triggerLedgerIndexRebuild(ctx)
	// give a moment for any (unexpected) goroutine to fire
	time.Sleep(50 * time.Millisecond)
	s.mu.Lock()
	assert.Equal(t, firstSha, s.lastLedgerSha, "sha should not change without new commits")
	s.mu.Unlock()
}

// TestTriggerLedgerIndexRebuild_NoLedger_Noop verifies that when no ledger is registered,
// triggerLedgerIndexRebuild is a safe no-op.
// Failure prevented: crash on fresh install before first sync.
func TestTriggerLedgerIndexRebuild_NoLedger_Noop(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := DefaultConfig()
	s := NewSyncScheduler(cfg, logger)

	cdbMgr := NewCodeDBManager(t.TempDir(), logger, nil)
	s.codedb = cdbMgr

	hookCalled := false
	cdbMgr.ledgerTestHook = func() {
		hookCalled = true
	}

	// no ledger set in workspace registry
	ctx := context.Background()
	s.triggerLedgerIndexRebuild(ctx) // must not panic

	assert.False(t, hookCalled, "BuildLedgerIndex should not fire when no ledger is registered")
	assert.Empty(t, s.lastLedgerSha, "lastLedgerSha should remain empty when no ledger exists")
}

// TestTriggerLedgerIndexRebuild_NilCodeDB_Noop verifies that when codedb is nil,
// triggerLedgerIndexRebuild returns immediately without panic.
// Failure prevented: NPE when codedb manager hasn't been initialized.
func TestTriggerLedgerIndexRebuild_NilCodeDB_Noop(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := DefaultConfig()
	s := NewSyncScheduler(cfg, logger)
	// s.codedb is nil

	ctx := context.Background()
	s.triggerLedgerIndexRebuild(ctx) // must not panic

	assert.Empty(t, s.lastLedgerSha)
}

// TestTriggerLedgerIndexRebuild_GitFailure_NoRebuild verifies that if git rev-parse HEAD
// fails (e.g. empty repo with no commits), no rebuild is triggered and no panic occurs.
// Failure prevented: corrupt/empty ledger repo crashes daemon sync loop.
func TestTriggerLedgerIndexRebuild_GitFailure_NoRebuild(t *testing.T) {
	t.Parallel()

	// create a dir that exists but is NOT a git repo
	badDir := t.TempDir()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := DefaultConfig()
	s := NewSyncScheduler(cfg, logger)

	cdbMgr := NewCodeDBManager(badDir, logger, nil)
	s.codedb = cdbMgr

	hookCalled := false
	cdbMgr.ledgerTestHook = func() {
		hookCalled = true
	}

	s.workspaceRegistry.ledger = &WorkspaceState{
		ID:     "ledger",
		Type:   WorkspaceTypeLedger,
		Path:   badDir, // exists but not a git repo
		Exists: true,
	}

	ctx := context.Background()
	s.triggerLedgerIndexRebuild(ctx) // must not panic

	assert.False(t, hookCalled, "BuildLedgerIndex should not fire when git rev-parse fails")
	assert.Empty(t, s.lastLedgerSha, "lastLedgerSha should remain empty on git failure")
}

// TestTriggerLedgerIndexRebuild_LedgerNotExists_Noop verifies that when ledger is
// registered but marked as not existing on disk, no rebuild fires.
// Failure prevented: attempting to index a ledger that hasn't been cloned yet.
func TestTriggerLedgerIndexRebuild_LedgerNotExists_Noop(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := DefaultConfig()
	s := NewSyncScheduler(cfg, logger)

	cdbMgr := NewCodeDBManager(t.TempDir(), logger, nil)
	s.codedb = cdbMgr

	hookCalled := false
	cdbMgr.ledgerTestHook = func() {
		hookCalled = true
	}

	s.workspaceRegistry.ledger = &WorkspaceState{
		ID:     "ledger",
		Type:   WorkspaceTypeLedger,
		Path:   "/nonexistent/ledger",
		Exists: false, // not cloned yet
	}

	ctx := context.Background()
	s.triggerLedgerIndexRebuild(ctx)

	assert.False(t, hookCalled, "BuildLedgerIndex should not fire when ledger.Exists is false")
}

// TestContentSourceFingerprint_IgnoresTeamContexts verifies that team context HEADs
// are not part of the fingerprint: BuildLedgerIndex only indexes the ledger repo.
// Failure prevented: any commit to any team context forcing a full 39k-commit
// ledger rebuild (the production rebuild storm).
func TestContentSourceFingerprint_IgnoresTeamContexts(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	t.Parallel()

	ledgerDir := t.TempDir()
	tcDir := t.TempDir()
	initLedgerGitRepo(t, ledgerDir)
	initLedgerGitRepo(t, tcDir)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := DefaultConfig()
	cfg.ProjectRoot = ledgerDir
	s := NewSyncScheduler(cfg, logger)

	// register team context in workspace registry
	s.workspaceRegistry.workspaces["team-abc"] = &WorkspaceState{
		ID:     "team-abc",
		Type:   WorkspaceTypeTeamContext,
		Path:   tcDir,
		Exists: true,
	}

	ctx := context.Background()

	fp1 := s.contentSourceFingerprint(ctx, ledgerDir)
	require.NotEmpty(t, fp1)
	assert.True(t, strings.HasPrefix(fp1, "ledger="), "fingerprint should start with ledger=")
	assert.NotContains(t, fp1, tcDir, "fingerprint must not contain team context paths")

	// add a commit to team context only (ledger stays the same)
	addCommit(t, tcDir, "team discussion")

	fp2 := s.contentSourceFingerprint(ctx, ledgerDir)
	assert.Equal(t, fp1, fp2, "fingerprint must not change when only a team context HEAD changes")
}

// TestContentSourceFingerprint_LedgerOnly_NoTeamContexts verifies fingerprint works
// with just a ledger and no team contexts registered.
// Failure prevented: NPE or empty fingerprint when no team contexts exist.
func TestContentSourceFingerprint_LedgerOnly_NoTeamContexts(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	t.Parallel()

	ledgerDir := t.TempDir()
	sha := initLedgerGitRepo(t, ledgerDir)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := DefaultConfig()
	cfg.ProjectRoot = ledgerDir
	s := NewSyncScheduler(cfg, logger)

	ctx := context.Background()
	fp := s.contentSourceFingerprint(ctx, ledgerDir)

	// with no team contexts, fingerprint is "ledger=sha"
	assert.Equal(t, "ledger="+sha, fp, "fingerprint with no team contexts should be ledger=sha")
}

// TestContentSourceFingerprint_InvalidLedger_ReturnsEmpty verifies that an invalid
// ledger path produces an empty fingerprint rather than panicking.
// Failure prevented: daemon crash when ledger path is stale.
func TestContentSourceFingerprint_InvalidLedger_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := DefaultConfig()
	s := NewSyncScheduler(cfg, logger)

	ctx := context.Background()
	fp := s.contentSourceFingerprint(ctx, "/nonexistent/ledger")

	assert.Empty(t, fp, "invalid ledger must return empty fingerprint, not panic")
}

// TestContentSourceFingerprint_SkipsUnavailableTeamContext verifies that a team context
// with a bad path is silently skipped rather than failing the whole fingerprint.
// Failure prevented: one broken team context prevents all ledger index rebuilds.
func TestContentSourceFingerprint_SkipsUnavailableTeamContext(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	t.Parallel()

	ledgerDir := t.TempDir()
	sha := initLedgerGitRepo(t, ledgerDir)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := DefaultConfig()
	cfg.ProjectRoot = ledgerDir
	s := NewSyncScheduler(cfg, logger)

	// register a team context with a bad path
	s.workspaceRegistry.workspaces["team-broken"] = &WorkspaceState{
		ID:     "team-broken",
		Type:   WorkspaceTypeTeamContext,
		Path:   "/nonexistent/tc",
		Exists: true, // says it exists, but path is invalid
	}

	ctx := context.Background()
	fp := s.contentSourceFingerprint(ctx, ledgerDir)

	// should still get a valid fingerprint (just ledger=sha, broken tc skipped)
	assert.Equal(t, "ledger="+sha, fp, "broken team context should be skipped, not fail the fingerprint")
}
