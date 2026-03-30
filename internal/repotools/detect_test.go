package repotools

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsInstalled_Git tests VCS detection for git
func TestIsInstalled_Git(t *testing.T) {
	result := IsInstalled(VCSGit)
	// check if git is actually in PATH
	_, err := exec.LookPath("git")
	expected := err == nil

	assert.Equal(t, expected, result, "IsInstalled(VCSGit)")
}

// TestIsInstalled_Svn tests VCS detection for svn
func TestIsInstalled_Svn(t *testing.T) {
	result := IsInstalled(VCSSvn)
	// check if svn is actually in PATH
	_, err := exec.LookPath("svn")
	expected := err == nil

	assert.Equal(t, expected, result, "IsInstalled(VCSSvn)")
}

// TestIsInstalled_TableDriven uses table-driven approach
func TestIsInstalled_TableDriven(t *testing.T) {
	tests := []struct {
		name string
		vcs  VCS
	}{
		{
			name: "git",
			vcs:  VCSGit,
		},
		{
			name: "svn",
			vcs:  VCSSvn,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsInstalled(tt.vcs)
			// verify that result matches actual PATH lookup
			_, err := exec.LookPath(string(tt.vcs))
			expected := err == nil

			assert.Equal(t, expected, result, "IsInstalled(%s)", tt.vcs)
		})
	}
}

// TestIsInstalled_NonexistentVCS tests detection of nonexistent VCS
func TestIsInstalled_NonexistentVCS(t *testing.T) {
	fakeVCS := VCS("definitely-not-a-real-vcs-tool-12345")
	result := IsInstalled(fakeVCS)

	assert.False(t, result, "IsInstalled(nonexistent) should be false")
}

// TestRequireVCS_Installed tests RequireVCS when VCS is installed
func TestRequireVCS_Installed(t *testing.T) {
	// only run this test if git is installed
	if !IsInstalled(VCSGit) {
		t.Skip("git is not installed, skipping test")
	}

	err := RequireVCS(VCSGit)
	assert.NoError(t, err, "RequireVCS(VCSGit) returned error when git is installed")
}

// TestRequireVCS_NotInstalled tests RequireVCS when VCS is not installed
func TestRequireVCS_NotInstalled(t *testing.T) {
	fakeVCS := VCS("definitely-not-a-real-vcs-tool-12345")
	err := RequireVCS(fakeVCS)

	require.Error(t, err, "RequireVCS(nonexistent) should return error")

	// verify error message contains VCS name
	assert.Contains(t, err.Error(), string(fakeVCS), "error message should contain VCS name")

	// verify error message mentions installation
	assert.Contains(t, err.Error(), "not installed", "error message should mention 'not installed'")
}

// TestRequireVCS_TableDriven uses table-driven approach
func TestRequireVCS_TableDriven(t *testing.T) {
	tests := []struct {
		name        string
		vcs         VCS
		shouldError bool
	}{
		{
			name:        "git_if_installed",
			vcs:         VCSGit,
			shouldError: !IsInstalled(VCSGit),
		},
		{
			name:        "svn_if_installed",
			vcs:         VCSSvn,
			shouldError: !IsInstalled(VCSSvn),
		},
		{
			name:        "nonexistent_vcs",
			vcs:         VCS("nonexistent"),
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := RequireVCS(tt.vcs)

			if tt.shouldError {
				assert.Error(t, err, "RequireVCS(%s) should return error", tt.vcs)
			} else {
				assert.NoError(t, err, "RequireVCS(%s) returned error", tt.vcs)
			}
		})
	}
}

// TestDetectVCS_InGitRepo tests VCS detection inside a real git repo
func TestDetectVCS_InGitRepo(t *testing.T) {
	if !IsInstalled(VCSGit) {
		t.Skip("git is not installed")
	}

	// create a real git repo so the test isn't environment-dependent
	tmpDir := t.TempDir()
	cmd := exec.Command("git", "init", tmpDir)
	require.NoError(t, cmd.Run(), "git init should succeed")

	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(tmpDir))
	t.Cleanup(func() { os.Chdir(origDir) })

	vcs, err := DetectVCS()
	require.NoError(t, err, "DetectVCS should succeed inside a git repo")
	assert.Equal(t, VCSGit, vcs, "DetectVCS() should return VCSGit")
}

