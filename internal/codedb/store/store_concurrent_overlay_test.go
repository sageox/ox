package store

import (
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// NOTE: Store overlay mutations (AttachDirtyIndexByID, DetachDirtyIndexByID,
// DetachDirtyOverlay) are NOT goroutine-safe by design. The daemon serializes
// all Store mutations and CombinedCodeIndex reads via its own mutex. These
// tests mirror that pattern: a shared mutex guards both mutations AND reads of
// CombinedCodeIndex (the snapshot), while the actual Bleve Search call runs
// outside the lock (Bleve IndexAlias is internally safe for concurrent reads
// once you have a reference).
//
// What these tests verify:
//   - Serialized overlay mutations interleaved with concurrent searches don't
//     cause panics, deadlocks, or corrupted results
//   - Multiple goroutines can search via a snapshotted IndexAlias concurrently
//   - Search correctness transitions cleanly as overlays attach and detach

// createTestOverlay builds an on-disk Bleve index with the given number of
// documents, closes it, and returns the path ready for AttachDirtyIndexByID.
func createTestOverlay(t *testing.T, id string, docs int) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), id)
	mapping := bleve.NewIndexMapping()
	idx, err := bleve.New(dir, mapping)
	require.NoError(t, err)
	for i := 0; i < docs; i++ {
		doc := map[string]interface{}{
			"content":  fmt.Sprintf("overlay %s doc %d searchable content", id, i),
			"filepath": fmt.Sprintf("file_%d.go", i),
		}
		require.NoError(t, idx.Index(fmt.Sprintf("dirty_%s_%d", id, i), doc))
	}
	require.NoError(t, idx.Close())
	return dir
}

// seedBaselineIndex populates the store's CodeIndex with documents so searches
// always have something to match against.
func seedBaselineIndex(t *testing.T, s *Store, docs int) {
	t.Helper()
	for i := 0; i < docs; i++ {
		doc := map[string]interface{}{
			"content":  fmt.Sprintf("baseline doc %d with searchable text", i),
			"filepath": fmt.Sprintf("baseline_%d.go", i),
		}
		require.NoError(t, s.CodeIndex.Index(fmt.Sprintf("base_%d", i), doc))
	}
}

// guardedSearchLoop snapshots CombinedCodeIndex under the mutex (mirroring
// daemon behavior), then runs the search outside the lock. This is the
// correct concurrent access pattern for the Store.
func guardedSearchLoop(t *testing.T, s *Store, mu *sync.RWMutex, done <-chan struct{}, errCh chan<- error, completed *atomic.Int64) {
	t.Helper()
	query := bleve.NewMatchQuery("searchable")
	for {
		select {
		case <-done:
			return
		default:
			// snapshot under read lock (daemon does this)
			mu.RLock()
			idx := s.CombinedCodeIndex
			mu.RUnlock()

			if idx == nil {
				errCh <- fmt.Errorf("CombinedCodeIndex is nil")
				return
			}
			req := bleve.NewSearchRequest(query)
			req.Size = 10
			_, err := idx.Search(req)
			if err != nil {
				// during detach a closed index may surface an error;
				// the old IndexAlias reference is stale but shouldn't
				// panic -- report and continue so callers see failures
				errCh <- fmt.Errorf("search on stale alias: %w", err)
				return
			}
			completed.Add(1)
		}
	}
}

// --- A. Concurrent search during overlay changes ---

// TestConcurrentSearchDuringOverlayAttach verifies that ongoing searches
// don't panic or return errors when overlays are being attached.
// Failure prevented: CombinedCodeIndex reassignment mid-search causes nil
// dereference or stale alias.
func TestConcurrentSearchDuringOverlayAttach(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: concurrent Bleve + SQLite operations")
	}

	s := openStore(t)
	seedBaselineIndex(t, s, 50)

	// pre-build 5 overlay indexes on disk
	overlayPaths := make([]string, 5)
	for i := range overlayPaths {
		overlayPaths[i] = createTestOverlay(t, fmt.Sprintf("attach_%d", i), 10)
	}

	// RWMutex mirrors daemon serialization: writes hold exclusive lock,
	// reads (CombinedCodeIndex snapshot) hold shared lock
	var mu sync.RWMutex

	done := make(chan struct{})
	errCh := make(chan error, 20)
	var completed atomic.Int64

	// start 10 search goroutines
	var searchWg sync.WaitGroup
	for i := 0; i < 10; i++ {
		searchWg.Add(1)
		go func() {
			defer searchWg.Done()
			guardedSearchLoop(t, s, &mu, done, errCh, &completed)
		}()
	}

	// attach overlays one by one from the main goroutine
	for i, p := range overlayPaths {
		time.Sleep(100 * time.Millisecond)
		mu.Lock()
		require.NoError(t, s.AttachDirtyIndexByID(fmt.Sprintf("ov_%d", i), p))
		mu.Unlock()
	}

	// let searches run a bit after all overlays attached
	time.Sleep(200 * time.Millisecond)
	close(done)

	waitDone := make(chan struct{})
	go func() {
		searchWg.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
	case <-time.After(10 * time.Second):
		t.Fatal("search goroutines did not finish within timeout")
	}

	close(errCh)
	for err := range errCh {
		t.Errorf("search error: %v", err)
	}

	assert.Greater(t, completed.Load(), int64(0), "at least some searches should have completed")
	assert.Equal(t, 5, s.DirtyOverlayCount(), "all 5 overlays should be attached")
}

