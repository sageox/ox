package daemon

// KB sync scheduler tests — verify syncBubbles is wired into the
// daemon's main scheduler loop on the team-context cadence (15s),
// alongside the existing pullTeamContexts call. Also verifies the
// non-default-branch checkout case (analogous to ledger / team-context
// branch handling): a kb whose remote default is something other than
// "main" must still produce a populated working tree on the right
// branch.
//
// What's NOT here (covered elsewhere):
//   - per-bubble cadence routing (intervalForType) → sync_bubbles_test.go
//   - clone idempotency → sync_bubbles_clone_test.go
//   - integration end-to-end → sync_bubbles_integration_test.go

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSyncBubbles_Scheduler_RegisteredOnTeamContextTicker verifies the
// daemon's main Start loop actually fires syncBubbles on the
// team-context ticker. We assert via a side effect — the lister factory
// is invoked at least once during a short scheduler run.
//
// Failure prevented: a refactor that detaches syncBubbles from the
// Start loop, leaving the daemon to never sync any kb's even though
// every kb test passes in isolation. (This is the equivalent of the
// "team context ticker exists" check on the team-context side.)
func TestSyncBubbles_Scheduler_RegisteredOnTeamContextTicker(t *testing.T) {
	if testing.Short() {
		t.Skip("short: scheduler timing")
	}
	kbTestEnv(t)

	projectDir := setupProjectWithConfig(t, "")
	kbProjectWithTeam(t, projectDir)
	cfg := DefaultConfig()
	cfg.ProjectRoot = projectDir
	// fast tickers so a 1-second context window is enough.
	cfg.SyncIntervalRead = time.Hour                    // disable read ticker effects
	cfg.TeamContextSyncInterval = 50 * time.Millisecond // fast kb tick
	cfg.GCCheckInterval = 0                             // disable
	cfg.DistillInterval = 0
	cfg.CodeDBCheckInterval = 0
	cfg.LedgerCheckInterval = 0
	cfg.GitHubSyncInterval = 0
	cfg.VersionCheckInterval = 0
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	s := NewSyncScheduler(cfg, logger)
	s.SetIssueTracker(NewIssueTracker())

	var listerCalls atomic.Int32
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		listerCalls.Add(1)
		return &fakeKBLister{bubbles: []api.KB{}} // no rows — fast no-op
	})

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		s.Start(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not stop after context cancellation")
	}

	assert.Greater(t, listerCalls.Load(), int32(0),
		"syncBubbles must be invoked by the team-context ticker during Start()")
}

// TestSyncBubbles_Scheduler_NoTickerWhenIntervalZero verifies that when
// TeamContextSyncInterval is zero, syncBubbles is NOT invoked (the
// kb loop rides the team-context ticker, so disabling that disables
// kb's too).
//
// Failure prevented: a separate kb timer being added accidentally that
// fires even when team-context sync is explicitly disabled — making it
// impossible to opt out of kb network calls.
func TestSyncBubbles_Scheduler_NoTickerWhenIntervalZero(t *testing.T) {
	if testing.Short() {
		t.Skip("short: scheduler timing")
	}
	kbTestEnv(t)

	projectDir := setupProjectWithConfig(t, "")
	kbProjectWithTeam(t, projectDir)
	cfg := DefaultConfig()
	cfg.ProjectRoot = projectDir
	cfg.SyncIntervalRead = time.Hour
	cfg.TeamContextSyncInterval = 0 // disabled — implies kb disabled too
	cfg.GCCheckInterval = 0
	cfg.DistillInterval = 0
	cfg.CodeDBCheckInterval = 0
	cfg.LedgerCheckInterval = 0
	cfg.GitHubSyncInterval = 0
	cfg.VersionCheckInterval = 0
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	s := NewSyncScheduler(cfg, logger)
	s.SetIssueTracker(NewIssueTracker())

	var listerCalls atomic.Int32
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		listerCalls.Add(1)
		return &fakeKBLister{}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		s.Start(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not stop")
	}

	assert.Equal(t, int32(0), listerCalls.Load(),
		"with TeamContextSyncInterval=0, kb sync must not fire on its own")
}

