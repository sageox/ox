//go:build !short

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/config"
)

func TestCheckGitRepoPaths_NotInGitRepo(t *testing.T) {
	skipIntegration(t)

	tmpDir, err := os.MkdirTemp("", "ox-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	result := checkGitRepoPaths(false)

	if !result.skipped {
		t.Error("expected skipped=true when not in git repo")
	}
	if result.name != "git repo paths" {
		t.Errorf("unexpected name: %s", result.name)
	}
}

func TestCheckGitRepoPaths_NoReposConfigured(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// no .sageox directory - check should be skipped
	// (with .sageox but no auth, it would warn; with auth, it would fail)

	result := checkGitRepoPaths(false)

	if !result.skipped {
		t.Error("expected skipped=true when no repos configured and no .sageox")
	}
	if result.message != "no repos configured" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckGitRepoPaths_LedgerPathIsValidGitRepo(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .sageox directory
	requireSageoxDir(t, gitRoot)

	// create a real ledger path that is a git repo
	ledgerPath := filepath.Join(filepath.Dir(gitRoot), "test_ledger")
	if err := os.MkdirAll(ledgerPath, 0755); err != nil {
		t.Fatalf("failed to create ledger dir: %v", err)
	}
	defer os.RemoveAll(ledgerPath)

	// initialize it as a git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = ledgerPath
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// save local config with ledger path
	localCfg := &config.LocalConfig{
		Ledger: &config.LedgerConfig{
			Path: ledgerPath,
		},
	}
	if err := config.SaveLocalConfig(gitRoot, localCfg); err != nil {
		t.Fatalf("failed to save local config: %v", err)
	}

	result := checkGitRepoPaths(false)

	if !result.passed {
		t.Errorf("expected passed=true when ledger is valid git repo, got: %+v", result)
	}
	if result.message != "all paths valid" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckGitRepoPaths_LedgerPathMissing(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .sageox directory
	requireSageoxDir(t, gitRoot)

	// save local config with non-existent ledger path
	localCfg := &config.LocalConfig{
		Ledger: &config.LedgerConfig{
			Path: "/nonexistent/ledger/path",
		},
	}
	if err := config.SaveLocalConfig(gitRoot, localCfg); err != nil {
		t.Fatalf("failed to save local config: %v", err)
	}

	result := checkGitRepoPaths(false)

	if result.passed {
		t.Error("expected passed=false when ledger path is missing")
	}
	if result.message != "1 repo(s) with issues" {
		t.Errorf("unexpected message: %s", result.message)
	}
	if !strings.Contains(result.detail, "not found") {
		t.Errorf("expected detail to mention 'not found', got: %s", result.detail)
	}
}

func TestCheckGitRepoPaths_LedgerPathEmptyDirectory(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .sageox directory
	requireSageoxDir(t, gitRoot)

	// create an EMPTY ledger directory (not a git repo)
	ledgerPath := filepath.Join(filepath.Dir(gitRoot), "test_ledger_empty")
	if err := os.MkdirAll(ledgerPath, 0755); err != nil {
		t.Fatalf("failed to create ledger dir: %v", err)
	}
	defer os.RemoveAll(ledgerPath)

	// save local config with the empty directory path
	localCfg := &config.LocalConfig{
		Ledger: &config.LedgerConfig{
			Path: ledgerPath,
		},
	}
	if err := config.SaveLocalConfig(gitRoot, localCfg); err != nil {
		t.Fatalf("failed to save local config: %v", err)
	}

	result := checkGitRepoPaths(false)

	if result.passed {
		t.Error("expected passed=false when ledger path is empty directory")
	}
	if result.message != "1 repo(s) with issues" {
		t.Errorf("unexpected message: %s", result.message)
	}
	if !strings.Contains(result.detail, "empty directory") {
		t.Errorf("expected detail to mention 'empty directory', got: %s", result.detail)
	}
}

func TestCheckGitRepoPaths_LedgerPathNotGitRepo(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .sageox directory
	requireSageoxDir(t, gitRoot)

	// create a directory with files but NO .git (not a git repo)
	ledgerPath := filepath.Join(filepath.Dir(gitRoot), "test_ledger_notgit")
	if err := os.MkdirAll(ledgerPath, 0755); err != nil {
		t.Fatalf("failed to create ledger dir: %v", err)
	}
	defer os.RemoveAll(ledgerPath)

	// add a file so it's not empty
	testFile := filepath.Join(ledgerPath, "readme.md")
	if err := os.WriteFile(testFile, []byte("# Test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// save local config with the non-git directory path
	localCfg := &config.LocalConfig{
		Ledger: &config.LedgerConfig{
			Path: ledgerPath,
		},
	}
	if err := config.SaveLocalConfig(gitRoot, localCfg); err != nil {
		t.Fatalf("failed to save local config: %v", err)
	}

	result := checkGitRepoPaths(false)

	if result.passed {
		t.Error("expected passed=false when ledger path is not a git repo")
	}
	if result.message != "1 repo(s) with issues" {
		t.Errorf("unexpected message: %s", result.message)
	}
	if !strings.Contains(result.detail, "not a git repo") {
		t.Errorf("expected detail to mention 'not a git repo', got: %s", result.detail)
	}
}

func TestCheckGitRepoPaths_TeamContextPathMissing(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .sageox directory
	requireSageoxDir(t, gitRoot)

	// save local config with non-existent team context path
	localCfg := &config.LocalConfig{
		TeamContexts: []config.TeamContext{
			{
				TeamID:   "team-123",
				TeamName: "Engineering",
				Path:     "/nonexistent/team/context",
			},
		},
	}
	if err := config.SaveLocalConfig(gitRoot, localCfg); err != nil {
		t.Fatalf("failed to save local config: %v", err)
	}

	result := checkGitRepoPaths(false)

	if result.passed {
		t.Error("expected passed=false when team context path is missing")
	}
	if result.message != "1 repo(s) with issues" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckGitRepoPaths_MultiplePathsMissing(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .sageox directory
	requireSageoxDir(t, gitRoot)

	// save local config with multiple missing paths
	localCfg := &config.LocalConfig{
		Ledger: &config.LedgerConfig{
			Path: "/nonexistent/ledger",
		},
		TeamContexts: []config.TeamContext{
			{
				TeamID:   "team-123",
				TeamName: "Engineering",
				Path:     "/nonexistent/team1",
			},
			{
				TeamID:   "team-456",
				TeamName: "Platform",
				Path:     "/nonexistent/team2",
			},
		},
	}
	if err := config.SaveLocalConfig(gitRoot, localCfg); err != nil {
		t.Fatalf("failed to save local config: %v", err)
	}

	result := checkGitRepoPaths(false)

	if result.passed {
		t.Error("expected passed=false when multiple paths are missing")
	}
	if result.message != "3 repo(s) with issues" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckGitRepoPaths_MixedExistingAndMissing(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .sageox directory
	requireSageoxDir(t, gitRoot)

	// create one existing path that is a valid git repo
	existingPath := filepath.Join(filepath.Dir(gitRoot), "existing_team")
	if err := os.MkdirAll(existingPath, 0755); err != nil {
		t.Fatalf("failed to create existing dir: %v", err)
	}
	defer os.RemoveAll(existingPath)

	// init as git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = existingPath
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// save local config with mixed paths
	localCfg := &config.LocalConfig{
		TeamContexts: []config.TeamContext{
			{
				TeamID:   "team-exists",
				TeamName: "Existing Team",
				Path:     existingPath,
			},
			{
				TeamID:   "team-missing",
				TeamName: "Missing Team",
				Path:     "/nonexistent/path",
			},
		},
	}
	if err := config.SaveLocalConfig(gitRoot, localCfg); err != nil {
		t.Fatalf("failed to save local config: %v", err)
	}

	result := checkGitRepoPaths(false)

	if result.passed {
		t.Error("expected passed=false when any path is missing")
	}
	if result.message != "1 repo(s) with issues" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

// Tests for new git repository health checks

func TestCheckGitConfig_NotInGitRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ox-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	result := checkGitConfig(false)

	if !result.skipped {
		t.Error("expected skipped=true when not in git repo")
	}
}

func TestCheckGitConfig_Complete(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// git config is already set up by setupTempGitRepo
	result := checkGitConfig(false)

	if !result.passed {
		t.Errorf("expected passed=true with complete git config, got: %+v", result)
	}
	if result.warning {
		t.Error("expected no warning when git config is complete")
	}
}

func TestCheckGitRemotes_NotInGitRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ox-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	result := checkGitRemotes()

	if !result.skipped {
		t.Error("expected skipped=true when not in git repo")
	}
}

func TestCheckGitRemotes_NoRemotes(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// no remotes configured in fresh repo
	result := checkGitRemotes()

	if !result.warning {
		t.Error("expected warning when no remotes configured")
	}
	if !strings.Contains(result.detail, "git remote add origin") {
		t.Error("expected detail to suggest adding remote")
	}
}

func TestCheckGitRemotes_WithOrigin(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// add origin remote
	cmd := exec.Command("git", "remote", "add", "origin", "https://github.com/example/repo.git")
	cmd.Dir = gitRoot
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to add remote: %v", err)
	}

	result := checkGitRemotes()

	if !result.passed {
		t.Errorf("expected passed=true when origin configured, got: %+v", result)
	}
	if result.warning {
		t.Error("expected no warning when origin is configured")
	}
}

func TestCheckGitRepoState_NotInGitRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ox-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	result := checkGitRepoState()

	if !result.skipped {
		t.Error("expected skipped=true when not in git repo")
	}
}

func TestCheckGitRepoState_Clean(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// clean repo with no uncommitted changes
	result := checkGitRepoState()

	if !result.passed {
		t.Errorf("expected passed=true for clean repo, got: %+v", result)
	}
	if result.warning {
		t.Error("expected no warning for clean repo")
	}
}

func TestCheckGitRepoState_UncommittedChanges(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create uncommitted file
	testFile := filepath.Join(gitRoot, "uncommitted.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	result := checkGitRepoState()

	if !result.warning {
		t.Error("expected warning for uncommitted changes")
	}
	if !strings.Contains(result.message, "uncommitted") {
		t.Errorf("expected message to mention uncommitted changes, got: %s", result.message)
	}
}

func TestCheckGitHooks_NotInGitRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ox-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	result := checkGitHooks()

	if !result.skipped {
		t.Error("expected skipped=true when not in git repo")
	}
}

func TestCheckGitHooks_NoActiveHooks(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	result := checkGitHooks()

	// fresh repo should have no active hooks (only .sample files)
	if !result.passed {
		t.Errorf("expected passed=true with no active hooks, got: %+v", result)
	}
}

func TestCheckGitHooks_WithActiveHook(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create an active hook
	hooksDir := filepath.Join(gitRoot, ".git", "hooks")
	hookPath := filepath.Join(hooksDir, "pre-commit")
	hookContent := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(hookPath, []byte(hookContent), 0755); err != nil {
		t.Fatalf("failed to create hook: %v", err)
	}

	result := checkGitHooks()

	if !result.passed {
		t.Errorf("expected passed=true with active hooks, got: %+v", result)
	}
	if !strings.Contains(result.message, "active") {
		t.Errorf("expected message to mention active hooks, got: %s", result.message)
	}
}

func TestCheckGitLFS_NotInGitRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ox-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	result := checkGitLFS()

	if !result.skipped {
		t.Error("expected skipped=true when not in git repo")
	}
}

func TestCheckGitLFS_NotUsed(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// no .gitattributes file, so LFS is not used
	result := checkGitLFS()

	if !result.skipped {
		t.Error("expected skipped=true when LFS not used")
	}
}

func TestCheckGitLFS_LFSPatternsPresent(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .gitattributes with LFS patterns
	gitattrsPath := filepath.Join(gitRoot, ".gitattributes")
	content := "*.bin filter=lfs diff=lfs merge=lfs -text\n"
	if err := os.WriteFile(gitattrsPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create .gitattributes: %v", err)
	}

	result := checkGitLFS()

	// result depends on whether git-lfs is installed
	// should not be skipped since LFS patterns are present
	if result.skipped {
		t.Error("expected not skipped when LFS patterns present")
	}
}

func TestCheckMergeConflicts_NotInGitRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ox-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	result := checkMergeConflicts()

	if !result.skipped {
		t.Error("expected skipped=true when not in git repo")
	}
}

func TestCheckMergeConflicts_NoConflicts(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	result := checkMergeConflicts()

	if !result.passed {
		t.Errorf("expected passed=true with no conflicts, got: %+v", result)
	}
	if result.message != "none" {
		t.Errorf("expected message 'none', got: %s", result.message)
	}
}

func TestCheckStashedChanges_NotInGitRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ox-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	result := checkStashedChanges()

	if !result.skipped {
		t.Error("expected skipped=true when not in git repo")
	}
}

func TestCheckStashedChanges_NoStashes(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	result := checkStashedChanges()

	if !result.passed {
		t.Errorf("expected passed=true with no stashes, got: %+v", result)
	}
	if result.message != "empty" {
		t.Errorf("expected message 'empty', got: %s", result.message)
	}
}

func TestCheckStashedChanges_WithStash(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create a file and commit it first
	testFile := filepath.Join(gitRoot, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	cmd := exec.Command("git", "add", "test.txt")
	cmd.Dir = gitRoot
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to add file: %v", err)
	}

	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = gitRoot
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// modify the file and stash it
	if err := os.WriteFile(testFile, []byte("modified"), 0644); err != nil {
		t.Fatalf("failed to modify test file: %v", err)
	}

	cmd = exec.Command("git", "stash")
	cmd.Dir = gitRoot
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to stash: %v", err)
	}

	result := checkStashedChanges()

	if !result.passed {
		t.Errorf("expected passed=true even with stashes, got: %+v", result)
	}
	if !strings.Contains(result.message, "stash") {
		t.Errorf("expected message to mention stash, got: %s", result.message)
	}
}

