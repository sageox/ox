package daemon

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ox-baz5.6 — Checkout()'s ledger branch used to clone straight into the
// final payload.RepoPath with no atomicity and no per-path lock. A clone
// that dropped mid-transfer (observed: "fetch-pack: unexpected disconnect
// while reading sideband packet" on a 711MB ledger) left a half-initialized
// .git AT the final location, which every subsequent daemon cycle and CLI
// command then treated as a real ledger — HEAD pointing at
// refs/heads/.invalid, every commit failing with "cannot lock ref HEAD:
// reference already exists". Worse, the bounded clone semaphore limits
// TOTAL concurrent clones but does not serialize by target path, so
// multiple independent triggers (background retry, GC self-heal, codedb
// indexing kickoff) could each pass the exists-check and race into cloning
// the same destination concurrently — the same failure class as the
// FETCH_HEAD race PR #868 closed, one layer earlier (before a .git even
// exists to lock on).
//
// These tests drive Checkout() against a real local git server (dumb HTTP
// over a plain static file server — isValidCloneURL only allows https or
// http on localhost/127.0.0.1, so file:// is not an option here) rather
// than mocking the clone step, per the "simulate the real environment"
// principle: a mock can't reproduce a genuine concurrent-clone race.

// setupBareLedgerHTTP creates a bare git repo with one commit, serves it
// over dumb HTTP via httptest, and returns the clone URL. isValidCloneURL
// accepts http:// only for localhost/127.0.0.1, which httptest.NewServer
// binds to.
func setupBareLedgerHTTP(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	bare := filepath.Join(root, "bare.git")
	work := filepath.Join(root, "work")

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run(root, "init", "--bare", "--initial-branch=main", bare)
	run(root, "clone", "--quiet", bare, work)
	run(work, "config", "user.email", "test@example.com")
	run(work, "config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(work, "seed.txt"), []byte("seed\n"), 0o644))
	run(work, "add", "-A")
	run(work, "commit", "-q", "-m", "seed")
	run(work, "push", "-q", "origin", "main")

	// dumb-HTTP needs the derived info/refs the smart-http path would
	// otherwise generate on the fly.
	run(bare, "update-server-info")

	srv := httptest.NewServer(http.FileServer(http.Dir(bare)))
	t.Cleanup(srv.Close)
	return srv.URL
}

// checkoutTestScheduler returns a scheduler configured with isolated
// credentials (never touches the real machine's ~/.sageox store) and a
// generous clone timeout for local HTTP fixtures.
func checkoutTestScheduler(t *testing.T) *SyncScheduler {
	t.Helper()
	isolateCredentialsWithDir(t)
	cfg := DefaultConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewSyncScheduler(cfg, logger)
}

// tmpCloneSiblings lists any ".tmp-*" directories left next to repoPath —
// the atomic clone's temp staging area. None should survive a Checkout()
// call, success or failure.
func tmpCloneSiblings(t *testing.T, repoPath string) []string {
	t.Helper()
	matches, err := filepath.Glob(repoPath + ".tmp-*")
	require.NoError(t, err)
	return matches
}

// TestSyncScheduler_Checkout_LedgerCloneIsAtomic proves the happy path
// still works end to end through the new temp-dir-then-rename path: a
// successful clone lands a complete, verifiable git repo at exactly
// payload.RepoPath, with no leftover temp staging directory.
func TestSyncScheduler_Checkout_LedgerCloneIsAtomic(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	cloneURL := setupBareLedgerHTTP(t)
	s := checkoutTestScheduler(t)

	repoPath := filepath.Join(t.TempDir(), "ledger")
	result, err := s.Checkout(CheckoutPayload{
		CloneURL: cloneURL,
		RepoPath: repoPath,
		RepoType: "ledger",
	}, nil)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Cloned)
	assert.False(t, result.AlreadyExists)

	// the clone must be complete and healthy — HEAD resolves.
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "-q", "HEAD").CombinedOutput()
	require.NoError(t, err, string(out))

	assert.Empty(t, tmpCloneSiblings(t, repoPath), "no temp staging directory should survive a successful clone")
}