// TestConcurrentSearchDuringOverlayDetach verifies that detaching overlays
// while searches are running doesn't cause panics or invalid results.
// Failure prevented: closing a Bleve index that's mid-search causes panic.
func TestConcurrentSearchDuringOverlayDetach(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: concurrent Bleve + SQLite operations")
	}

	s := openStore(t)
	seedBaselineIndex(t, s, 50)

	// attach 5 overlays
	for i := 0; i < 5; i++ {
		p := createTestOverlay(t, fmt.Sprintf("detach_%d", i), 10)
		require.NoError(t, s.AttachDirtyIndexByID(fmt.Sprintf("ov_%d", i), p))
	}

	var mu sync.RWMutex
	done := make(chan struct{})
	errCh := make(chan error, 20)
	var completed atomic.Int64

	// start 10 search goroutines
	var searchWg sync.WaitGroup
	for i := 0; i < 10; i++ {
		searchWg.Add(1)
		go func() {
			defer searchWg.Done()
			guardedSearchLoop(t, s, &mu, done, errCh, &completed)
		}()
	}

	// detach overlays one by one
	for i := 0; i < 5; i++ {
		time.Sleep(100 * time.Millisecond)
		mu.Lock()
		s.DetachDirtyIndexByID(fmt.Sprintf("ov_%d", i))
		mu.Unlock()
	}

	// let searches continue briefly after all detached
	time.Sleep(200 * time.Millisecond)
	close(done)

	waitDone := make(chan struct{})
	go func() {
		searchWg.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
	case <-time.After(10 * time.Second):
		t.Fatal("search goroutines did not finish within timeout")
	}

	close(errCh)
	for err := range errCh {
		t.Errorf("search error: %v", err)
	}

	assert.Greater(t, completed.Load(), int64(0), "at least some searches should have completed")
	assert.Equal(t, 0, s.DirtyOverlayCount(), "all overlays should be detached")
}

// TestConcurrentSearchDuringAttachDetachCycle verifies the full lifecycle:
// attach, search, detach, re-attach -- all with concurrent readers.
// Failure prevented: combination of attach+detach+search causes deadlock or corruption.
func TestConcurrentSearchDuringAttachDetachCycle(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: concurrent Bleve + SQLite operations")
	}

	s := openStore(t)
	seedBaselineIndex(t, s, 50)

	// pre-build 3 overlay indexes for the first cycle
	overlayPaths := make([]string, 3)
	for i := range overlayPaths {
		overlayPaths[i] = createTestOverlay(t, fmt.Sprintf("cycle_%d", i), 10)
	}

	var mu sync.RWMutex
	done := make(chan struct{})
	errCh := make(chan error, 20)
	var completed atomic.Int64

	// start 10 search goroutines
	var searchWg sync.WaitGroup
	for i := 0; i < 10; i++ {
		searchWg.Add(1)
		go func() {
			defer searchWg.Done()
			guardedSearchLoop(t, s, &mu, done, errCh, &completed)
		}()
	}

	// cycle attach/detach from the main goroutine
	// each cycle: attach all overlays, pause, detach all, pause
	for cycle := 0; cycle < 3; cycle++ {
		for i, p := range overlayPaths {
			// re-create overlay for subsequent cycles since detach closes the index
			if cycle > 0 {
				p = createTestOverlay(t, fmt.Sprintf("cycle_%d_r%d", i, cycle), 10)
			}
			mu.Lock()
			require.NoError(t, s.AttachDirtyIndexByID(fmt.Sprintf("ov_%d", i), p))
			mu.Unlock()
		}
		time.Sleep(150 * time.Millisecond)

		mu.Lock()
		s.DetachDirtyOverlay()
		mu.Unlock()
		time.Sleep(100 * time.Millisecond)
	}

	close(done)

	waitDone := make(chan struct{})
	go func() {
		searchWg.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
	case <-time.After(15 * time.Second):
		t.Fatal("search goroutines did not finish within timeout")
	}

	close(errCh)
	for err := range errCh {
		t.Errorf("search error: %v", err)
	}

	assert.Greater(t, completed.Load(), int64(0), "searches should have completed during cycling")
}

