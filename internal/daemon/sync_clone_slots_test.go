package daemon

import (
	"context"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/require"
)

// A Ledger's first clone holds its pre-clone lock, and one clone slot, for as
// long as its download runs, which can be many minutes. These tests start a
// real paced Ledger download through the daemon's Checkout, then ask for that
// Ledger again the way `ox doctor --fix` does, and check what that does to
// everyone else's clones.

// startLedgerDownload starts the daemon's clone of a paced Ledger and returns
// once its pack is flowing, i.e. while it holds the Ledger's pre-clone lock
// and a clone slot. wait blocks until the clone has finished.
func startLedgerDownload(t *testing.T, s *SyncScheduler, srv *pacedGitServer, ledgerPath string) (wait func() (*CheckoutResult, error)) {
	t.Helper()
	var result *CheckoutResult
	var err error
	done := make(chan struct{})
	go func() {
		defer close(done)
		result, err = checkoutLedger(s, srv.URL, ledgerPath)
	}()
	t.Cleanup(func() { <-done })
	select {
	case <-srv.streaming:
	case <-done:
		t.Fatalf("the Ledger clone ended before it downloaded anything: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the Ledger clone never started downloading")
	}
	return func() (*CheckoutResult, error) { <-done; return result, err }
}

// serveTeamContext serves a real team-context repo over git's smart HTTP
// backend, which its two-phase clone needs, and returns its clone URL.
func serveTeamContext(t *testing.T) string {
	t.Helper()
	bare := setupTeamContextBareRepo(t, "version = 1\n", nil)
	gitExec, err := exec.Command("git", "--exec-path").Output()
	require.NoError(t, err)
	server := httptest.NewServer(&cgi.Handler{
		Path: filepath.Join(strings.TrimSpace(string(gitExec)), "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + filepath.Dir(bare), "GIT_HTTP_EXPORT_ALL=1"},
	})
	t.Cleanup(server.Close)
	return server.URL + "/" + filepath.Base(bare)
}

// sameLedgerCheckout is one caller asking for a Ledger that is already being
// cloned, the way a second `ox doctor --fix` does.
type sameLedgerCheckout struct {
	result  *CheckoutResult
	err     error
	elapsed time.Duration
}

// TestLedgerClone_CallersOfARunningCloneDoNotStarveOtherClones verifies that
// asking for a Ledger that is already downloading neither waits for it nor
// takes a clone slot, so other repos still clone.
// Failure prevented: each caller took a clone slot, then waited for the
// Ledger's pre-clone lock while holding it, for up to 5m10s. Two `ox doctor
// --fix` runs during a long Ledger download filled all three slots, and every
// team-context clone failed with ErrCloneSemaphoreTimeout until they gave up.
// The slot wait is scaled from 2m to 1s here.
func TestLedgerClone_CallersOfARunningCloneDoNotStarveOtherClones(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real paced Git clone")
	}
	setCloneBudgets(t, 60*time.Second, 2*time.Minute)
	srv := servePacedLedger(t, ledgerFixtureBytes, ledgerPace, 0)
	s, ledgerPath := newCloneLifecycleScheduler(t, srv.URL)
	s.cloneSemTimeoutOverride = time.Second
	teamURL := serveTeamContext(t)

	ledgerDone := startLedgerDownload(t, s, srv, ledgerPath)

	// the two doctor runs: one slot is the download's, so these two are
	// exactly enough to fill the rest
	callers := make([]sameLedgerCheckout, maxConcurrentClones-1)
	var wg sync.WaitGroup
	t.Cleanup(wg.Wait)
	for i := range callers {
		wg.Go(func() {
			start := time.Now()
			callers[i].result, callers[i].err = checkoutLedger(s, srv.URL, ledgerPath)
			callers[i].elapsed = time.Since(start)
		})
	}
	// settled: either the callers have their answer, or they hold every slot
	settled := make(chan struct{})
	go func() { wg.Wait(); close(settled) }()
	require.Eventually(t, func() bool {
		select {
		case <-settled:
			return true
		default:
			return len(s.cloneSem) == cap(s.cloneSem)
		}
	}, 5*time.Second, 10*time.Millisecond)

	teamPath := filepath.Join(t.TempDir(), "team-context")
	team, err := s.Checkout(CheckoutPayload{CloneURL: teamURL, RepoPath: teamPath, RepoType: "team-context"}, nil)
	require.NoError(t, err, "a team context must still get a clone slot while a Ledger downloads")
	require.True(t, team.Cloned)
	require.FileExists(t, filepath.Join(teamPath, "SOUL.md"))

	<-settled
	for i, c := range callers {
		require.NoError(t, c.err, "caller %d", i)
		require.True(t, c.result.InProgress, "caller %d must be told the Ledger is already being cloned: %+v", i, c.result)
		require.False(t, c.result.Cloned || c.result.AlreadyExists, "caller %d: %+v", i, c.result)
		require.Less(t, c.elapsed, time.Second, "caller %d must be told at once, not wait out the download", i)
	}

	ledger, err := ledgerDone()
	require.NoError(t, err, "the running Ledger clone must be unaffected")
	require.True(t, ledger.Cloned)
	require.NoError(t, exec.Command("git", "-C", ledgerPath, "rev-parse", "--verify", "-q", "HEAD").Run())
}

