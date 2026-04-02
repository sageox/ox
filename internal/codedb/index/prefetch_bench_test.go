package index

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/go-git/go-git/v6"
)

// initBenchRepo creates a git repo with n committed files and returns the repo
// path plus a slice of blob content hashes. Uses b.TempDir for automatic cleanup.
func initBenchRepo(b *testing.B, n int) (string, []string) {
	b.Helper()
	dir := b.TempDir()

	run := func(args ...string) {
		b.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), // safe: git subprocess in temp dir, not ox CLI
			"GIT_AUTHOR_NAME=bench",
			"GIT_AUTHOR_EMAIL=bench@sageox.ai",
			"GIT_COMMITTER_NAME=bench",
			"GIT_COMMITTER_EMAIL=bench@sageox.ai",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			b.Fatalf("git %v: %s", args, out)
		}
	}

	run("init", "-b", "main")

	// create files in a subdirectory to keep ls-tree output clean
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < n; i++ {
		fname := filepath.Join(srcDir, fmt.Sprintf("file%d.go", i))
		// unique content per file so each produces a distinct blob hash
		content := fmt.Sprintf("package src\n\n// blob %d\nfunc F%d() {\n\tprintln(%q)\n}\n", i, i, fmt.Sprintf("value-%d", i))
		if err := os.WriteFile(fname, []byte(content), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	run("add", ".")
	run("commit", "-m", "add bench files")

	// collect blob hashes
	cmd := exec.Command("git", "ls-tree", "-r", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		b.Fatal(err)
	}

	var hashes []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 4 && parts[1] == "blob" {
			hashes = append(hashes, parts[2])
		}
	}
	if len(hashes) == 0 {
		b.Fatal("no blob hashes collected from repo")
	}
	return dir, hashes
}

// BenchmarkPrefetchMutexPool measures blob prefetch using the mutex-based repoPool.
// This is the baseline (pre-#259 behavior): all workers contend on a single mutex.
func BenchmarkPrefetchMutexPool(b *testing.B) {
	if testing.Short() {
		b.Skip("short: git operations")
	}

	dir, hashes := initBenchRepo(b, 50)
	repo, err := plainOpenTolerant(dir)
	if err != nil {
		b.Fatal(err)
	}
	defer repo.Close()

	ctx := context.Background()
	noop := func(string) {}
	workers := runtime.GOMAXPROCS(0)
	if workers > len(hashes) {
		workers = len(hashes)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		prefetchWithMutexPool(ctx, []*git.Repository{repo}, workers, hashes, noop)
	}
}

// BenchmarkPrefetchParallelPool measures blob prefetch using per-worker repo handles.
// This is the optimized path (#259): each worker has its own go-git handle, no locks.
func BenchmarkPrefetchParallelPool(b *testing.B) {
	if testing.Short() {
		b.Skip("short: git operations")
	}

	dir, hashes := initBenchRepo(b, 50)
	ppool, err := newRepoPoolParallel(dir, runtime.GOMAXPROCS(0))
	if err != nil {
		b.Fatal(err)
	}
	defer ppool.Close()

	ctx := context.Background()
	noop := func(string) {}
	workers := runtime.GOMAXPROCS(0)
	if workers > len(hashes) {
		workers = len(hashes)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		prefetchWithParallelPool(ctx, ppool, workers, hashes, noop)
	}
}

// BenchmarkPrefetchEndToEnd compares the full prefetchBlobContents function
// at different blob counts. This exercises the auto-selection between mutex
// and parallel pools (single-repo case triggers parallel).
func BenchmarkPrefetchEndToEnd(b *testing.B) {
	if testing.Short() {
		b.Skip("short: git operations")
	}

	for _, blobCount := range []int{10, 50, 200} {
		b.Run(fmt.Sprintf("blobs_%d", blobCount), func(b *testing.B) {
			dir, hashes := initBenchRepo(b, blobCount)
			repo, err := plainOpenTolerant(dir)
			if err != nil {
				b.Fatal(err)
			}
			defer repo.Close()

			ctx := context.Background()
			noop := func(string) {}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				prefetchBlobContents(ctx, []*git.Repository{repo}, []string{dir}, hashes, noop)
			}
		})
	}
}

// BenchmarkReadBlobMutex measures raw single-blob read throughput through the mutex pool.
func BenchmarkReadBlobMutex(b *testing.B) {
	if testing.Short() {
		b.Skip("short: git operations")
	}

	dir, hashes := initBenchRepo(b, 10)
	repo, err := plainOpenTolerant(dir)
	if err != nil {
		b.Fatal(err)
	}
	defer repo.Close()

	pool := newRepoPool([]*git.Repository{repo})
	hash := hashes[0]

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pool.readBlob(hash)
	}
}

// BenchmarkReadBlobParallel measures raw single-blob read throughput through a parallel pool handle.
func BenchmarkReadBlobParallel(b *testing.B) {
	if testing.Short() {
		b.Skip("short: git operations")
	}

	dir, hashes := initBenchRepo(b, 10)
	ppool, err := newRepoPoolParallel(dir, 1)
	if err != nil {
		b.Fatal(err)
	}
	defer ppool.Close()

	hash := hashes[0]

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ppool.readBlob(0, hash)
	}
}
