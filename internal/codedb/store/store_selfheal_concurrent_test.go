package store

// Concurrency + cross-process regression coverage for the bleve self-heal race
// (GitHub #828): two codedb.Open callers must not race to delete each other's
// freshly-created replacement sub-index. The destructive nuke+recreate is now
// serialized by a per-sub-index filesystem lock (rebuildSubIndexLocked), and the
// recheck-under-lock reuses the real corruption classifier so a healthy index
// held open by the winner is DEFERRED to, never nuked.
//
// Failure prevented: a concurrent in-process `ox index`, two `ox index` runs, or
// two worktrees sharing one ledger codedb both self-healing the same sub-index
// and clobbering each other's fresh empty bleve — transient churn today, and a
// data-loss-shaped race that widened with the torn-bolt trigger in #826.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

// Env vars set on a re-exec of the test binary make TestMain run
// runSelfHealHelper instead of the normal test suite (env values cannot contain
// NUL, so the spec is passed as three separate vars).
const (
	selfHealHelperEnv = "OX_STORE_SELFHEAL_HELPER"
	selfHealRootEnv   = "OX_STORE_SELFHEAL_ROOT"
	selfHealPathEnv   = "OX_STORE_SELFHEAL_PATH"
	selfHealNameEnv   = "OX_STORE_SELFHEAL_NAME"
)

func TestMain(m *testing.M) {
	if os.Getenv(selfHealHelperEnv) != "" {
		os.Exit(runSelfHealHelper(
			os.Getenv(selfHealRootEnv),
			os.Getenv(selfHealPathEnv),
			os.Getenv(selfHealNameEnv),
		))
	}
	os.Exit(m.Run())
}

// runSelfHealHelper is the child-process body for the real multi-process race
// test. It drives the actual production entry point (openOrCreateBleveIndex) on
// a shared, already-corrupted sub-index, retrying on the retryable "lock
// contention" verdict exactly as a real daemon/CLI caller would, and exits 0
// once it holds a working index. Any process that permanently fails to get a
// working index exits non-zero — which the parent treats as the #828 race
// destroying the replacement.
func runSelfHealHelper(root, path, name string) int {
	if root == "" || path == "" || name == "" {
		fmt.Fprintf(os.Stderr, "bad helper spec root=%q path=%q name=%q\n", root, path, name)
		return 2
	}

	var lastErr error
	for attempt := 0; attempt < 400; attempt++ {
		idx, err := openOrCreateBleveIndex(root, path, name)
		if err == nil {
			// Hold briefly, as a real caller would while it indexes, then release
			// so peers can make progress. This is what makes the winner's fresh
			// index visible-but-momentarily-locked to the losers.
			_ = idx.Close()
			return 0
		}
		lastErr = err
		time.Sleep(15 * time.Millisecond)
	}
	fmt.Fprintf(os.Stderr, "helper never obtained a working index: %v\n", lastErr)
	return 1
}

// installFastCorruptionProbes shrinks the read-only peek and exclusive-probe
// budgets so a reclassify against a genuinely-held index (which must time out to
// reach the DEFER verdict) resolves in tens of ms instead of seconds. Restores
// the defaults on cleanup. Timeouts only ever bias toward the safe "don't nuke"
// verdict, so shrinking them cannot manufacture a false result.
func installFastCorruptionProbes(t *testing.T) {
	t.Helper()
	origAttempts, origDelay := bleveCorruptionPeekAttempts, bleveCorruptionPeekDelay
	origProbe := bleveExclusiveProbeTimeout
	t.Cleanup(func() {
		bleveCorruptionPeekAttempts = origAttempts
		bleveCorruptionPeekDelay = origDelay
		bleveExclusiveProbeTimeout = origProbe
	})
	bleveCorruptionPeekAttempts = 1
	bleveCorruptionPeekDelay = time.Millisecond
	bleveExclusiveProbeTimeout = 200 * time.Millisecond
}

// actionCounter is a thread-safe tally of the verdicts rebuildSubIndexLocked
// took, installed via subIndexRebuildActionHook so the concurrency tests assert
// the exact loser behavior instead of inferring it from side effects.
type actionCounter struct {
	mu sync.Mutex
	n  map[string]int
}

