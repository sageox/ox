//go:build slow

package daemon

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Corrupt pointer handling ---

// TestLFS_CorruptPointer_NotTreatedAsPointer verifies that a file starting with
// the LFS pointer prefix but containing garbage after it does not cause the
// repair function to panic or leave the repo in a broken state.
// Failure prevented: corrupt or adversarial pointer files crash LFS repair,
// leaving the repo unable to push.
func TestLFS_CorruptPointer_NotTreatedAsPointer(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Skip("git-lfs not available")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-lfs-corrupt-pointer")
	cloneDir := filepath.Join(t.TempDir(), "repo")
	g.cloneRepo(t, cloneURL, cloneDir)

	// set up LFS tracking for .bin files
	require.NoError(t, os.WriteFile(
		filepath.Join(cloneDir, ".gitattributes"),
		[]byte("*.bin filter=lfs diff=lfs merge=lfs -text\n"), 0o644))

	// write a file that starts with the LFS pointer prefix but has garbage payload
	corruptPointer := "version https://git-lfs.github.com/spec/v1\nGARBAGE NOT A REAL POINTER"
	require.NoError(t, os.WriteFile(
		filepath.Join(cloneDir, "test.bin"),
		[]byte(corruptPointer), 0o644))

	cmd := exec.Command("git", "-C", cloneDir, "add", ".gitattributes", "test.bin")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add failed: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "add corrupt pointer file")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit failed: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git push failed: %s", string(out))

	// run repair -- should not panic or return an unrecoverable error
	ctx := context.Background()
	_, repairErr := gitutil.RepairMissingLFSObjects(ctx, cloneDir)

	// the repo must remain valid regardless of repair outcome
	if repairErr != nil {
		t.Logf("repair returned error (acceptable): %v", repairErr)
	}

	// verify repo integrity: git status must succeed
	cmd = exec.Command("git", "-C", cloneDir, "status")
	out, err = cmd.CombinedOutput()
	assert.NoError(t, err, "repo should remain valid after repair; git status output: %s", string(out))
}

// --- B. Pointer file overwrites normal file ---

// TestLFS_PointerFileOverwritesNormalFile verifies that when a normal file is
// retroactively tracked by LFS (via adding .gitattributes after the fact), the
// repo remains usable and repair does not crash.
// Failure prevented: retroactive LFS tracking creates orphaned references that
// brick the repo on push.
func TestLFS_PointerFileOverwritesNormalFile(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Skip("git-lfs not available")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-lfs-overwrite-normal")
	cloneDir := filepath.Join(t.TempDir(), "repo")
	g.cloneRepo(t, cloneURL, cloneDir)

	// create a normal (non-LFS) file
	docsDir := filepath.Join(cloneDir, "docs")
	require.NoError(t, os.MkdirAll(docsDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(docsDir, "notes.txt"),
		[]byte("important notes"), 0o644))

	cmd := exec.Command("git", "-C", cloneDir, "add", "docs/notes.txt")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add failed: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "add normal text file")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit failed: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git push failed: %s", string(out))

	// retroactively add LFS tracking for .txt files
	require.NoError(t, os.WriteFile(
		filepath.Join(cloneDir, ".gitattributes"),
		[]byte("*.txt filter=lfs diff=lfs merge=lfs -text\n"), 0o644))

	cmd = exec.Command("git", "-C", cloneDir, "add", ".gitattributes")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git add .gitattributes failed: %s", string(out))

	// re-add the txt file so git renormalizes it through the new LFS filter
	cmd = exec.Command("git", "-C", cloneDir, "add", "--renormalize", "docs/notes.txt")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git add --renormalize failed: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "retroactively track txt via LFS")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit failed: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git push failed: %s", string(out))

	// run repair -- should handle the mixed state gracefully
	ctx := context.Background()
	_, repairErr := gitutil.RepairMissingLFSObjects(ctx, cloneDir)
	if repairErr != nil {
		t.Logf("repair returned error (acceptable): %v", repairErr)
	}

	// repo must remain usable
	cmd = exec.Command("git", "-C", cloneDir, "status")
	out, err = cmd.CombinedOutput()
	assert.NoError(t, err, "repo should remain valid after repair; git status output: %s", string(out))
}