func TestCheckGitAuth_NotInGitRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ox-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	result := checkGitAuth()

	if !result.skipped {
		t.Error("expected skipped=true when not in git repo")
	}
}

func TestIsValidGitURL(t *testing.T) {
	tests := []struct {
		url   string
		valid bool
	}{
		{"https://github.com/org/repo.git", true},
		{"http://github.com/org/repo.git", true},
		{"git@github.com:org/repo.git", true},
		{"ssh://git@github.com/org/repo.git", true},
		{"git://github.com/org/repo.git", true},
		{"file:///path/to/repo", true},
		{"invalid-url", false},
		{"/local/path", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			result := isValidGitURL(tt.url)
			if result != tt.valid {
				t.Errorf("isValidGitURL(%q) = %v, want %v", tt.url, result, tt.valid)
			}
		})
	}
}

func TestNormalizeGitURLForCompare(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"bare HTTPS", "https://git.sageox.ai/team/ledger.git", "git.sageox.ai/team/ledger"},
		{"HTTPS with oauth2 credentials", "https://oauth2:secret-token@git.sageox.ai/team/ledger.git", "git.sageox.ai/team/ledger"},
		{"HTTPS with other credentials", "https://user:pass@git.sageox.ai/team/ledger.git", "git.sageox.ai/team/ledger"},
		{"SSH format", "git@github.com:org/repo.git", "github.com/org/repo"},
		{"HTTP", "http://localhost:3000/team/repo.git", "localhost:3000/team/repo"},
		{"no .git suffix", "https://git.sageox.ai/team/ledger", "git.sageox.ai/team/ledger"},
		{"mixed case", "HTTPS://Git.SageOx.AI/Team/Ledger.git", "git.sageox.ai/team/ledger"},
		{"with trailing spaces", "  https://git.sageox.ai/repo.git  ", "git.sageox.ai/repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeGitURLForCompare(tt.input)
			if got != tt.want {
				t.Errorf("normalizeGitURLForCompare(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCheckSSHAuth(t *testing.T) {
	// this test just verifies the function doesn't panic
	// actual SSH key presence depends on the test environment
	result := checkSSHAuth()
	// result can be true or false depending on environment
	_ = result
}
