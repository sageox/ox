package index

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// --- gitStatusDirtyFiles ---

func TestGitStatusDirtyFiles(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git indexing")
	}

	// helper to init a git repo with one committed file
	setupRepo := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
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
		run("init", "-b", "main")
		if err := os.WriteFile(filepath.Join(dir, "base.go"), []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", "base.go")
		run("commit", "-m", "init")
		return dir
	}

	t.Run("clean worktree returns nil", func(t *testing.T) {
		t.Parallel()
		dir := setupRepo(t)
		files, err := gitStatusDirtyFiles(context.Background(), dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if files != nil {
			t.Errorf("expected nil for clean worktree, got %v", files)
		}
	})

	t.Run("modified file detected", func(t *testing.T) {
		t.Parallel()
		dir := setupRepo(t)
		if err := os.WriteFile(filepath.Join(dir, "base.go"), []byte("package main\n// modified\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		files, err := gitStatusDirtyFiles(context.Background(), dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(files) != 1 || files[0] != "base.go" {
			t.Errorf("expected [base.go], got %v", files)
		}
	})

	t.Run("untracked file detected", func(t *testing.T) {
		t.Parallel()
		dir := setupRepo(t)
		if err := os.WriteFile(filepath.Join(dir, "new.go"), []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		files, err := gitStatusDirtyFiles(context.Background(), dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		found := false
		for _, f := range files {
			if f == "new.go" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected new.go in dirty files, got %v", files)
		}
	})

	t.Run("deleted file excluded", func(t *testing.T) {
		t.Parallel()
		dir := setupRepo(t)
		os.Remove(filepath.Join(dir, "base.go"))
		files, err := gitStatusDirtyFiles(context.Background(), dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, f := range files {
			if f == "base.go" {
				t.Error("deleted file should be excluded from dirty files")
			}
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		t.Parallel()
		dir := setupRepo(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := gitStatusDirtyFiles(ctx, dir)
		if err == nil {
			t.Error("expected error with canceled context")
		}
	})
}
