package daemon

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/require"
)

// Ledger first-clone budget. A Ledger is cloned in full, its whole history,
// which is hundreds of MB for an active team, so how long the download takes
// is set by the coworker's link, not by ox. These tests serve a real Ledger
// through git's smart HTTP backend, paced so a small fixture downloads as
// slowly as a large Ledger does on a home connection, and drive the daemon's
// real Checkout end to end: credential helper args, curl stall flags, temp
// sibling, verification, atomic rename.

const (
	// ledgerPace and ledgerFixtureBytes make one fixture clone take ~5s.
	ledgerPace         = 64 << 10 // bytes per second
	ledgerFixtureBytes = 320 << 10

	// pacedChunk is how many bytes the server sends between pauses.
	pacedChunk = 4 << 10

	// streamingMark is how far into one response the pack must be before the
	// server reports it as flowing. info/refs and ls-refs replies are far
	// smaller, so only the pack crosses it.
	streamingMark = 32 << 10
)

// pacedGitServer is a real git smart HTTP server whose responses are paced.
type pacedGitServer struct {
	URL string
	// streaming is closed once a response has sent streamingMark bytes:
	// the clone is downloading the pack, not still negotiating.
	streaming chan struct{}
}

// servePacedLedger seeds a bare Ledger with blobBytes of incompressible data
// and serves it at bytesPerSec. stallAfter > 0 makes every response stop
// sending after that many bytes and hold the connection open, the way a
// dead link looks to curl, until the client hangs up.
func servePacedLedger(t *testing.T, blobBytes, bytesPerSec, stallAfter int) *pacedGitServer {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("CGI Git HTTP fixture requires Unix process semantics")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is not available")
	}

	bare, work := initBareRepo(t, "paced-ledger")
	// incompressible, so the pack is as large as the blob; seeded, so every
	// run moves the same bytes
	blob := make([]byte, blobBytes)
	_, _ = rand.NewChaCha8([32]byte{'o', 'x'}).Read(blob)
	require.NoError(t, os.MkdirAll(filepath.Join(work, "sessions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(work, "sessions", "history.bin"), blob, 0o600))
	gitInDir(t, work, "add", ".")
	gitInDir(t, work, "commit", "-m", "seed Ledger history")
	gitInDir(t, work, "push", "origin", "HEAD:main")

	gitExec, err := exec.Command("git", "--exec-path").Output()
	require.NoError(t, err)
	backend := &cgi.Handler{
		Path: filepath.Join(strings.TrimSpace(string(gitExec)), "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + filepath.Dir(bare), "GIT_HTTP_EXPORT_ALL=1"},
	}

	srv := &pacedGitServer{streaming: make(chan struct{})}
	var streamingOnce sync.Once
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backend.ServeHTTP(&pacedWriter{
			ResponseWriter: w,
			ctx:            r.Context(),
			release:        release,
			pause:          time.Duration(pacedChunk) * time.Second / time.Duration(bytesPerSec),
			stallAfter:     stallAfter,
			onStreaming:    func() { streamingOnce.Do(func() { close(srv.streaming) }) },
		}, r)
	}))
	// cleanups run last-in first-out: unblock stalled handlers, then close
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	srv.URL = server.URL + "/" + filepath.Base(bare)
	return srv
}

// pacedWriter sends a response pacedChunk bytes at a time, pausing between
// chunks, and flushes each chunk so the client sees steady progress.
type pacedWriter struct {
	http.ResponseWriter
	ctx         context.Context
	release     <-chan struct{}
	pause       time.Duration
	stallAfter  int
	onStreaming func()
	sent        int
}

func (p *pacedWriter) Write(b []byte) (int, error) {
	written := 0
	for len(b) > 0 {
		n := min(len(b), pacedChunk)
		if p.stallAfter > 0 && p.sent+n > p.stallAfter {
			n = p.stallAfter - p.sent
		}
		if n > 0 {
			m, err := p.ResponseWriter.Write(b[:n])
			written += m
			p.sent += m
			if err != nil {
				return written, err
			}
			if f, ok := p.ResponseWriter.(http.Flusher); ok {
				f.Flush()
			}
			if p.sent >= streamingMark {
				p.onStreaming()
			}
			b = b[n:]
		}
		if p.stallAfter > 0 && p.sent >= p.stallAfter {
			// a dead link: nothing more arrives until the client gives up.
			// The error makes cgi.Handler kill git-http-backend.
			select {
			case <-p.ctx.Done():
				return written, p.ctx.Err()
			case <-p.release:
				return written, http.ErrHandlerTimeout
			}
		}
		select {
		case <-p.ctx.Done():
			return written, p.ctx.Err()
		case <-time.After(p.pause):
		}
	}
	return written, nil
}

