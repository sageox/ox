//go:build memprofile

// Memory accounting harness for the codedb indexing pipeline.
//
// It measures the *peak* live heap of each phase (a phase-boundary snapshot
// misses in-phase spikes like the all-at-once blob prefetch), the bytes
// allocated per phase, GC pressure, and the resulting on-disk index sizes.
//
// Run against the current repo:
//
//	go test -tags=memprofile -run TestMemoryProfile_Pipeline -v -timeout 30m ./internal/codedb/index/
//
// Against another repo, and/or dump a pprof heap profile for drill-down:
//
//	CODEDB_PROFILE_REPO=/path/to/repo \
//	CODEDB_PROFILE_HEAP=/tmp/codedb.heap \
//	go test -tags=memprofile -run TestMemoryProfile_Pipeline -v -timeout 30m ./internal/codedb/index/
//	go tool pprof -top -sample_index=alloc_space /tmp/codedb.heap
package index

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/codedb/store"
)

func mb(b uint64) float64 { return float64(b) / (1024 * 1024) }

// peakSampler polls runtime.MemStats on an interval and records the maximum
// live heap (HeapInuse) observed, so an in-phase transient spike is captured
// even when the heap is reclaimed by the time the phase returns.
type peakSampler struct {
	stop     chan struct{}
	wg       sync.WaitGroup
	peakHeap atomic.Uint64
}

func startPeakSampler(every time.Duration) *peakSampler {
	ps := &peakSampler{stop: make(chan struct{})}
	ps.wg.Add(1)
	go func() {
		defer ps.wg.Done()
		var m runtime.MemStats
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ps.stop:
				return
			case <-t.C:
				runtime.ReadMemStats(&m)
				for {
					cur := ps.peakHeap.Load()
					if m.HeapInuse <= cur || ps.peakHeap.CompareAndSwap(cur, m.HeapInuse) {
						break
					}
				}
			}
		}
	}()
	return ps
}

func (ps *peakSampler) stopAndPeak() uint64 {
	close(ps.stop)
	ps.wg.Wait()
	return ps.peakHeap.Load()
}

// phase runs fn while sampling peak heap and reports per-phase memory.
func phase(t *testing.T, name string, fn func() error) {
	t.Helper()
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	ps := startPeakSampler(10 * time.Millisecond)
	start := time.Now()
	err := fn()
	dur := time.Since(start)
	peak := ps.stopAndPeak()

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	require.NoError(t, err)

	t.Logf("%-16s dur=%-9s peakHeapInuse=%7.1f MB  endHeapInuse=%7.1f MB  alloc=%8.1f MB  numGC=%d",
		name, dur.Round(time.Millisecond),
		mb(peak), mb(after.HeapInuse), mb(after.TotalAlloc-before.TotalAlloc),
		after.NumGC-before.NumGC)
}

// findRepoRoot walks up from start until it finds a directory containing .git.
func findRepoRoot(start string) string {
	dir := start
	for {
		if fi, err := os.Stat(filepath.Join(dir, ".git")); err == nil && (fi.IsDir() || fi.Mode().IsRegular()) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func dirSize(root string) uint64 {
	var total uint64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += uint64(info.Size())
		}
		return nil
	})
	return total
}

// TestMemoryProfile_Pipeline indexes a real repo end-to-end and reports the
// peak heap of each phase plus the resulting on-disk footprint. It is opt-in
// (build tag memprofile) and never runs in the normal suite.
func TestMemoryProfile_Pipeline(t *testing.T) {
	repoPath := os.Getenv("CODEDB_PROFILE_REPO")
	if repoPath == "" {
		cwd, err := os.Getwd()
		require.NoError(t, err)
		repoPath = findRepoRoot(cwd)
	}
	if repoPath == "" {
		t.Skip("no repo found; set CODEDB_PROFILE_REPO")
	}
	t.Logf("profiling repo: %s", repoPath)

	codedbDir := t.TempDir()
	s, err := store.Open(codedbDir)
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()

	ctx := context.Background()

	phase(t, "IndexLocalRepo", func() error { return IndexLocalRepo(ctx, s, repoPath, IndexOptions{}) })
	phase(t, "ParseSymbols", func() error { _, e := ParseSymbols(ctx, s, nil); return e })
	phase(t, "ParseComments", func() error { _, e := ParseComments(ctx, s, nil); return e })
	dirtyPath := DirtyIndexPath(s.Root, repoPath)
	phase(t, "BuildDirtyIndex", func() error { _, e := BuildDirtyIndex(ctx, repoPath, dirtyPath, IndexOptions{}); return e })

	// Steady-state: live heap after a forced GC, and on-disk index sizes.
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	t.Logf("STEADY-STATE     liveHeapInuse=%7.1f MB  heapSys=%7.1f MB  totalSys=%7.1f MB",
		mb(ms.HeapInuse), mb(ms.HeapSys), mb(ms.Sys))

	bleveBytes := dirSize(filepath.Join(codedbDir, "bleve"))
	sqlBytes := dirSize(filepath.Join(codedbDir, "metadata.db"))
	t.Logf("ON-DISK          bleve=%7.1f MB  sqlite=%7.1f MB  total=%7.1f MB",
		mb(bleveBytes), mb(sqlBytes), mb(dirSize(codedbDir)))

	if out := os.Getenv("CODEDB_PROFILE_HEAP"); out != "" {
		f, err := os.Create(out)
		require.NoError(t, err)
		defer f.Close()
		runtime.GC()
		require.NoError(t, pprof.WriteHeapProfile(f))
		t.Logf("heap profile written: %s (go tool pprof -top -sample_index=alloc_space %s)", out, out)
	}
}
