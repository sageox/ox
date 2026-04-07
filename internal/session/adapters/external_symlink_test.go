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

func TestResolveRepoRoot_EmptyString(t *testing.T) {
	t.Setenv("OX_REPO_ROOT", "")

	got := resolveRepoRoot()
	if got != "" {
		t.Errorf("resolveRepoRoot() = %q, want %q", got, "")
	}
}