// requireNoClone asserts that a failed clone left nothing behind: no Ledger
// at ledgerPath for the daemon or CLI to mistake for a real one, and no temp
// sibling holding a partial download.
func requireNoClone(t *testing.T, ledgerPath string) {
	t.Helper()
	require.NoDirExists(t, ledgerPath, "a failed clone must not leave a Ledger in place")
	leftovers, err := filepath.Glob(ledgerPath + ".tmp-*")
	require.NoError(t, err)
	require.Empty(t, leftovers, "a failed clone must not leave its partial download behind")
}

// setCloneBudgets shrinks the clone budgets for one test: fixed is the
// team-context budget, the fixed 60s a Ledger clone used to share; backstop is
// the Ledger's own wall-clock limit. Progress is sampled often enough to see
// a download that only lasts a few seconds.
func setCloneBudgets(t *testing.T, fixed, backstop time.Duration) {
	t.Helper()
	prevFixed, prevBackstop, prevInterval := teamContextCloneTimeout, ledgerCloneTimeout, cloneProgressInterval
	teamContextCloneTimeout, ledgerCloneTimeout, cloneProgressInterval = fixed, backstop, 50*time.Millisecond
	t.Cleanup(func() {
		teamContextCloneTimeout, ledgerCloneTimeout, cloneProgressInterval = prevFixed, prevBackstop, prevInterval
	})
}

// shortStallWindow makes curl give up on a dead link after seconds instead of
// production's 15, keeping every other production HTTP flag. A later -c wins.
func shortStallWindow(t *testing.T, seconds int) {
	t.Helper()
	prev := gitHTTPTimeoutFlags
	gitHTTPTimeoutFlags = func() []string {
		return append(prev(), "-c", fmt.Sprintf("http.lowSpeedTime=%d", seconds))
	}
	t.Cleanup(func() { gitHTTPTimeoutFlags = prev })
}

func checkoutLedger(s *SyncScheduler, cloneURL, ledgerPath string) (*CheckoutResult, error) {
	return s.Checkout(CheckoutPayload{CloneURL: cloneURL, RepoPath: ledgerPath, RepoType: "ledger"}, nil)
}

// TestLedgerClone_ProgressingDownloadOutlivesFixedCloneBudget verifies a
// Ledger clone that is still downloading is not killed by a fixed budget.
// Failure prevented: a Ledger whose full clone takes longer than 60s on the
// coworker's link was killed on every attempt ("signal: terminated", "fetch-pack:
// unexpected disconnect while reading sideband packet"), each retry started
// over, and the Ledger never arrived. The fixed budget is scaled from 60s to
// 2s here; the fixture downloads for ~5s.
func TestLedgerClone_ProgressingDownloadOutlivesFixedCloneBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real paced Git clone")
	}
	setCloneBudgets(t, 2*time.Second, 2*time.Minute)
	srv := servePacedLedger(t, ledgerFixtureBytes, ledgerPace, 0)
	s, ledgerPath := newCloneLifecycleScheduler(t, srv.URL)

	start := time.Now()
	result, err := checkoutLedger(s, srv.URL, ledgerPath)
	elapsed := time.Since(start)

	require.NoError(t, err, "a Ledger clone that is still downloading must not be killed")
	require.True(t, result.Cloned)
	require.Greater(t, elapsed, teamContextCloneTimeout,
		"the download finished inside the fixed budget, so this run proved nothing; slow the pace")
	size, err := exec.Command("git", "-C", ledgerPath, "cat-file", "-s", "HEAD:sessions/history.bin").Output()
	require.NoError(t, err, "the Ledger's history must have arrived in full")
	require.Equal(t, fmt.Sprint(ledgerFixtureBytes), strings.TrimSpace(string(size)))
	lastErr, _ := s.LastError()
	require.Empty(t, lastErr, "a clone that finished must not leave an error in ox status")
}