// --- C. Repair on repo without LFS ---

// TestLFS_RepairOnRepoWithoutLFS verifies that calling LFS repair on a repo
// with no LFS configuration is a safe no-op returning zero repaired files.
// Failure prevented: repair crashing or mutating repos that never used LFS.
func TestLFS_RepairOnRepoWithoutLFS(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-lfs-no-lfs")
	cloneDir := filepath.Join(t.TempDir(), "repo")
	g.cloneRepo(t, cloneURL, cloneDir)

	// add a normal file with no LFS config at all
	require.NoError(t, os.WriteFile(
		filepath.Join(cloneDir, "readme.txt"),
		[]byte("just a plain file"), 0o644))

	cmd := exec.Command("git", "-C", cloneDir, "add", "readme.txt")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git add failed: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "add plain file")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit failed: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git push failed: %s", string(out))

	// repair should be a no-op
	ctx := context.Background()
	repaired, repairErr := gitutil.RepairMissingLFSObjects(ctx, cloneDir)

	assert.NoError(t, repairErr, "repair on non-LFS repo should not error")
	assert.Equal(t, 0, repaired, "repair on non-LFS repo should report zero files repaired")

	// repo should be clean
	cmd = exec.Command("git", "-C", cloneDir, "status", "--porcelain")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git status failed: %s", string(out))
	assert.Empty(t, string(out), "repo should have no uncommitted changes after no-op repair")
}

// --- D. Large LFS file round-trip ---

// TestLFS_LargeFileViaGitea_RoundTrip verifies that a large LFS-tracked file
// (>1MB) survives a push/pull round-trip through Gitea with byte-for-byte
// content preservation.
// Failure prevented: chunked transfer encoding, content-length mismatches, or
// LFS smudge filter failures silently replacing large files with pointer stubs.
func TestLFS_LargeFileViaGitea_RoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("short: Gitea digital twin")
	}
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Skip("git-lfs not available")
	}

	g := getSharedGitea(t)
	cloneURL := g.createRepo(t, "twin-lfs-large-roundtrip")
	cloneDir := filepath.Join(t.TempDir(), "repo")
	g.cloneRepo(t, cloneURL, cloneDir)

	// configure LFS tracking for .dat files
	cmd := exec.Command("git", "-C", cloneDir, "lfs", "track", "*.dat")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git lfs track failed: %s", string(out))

	// create a 2MB file with deterministic content (repeating 0-255 byte pattern)
	const fileSize = 2 * 1024 * 1024
	original := make([]byte, fileSize)
	for i := range original {
		original[i] = byte(i % 256)
	}

	require.NoError(t, os.WriteFile(
		filepath.Join(cloneDir, "large.dat"),
		original, 0o644))

	cmd = exec.Command("git", "-C", cloneDir, "add", ".gitattributes", "large.dat")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git add failed: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "commit", "-m", "add large LFS file")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git commit failed: %s", string(out))

	cmd = exec.Command("git", "-C", cloneDir, "push")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "git push failed: %s", string(out))

	// clone to a fresh directory and verify content
	verifyDir := filepath.Join(t.TempDir(), "verify")
	g.cloneRepo(t, cloneURL, verifyDir)

	retrieved, err := os.ReadFile(filepath.Join(verifyDir, "large.dat"))
	require.NoError(t, err, "reading large.dat from fresh clone")

	// file should be actual content, not an LFS pointer stub
	assert.False(t,
		bytes.HasPrefix(retrieved, []byte("version https://git-lfs")),
		"file in fresh clone should be actual content, not an LFS pointer")

	assert.Equal(t, fileSize, len(retrieved),
		"file size should be exactly 2MB")

	assert.True(t, bytes.Equal(original, retrieved),
		"file content should match byte-for-byte after round-trip")
}