// TestDetectVCS_OutsideRepo tests VCS detection outside a repository
func TestDetectVCS_OutsideRepo(t *testing.T) {
	// create a temporary directory that is not a git or svn repo
	tmpDir, err := os.MkdirTemp("", "ox-test-detect-")
	require.NoError(t, err, "failed to create temp directory")
	defer os.RemoveAll(tmpDir)

	// change to temp directory
	originalDir, err := os.Getwd()
	require.NoError(t, err, "failed to get current directory")
	defer os.Chdir(originalDir)

	err = os.Chdir(tmpDir)
	require.NoError(t, err, "failed to change to temp directory")

	// detect VCS should fail
	_, err = DetectVCS()
	require.Error(t, err, "DetectVCS() in non-repo directory should return error")

	// error should mention VCS detection
	assert.Contains(t, err.Error(), "no supported VCS detected", "error message should mention VCS detection")
}

// TestDetectVCS_NoVCSInstalled tests behavior when no VCS tools are installed
func TestDetectVCS_NoVCSInstalled(t *testing.T) {
	// we can't actually uninstall git/svn in a test, but we can verify
	// the error message format when VCS is not detected

	// skip if we are actually in a repo
	vcs, _ := DetectVCS()
	if vcs != "" {
		t.Skip("in a VCS repository, cannot test no-VCS scenario")
	}

	// if we get here, we're not in a repo
	// the error should mention checking git and svn
	tmpDir, err := os.MkdirTemp("", "ox-test-no-vcs-")
	require.NoError(t, err, "failed to create temp directory")
	defer os.RemoveAll(tmpDir)

	originalDir, _ := os.Getwd()
	defer os.Chdir(originalDir)
	os.Chdir(tmpDir)

	_, err = DetectVCS()
	require.Error(t, err, "expected error when no VCS detected")

	errMsg := err.Error()
	assert.Contains(t, errMsg, "git", "error should mention git")
	assert.Contains(t, errMsg, "svn", "error should mention svn")
}

// TestFindRepoRoot_Git tests finding git repository root
func TestFindRepoRoot_Git(t *testing.T) {
	// only run if git is installed
	if !IsInstalled(VCSGit) {
		t.Skip("git is not installed, skipping test")
	}

	// check if we're in a git repo
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	if err := cmd.Run(); err != nil {
		t.Skip("not in a git repository, skipping test")
	}

	// find git root
	root, err := FindRepoRoot(VCSGit)
	require.NoError(t, err, "FindRepoRoot(VCSGit) failed")

	// verify root is a valid path
	assert.NotEmpty(t, root, "FindRepoRoot(VCSGit) returned empty path")

	// verify root is an absolute path
	assert.True(t, filepath.IsAbs(root), "FindRepoRoot(VCSGit) should return absolute path, got: %s", root)

	// verify .git directory exists in root
	gitDir := filepath.Join(root, ".git")
	_, err = os.Stat(gitDir)
	assert.NoError(t, err, ".git directory not found in repo root %s", root)
}

// TestFindRepoRoot_Git_OutsideRepo tests finding git root outside a repository
func TestFindRepoRoot_Git_OutsideRepo(t *testing.T) {
	// only run if git is installed
	if !IsInstalled(VCSGit) {
		t.Skip("git is not installed, skipping test")
	}

	// create temp directory outside any repo
	tmpDir, err := os.MkdirTemp("", "ox-test-no-git-")
	require.NoError(t, err, "failed to create temp directory")
	defer os.RemoveAll(tmpDir)

	originalDir, err := os.Getwd()
	require.NoError(t, err, "failed to get current directory")
	defer os.Chdir(originalDir)

	err = os.Chdir(tmpDir)
	require.NoError(t, err, "failed to change to temp directory")

	// should fail to find git root
	_, err = FindRepoRoot(VCSGit)
	require.Error(t, err, "FindRepoRoot(VCSGit) outside repo should return error")

	// error should mention git
	assert.Contains(t, err.Error(), "git", "error message should mention git")
}

// TestFindRepoRoot_Svn tests finding svn repository root
func TestFindRepoRoot_Svn(t *testing.T) {
	// only run if svn is installed
	if !IsInstalled(VCSSvn) {
		t.Skip("svn is not installed, skipping test")
	}

	// check if we're in an svn repo
	cmd := exec.Command("svn", "info")
	if err := cmd.Run(); err != nil {
		t.Skip("not in an svn repository, skipping test")
	}

	// find svn root
	root, err := FindRepoRoot(VCSSvn)
	require.NoError(t, err, "FindRepoRoot(VCSSvn) failed")

	// verify root is a valid path
	assert.NotEmpty(t, root, "FindRepoRoot(VCSSvn) returned empty path")

	// verify root is an absolute path
	assert.True(t, filepath.IsAbs(root), "FindRepoRoot(VCSSvn) should return absolute path, got: %s", root)
}

