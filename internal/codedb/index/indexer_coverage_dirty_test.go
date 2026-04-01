package index

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// --- BuildDirtyIndex edge cases ---

func TestBuildDirtyIndex_SkipsNonCodeFiles(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, _ := initGitRepo(t, 1)

	// add a non-code file (no language detection match)
	if err := os.WriteFile(filepath.Join(dir, "data.csv"), []byte("a,b,c\n1,2,3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dirtyPath := filepath.Join(t.TempDir(), "dirty")
	n, err := BuildDirtyIndex(context.Background(), dir, dirtyPath, IndexOptions{})
	if err != nil {
		t.Fatalf("BuildDirtyIndex: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 indexed files for non-code file, got %d", n)
	}
}

func TestBuildDirtyIndex_SkipsBinaryFiles(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, _ := initGitRepo(t, 1)

	// write binary content with a code extension
	binary := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE}
	if err := os.WriteFile(filepath.Join(dir, "binary.go"), binary, 0o644); err != nil {
		t.Fatal(err)
	}

	dirtyPath := filepath.Join(t.TempDir(), "dirty")
	n, err := BuildDirtyIndex(context.Background(), dir, dirtyPath, IndexOptions{})
	if err != nil {
		t.Fatalf("BuildDirtyIndex: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 indexed files for binary content, got %d", n)
	}
}

func TestBuildDirtyIndex_SkipsEmptyFiles(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, _ := initGitRepo(t, 1)

	if err := os.WriteFile(filepath.Join(dir, "empty.go"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	dirtyPath := filepath.Join(t.TempDir(), "dirty")
	n, err := BuildDirtyIndex(context.Background(), dir, dirtyPath, IndexOptions{})
	if err != nil {
		t.Fatalf("BuildDirtyIndex: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 indexed files for empty file, got %d", n)
	}
}

func TestBuildDirtyIndex_AtomicSwap(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, _ := initGitRepo(t, 1)

	// add dirty go file
	if err := os.WriteFile(filepath.Join(dir, "dirty.go"), []byte("package main\nfunc Dirty() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dirtyPath := filepath.Join(t.TempDir(), "dirty_index")

	// build twice — second build should replace first atomically
	for i := 0; i < 2; i++ {
		n, err := BuildDirtyIndex(context.Background(), dir, dirtyPath, IndexOptions{})
		if err != nil {
			t.Fatalf("BuildDirtyIndex pass %d: %v", i+1, err)
		}
		if n != 1 {
			t.Errorf("pass %d: expected 1, got %d", i+1, n)
		}
	}

	// verify the index dir exists and .tmp does not
	if _, err := os.Stat(dirtyPath); err != nil {
		t.Errorf("dirty index should exist: %v", err)
	}
	if _, err := os.Stat(dirtyPath + ".tmp"); err == nil {
		t.Error("tmp dir should not exist after swap")
	}
}

func TestBuildDirtyIndex_RemovesStaleOnClean(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	dir, _ := initGitRepo(t, 1)

	dirtyPath := filepath.Join(t.TempDir(), "dirty_index")

	// first: create dirty file and build index
	if err := os.WriteFile(filepath.Join(dir, "dirty.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := BuildDirtyIndex(context.Background(), dir, dirtyPath, IndexOptions{})
	if err != nil {
		t.Fatalf("build dirty: %v", err)
	}
	if n == 0 {
		t.Fatal("expected dirty files")
	}

	// commit the file so worktree is clean
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), // safe: git subprocess needs parent env for PATH
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@sageox.ai",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@sageox.ai",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	run("add", "dirty.go")
	run("commit", "-m", "commit dirty")

	// rebuild — should remove stale index
	n, err = BuildDirtyIndex(context.Background(), dir, dirtyPath, IndexOptions{})
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 dirty files after commit, got %d", n)
	}
	if _, err := os.Stat(dirtyPath); !os.IsNotExist(err) {
		t.Error("stale dirty index should be removed on clean worktree")
	}
}

// --- mtime tracking (saveFileMtime / loadFileMtimes) ---

func TestFileMtimePersistence(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}
	s := openTestStore(t)
	ctx := context.Background()

	// save mtime
	if err := saveFileMtime(ctx, s, "/path/to/file.json", 1234567890); err != nil {
		t.Fatalf("saveFileMtime: %v", err)
	}

	// load and verify
	mtimes, err := loadFileMtimes(ctx, s)
	if err != nil {
		t.Fatalf("loadFileMtimes: %v", err)
	}
	if mtimes["/path/to/file.json"] != 1234567890 {
		t.Errorf("expected mtime 1234567890, got %d", mtimes["/path/to/file.json"])
	}

	// overwrite mtime
	if err := saveFileMtime(ctx, s, "/path/to/file.json", 9999999999); err != nil {
		t.Fatalf("saveFileMtime overwrite: %v", err)
	}
	mtimes, err = loadFileMtimes(ctx, s)
	if err != nil {
		t.Fatalf("loadFileMtimes after overwrite: %v", err)
	}
	if mtimes["/path/to/file.json"] != 9999999999 {
		t.Errorf("expected updated mtime 9999999999, got %d", mtimes["/path/to/file.json"])
	}
}