func installActionCounter(t *testing.T) *actionCounter {
	t.Helper()
	c := &actionCounter{n: map[string]int{}}
	orig := subIndexRebuildActionHook
	t.Cleanup(func() { subIndexRebuildActionHook = orig })
	subIndexRebuildActionHook = func(action string) {
		c.mu.Lock()
		c.n[action]++
		c.mu.Unlock()
	}
	return c
}

func (c *actionCounter) get(action string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[action]
}

// --- A. Verdict units: the three branches of reclassify-under-lock ---

// TestRebuildSubIndexLocked_Corrupt_Nukes proves the healNuke branch: a proven-
// corrupt index held under the lock is rebuilt empty and gets a reindex marker.
func TestRebuildSubIndexLocked_Corrupt_Nukes(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Bleve + bbolt operations")
	}
	installFastCorruptionProbes(t)
	counter := installActionCounter(t)

	tmp := t.TempDir()
	indexPath := filepath.Join(tmp, "test-index")
	first, err := openOrCreateBleveIndex(tmp, indexPath, "test")
	require.NoError(t, err)
	require.NoError(t, first.Close())

	emptyMappingForLatestSnapshot(t, filepath.Join(indexPath, "store", "root.bolt"))

	idx, err := rebuildSubIndexLocked(tmp, indexPath, "test", false,
		fmt.Errorf("simulated corrupt open"))
	require.NoError(t, err, "corrupt index must self-heal")
	defer func() { _ = idx.Close() }()

	count, err := idx.DocCount()
	require.NoError(t, err)
	require.Equal(t, uint64(0), count, "healed index must be empty")
	require.True(t, HasNeedsReindexMarker(tmp, "test"), "nuke must write reindex marker")
	require.Equal(t, 1, counter.get("nuke"))
	require.Equal(t, 0, counter.get("adopt"))
}

// TestRebuildSubIndexLocked_Healthy_Adopts proves the healAdopt branch: when the
// on-disk index already opens cleanly (a concurrent process healed it first),
// the caller adopts it instead of nuking — and writes NO new marker.
func TestRebuildSubIndexLocked_Healthy_Adopts(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Bleve + bbolt operations")
	}
	installFastCorruptionProbes(t)
	counter := installActionCounter(t)

	tmp := t.TempDir()
	indexPath := filepath.Join(tmp, "test-index")
	first, err := openOrCreateBleveIndex(tmp, indexPath, "test")
	require.NoError(t, err)
	require.NoError(t, first.Close())

	// Index is healthy on disk and not held open. rebuildSubIndexLocked must
	// recognize a fit replacement and adopt it.
	idx, err := rebuildSubIndexLocked(tmp, indexPath, "test", false,
		fmt.Errorf("simulated corrupt open"))
	require.NoError(t, err)
	defer func() { _ = idx.Close() }()

	require.Equal(t, 1, counter.get("adopt"))
	require.Equal(t, 0, counter.get("nuke"))
	require.False(t, HasNeedsReindexMarker(tmp, "test"),
		"adopting a healthy index must NOT write a marker (no rebuild happened)")
}

// TestRebuildSubIndexLocked_HeldOpen_Defers proves the healDefer branch: an index
// that cannot be reopened only because a live writer holds its exclusive bbolt
// lock is NOT corrupt — it must be deferred to (retryable contention error),
// never nuked. This is the exact misclassification that would let a loser delete
// the winner's healthy index in #828.
func TestRebuildSubIndexLocked_HeldOpen_Defers(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Bleve + bbolt operations")
	}
	installFastCorruptionProbes(t)
	counter := installActionCounter(t)

	tmp := t.TempDir()
	indexPath := filepath.Join(tmp, "test-index")
	held, err := openOrCreateBleveIndex(tmp, indexPath, "test")
	require.NoError(t, err)
	defer func() { _ = held.Close() }() // keep the exclusive bbolt lock held

	idx, err := rebuildSubIndexLocked(tmp, indexPath, "test", false,
		fmt.Errorf("simulated corrupt open"))
	require.Nil(t, idx)
	require.Error(t, err, "a healthy-but-held index must not be healed")
	require.Contains(t, err.Error(), "lock contention",
		"held index must surface the retryable contention error")
	require.Equal(t, 1, counter.get("defer"))
	require.Equal(t, 0, counter.get("nuke"),
		"a live-locked healthy index must never be nuked")
	require.False(t, HasNeedsReindexMarker(tmp, "test"))
}