// --- B. Concurrent overlay mutations ---

// TestConcurrentAttachDetach_NoRace verifies that serialized overlay mutations
// interleaved across goroutines don't corrupt the store's internal state.
// Failure prevented: map[string]bleve.Index concurrent read/write panic.
//
// Uses a mutex to simulate the daemon's serialization pattern. The value here
// is verifying that with proper external serialization, the internal state
// remains consistent even when many goroutines are competing for access.
func TestConcurrentAttachDetach_NoRace(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: concurrent Bleve + SQLite operations")
	}

	s := openStore(t)
	seedBaselineIndex(t, s, 20)

	const overlayCount = 10
	overlayPaths := make([]string, overlayCount)
	for i := range overlayPaths {
		overlayPaths[i] = createTestOverlay(t, fmt.Sprintf("race_%d", i), 5)
	}

	// simulate daemon-level serialization with a mutex
	var mu sync.Mutex
	var wg sync.WaitGroup

	// 5 goroutines each attach and then detach different overlays
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			// each goroutine owns 2 overlay IDs
			for i := 0; i < 2; i++ {
				idx := goroutineID*2 + i
				if idx >= overlayCount {
					return
				}
				id := fmt.Sprintf("ov_%d", idx)

				mu.Lock()
				err := s.AttachDirtyIndexByID(id, overlayPaths[idx])
				// snapshot the combined index while still holding the lock
				cIdx := s.CombinedCodeIndex
				mu.Unlock()

				if err != nil {
					t.Errorf("attach %s: %v", id, err)
					return
				}

				// search outside the lock -- safe on the snapshotted alias
				query := bleve.NewMatchQuery("searchable")
				req := bleve.NewSearchRequest(query)
				req.Size = 5
				if cIdx != nil {
					res, searchErr := cIdx.Search(req)
					if searchErr != nil {
						t.Errorf("search after attach %s: %v", id, searchErr)
					} else if res.Total == 0 {
						t.Errorf("search after attach %s: expected results, got 0", id)
					}
				}

				// brief pause to interleave with other goroutines
				time.Sleep(10 * time.Millisecond)

				mu.Lock()
				s.DetachDirtyIndexByID(id)
				mu.Unlock()
			}
		}(g)
	}

	waitDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
	case <-time.After(10 * time.Second):
		t.Fatal("goroutines did not finish within timeout")
	}

	// after all attach/detach cycles, store should be clean
	assert.Equal(t, 0, s.DirtyOverlayCount(), "all overlays should be detached")
}

// --- C. Search result correctness during transitions ---

// TestSearchResultsIncludeNewOverlay verifies that after an overlay is attached,
// subsequent searches include its content.
// Failure prevented: rebuildCombinedIndex silently drops new overlay from alias.
func TestSearchResultsIncludeNewOverlay(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: Bleve + SQLite operations")
	}

	s := openStore(t)
	seedBaselineIndex(t, s, 10)

	// search before overlay -- should find baseline only
	query := bleve.NewMatchQuery("searchable")
	req := bleve.NewSearchRequest(query)
	before, err := s.CombinedCodeIndex.Search(req)
	require.NoError(t, err)
	baselineHits := before.Total

	// attach overlay with searchable content
	p := createTestOverlay(t, "verify", 5)
	require.NoError(t, s.AttachDirtyIndexByID("verify", p))

	// search after overlay -- should find baseline + overlay
	req = bleve.NewSearchRequest(query)
	after, err := s.CombinedCodeIndex.Search(req)
	require.NoError(t, err)

	assert.Greater(t, after.Total, baselineHits,
		"search after overlay attach should return more results")
}

// TestSearchResultsExcludeDetachedOverlay verifies that after an overlay is
// detached, its content no longer appears in search results.
// Failure prevented: stale alias retains reference to closed index.
func TestSearchResultsExcludeDetachedOverlay(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: Bleve + SQLite operations")
	}

	s := openStore(t)

	// attach overlay with unique token
	p := createTestOverlay(t, "ephemeral", 3)
	require.NoError(t, s.AttachDirtyIndexByID("ephemeral", p))

	// verify it's searchable
	query := bleve.NewMatchQuery("ephemeral")
	req := bleve.NewSearchRequest(query)
	result, err := s.CombinedCodeIndex.Search(req)
	require.NoError(t, err)
	assert.Greater(t, result.Total, uint64(0), "overlay content should be findable")

	// detach
	s.DetachDirtyIndexByID("ephemeral")

	// verify it's gone
	req = bleve.NewSearchRequest(query)
	result, err = s.CombinedCodeIndex.Search(req)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), result.Total, "detached overlay content must not appear")
}