// TestSyncScheduler_Checkout_FailedLedgerCloneLeavesNoHalfClone is the
// direct regression test for the original symptom: a clone that fails must
// leave NOTHING at payload.RepoPath — not a half-initialized .git with an
// unresolvable HEAD, not an orphaned temp directory either.
func TestSyncScheduler_Checkout_FailedLedgerCloneLeavesNoHalfClone(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git operations")
	}
	s := checkoutTestScheduler(t)

	// a real HTTP server that serves 404 for everything — git's clone
	// fails cleanly (unlike a raw connection-refused, this exercises the
	// path where git has already started talking to a server and still
	// fails partway through negotiation, closer to the real early-EOF
	// shape than an instant connection error).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	repoPath := filepath.Join(t.TempDir(), "ledger")
	result, err := s.Checkout(CheckoutPayload{
		CloneURL: srv.URL,
		RepoPath: repoPath,
		RepoType: "ledger",
	}, nil)

	require.Error(t, err)
	assert.Nil(t, result)

	_, statErr := os.Stat(repoPath)
	assert.True(t, os.IsNotExist(statErr), "failed clone must leave nothing at the target path, got: %v", statErr)
	assert.Empty(t, tmpCloneSiblings(t, repoPath), "failed clone must not leave an orphaned temp staging directory")
}

// TestSyncScheduler_Checkout_ConcurrentLedgerCloneRace is the core
// regression test for ox-baz5.6: N goroutines call Checkout() concurrently
// for the SAME new ledger path (the exact shape of a background clone
// retry racing GC self-heal racing a codedb indexing kickoff). Before the
// pre-clone lock, nothing prevented more than one of these from actually
// invoking `git clone` into the same destination at once. Red on main
// (pre-ox-baz5.6): flip WithPreCloneLock to a no-op and this test should
// show more than one goroutine's clone command actually running, and/or a
// corrupted final repo.
func TestSyncScheduler_Checkout_ConcurrentLedgerCloneRace(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	cloneURL := setupBareLedgerHTTP(t)
	s := checkoutTestScheduler(t)
	// enough headroom that the semaphore itself never gates this test —
	// the pre-clone lock is what's under test, not maxConcurrentClones.
	s.cloneSemTimeoutOverride = 30 * time.Second

	repoPath := filepath.Join(t.TempDir(), "ledger")

	const n = 8
	var wg sync.WaitGroup
	results := make([]*CheckoutResult, n)
	errs := make([]error, n)
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = s.Checkout(CheckoutPayload{
				CloneURL: cloneURL,
				RepoPath: repoPath,
				RepoType: "ledger",
			}, nil)
		}(i)
	}
	wg.Wait()

	var cloned, alreadyExists, failed int
	var failureMsgs []string
	for i := range n {
		switch {
		case errs[i] != nil:
			failed++
			failureMsgs = append(failureMsgs, errs[i].Error())
		case results[i].Cloned:
			cloned++
		case results[i].AlreadyExists:
			alreadyExists++
		}
	}

	assert.Empty(t, failureMsgs, "no Checkout() call should fail under a race for the same new path: %s", strings.Join(failureMsgs, " | "))
	assert.Equal(t, 1, cloned, "exactly one of %d concurrent callers should have actually cloned", n)
	assert.Equal(t, n-1, alreadyExists, "every other caller should observe AlreadyExists once the winner finishes")
	assert.Zero(t, failed)

	// the final repo must be a single, healthy clone — not corrupted by a
	// second clone racing into the same destination.
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "-q", "HEAD").CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Empty(t, tmpCloneSiblings(t, repoPath), "no temp staging directories should survive the race")
}