// --- B. Deterministic two-healer race (the #828 regression) ---

// TestConcurrentSelfHeal_LoserDoesNotClobberWinner is the core #828 regression.
// Two goroutines each call openOrCreateBleveIndex on the SAME corrupt sub-index.
// The heal-lock acquired hook pins the winner (A) holding the lock while the
// loser (B) is forced to wait; when A finishes it holds its fresh index open, so
// B — reclassifying under the lock — sees a healthy-but-locked index and defers
// instead of deleting A's replacement.
//
// Red-first: with the pre-fix unconditional os.RemoveAll+recreate, B would return
// its OWN new index (errB == nil) after having RemoveAll'd A's — so the
// "errB is a contention error" and "exactly one nuke" assertions fail.
func TestConcurrentSelfHeal_LoserDoesNotClobberWinner(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Bleve + bbolt operations + goroutines")
	}
	installFastCorruptionProbes(t)
	counter := installActionCounter(t)

	tmp := t.TempDir()
	indexPath := filepath.Join(tmp, "test-index")
	first, err := openOrCreateBleveIndex(tmp, indexPath, "test")
	require.NoError(t, err)
	require.NoError(t, first.Close())
	emptyMappingForLatestSnapshot(t, filepath.Join(indexPath, "store", "root.bolt"))

	// Pin the first acquirer holding the heal lock until we release it, so the
	// second healer is provably blocked on the lock (no wall-clock racing).
	var once sync.Once
	gotLock := make(chan struct{})
	proceed := make(chan struct{})
	origHook := subIndexHealLockAcquiredHook
	t.Cleanup(func() { subIndexHealLockAcquiredHook = origHook })
	subIndexHealLockAcquiredHook = func(string) {
		once.Do(func() {
			close(gotLock)
			<-proceed
		})
	}

	type result struct {
		idx interface{ Close() error }
		err error
	}
	resA := make(chan result, 1)
	resB := make(chan result, 1)

	go func() {
		idx, err := openOrCreateBleveIndex(tmp, indexPath, "test")
		resA <- result{idx, err}
	}()

	<-gotLock // A holds the heal lock, has not nuked yet

	go func() {
		idx, err := openOrCreateBleveIndex(tmp, indexPath, "test")
		resB <- result{idx, err}
	}()

	// Give B a beat to reach (and block on) the heal lock, then let A proceed.
	time.Sleep(50 * time.Millisecond)
	close(proceed)

	a := <-resA
	b := <-resB

	// Winner healed a real, working index.
	require.NoError(t, a.err, "winner must heal successfully")
	require.NotNil(t, a.idx)
	require.Equal(t, 1, counter.get("nuke"), "exactly one healer may nuke")

	// Loser deferred — it did NOT create a competing index and did NOT nuke.
	require.Error(t, b.err, "loser must defer to the winner, not heal in parallel")
	require.Contains(t, b.err.Error(), "lock contention")
	require.Nil(t, b.idx)
	require.Equal(t, 1, counter.get("defer"))

	// The winner's replacement survives: close it, then it reopens cleanly + empty.
	require.NoError(t, a.idx.Close())
	require.True(t, HasNeedsReindexMarker(tmp, "test"))
	reopened, err := openOrCreateBleveIndex(tmp, indexPath, "test")
	require.NoError(t, err, "winner's replacement must survive the loser")
	defer func() { _ = reopened.Close() }()
	n, err := reopened.DocCount()
	require.NoError(t, err)
	require.Equal(t, uint64(0), n)
}

// --- C. Real cross-process race (the issue's literal ask) ---

