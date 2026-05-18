package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/store"
	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

// TestDoIndex_HonorsSelfHealMarker_ForcesFullReindex is the end-to-end test
// for the layer-2 contract of the codedb self-heal pipeline:
//
//  1. store.openOrCreateBleveIndex detects mapping corruption, nukes +
//     recreates the sub-index, and writes `.needs_reindex_<name>`.
//  2. The daemon's next doIndex pass sees the marker, forces payload.Full=true
//     (which wipes dataDir and rebuilds from scratch), and clears the marker.
//
// Without this test, layer-2 could silently regress — search would return
// empty results forever even after the daemon ran an indexing pass, because
// the incremental path skips already-committed SHAs and the empty bleve
// would never get repopulated.
//
// Failure prevented: a future refactor decouples NeedsReindexMarkers from
// the Full-forcing logic in doIndex, and code search stays permanently
// empty after a corruption + auto-heal.
func TestDoIndex_HonorsSelfHealMarker_ForcesFullReindex(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git + SQLite + Bleve operations")
	}

	// Seed a real local git repo so doIndex has something to walk.
	repoDir := t.TempDir()
	seedGitRepo(t, repoDir)

	dataDir := t.TempDir()
	mgr := NewCodeDBManager(repoDir, codedbTestLogger(), nil)
	mgr.dataDir = dataDir // skip path-resolution

	// First indexing pass populates SQL + bleve normally. Each pass gets its
	// own 60s deadline so the second pass doesn't inherit time spent by the
	// first (which can cause spurious timeouts on slow CI).
	ctx1, cancel1 := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel1()
	_, err := mgr.Index(ctx1, CodeIndexPayload{}, nil)
	require.NoError(t, err, "first index must succeed")

	// Capture commit count after first pass — proves SQL was populated.
	commitsAfterFirst := countCommits(t, dataDir)
	require.Greater(t, commitsAfterFirst, 0, "first index must populate commits")

	// Write a self-heal marker for "code" — simulating openOrCreateBleveIndex
	// having nuked + recreated the code sub-index.
	require.NoError(t, writeMarkerForTest(dataDir, "code"))
	require.True(t, store.HasNeedsReindexMarker(dataDir, "code"))

	// Next indexing pass must observe the marker and force Full=true. We
	// verify this indirectly by confirming the marker is cleared post-pass
	// (markers are removed by the dataDir wipe Full=true triggers, and
	// explicitly cleared in the success path as a defensive invariant).
	ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel2()
	_, err = mgr.Index(ctx2, CodeIndexPayload{}, nil)
	require.NoError(t, err, "second index must succeed")

	require.False(t, store.HasNeedsReindexMarker(dataDir, "code"),
		"marker must be cleared after Full=true reindex")
	require.Empty(t, store.NeedsReindexMarkers(dataDir),
		"no markers must remain after successful pass")

	// SQL must still be populated (proves the rebuild actually ran, not just
	// the marker clear).
	commitsAfterSecond := countCommits(t, dataDir)
	require.Greater(t, commitsAfterSecond, 0, "second index must repopulate commits after wipe")
}

// TestStoreOpen_SelfHealsTransparently_NoDaemonRetryNeeded verifies the
// integration boundary between store.Open self-heal and the daemon: when the
// daemon's CheckFreshness opens the store and bleve is corrupt, Open self-
// heals, doIndex sees no MappingCorruptError, and the marker is left behind
// for the next pass — no special daemon-side branch needed.
//
// Failure prevented: a future refactor reintroduces MappingCorruptError to
// callers of store.Open, requiring the daemon to special-case it again.
func TestStoreOpen_SelfHealsTransparently_NoDaemonRetryNeeded(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: SQLite + Bleve operations")
	}

	tmp := t.TempDir()
	db, err := codedb.Open(tmp)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	poisonCommentMapping(t, tmp)

	// Open must succeed silently — no typed error to handle.
	db2, err := codedb.Open(tmp)
	require.NoError(t, err, "Open must self-heal, not return MappingCorruptError")
	defer db2.Close()

	// Marker must have been written so the next daemon pass forces Full.
	require.True(t, store.HasNeedsReindexMarker(tmp, "comment"),
		"self-heal must leave a marker for daemon to consume")
}

