package adapters

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRepoRoot_Symlink(t *testing.T) {
	realDir := t.TempDir()
	// resolve any intermediate symlinks (e.g., /tmp -> /private/tmp on macOS)
	realDir, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	symlinkDir := filepath.Join(t.TempDir(), "symlink-repo")
	if err := os.Symlink(realDir, symlinkDir); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	t.Setenv("OX_REPO_ROOT", symlinkDir)

	got := resolveRepoRoot()
	if got != realDir {
		t.Errorf("resolveRepoRoot() = %q, want %q", got, realDir)
	}
}

func TestResolveRepoRoot_NonexistentPath(t *testing.T) {
	t.Setenv("OX_REPO_ROOT", "/nonexistent/path")

	got := resolveRepoRoot()
	if got != "/nonexistent/path" {
		t.Errorf("resolveRepoRoot() = %q, want %q", got, "/nonexistent/path")
	}
}

func TestResolveRepoRoot_EmptyString_FallsBackToCwd(t *testing.T) {
	t.Setenv("OX_REPO_ROOT", "")

	// when OX_REPO_ROOT is empty, resolveRepoRoot falls back to cwd-based
	// detection (walk up looking for .sageox/). Since this test runs from the
	// repo root which has .sageox/, it should return a non-empty value.
	got := resolveRepoRoot()
	if got == "" {
		t.Error("resolveRepoRoot() returned empty, expected cwd-based fallback")
	}
}

func TestResolveRepoRoot_CwdFallback_WithSageoxDir(t *testing.T) {
	// set up a temp dir with .sageox/ to simulate a project root
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir) // resolve /tmp -> /private/tmp on macOS
	if err := os.MkdirAll(filepath.Join(tmpDir, ".sageox"), 0o755); err != nil {
		t.Fatal(err)
	}

	// change to the temp dir so cwd detection finds .sageox/
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(origDir) })

	t.Setenv("OX_REPO_ROOT", "")

	got := resolveRepoRoot()
	if got != tmpDir {
		t.Errorf("resolveRepoRoot() = %q, want %q", got, tmpDir)
	}
}
