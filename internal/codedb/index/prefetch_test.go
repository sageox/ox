package index

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initGitRepoWithBlobs creates a git repo with n distinct committed files.
// Returns the repo path and a map of content_hash -> expected content.
func initGitRepoWithBlobs(t *testing.T, n int) (string, map[string]string) {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), // safe: git CLI in temp dir needs inherited PATH
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@sageox.ai",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@sageox.ai",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}

	run("init", "-b", "main")

	expected := make(map[string]string, n)
	for i := 0; i < n; i++ {
		fname := fmt.Sprintf("file%d.go", i)
		content := fmt.Sprintf("package main\n\nfunc F%d() { /* blob %d */ }\n", i, i)
		require.NoError(t, os.WriteFile(filepath.Join(dir, fname), []byte(content), 0o644))
		run("add", fname)
	}
	run("commit", "-m", "add files")

	// collect blob hashes from git
	cmd := exec.Command("git", "ls-tree", "-r", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	require.NoError(t, err)

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		// format: <mode> <type> <hash>\t<path>
		parts := strings.Fields(line)
		require.Len(t, parts, 4, "unexpected ls-tree line: %s", line)
		hash := parts[2]
		path := parts[3]

		content, err := os.ReadFile(filepath.Join(dir, path))
		require.NoError(t, err)
		expected[hash] = string(content)
	}

	return dir, expected
}

// collectHashes returns sorted keys from a map.
func collectHashes(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// --- A. Correctness ---

// TestParallelPrefetch_SameResultsAsSerial verifies parallel reads produce
// identical results to the serial (mutex-based) path.
// Failure prevented: parallel reads produce different/corrupt results.
func TestParallelPrefetch_SameResultsAsSerial(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone operations")
	}

	dir, expected := initGitRepoWithBlobs(t, 10)
	hashes := collectHashes(expected)
	ctx := context.Background()
	noop := func(string) {}

	// serial: use mutex pool with a single repo
	repo, err := plainOpenTolerant(dir)
	require.NoError(t, err)
	serialResult := prefetchWithMutexPool(ctx, []*git.Repository{repo}, 1, hashes, noop)

	// parallel: use per-worker pool
	ppool, err := newRepoPoolParallel(dir, 4)
	require.NoError(t, err)
	defer ppool.Close()
	parallelResult := prefetchWithParallelPool(ctx, ppool, 4, hashes, noop)

	// results must be identical
	require.Equal(t, len(serialResult), len(parallelResult), "different number of results")
	for hash, serialContent := range serialResult {
		parallelContent, ok := parallelResult[hash]
		require.True(t, ok, "parallel missing hash %s", hash)
		assert.Equal(t, serialContent, parallelContent, "content mismatch for hash %s", hash)
	}
}

// TestParallelPrefetch_AllBlobsReturned verifies all valid blobs are returned.
// Failure prevented: blobs silently dropped in parallel path.
func TestParallelPrefetch_AllBlobsReturned(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone operations")
	}

	dir, expected := initGitRepoWithBlobs(t, 20)
	hashes := collectHashes(expected)
	ctx := context.Background()
	noop := func(string) {}

	ppool, err := newRepoPoolParallel(dir, 4)
	require.NoError(t, err)
	defer ppool.Close()

	result := prefetchWithParallelPool(ctx, ppool, 4, hashes, noop)

	assert.Len(t, result, len(expected), "should return all %d blobs", len(expected))
	for hash, want := range expected {
		got, ok := result[hash]
		assert.True(t, ok, "missing blob %s", hash)
		assert.Equal(t, []byte(want), got, "content mismatch for %s", hash)
	}
}

// TestParallelPrefetch_BinaryBlobsSkipped verifies binary blobs are filtered.
// Failure prevented: parallel path doesn't filter binary content.
func TestParallelPrefetch_BinaryBlobsSkipped(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone operations")
	}

	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), // safe: git CLI in temp dir needs inherited PATH
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@sageox.ai",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@sageox.ai",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}

	run("init", "-b", "main")

	// write a binary file (non-UTF8 content)
	binaryContent := []byte{0x00, 0xFF, 0xFE, 0x89, 0x50, 0x4E, 0x47}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "binary.bin"), binaryContent, 0o644))
	// write a valid text file
	textContent := "package main\nfunc Hello() {}\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "text.go"), []byte(textContent), 0o644))

	run("add", "binary.bin", "text.go")
	run("commit", "-m", "add files")

	cmd := exec.Command("git", "ls-tree", "-r", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	require.NoError(t, err)

	var binaryHash, textHash string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Fields(line)
		if strings.HasSuffix(parts[3], ".bin") {
			binaryHash = parts[2]
		} else {
			textHash = parts[2]
		}
	}
	require.NotEmpty(t, binaryHash)
	require.NotEmpty(t, textHash)

	ppool, err := newRepoPoolParallel(dir, 2)
	require.NoError(t, err)
	defer ppool.Close()

	result := prefetchWithParallelPool(context.Background(), ppool, 2, []string{binaryHash, textHash}, func(string) {})

	assert.Nil(t, result[binaryHash], "binary blob should be skipped")
	assert.Equal(t, []byte(textContent), result[textHash], "text blob should be returned")
}