// seedGitRepo creates a minimal git repo with one commit at repoDir.
// Uses `git` CLI directly (not go-git) so the repo state is byte-identical
// to what a real user creates, and so we don't pull in go-git for tests
// that only need a walk-able history.
func seedGitRepo(t *testing.T, repoDir string) {
	t.Helper()
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		// Test isolation: never read the host's git config (user.email/signing key/etc.).
		// Without this, signed-commit hooks can prompt for SSH passphrases mid-test.
		cmd.Env = append(cmd.Env,
			"HOME="+t.TempDir(),
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=ox-test", "GIT_AUTHOR_EMAIL=ox@test",
			"GIT_COMMITTER_NAME=ox-test", "GIT_COMMITTER_EMAIL=ox@test",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	runGit("init", "-q", "-b", "main")
	runGit("config", "commit.gpgsign", "false")
	runGit("config", "tag.gpgsign", "false")

	// one small file so the indexer has something to walk
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "hello.go"), []byte("package main\n"), 0o600))
	runGit("add", "hello.go")
	runGit("commit", "-q", "-m", "initial")
}

// writeMarkerForTest creates a needs_reindex marker the same way Open's
// self-heal does. We don't call the unexported writeNeedsReindexMarker
// directly; instead we mimic its contract (the daemon only cares about
// file *presence*, not body content).
func writeMarkerForTest(dataDir, name string) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(store.NeedsReindexMarkerPath(dataDir, name), []byte("test-marker\n"), 0o600)
}

// countCommits queries the codedb SQL store for the commit count without
// going through bleve.
func countCommits(t *testing.T, dataDir string) int {
	t.Helper()
	db, err := codedb.OpenSQLOnly(dataDir)
	require.NoError(t, err)
	defer db.Close()
	var n int
	require.NoError(t, db.Store().QueryRow("SELECT COUNT(*) FROM commits").Scan(&n))
	return n
}

// TestDoIndex_RestoresMarkersWhenMarkerForcedReindexFails verifies the
// crash-safety contract for self-heal markers: if a marker-forced full
// reindex fails AFTER the dataDir wipe (which deletes markers), the
// markers must be restored so the NEXT freshness pass re-forces --full.
// Without this, a single rebuild failure silently drops the signal and
// code search stays empty until manual `ox code index --full`.
//
// Failure prevented: marker-driven self-heal regresses to one-shot
// recovery — any failure on the first marker pass loses the signal.
func TestDoIndex_RestoresMarkersWhenMarkerForcedReindexFails(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: filesystem + indexer operations")
	}

	// Point projectRoot at a directory that doesn't exist so doIndex fails
	// at its "project root no longer exists" guard. This triggers the error
	// path of doIndex AFTER the marker-forced wipe would have run — letting
	// us assert markers are restored when reindex fails.
	bogusRoot := filepath.Join(t.TempDir(), "deleted-worktree")
	dataDir := t.TempDir()

	// Pre-seed a marker so the marker-forced-full path activates.
	require.NoError(t, writeMarkerForTest(dataDir, "code"))
	require.True(t, store.HasNeedsReindexMarker(dataDir, "code"))

	mgr := NewCodeDBManager(bogusRoot, codedbTestLogger(), nil)
	mgr.dataDir = dataDir

	_, err := mgr.Index(context.Background(), CodeIndexPayload{}, nil)
	require.Error(t, err, "Index must fail when projectRoot doesn't exist")

	// Marker must still be present so the NEXT freshness pass re-forces --full.
	require.True(t, store.HasNeedsReindexMarker(dataDir, "code"),
		"self-heal marker must be restored after failed marker-forced reindex")
}