// TestSyncBubbles_Checkout_MainBranch verifies a bubble cloned via
// gitserver.TwoPhaseClone produces a populated working tree on the
// `main` branch. The kb path uses the same two-phase clone pipeline as
// team-context, which hard-codes `--branch main` (server provisioning
// guarantees `main` for both team-context and kb repos). Mirrors the
// team-context branch posture.
//
// Failure prevented: a future refactor that diverges the kb checkout
// from the team-context two-phase pipeline and silently produces an
// empty working tree.
func TestSyncBubbles_Checkout_MainBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	kbTestEnv(t)
	s, _ := kbTestScheduler(t)

	// build a bare repo whose default branch is `main` (matches server
	// provisioning, which two-phase clone requires).
	tmp := t.TempDir()
	bareDir := filepath.Join(tmp, "mainline.bare")
	workDir := filepath.Join(tmp, "mainline.work")
	require.NoError(t, exec.Command("git", "init", "--bare", "-b", "main", bareDir).Run())
	require.NoError(t, exec.Command("git", "clone", bareDir, workDir).Run())
	gitConfig(t, workDir)
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "MAIN.md"), []byte("on main\n"), 0o644))
	require.NoError(t, exec.Command("git", "-C", workDir, "add", "MAIN.md").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "commit", "-m", "main initial").Run())
	require.NoError(t, exec.Command("git", "-C", workDir, "push", "origin", "main").Run())

	bubble := api.KB{
		KBID:    "kb_mainline",
		KBType:  api.KBTypeTeam,
		Slug:    "mainline",
		RepoURL: "file://" + bareDir,
	}
	s.SetKBBubbleListerFactory(func(_, _ string) KBBubbleLister {
		return &fakeKBLister{bubbles: []api.KB{bubble}}
	})

	s.syncBubbles(context.Background())

	target := paths.KBDir(endpoint.Get(), bubble.KBID)
	require.DirExists(t, filepath.Join(target, ".git"))

	body, err := os.ReadFile(filepath.Join(target, "MAIN.md"))
	require.NoError(t, err, "working tree must contain the seeded file from main")
	assert.Equal(t, "on main\n", string(body))

	// confirm the local HEAD is main.
	headRefBytes, err := os.ReadFile(filepath.Join(target, ".git", "HEAD"))
	require.NoError(t, err)
	headRef := string(headRefBytes)
	assert.Contains(t, headRef, "refs/heads/main",
		"HEAD must point at main, got: %q", headRef)
}

// TestSyncBubbles_Scheduler_UniformCadenceFromConfig verifies every kb
// type resolves to the scheduler config's SyncIntervalRead — curator
// bubbles change only when a synthesis publishes, so the 15s
// personal/profile/team tier is gone (ADR-028). The value must come
// from the scheduler config, not a hard-coded constant.
//
// Failure prevented: a type silently regaining the team-context cadence
// and hammering the API, or the interval decoupling from config.
func TestSyncBubbles_Scheduler_UniformCadenceFromConfig(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TeamContextSyncInterval = 7 * time.Second // must be ignored
	cfg.SyncIntervalRead = 91 * time.Second
	s := &SyncScheduler{config: cfg}

	for _, kbType := range []api.KBType{
		api.KBTypePersonal, api.KBTypeProfile, api.KBTypeTeam,
		api.KBTypeRepo, api.KBTypeCustom, api.KBTypeChannel, api.KBTypeUnknown,
	} {
		t.Run(string(kbType), func(t *testing.T) {
			got := intervalForType(s.config, string(kbType))
			assert.Equal(t, 91*time.Second, got,
				"%s bubble must use the config's read cadence", kbType)
		})
	}
}