// TestLedgerClone_StalledDownloadIsStillAborted verifies a Ledger clone over a
// dead link is aborted by curl's stall check, long before the backstop.
// Failure prevented: replacing the fixed budget with a long backstop turns a
// dead link into a clone that holds its slot and the pre-clone lock for an hour.
func TestLedgerClone_StalledDownloadIsStillAborted(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real paced Git clone")
	}
	setCloneBudgets(t, 60*time.Second, time.Minute)
	shortStallWindow(t, 2)
	// the pack stops after 96 KiB and the connection stays open
	srv := servePacedLedger(t, ledgerFixtureBytes, ledgerPace, 96<<10)
	s, ledgerPath := newCloneLifecycleScheduler(t, srv.URL)

	start := time.Now()
	_, err := checkoutLedger(s, srv.URL, ledgerPath)
	elapsed := time.Since(start)

	require.Error(t, err, "a clone over a dead link must fail")
	// curl averages speed over ~5s before its 2s window starts counting
	require.Less(t, elapsed, 30*time.Second, "the stall check, not the backstop, must end a dead clone")
	// curl's low-speed abort, so the gate that fired is the stall check
	require.Contains(t, err.Error(), "Operation too slow")
	require.False(t, gitutil.IsPreCloneLockBusy(err), "a stalled clone is a clone failure, not lock contention")
	requireNoClone(t, ledgerPath)
}

// TestLedgerClone_BackstopKillIsReportedAsTimeout verifies a clone killed by
// the daemon's own deadline is reported as a timeout, with how much arrived.
// Failure prevented: the kill was logged and shown in ox status as "signal:
// terminated" and "fetch-pack: unexpected disconnect while reading sideband
// packet", which reads as a server fault and sends triage the wrong way.
func TestLedgerClone_BackstopKillIsReportedAsTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real paced Git clone")
	}
	setCloneBudgets(t, 60*time.Second, 2500*time.Millisecond)
	srv := servePacedLedger(t, ledgerFixtureBytes, ledgerPace, 0)
	s, ledgerPath := newCloneLifecycleScheduler(t, srv.URL)

	_, err := checkoutLedger(s, srv.URL, ledgerPath)

	require.Error(t, err)
	lastErr, _ := s.LastError()
	for _, reported := range []string{err.Error(), lastErr} {
		require.Contains(t, reported, "clone ledger timed out after 2.5s")
		for _, misleading := range []string{"unexpected disconnect", "early EOF", "signal:"} {
			require.NotContains(t, reported, misleading, "git's reaction to being killed is not the cause")
		}
	}
	require.Regexp(t, `\((0\.[1-9]|[1-9]\d*\.\d) MB received\)`, err.Error(),
		"the report must say how much of the Ledger arrived before the kill")
	require.False(t, gitutil.IsPreCloneLockBusy(err),
		"a timed-out clone must count as a failure for backoff and status, not as lock contention")
	requireNoClone(t, ledgerPath)
}

// TestLedgerClone_DaemonShutdownStopsDownload verifies a background Ledger
// clone ends with the daemon that started it, without counting as a failure.
// Failure prevented: git runs in its own process group, so a clone that may
// now run for many minutes would outlive the daemon, keep downloading, and
// leave a temp dir that nothing ever removes; and a shutdown that stopped it
// must not push the next start's clone into retry backoff.
func TestLedgerClone_DaemonShutdownStopsDownload(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real paced Git clone")
	}
	setCloneBudgets(t, 60*time.Second, 2*time.Minute)
	srv := servePacedLedger(t, ledgerFixtureBytes, ledgerPace, 0)
	s, ledgerPath := newCloneLifecycleScheduler(t, srv.URL)
	daemonCtx, daemonCancel := context.WithCancel(context.Background())
	t.Cleanup(daemonCancel)
	s.ctx = daemonCtx

	require.True(t, s.addClone())
	go s.cloneInBackground(s.backgroundCloneContext(), srv.URL, ledgerPath, "ledger", "ledger")
	select {
	case <-srv.streaming:
	case <-time.After(10 * time.Second):
		t.Fatal("the clone never started downloading")
	}
	daemonCancel()
	// the download has ~4s left; finishing it would mean the clone ignored shutdown
	mustCompleteWithin(t, 2*time.Second, "the clone must stop promptly when the daemon does", s.cloneWg.Wait)

	requireNoClone(t, ledgerPath)
	attempts, _ := s.workspaceRegistry.GetCloneRetryInfo("ledger")
	require.Zero(t, attempts, "shutdown is not a clone failure and must not create retry backoff")
	lastErr, _ := s.LastError()
	require.Empty(t, lastErr, "a shutdown is not a clone failure to show in ox status")
	require.Empty(t, s.workspaceRegistry.GetLedger().LastErr, "a shutdown must not mark the Ledger as failing")
}