// TestConcurrentSelfHeal_MultiProcess re-execs the test binary as several
// independent OS processes that all self-heal the SAME corrupted sub-index at
// once — the exact "multiple codedb.Open callers with no shared in-process lock"
// scenario from #828 (concurrent `ox index`, multi-worktree shared ledger). It
// proves the flock is genuinely cross-process: every process obtains a working
// index and the final on-disk index is clean + empty + marked for reindex.
func TestConcurrentSelfHeal_MultiProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("short: spawns subprocesses")
	}

	tmp := t.TempDir()
	indexPath := filepath.Join(tmp, "test-index")
	first, err := openOrCreateBleveIndex(tmp, indexPath, "code")
	require.NoError(t, err)
	require.NoError(t, first.Close())
	emptyMappingForLatestSnapshot(t, filepath.Join(indexPath, "store", "root.bolt"))

	const procs = 6

	var wg sync.WaitGroup
	errs := make([]error, procs)
	outs := make([]string, procs)
	start := make(chan struct{})
	for i := 0; i < procs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^$")
			cmd.Dir = tmp // keep the re-exec'd child out of the repo working tree
			cmd.Env = append(os.Environ(),
				selfHealHelperEnv+"=1",
				selfHealRootEnv+"="+tmp,
				selfHealPathEnv+"="+indexPath,
				selfHealNameEnv+"=code",
			)
			<-start // fire as simultaneously as the scheduler allows
			out, err := cmd.CombinedOutput()
			errs[i] = err
			outs[i] = string(out)
		}(i)
	}
	close(start)
	wg.Wait()

	for i := 0; i < procs; i++ {
		require.NoErrorf(t, errs[i], "process %d failed (race destroyed the replacement): %s", i, outs[i])
	}

	// Final on-disk index must be usable and empty — no process left it deleted
	// or half-created.
	final, err := openOrCreateBleveIndex(tmp, indexPath, "code")
	require.NoError(t, err, "final index must open cleanly after the multi-process race")
	defer func() { _ = final.Close() }()
	n, err := final.DocCount()
	require.NoError(t, err)
	require.Equal(t, uint64(0), n)
	require.True(t, HasNeedsReindexMarker(tmp, "code"),
		"at least one process must have marked the sub-index for reindex")
}

// --- D. Lock unavailable: never nuke racily, recover on retry ---

// TestSelfHeal_LockTimeout_DefersThenRecovers proves the safety valve: when the
// heal lock cannot be acquired (a live healer holds it), a corrupt-index open
// must NOT nuke without the lock (that would reopen the race) — it surfaces a
// retryable error and writes no marker. Once the lock frees, the retry heals.
//
// This guards the #826 crash-loop fix from regressing in either direction: no
// racy nuke, and no permanent error.
func TestSelfHeal_LockTimeout_DefersThenRecovers(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Bleve + bbolt operations")
	}
	installFastCorruptionProbes(t)

	origTimeout := subIndexHealLockTimeout
	t.Cleanup(func() { subIndexHealLockTimeout = origTimeout })
	subIndexHealLockTimeout = 150 * time.Millisecond

	tmp := t.TempDir()
	indexPath := filepath.Join(tmp, "test-index")
	first, err := openOrCreateBleveIndex(tmp, indexPath, "test")
	require.NoError(t, err)
	require.NoError(t, first.Close())
	emptyMappingForLatestSnapshot(t, filepath.Join(indexPath, "store", "root.bolt"))

	// Simulate a live healer holding the per-sub-index heal lock throughout.
	holder := flock.New(subIndexHealLockPath(indexPath))
	locked, err := holder.TryLock()
	require.NoError(t, err)
	require.True(t, locked, "test must hold the heal lock to simulate a live healer")

	idx, err := openOrCreateBleveIndex(tmp, indexPath, "test")
	require.Nil(t, idx)
	require.Error(t, err, "must not nuke a corrupt index without the heal lock")
	require.False(t, HasNeedsReindexMarker(tmp, "test"),
		"no destructive rebuild may happen while the lock is held elsewhere")

	// Holder releases → the retry now heals cleanly.
	require.NoError(t, holder.Unlock())
	healed, err := openOrCreateBleveIndex(tmp, indexPath, "test")
	require.NoError(t, err, "retry after the lock frees must heal")
	defer func() { _ = healed.Close() }()
	require.True(t, HasNeedsReindexMarker(tmp, "test"))
}