// TestFindRepoRoot_UnsupportedVCS tests finding root for unsupported VCS
func TestFindRepoRoot_UnsupportedVCS(t *testing.T) {
	fakeVCS := VCS("unsupported-vcs")
	_, err := FindRepoRoot(fakeVCS)

	require.Error(t, err, "FindRepoRoot(unsupported) should return error")

	// error should mention unsupported VCS
	assert.Contains(t, err.Error(), "unsupported VCS", "error should mention 'unsupported VCS'")

	// error should include the VCS name
	assert.Contains(t, err.Error(), string(fakeVCS), "error should include VCS name")
}

// TestFindRepoRoot_TableDriven uses table-driven approach
func TestFindRepoRoot_TableDriven(t *testing.T) {
	tests := []struct {
		name        string
		vcs         VCS
		shouldSkip  bool
		skipReason  string
		shouldError bool
	}{
		{
			name:       "git",
			vcs:        VCSGit,
			shouldSkip: !IsInstalled(VCSGit),
			skipReason: "git not installed",
		},
		{
			name:       "svn",
			vcs:        VCSSvn,
			shouldSkip: !IsInstalled(VCSSvn),
			skipReason: "svn not installed",
		},
		{
			name:        "unsupported",
			vcs:         VCS("mercurial"),
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.shouldSkip {
				t.Skip(tt.skipReason)
			}

			root, err := FindRepoRoot(tt.vcs)

			if tt.shouldError {
				assert.Error(t, err, "FindRepoRoot(%s) should return error", tt.vcs)
				return
			}

			// for supported VCS, result depends on whether we're in a repo
			if err != nil {
				// not in a repo is acceptable
				t.Logf("not in %s repository: %v", tt.vcs, err)
			} else {
				// if successful, verify root is valid
				assert.NotEmpty(t, root, "FindRepoRoot(%s) returned empty path", tt.vcs)
				assert.True(t, filepath.IsAbs(root), "FindRepoRoot(%s) should return absolute path, got: %s", tt.vcs, root)
			}
		})
	}
}

// TestVCSConstants tests that VCS constants have expected values
func TestVCSConstants(t *testing.T) {
	assert.Equal(t, VCS("git"), VCSGit)
	assert.Equal(t, VCS("svn"), VCSSvn)
}

// BenchmarkIsInstalled benchmarks VCS detection performance
func BenchmarkIsInstalled(b *testing.B) {
	for i := 0; i < b.N; i++ {
		IsInstalled(VCSGit)
	}
}

// BenchmarkRequireVCS benchmarks VCS requirement check
func BenchmarkRequireVCS(b *testing.B) {
	for i := 0; i < b.N; i++ {
		RequireVCS(VCSGit)
	}
}

// BenchmarkDetectVCS benchmarks VCS detection
func BenchmarkDetectVCS(b *testing.B) {
	if !IsInstalled(VCSGit) {
		b.Skip("git not installed")
	}

	for i := 0; i < b.N; i++ {
		DetectVCS()
	}
}

// TestFindMainRepoRoot_Git tests finding main repo root from current repo
func TestFindMainRepoRoot_Git(t *testing.T) {
	if !IsInstalled(VCSGit) {
		t.Skip("git is not installed, skipping test")
	}

	// check if we're in a git repo
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	if err := cmd.Run(); err != nil {
		t.Skip("not in a git repository, skipping test")
	}

	root, err := FindMainRepoRoot(VCSGit)
	require.NoError(t, err, "FindMainRepoRoot(VCSGit) failed")

	assert.NotEmpty(t, root, "FindMainRepoRoot(VCSGit) returned empty path")
	assert.True(t, filepath.IsAbs(root), "FindMainRepoRoot(VCSGit) should return absolute path, got: %s", root)

	// verify .git directory exists in root
	gitDir := filepath.Join(root, ".git")
	_, err = os.Stat(gitDir)
	assert.NoError(t, err, ".git directory not found in repo root %s", root)
}

