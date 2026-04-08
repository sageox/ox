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

// --- B. Cwd-based fallback when OX_REPO_ROOT is unset ---

// TestResolveRepoRoot_CwdFallback_FindsSageoxDir verifies that when OX_REPO_ROOT
// is empty, resolveRepoRoot walks up from cwd to find .sageox/.
// Failure prevented: adapter receives empty --repo-root, searches wrong Claude project dir.
func TestResolveRepoRoot_CwdFallback_FindsSageoxDir(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)
	if err := os.MkdirAll(filepath.Join(tmpDir, ".sageox"), 0o755); err != nil {
		t.Fatal(err)
	}

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

// TestResolveRepoRoot_CwdFallback_WalksUpToParent verifies cwd fallback finds
// .sageox/ when cwd is a subdirectory of the project root.
// Failure prevented: adapter fails when ox commands run from a subdirectory.
func TestResolveRepoRoot_CwdFallback_WalksUpToParent(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)
	if err := os.MkdirAll(filepath.Join(tmpDir, ".sageox"), 0o755); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(tmpDir, "src", "pkg", "deep")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	origDir, _ := os.Getwd()
	if err := os.Chdir(subDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(origDir) })
	t.Setenv("OX_REPO_ROOT", "")

	got := resolveRepoRoot()
	if got != tmpDir {
		t.Errorf("resolveRepoRoot() = %q, want %q (should walk up from subdirectory)", got, tmpDir)
	}
}

// TestResolveRepoRoot_CwdFallback_ResolvesSymlinks verifies that the cwd fallback
// resolves symlinks in the detected path. This is the actual production scenario:
// /Users/ryan/Code → /Users/ryan/Documents/Code (symlink), and Claude Code stores
// sessions under the resolved path.
// Failure prevented: adapter computes hash from unresolved symlink path, wrong project dir.
func TestResolveRepoRoot_CwdFallback_ResolvesSymlinks(t *testing.T) {
	// create real dir with .sageox/
	realDir := t.TempDir()
	realDir, _ = filepath.EvalSymlinks(realDir)
	if err := os.MkdirAll(filepath.Join(realDir, ".sageox"), 0o755); err != nil {
		t.Fatal(err)
	}

	// create symlink pointing to the real dir
	symlinkParent := t.TempDir()
	symlinkDir := filepath.Join(symlinkParent, "symlink-project")
	if err := os.Symlink(realDir, symlinkDir); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	// cd into symlink path
	origDir, _ := os.Getwd()
	if err := os.Chdir(symlinkDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(origDir) })
	t.Setenv("OX_REPO_ROOT", "")

	got := resolveRepoRoot()
	if got != realDir {
		t.Errorf("resolveRepoRoot() = %q, want %q (should resolve symlink)", got, realDir)
	}
}

// TestResolveRepoRoot_CwdFallback_NoSageoxDir verifies that when no .sageox/
// exists anywhere, the fallback returns cwd rather than empty string.
// Failure prevented: nil/empty path causing panic or cryptic errors downstream.
func TestResolveRepoRoot_CwdFallback_NoSageoxDir(t *testing.T) {
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)

	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(origDir) })
	t.Setenv("OX_REPO_ROOT", "")

	got := resolveRepoRoot()
	// should return cwd (resolved), not empty string
	if got == "" {
		t.Error("resolveRepoRoot() returned empty, want cwd fallback")
	}
}