// TestSelfHeal_FlockHostileFS_DefersNeverRacyNuke proves that when the heal lock
// cannot be SET UP at all (flock-hostile filesystem, not a mere timeout), a
// destructive rebuild is REFUSED, not run lockless. A lockless nuke would let two
// concurrent healers RemoveAll each other's fresh replacement; since the codedb
// is a rebuildable derived cache, we defer instead and leave recovery to a full
// reindex. The no-racy-nuke guarantee holds even where flock does not work.
//
// Failure prevented: two processes on a flock-less FS racing RemoveAll+recreate
// (REMOVE CREATE REMOVE CREATE) and one healer losing metadata the other deleted.
func TestSelfHeal_FlockHostileFS_DefersNeverRacyNuke(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Bleve + bbolt operations")
	}
	installFastCorruptionProbes(t)
	counter := installActionCounter(t)

	tmp := t.TempDir()
	indexPath := filepath.Join(tmp, "test-index")
	first, err := openOrCreateBleveIndex(tmp, indexPath, "code")
	require.NoError(t, err)
	require.NoError(t, first.Close())
	emptyMappingForLatestSnapshot(t, filepath.Join(indexPath, "store", "root.bolt"))
	boltPath := filepath.Join(indexPath, "store", "root.bolt")
	infoBefore, err := os.Stat(boltPath)
	require.NoError(t, err)

	// Simulate a filesystem where flock setup fails: the seam returns
	// (locked=false, err!=nil) — no peer, and no safe way to serialize a rebuild.
	orig := acquireSubIndexHealLockFn
	t.Cleanup(func() { acquireSubIndexHealLockFn = orig })
	acquireSubIndexHealLockFn = func(string) (*flock.Flock, bool, error) {
		return nil, false, errors.New("simulated flock-hostile filesystem")
	}

	idx, err := rebuildSubIndexLocked(tmp, indexPath, "code", false,
		fmt.Errorf("corrupt mapping"))
	require.Nil(t, idx)
	require.Error(t, err, "must defer, never nuke without a cross-process lock")
	require.Equal(t, 1, counter.get("defer"))
	require.Equal(t, 0, counter.get("nuke"), "no lockless RemoveAll may happen")
	require.False(t, HasNeedsReindexMarker(tmp, "code"))

	// The corrupt index must be left exactly as-is (not RemoveAll'd + recreated).
	infoAfter, err := os.Stat(boltPath)
	require.NoError(t, err, "the sub-index must not be destroyed by a lockless heal")
	require.Equal(t, infoBefore.ModTime(), infoAfter.ModTime(),
		"root.bolt must be untouched — no rebuild ran")
}

// --- D2. reclassifyUnderLock must never destroy or adopt on a soft failure ---