// TestLedgerClone_IPCCheckoutOfARunningCloneAnswersAtOnce verifies a CLI
// asking over IPC for a Ledger the daemon is already cloning is told so at
// once, with ErrCloneAlreadyRunning.
// Failure prevented: the daemon's handler sat on the pre-clone lock without a
// word, so `ox doctor --fix` waited out its whole idle window (60s) before
// reporting a clone that had been running all along.
func TestLedgerClone_IPCCheckoutOfARunningCloneAnswersAtOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real paced Git clone")
	}
	setCloneBudgets(t, 60*time.Second, 2*time.Minute)
	srv := servePacedLedger(t, ledgerFixtureBytes, ledgerPace, 0)
	s, ledgerPath := newCloneLifecycleScheduler(t, srv.URL)
	const idle = 3 * time.Second
	client := serveCheckoutIPC(t, s.Checkout, idle)

	ledgerDone := startLedgerDownload(t, s, srv, ledgerPath)

	start := time.Now()
	result, err := client.Checkout(CheckoutPayload{CloneURL: srv.URL, RepoPath: ledgerPath, RepoType: "ledger"}, nil)
	elapsed := time.Since(start)

	require.ErrorIs(t, err, ErrCloneAlreadyRunning)
	require.ErrorIs(t, err, ErrCheckoutInProgress, "callers that handle a clone left running must handle this one too")
	require.Nil(t, result)
	require.Less(t, elapsed, idle/3, "the CLI must be told at once, not after its idle window")

	ledger, err := ledgerDone()
	require.NoError(t, err)
	require.True(t, ledger.Cloned)
}

// TestCheckout_HalfFinishedTeamContextIsOnlyMovedAsideUnderTheLock verifies a
// team context whose two-phase clone stopped before .sageox/ arrived is moved
// aside and cloned again, but only by the caller that holds its pre-clone lock.
// Failure prevented: the move-aside ran before the lock was taken, so a second
// caller moved a team context aside while another clone was still writing it.
func TestCheckout_HalfFinishedTeamContextIsOnlyMovedAsideUnderTheLock(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real Git clone")
	}
	for _, held := range []bool{true, false} {
		name := "lock free: moved aside and cloned again"
		if held {
			name = "another clone holds the lock: left alone"
		}
		t.Run(name, func(t *testing.T) {
			teamURL := serveTeamContext(t)
			s := checkoutTestScheduler(t)
			teamPath := filepath.Join(t.TempDir(), "team-context")
			// phase 1 wrote .git; phase 2 never materialized .sageox/
			require.NoError(t, os.MkdirAll(filepath.Join(teamPath, ".git"), 0o755))
			if held {
				release, holding := make(chan struct{}), make(chan struct{})
				var wg sync.WaitGroup
				wg.Go(func() {
					_ = gitutil.WithPreCloneLock(context.Background(), teamPath, func() error { close(holding); <-release; return nil })
				})
				<-holding
				t.Cleanup(func() { close(release); wg.Wait() })
			}

			result, err := s.Checkout(CheckoutPayload{CloneURL: teamURL, RepoPath: teamPath, RepoType: "team-context"}, nil)
			require.NoError(t, err)
			backups, _ := filepath.Glob(teamPath + ".bak.*")

			if held {
				require.True(t, result.InProgress, "%+v", result)
				require.Empty(t, backups, "a clone that another caller is writing must not be moved aside")
				require.DirExists(t, filepath.Join(teamPath, ".git"))
				return
			}
			require.True(t, result.Cloned, "%+v", result)
			require.Len(t, backups, 1, "the half-finished clone is kept aside, not deleted")
			require.DirExists(t, filepath.Join(teamPath, ".sageox"))
			require.FileExists(t, filepath.Join(teamPath, "SOUL.md"))
		})
	}
}