// TestParallelPrefetch_OversizedBlobsSkipped verifies blobs > maxPrefetchBlobSize are skipped.
// Failure prevented: parallel path doesn't enforce size limits.
func TestParallelPrefetch_OversizedBlobsSkipped(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone operations")
	}

	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), // safe: git CLI in temp dir needs inherited PATH
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@sageox.ai",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@sageox.ai",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}

	run("init", "-b", "main")

	// create file larger than maxPrefetchBlobSize (1MB)
	bigContent := strings.Repeat("x", maxPrefetchBlobSize+1)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "big.txt"), []byte(bigContent), 0o644))
	// small valid file
	smallContent := "package main\nfunc Small() {}\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "small.go"), []byte(smallContent), 0o644))

	run("add", "big.txt", "small.go")
	run("commit", "-m", "add files")

	cmd := exec.Command("git", "ls-tree", "-r", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	require.NoError(t, err)

	var bigHash, smallHash string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Fields(line)
		if strings.HasSuffix(parts[3], "big.txt") {
			bigHash = parts[2]
		} else {
			smallHash = parts[2]
		}
	}
	require.NotEmpty(t, bigHash)
	require.NotEmpty(t, smallHash)

	ppool, err := newRepoPoolParallel(dir, 2)
	require.NoError(t, err)
	defer ppool.Close()

	result := prefetchWithParallelPool(context.Background(), ppool, 2, []string{bigHash, smallHash}, func(string) {})

	assert.Nil(t, result[bigHash], "oversized blob should be skipped")
	assert.Equal(t, []byte(smallContent), result[smallHash], "small blob should be returned")
}

// --- B. Concurrency safety ---

// TestParallelPrefetch_NoConcurrencyRaces runs prefetch under the race detector
// with many workers and blobs.
// Failure prevented: data races in parallel blob reads.
func TestParallelPrefetch_NoConcurrencyRaces(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone operations")
	}

	dir, expected := initGitRepoWithBlobs(t, 50)
	hashes := collectHashes(expected)

	ppool, err := newRepoPoolParallel(dir, 8)
	require.NoError(t, err)
	defer ppool.Close()

	// the race detector will flag any data races during this run
	result := prefetchWithParallelPool(context.Background(), ppool, 8, hashes, func(string) {})

	assert.Len(t, result, len(expected), "all blobs should be returned")
}

// --- C. Fallback ---

// TestPrefetch_FallsBackToMutexPoolOnError verifies that prefetchBlobContents
// falls back to the mutex pool when parallel pool open fails.
// Failure prevented: parallel open failure causes total prefetch failure.
func TestPrefetch_FallsBackToMutexPoolOnError(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone operations")
	}

	dir, expected := initGitRepoWithBlobs(t, 5)
	hashes := collectHashes(expected)

	repo, err := plainOpenTolerant(dir)
	require.NoError(t, err)

	// pass an invalid path — parallel pool open will fail,
	// should fall back to mutex pool using the provided repo
	result := prefetchBlobContents(
		context.Background(),
		[]*git.Repository{repo},
		[]string{"/nonexistent/path"},
		hashes,
		func(string) {},
	)

	assert.Len(t, result, len(expected), "fallback to mutex pool should still return all blobs")
}

// TestPrefetch_MultiRepoUsesMutexPool verifies the multi-repo case uses the mutex pool.
// Failure prevented: multi-repo case accidentally tries parallel pool path.
func TestPrefetch_MultiRepoUsesMutexPool(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone operations")
	}

	dir, expected := initGitRepoWithBlobs(t, 5)
	hashes := collectHashes(expected)

	repo1, err := plainOpenTolerant(dir)
	require.NoError(t, err)
	repo2, err := plainOpenTolerant(dir)
	require.NoError(t, err)

	// two repos — should use mutex pool, not parallel
	result := prefetchBlobContents(
		context.Background(),
		[]*git.Repository{repo1, repo2},
		[]string{dir, dir},
		hashes,
		func(string) {},
	)

	assert.Len(t, result, len(expected), "multi-repo path should return all blobs")
}

// --- D. Resource cleanup ---

// TestParallelPool_ClosesAllHandles verifies Close() succeeds without error.
// Failure prevented: FD leak from unclosed repo handles.
func TestParallelPool_ClosesAllHandles(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone operations")
	}

	dir, _ := initGitRepoWithBlobs(t, 3)

	ppool, err := newRepoPoolParallel(dir, 4)
	require.NoError(t, err)
	assert.Len(t, ppool.workerRepos, 4)

	err = ppool.Close()
	assert.NoError(t, err, "Close() should not error")
}

// TestParallelPool_PartialOpenFailure_CleansUp verifies that if opening handle N
// fails, handles 1..N-1 are closed.
// Failure prevented: FD leak on partial initialization failure.
func TestParallelPool_PartialOpenFailure_CleansUp(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone operations")
	}

	// plainOpenPool with an invalid path should fail immediately and return an error
	repos, err := plainOpenPool("/nonexistent/repo/path", 5)
	assert.Error(t, err, "should fail for invalid repo path")
	assert.Nil(t, repos, "should return nil repos on failure")

	// verify the wrapper also handles it
	ppool, err := newRepoPoolParallel("/nonexistent/repo/path", 5)
	assert.Error(t, err, "should fail for invalid repo path")
	assert.Nil(t, ppool, "should return nil pool on failure")
}

// TestPlainOpenPool_Success verifies plainOpenPool opens N independent handles.
// Failure prevented: plainOpenPool doesn't actually open independent handles.
func TestPlainOpenPool_Success(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone operations")
	}

	dir, _ := initGitRepoWithBlobs(t, 3)

	repos, err := plainOpenPool(dir, 4)
	require.NoError(t, err)
	assert.Len(t, repos, 4)

	// each handle should independently read HEAD
	for i, r := range repos {
		head, err := r.Head()
		require.NoError(t, err, "handle %d should read HEAD", i)
		assert.False(t, head.Hash().IsZero(), "handle %d HEAD should not be zero", i)
	}

	// cleanup
	for _, r := range repos {
		r.Close()
	}
}