// TestReclassify_SoftFailure_DefersNotDestructive proves that a SOFT failure
// during reclassification — one that does NOT prove corruption — resolves to
// healDefer, never to a destructive rebuild or a silent adoption. Both cases
// share the "must not nuke, must not adopt, must not mark" contract; each induces
// a different soft failure and adds its own survival check.
//
// Failure prevented: a retryable error (a stat blip, an unreadable version
// marker) either destroying a healthy index or laundering a stale one as current.
func TestReclassify_SoftFailure_DefersNotDestructive(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Bleve + bbolt operations")
	}
	cases := []struct {
		name string
		// requireVersion selects the plain self-heal (false) vs mapping-upgrade
		// (true) path through rebuildSubIndexLocked.
		requireVersion bool
		skip           func(t *testing.T)
		// induce corrupts the soft classification input on an otherwise
		// healthy+free index, and returns an extra post-run assertion (or nil).
		induce func(t *testing.T, indexPath string) func(t *testing.T)
	}{
		{
			name:           "transient stat error never nukes a healthy index",
			requireVersion: false,
			skip: func(t *testing.T) {
				if os.Geteuid() == 0 {
					t.Skip("root bypasses directory permissions; cannot force EACCES")
				}
			},
			induce: func(t *testing.T, indexPath string) func(t *testing.T) {
				// Drop rx on "store" so stat of its child root.bolt fails with EACCES
				// (a non-ENOENT error) while the bolt still exists and is healthy.
				storeDir := filepath.Join(indexPath, "store")
				require.NoError(t, os.Chmod(storeDir, 0o000))
				t.Cleanup(func() { _ = os.Chmod(storeDir, 0o755) })
				return func(t *testing.T) {
					// The healthy bolt must survive — no RemoveAll happened.
					require.NoError(t, os.Chmod(storeDir, 0o755))
					_, statErr := os.Stat(filepath.Join(storeDir, "root.bolt"))
					require.NoError(t, statErr, "healthy bolt must survive a stat-error classification")
				}
			},
		},
		{
			name:           "marker read failure never adopts a stale index",
			requireVersion: true,
			induce: func(t *testing.T, indexPath string) func(t *testing.T) {
				// Replace the version marker file with a dir so the under-lock re-read
				// returns a non-nil error. The index is healthy + free.
				marker := filepath.Join(indexPath, mappingVersionMarker)
				require.NoError(t, os.Remove(marker))
				require.NoError(t, os.Mkdir(marker, 0o755))
				return nil
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skip != nil {
				tc.skip(t)
			}
			installFastCorruptionProbes(t)
			counter := installActionCounter(t)

			tmp := t.TempDir()
			indexPath := filepath.Join(tmp, "test-index")
			first, err := openOrCreateBleveIndex(tmp, indexPath, "code")
			require.NoError(t, err)
			require.NoError(t, first.Close())

			extraCheck := tc.induce(t, indexPath)

			idx, err := rebuildSubIndexLocked(tmp, indexPath, "code", tc.requireVersion,
				fmt.Errorf("soft failure"))
			require.Nil(t, idx)
			require.Error(t, err, "a soft failure must defer, not rebuild or adopt")
			require.Equal(t, 1, counter.get("defer"))
			require.Equal(t, 0, counter.get("nuke"), "no destructive rebuild on a soft failure")
			require.Equal(t, 0, counter.get("adopt"), "no silent adoption on a soft failure")
			require.False(t, HasNeedsReindexMarker(tmp, "code"))

			if extraCheck != nil {
				extraCheck(t)
			}
		})
	}
}

// TestBoundedAdoptOpen_TimesOutUnderContention proves the adopt open is bounded:
// if a live writer holds the sub-index bbolt lock, boundedAdoptOpen returns an
// error near the timeout instead of blocking in bleve.Open for the writer's
// whole lifetime (the post-probe race window in reclassifyUnderLock).
//
// Failure prevented: the self-heal path hanging indefinitely when an ordinary
// codedb.Open grabs the lock between the freedom probe and the adopt open.
func TestBoundedAdoptOpen_TimesOutUnderContention(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Bleve + bbolt operations")
	}
	tmp := t.TempDir()
	indexPath := filepath.Join(tmp, "test-index")
	first, err := openOrCreateBleveIndex(tmp, indexPath, "code")
	require.NoError(t, err)
	require.NoError(t, first.Close())

	// A live writer holds bbolt's exclusive lock for the sub-index, mimicking an
	// ordinary codedb.Open that raced into the post-probe window.
	boltPath := filepath.Join(indexPath, "store", "root.bolt")
	held, err := bbolt.Open(boltPath, 0o600, &bbolt.Options{Timeout: time.Second})
	require.NoError(t, err)
	defer func() { _ = held.Close() }()

	start := time.Now()
	idx, err := boundedAdoptOpen(indexPath, 200*time.Millisecond)
	elapsed := time.Since(start)
	require.Nil(t, idx)
	require.Error(t, err, "adopt open must time out, not hang, while a writer holds the lock")
	require.Less(t, elapsed, 3*time.Second, "must return near the timeout, not block indefinitely")
}

// --- E. Lock placement guardrail ---