// TestFindMainRepoRoot_Worktree tests that FindMainRepoRoot returns main repo path from a worktree
func TestFindMainRepoRoot_Worktree(t *testing.T) {
	if !IsInstalled(VCSGit) {
		t.Skip("git is not installed, skipping test")
	}

	// create temp directory for main repo
	mainRepoDir, err := os.MkdirTemp("", "ox-test-main-repo-")
	require.NoError(t, err, "failed to create temp directory for main repo")
	defer os.RemoveAll(mainRepoDir)

	// resolve symlinks (macOS /var -> /private/var) for accurate comparison
	mainRepoDir, err = filepath.EvalSymlinks(mainRepoDir)
	require.NoError(t, err, "failed to resolve symlinks for main repo dir")

	// create temp directory for worktree (as sibling)
	worktreeDir, err := os.MkdirTemp("", "ox-test-worktree-")
	require.NoError(t, err, "failed to create temp directory for worktree")
	defer os.RemoveAll(worktreeDir)
	// worktree dir will be created by git, so remove the empty one first
	os.RemoveAll(worktreeDir)

	// resolve symlinks for worktree dir comparison
	// note: we need to get the parent and append the dir name since the dir doesn't exist yet
	worktreeParent, err := filepath.EvalSymlinks(filepath.Dir(worktreeDir))
	require.NoError(t, err, "failed to resolve symlinks for worktree parent")
	worktreeDir = filepath.Join(worktreeParent, filepath.Base(worktreeDir))

	// initialize git repo
	initCmd := exec.Command("git", "init")
	initCmd.Dir = mainRepoDir
	require.NoError(t, initCmd.Run(), "failed to init git repo")

	// configure git user for commits
	configCmds := [][]string{
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test User"},
	}
	for _, args := range configCmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = mainRepoDir
		require.NoError(t, cmd.Run(), "failed to configure git: %v", args)
	}

	// create initial commit (required for worktrees)
	dummyFile := filepath.Join(mainRepoDir, "README.md")
	require.NoError(t, os.WriteFile(dummyFile, []byte("# Test"), 0644), "failed to create dummy file")

	addCmd := exec.Command("git", "add", ".")
	addCmd.Dir = mainRepoDir
	require.NoError(t, addCmd.Run(), "failed to git add")

	commitCmd := exec.Command("git", "commit", "-m", "initial commit")
	commitCmd.Dir = mainRepoDir
	require.NoError(t, commitCmd.Run(), "failed to git commit")

	// create worktree
	worktreeCmd := exec.Command("git", "worktree", "add", worktreeDir, "HEAD")
	worktreeCmd.Dir = mainRepoDir
	require.NoError(t, worktreeCmd.Run(), "failed to create git worktree")

	// save original directory
	originalDir, err := os.Getwd()
	require.NoError(t, err, "failed to get current directory")
	defer os.Chdir(originalDir)

	// change to worktree directory
	require.NoError(t, os.Chdir(worktreeDir), "failed to change to worktree directory")

	// FindMainRepoRoot should return main repo, not worktree
	root, err := FindMainRepoRoot(VCSGit)
	require.NoError(t, err, "FindMainRepoRoot from worktree failed")

	// verify it returns the main repo path, not the worktree path
	assert.Equal(t, mainRepoDir, root,
		"FindMainRepoRoot from worktree should return main repo path, got: %s, expected: %s", root, mainRepoDir)

	// also verify FindRepoRoot returns the worktree path (different behavior)
	worktreeRoot, err := FindRepoRoot(VCSGit)
	require.NoError(t, err, "FindRepoRoot from worktree failed")
	assert.Equal(t, worktreeDir, worktreeRoot,
		"FindRepoRoot from worktree should return worktree path, got: %s, expected: %s", worktreeRoot, worktreeDir)

	// cleanup: remove worktree before deleting directories
	os.Chdir(originalDir)
	cleanupCmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
	cleanupCmd.Dir = mainRepoDir
	cleanupCmd.Run() // ignore error, cleanup will happen anyway
}

// TestFindMainRepoRoot_UnsupportedVCS tests finding main root for unsupported VCS
func TestFindMainRepoRoot_UnsupportedVCS(t *testing.T) {
	fakeVCS := VCS("unsupported-vcs")
	_, err := FindMainRepoRoot(fakeVCS)

	require.Error(t, err, "FindMainRepoRoot(unsupported) should return error")
	assert.Contains(t, err.Error(), "unsupported VCS", "error should mention 'unsupported VCS'")
}

// BenchmarkFindMainRepoRoot benchmarks finding main repository root
func BenchmarkFindMainRepoRoot(b *testing.B) {
	if !IsInstalled(VCSGit) {
		b.Skip("git not installed")
	}

	// check if in git repo
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	if err := cmd.Run(); err != nil {
		b.Skip("not in git repository")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		FindMainRepoRoot(VCSGit)
	}
}

// BenchmarkFindRepoRoot benchmarks finding repository root
func BenchmarkFindRepoRoot(b *testing.B) {
	if !IsInstalled(VCSGit) {
		b.Skip("git not installed")
	}

	// check if in git repo
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	if err := cmd.Run(); err != nil {
		b.Skip("not in git repository")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		FindRepoRoot(VCSGit)
	}
}