// TestIsIndexing_ReflectsFlag verifies the IsIndexing accessor used by
// WaitIdle. Reading m.indexing directly under m.mu is the only race-free
// way to make the drain decision; a refactor that bypasses the lock or
// aliases the field would silently break shutdown-time bleve drain.
//
// Failure prevented: WaitIdle returns immediately while indexing is still
// in flight, returning to the kill-mid-flush regression.
func TestIsIndexing_ReflectsFlag(t *testing.T) {
	t.Parallel()

	mgr := NewCodeDBManager(t.TempDir(), codedbTestLogger(), nil)
	require.False(t, mgr.IsIndexing(), "fresh manager must not report indexing")

	mgr.mu.Lock()
	mgr.indexing = true
	mgr.mu.Unlock()
	require.True(t, mgr.IsIndexing(), "must reflect flag flip")

	mgr.mu.Lock()
	mgr.indexing = false
	mgr.mu.Unlock()
	require.False(t, mgr.IsIndexing(), "must reflect flag reset")
}

// TestWaitIdle_BlocksUntilFlagClears verifies that WaitIdle is a real
// drain primitive — it returns ONLY after m.indexing flips false, not
// immediately. daemon.shutdown depends on this contract to keep bleve
// batches from being killed mid-flush.
//
// Failure prevented: regression to the polling-free / no-op WaitIdle
// where shutdown completes early and the bleve _mapping doc gets torn.
func TestWaitIdle_BlocksUntilFlagClears(t *testing.T) {
	t.Parallel()

	mgr := NewCodeDBManager(t.TempDir(), codedbTestLogger(), nil)
	mgr.mu.Lock()
	mgr.indexing = true
	mgr.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		done <- mgr.WaitIdle(context.Background())
	}()

	// Confirm WaitIdle is blocking (gives the goroutine time to enter the loop).
	select {
	case <-done:
		t.Fatal("WaitIdle returned before flag cleared — drain contract broken")
	case <-time.After(200 * time.Millisecond):
	}

	// Clear the flag — WaitIdle's next tick must observe and return.
	mgr.mu.Lock()
	mgr.indexing = false
	mgr.mu.Unlock()

	select {
	case err := <-done:
		require.NoError(t, err, "WaitIdle must return nil when flag clears")
	case <-time.After(2 * time.Second):
		t.Fatal("WaitIdle did not unblock within 2s of flag clear")
	}
}

// TestWaitIdle_RespectsContext verifies cancellation: if indexing never
// finishes within the deadline, WaitIdle returns ctx.Err() (not nil).
// daemon.shutdown uses the returned error to log "did not drain" so
// operators can investigate; a swallowed cancel would hide stuck indexers.
func TestWaitIdle_RespectsContext(t *testing.T) {
	t.Parallel()

	mgr := NewCodeDBManager(t.TempDir(), codedbTestLogger(), nil)
	mgr.mu.Lock()
	mgr.indexing = true // never flipped back
	mgr.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := mgr.WaitIdle(ctx)
	require.Error(t, err, "WaitIdle must return ctx.Err() on deadline, not nil")
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

// poisonCommentMapping overwrites the latest snapshot's `_mapping` value
// with zero bytes — the exact on-disk state observed in the field.
func poisonCommentMapping(t *testing.T, root string) {
	t.Helper()
	boltPath := filepath.Join(root, "bleve", "comment", "store", "root.bolt")
	db, err := bbolt.Open(boltPath, 0600, nil)
	require.NoError(t, err)
	require.NoError(t, db.Update(func(tx *bbolt.Tx) error {
		snaps := tx.Bucket([]byte{'s'})
		require.NotNil(t, snaps)
		var lastKey []byte
		require.NoError(t, snaps.ForEach(func(k, _ []byte) error {
			lastKey = append(lastKey[:0], k...)
			return nil
		}))
		snap := snaps.Bucket(lastKey)
		require.NotNil(t, snap)
		internal := snap.Bucket([]byte{'i'})
		require.NotNil(t, internal)
		return internal.Put([]byte("_mapping"), []byte{})
	}))
	require.NoError(t, db.Close())
}