// TestSelfHealLock_IsSiblingAndSurvivesNuke guards the load-bearing placement
// choice: the heal lock file must live OUTSIDE the sub-index dir. If it were a
// child, os.RemoveAll(path) during the nuke would delete the lock a healer is
// holding, handing the next process a fresh uncontended lock and reopening the
// race. Assert the lock path is a sibling and that it survives a real heal.
func TestSelfHealLock_IsSiblingAndSurvivesNuke(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Bleve + bbolt operations")
	}
	installFastCorruptionProbes(t)

	tmp := t.TempDir()
	indexPath := filepath.Join(tmp, "bleve", "code")
	lockPath := subIndexHealLockPath(indexPath)

	// Structural: the lock lives beside the dir, not inside it.
	require.Equal(t, filepath.Dir(indexPath), filepath.Dir(lockPath),
		"heal lock must be a sibling of the sub-index dir")
	require.False(t, strings.HasPrefix(lockPath, indexPath+string(os.PathSeparator)),
		"heal lock must not be a child of the sub-index dir (RemoveAll would delete it)")

	first, err := openOrCreateBleveIndex(tmp, indexPath, "code")
	require.NoError(t, err)
	require.NoError(t, first.Close())
	emptyMappingForLatestSnapshot(t, filepath.Join(indexPath, "store", "root.bolt"))

	idx, err := openOrCreateBleveIndex(tmp, indexPath, "code")
	require.NoError(t, err)
	require.NoError(t, idx.Close())

	// The lock file created during acquisition must still exist after the nuke.
	_, statErr := os.Stat(lockPath)
	require.NoError(t, statErr, "sibling lock file must survive the sub-index RemoveAll")
}

// --- F. Mapping-version upgrade is serialized on the same lock ---

// TestUpgradeSubIndex_StaleVersion_Nukes proves the mapping-version upgrade path
// (the other #828 self-heal trigger named in the issue) routes through the same
// locked rebuild: a clean-but-stale index is rebuilt to the current mapping
// version with a reindex marker.
func TestUpgradeSubIndex_StaleVersion_Nukes(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Bleve + bbolt operations")
	}
	installFastCorruptionProbes(t)
	counter := installActionCounter(t)

	tmp := t.TempDir()
	indexPath := filepath.Join(tmp, "test-index")
	first, err := openOrCreateBleveIndex(tmp, indexPath, "code")
	require.NoError(t, err)
	require.NoError(t, first.Close())

	// Force the on-disk mapping version below current so the reclassify treats a
	// clean open as still-stale (healNuke), not adopt.
	require.NoError(t, os.WriteFile(filepath.Join(indexPath, mappingVersionMarker), []byte("0"), 0o644))

	idx, err := rebuildSubIndexLocked(tmp, indexPath, "code", true,
		fmt.Errorf("mapping version 0 below current"))
	require.NoError(t, err)
	defer func() { _ = idx.Close() }()

	require.Equal(t, 1, counter.get("nuke"))
	got, err := readMappingVersion(indexPath)
	require.NoError(t, err)
	require.Equal(t, bleveMappingVersion("code"), got, "upgrade must land the current mapping version")
	require.True(t, HasNeedsReindexMarker(tmp, "code"))
}

// TestUpgradeSubIndex_AlreadyCurrent_Adopts proves the upgrade recheck adopts a
// replacement a concurrent process already upgraded to the current version,
// instead of re-nuking it.
func TestUpgradeSubIndex_AlreadyCurrent_Adopts(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Bleve + bbolt operations")
	}
	installFastCorruptionProbes(t)
	counter := installActionCounter(t)

	tmp := t.TempDir()
	indexPath := filepath.Join(tmp, "test-index")
	first, err := openOrCreateBleveIndex(tmp, indexPath, "code")
	require.NoError(t, err)
	require.NoError(t, first.Close())

	// On-disk version is already current (openOrCreateBleveIndex wrote it), so an
	// upgrade-path recheck must adopt, not nuke.
	idx, err := rebuildSubIndexLocked(tmp, indexPath, "code", true,
		fmt.Errorf("simulated stale trigger"))
	require.NoError(t, err)
	defer func() { _ = idx.Close() }()

	require.Equal(t, 1, counter.get("adopt"))
	require.Equal(t, 0, counter.get("nuke"))
	require.False(t, HasNeedsReindexMarker(tmp, "code"),
		"adopting an already-current index must not write a marker")
}