// TestWithPreCloneLock_SerializesConcurrentCallers is a focused unit test
// on the lock primitive itself, independent of Checkout()'s plumbing:
// concurrent holders for the SAME path never run their critical sections
// at overlapping times.
func TestWithPreCloneLock_SerializesConcurrentCallers(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "target")

	var mu sync.Mutex
	active := 0
	maxActive := 0
	const n = 6
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			_ = gitutil.WithPreCloneLock(context.Background(), repoPath, func() error {
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
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, maxActive, "at most one caller should be inside the locked section at a time")
}

// TestCloneInBackground_PreCloneLockBusyDoesNotEscalateBackoff covers the
// caller-side half of ox-baz5.6's pre-clone lock: when Checkout() reports
// the lock is busy (another actor is already cloning this exact path),
// cloneInBackground must treat it the same way it already treats a busy
// clone semaphore — retry next cycle without escalating backoff — not as a
// real clone failure worth penalizing.
func TestCloneInBackground_PreCloneLockBusyDoesNotEscalateBackoff(t *testing.T) {
	s := checkoutTestScheduler(t)
	// short wait budget so the test doesn't sit for the production 5m10s
	// default while a peer holds the lock.
	s.preCloneLockWaitOverride = 200 * time.Millisecond

	repoPath := filepath.Join(t.TempDir(), "ledger")
	workspaceID := "ws-lock-busy-test"

	holding := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = gitutil.WithPreCloneLock(context.Background(), repoPath, func() error {
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding
	defer func() {
		close(release)
		<-done // wait for the holder's unlock to finish before t.TempDir() cleanup runs
	}()

	s.cloneWg.Add(1)
	// a URL that passes isValidCloneURL (localhost is always allowed) but is
	// never actually dialed — the lock must block entry before any network
	// attempt happens.
	s.cloneInBackground("http://127.0.0.1:1/repo.git", repoPath, "ledger", workspaceID)

	attempts, _ := s.workspaceRegistry.GetCloneRetryInfo(workspaceID)
	assert.Zero(t, attempts, "a busy pre-clone lock must not increment the retry/backoff counter")

	_, statErr := os.Stat(repoPath)
	assert.True(t, os.IsNotExist(statErr), "nothing should have been cloned while the lock was held by a peer")
}

// TestSyncScheduler_Checkout_EmptyRemoteFailsVerification covers the second
// half of the atomic clone's own verification (the first half — clone exit
// code — is covered by FailedLedgerCloneLeavesNoHalfClone above): `git
// clone` can exit 0 against a genuinely empty bare remote (zero commits)
// and still produce a temp clone whose HEAD does not resolve to anything.
// The atomic-clone verification must catch this before ever renaming the
// temp clone into place, the same way it catches an early-EOF exit failure.
func TestSyncScheduler_Checkout_EmptyRemoteFailsVerification(t *testing.T) {
	if testing.Short() {
		t.Skip("short: git clone operations")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// a bare remote with ZERO commits — `git clone` against it succeeds
	// (exit 0) but leaves an unborn HEAD in the resulting clone.
	root := t.TempDir()
	bare := filepath.Join(root, "empty.git")
	cmd := exec.Command("git", "init", "--bare", "--initial-branch=main", bare)
	require.NoError(t, cmd.Run())
	require.NoError(t, exec.Command("git", "-C", bare, "update-server-info").Run())
	srv := httptest.NewServer(http.FileServer(http.Dir(bare)))
	t.Cleanup(srv.Close)

	s := checkoutTestScheduler(t)
	repoPath := filepath.Join(t.TempDir(), "ledger")
	result, err := s.Checkout(CheckoutPayload{
		CloneURL: srv.URL,
		RepoPath: repoPath,
		RepoType: "ledger",
	}, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "HEAD does not resolve")
	assert.Nil(t, result)

	_, statErr := os.Stat(repoPath)
	assert.True(t, os.IsNotExist(statErr), "a clone whose HEAD doesn't resolve must not be published to the final path")
	assert.Empty(t, tmpCloneSiblings(t, repoPath), "no temp staging directory should survive a failed verification")
}
