package codedb_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/index"
	"github.com/sageox/ox/internal/codedb/store"
)

// The crash-loop this fix targets: an indexing pass fails against a half-written
// cache, and every retry fails identically. OpenIndexWithHeal must discard the
// cache and retry exactly once on a corruption-class error.
func TestOpenIndexWithHeal_DiscardsAndRetriesOnceOnCorruption(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "codedb")
	marker := filepath.Join(dataDir, "CORRUPT_MARKER")

	calls := 0
	indexFn := func(ctx context.Context, db *codedb.DB) error {
		calls++
		if calls == 1 {
			// leave a marker to prove the cache is discarded before the retry
			if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
				t.Fatalf("write marker: %v", err)
			}
			return index.ErrBleveCorrupt
		}
		// second attempt: the discard must have wiped the whole cache dir
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("expected cache discarded before retry, marker still present (err=%v)", err)
		}
		return nil
	}

	db, err := codedb.OpenIndexWithHeal(context.Background(), dataDir, indexFn)
	if err != nil {
		t.Fatalf("OpenIndexWithHeal: %v", err)
	}
	defer db.Close()
	if calls != 2 {
		t.Fatalf("expected exactly 2 index attempts (fail + one retry), got %d", calls)
	}
}

// The security-review guard: a timeout/cancel must NOT be treated as corruption.
// The cache is preserved and no retry is attempted.
func TestOpenIndexWithHeal_DoesNotDiscardOnNonCorruption(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "codedb")
	marker := filepath.Join(dataDir, "KEEP_ME")

	calls := 0
	indexFn := func(ctx context.Context, db *codedb.DB) error {
		calls++
		if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
			t.Fatalf("write marker: %v", err)
		}
		return context.Canceled
	}

	db, err := codedb.OpenIndexWithHeal(context.Background(), dataDir, indexFn)
	if db != nil {
		db.Close()
		t.Fatal("expected nil DB when the pass fails with a non-corruption error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled returned as-is, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected no retry on a non-corruption error, got %d attempts", calls)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("expected cache preserved on non-corruption failure, marker missing: %v", statErr)
	}
}

// A kill mid from-scratch build must never leave a half-written cache: the prior
// healthy cache stays untouched and only the temp build dir is cleaned up.
func TestBuildCodeDBAtomic_PreservesFinalDirOnFailure(t *testing.T) {
	finalDir := filepath.Join(t.TempDir(), "codedb")
	if err := os.MkdirAll(finalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(finalDir, "GOOD")
	if err := os.WriteFile(good, []byte("healthy"), 0o644); err != nil {
		t.Fatal(err)
	}

	wantErr := errors.New("build blew up")
	err := codedb.BuildCodeDBAtomic(context.Background(), finalDir, func(ctx context.Context, db *codedb.DB) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected the build error propagated, got %v", err)
	}
	if _, statErr := os.Stat(good); statErr != nil {
		t.Fatalf("expected finalDir preserved on failure, GOOD missing: %v", statErr)
	}
	if leftovers, _ := filepath.Glob(finalDir + ".*"); len(leftovers) != 0 {
		t.Fatalf("expected staging/backup dirs cleaned up on failure, found %v", leftovers)
	}
}

// A successful from-scratch build atomically replaces the stale cache.
func TestBuildCodeDBAtomic_SwapsFreshBuildIntoPlace(t *testing.T) {
	finalDir := filepath.Join(t.TempDir(), "codedb")
	if err := os.MkdirAll(finalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(finalDir, "STALE")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	built := false
	err := codedb.BuildCodeDBAtomic(context.Background(), finalDir, func(ctx context.Context, db *codedb.DB) error {
		built = true // db is a real open store rooted at the temp build dir
		return nil
	})
	if err != nil {
		t.Fatalf("BuildCodeDBAtomic: %v", err)
	}
	if !built {
		t.Fatal("build closure was not invoked")
	}
	if _, statErr := os.Stat(stale); !os.IsNotExist(statErr) {
		t.Fatalf("expected stale cache replaced, STALE still present (err=%v)", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(finalDir, "repos")); statErr != nil {
		t.Fatalf("expected the freshly-built store swapped in (repos/ dir), err=%v", statErr)
	}
	if leftovers, _ := filepath.Glob(finalDir + ".*"); len(leftovers) != 0 {
		t.Fatalf("expected no staging/backup leftovers after swap, found %v", leftovers)
	}
}

// Concurrent from-scratch builds share the ledger cache (one per worktree). A
// fixed staging path let one build delete another's in-progress files and could
// leave finalDir absent. With per-build staging dirs, finalDir must always end
// as a valid, openable store — never absent or half-written.
func TestBuildCodeDBAtomic_ConcurrentBuildsLeaveValidCache(t *testing.T) {
	if testing.Short() {
		t.Skip("short: opens several real stores concurrently")
	}
	finalDir := filepath.Join(t.TempDir(), "codedb")

	const builders = 4
	var wg sync.WaitGroup
	for i := 0; i < builders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A promotion race may make one builder error; that is acceptable, as
			// long as no builder ever corrupts or removes the published cache.
			_ = codedb.BuildCodeDBAtomic(context.Background(), finalDir, func(ctx context.Context, db *codedb.DB) error {
				return nil
			})
		}()
	}
	wg.Wait()

	// finalDir must exist and open cleanly — the data-loss failure was an absent
	// finalDir after an overlapping build removed a just-published cache.
	if _, statErr := os.Stat(filepath.Join(finalDir, "repos")); statErr != nil {
		t.Fatalf("finalDir must be a valid store after concurrent builds, err=%v", statErr)
	}
	db, err := codedb.Open(finalDir)
	if err != nil {
		t.Fatalf("published cache must open cleanly after concurrent builds: %v", err)
	}
	_ = db.Close()
	if leftovers, _ := filepath.Glob(finalDir + ".*"); len(leftovers) != 0 {
		t.Fatalf("expected no staging/backup leftovers after concurrent builds, found %v", leftovers)
	}
}

// A read-only store has no writable path through OpenIndexWithHeal: indexFn
// only writes, and both recovery arms (marker escalation, corruption retry)
// wipe dataDir. Refusing up front keeps a mounted read-only index from being
// half-processed and, more importantly, from reaching os.RemoveAll.
func TestOpenIndexWithHeal_RefusesReadOnlyStore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod does not make a directory unwritable on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod does not deny writes")
	}

	dataDir := filepath.Join(t.TempDir(), "codedb")
	seed, err := codedb.Open(dataDir)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}
	if out, err := exec.Command("chmod", "-R", "a-w", dataDir).CombinedOutput(); err != nil {
		t.Fatalf("chmod: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("chmod", "-R", "u+w", dataDir).Run() })

	called := false
	db, err := codedb.OpenIndexWithHeal(context.Background(), dataDir,
		func(context.Context, *codedb.DB) error { called = true; return nil })
	if err == nil {
		_ = db.Close()
		t.Fatal("OpenIndexWithHeal succeeded on a read-only store; want refusal")
	}
	if !errors.Is(err, store.ErrReadOnly) {
		t.Errorf("error = %v, want store.ErrReadOnly", err)
	}
	if called {
		t.Error("indexFn ran against a read-only store")
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, store.MetadataDBFile)); statErr != nil {
		t.Errorf("metadata.db missing after refusal: %v", statErr)
	}
}
