//go:build !short

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckGitStatus_NotInGitRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ox-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	result := checkGitStatus()

	if !result.skipped {
		t.Error("expected skipped=true when not in git repo")
	}
	if result.name != "Git repository" {
		t.Errorf("unexpected name: %s", result.name)
	}
	if result.message != "not in a git repo" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckGitStatus_NoSageoxDir(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	result := checkGitStatus()

	if !result.skipped {
		t.Error("expected skipped=true when .sageox/ does not exist")
	}
	if result.name != ".sageox/ tracked" {
		t.Errorf("unexpected name: %s", result.name)
	}
	if result.message != "directory not found" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckGitStatus_Clean(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	// create and commit a file
	configPath := filepath.Join(sageoxDir, "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0644); err != nil {
		t.Fatalf("failed to create config.json: %v", err)
	}

	cmd := exec.Command("git", "add", ".sageox/config.json")
	cmd.Dir = gitRoot
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to add file: %v", err)
	}

	cmd = exec.Command("git", "commit", "-m", "Add config")
	cmd.Dir = gitRoot
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	result := checkGitStatus()

	if !result.passed {
		t.Errorf("expected passed=true when clean, got: %+v", result)
	}
	if result.name != ".sageox/ tracked" {
		t.Errorf("unexpected name: %s", result.name)
	}
	if result.message != "committed" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckGitStatus_UncommittedChanges(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	// create uncommitted file
	configPath := filepath.Join(sageoxDir, "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0644); err != nil {
		t.Fatalf("failed to create config.json: %v", err)
	}

	result := checkGitStatus()

	if !result.passed {
		t.Errorf("expected passed=true (with warning), got: %+v", result)
	}
	if !result.warning {
		t.Error("expected warning=true for unstaged changes")
	}
	if result.name != ".sageox/ changes" {
		t.Errorf("unexpected name: %s", result.name)
	}
	if result.message != "unstaged" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckSageoxFilesTracked_NoFiles(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	result := checkSageoxFilesTracked(false)

	if !result.skipped {
		t.Error("expected skipped=true when no files found")
	}
	if result.message != "no files found" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckSageoxFilesTracked_UntrackedFiles(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	// create untracked file
	configPath := filepath.Join(sageoxDir, "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0644); err != nil {
		t.Fatalf("failed to create config.json: %v", err)
	}

	result := checkSageoxFilesTracked(false)

	if result.passed {
		t.Error("expected passed=false when files are untracked")
	}
	if result.message != "untracked files" {
		t.Errorf("unexpected message: %s", result.message)
	}
	if !strings.Contains(result.detail, "ox init") {
		t.Error("expected detail to mention ox init")
	}
}

func TestCheckSageoxFilesTracked_UntrackedFilesWithFix(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	// create untracked file
	configPath := filepath.Join(sageoxDir, "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0644); err != nil {
		t.Fatalf("failed to create config.json: %v", err)
	}

	result := checkSageoxFilesTracked(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}
	if result.message != "fixed (added to VCS)" {
		t.Errorf("unexpected message: %s", result.message)
	}

	// verify file was added
	cmd := exec.Command("git", "ls-files", ".sageox/config.json")
	cmd.Dir = gitRoot
	output, err := cmd.Output()
	if err != nil || len(output) == 0 {
		t.Error("config.json should be in git index after fix")
	}
}

func TestCheckSageoxFilesTracked_AllTracked(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	// create and track file
	configPath := filepath.Join(sageoxDir, "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0644); err != nil {
		t.Fatalf("failed to create config.json: %v", err)
	}

	cmd := exec.Command("git", "add", ".sageox/config.json")
	cmd.Dir = gitRoot
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to add file: %v", err)
	}

	result := checkSageoxFilesTracked(false)

	if !result.passed {
		t.Errorf("expected passed=true when all tracked, got: %+v", result)
	}
	if result.message != "all tracked" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

// edge case tests for checkGitStatus

func TestCheckGitStatus_StagedButUncommitted(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	// create file and stage it
	configPath := filepath.Join(sageoxDir, "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0644); err != nil {
		t.Fatalf("failed to create config.json: %v", err)
	}

	cmd := exec.Command("git", "add", ".sageox/config.json")
	cmd.Dir = gitRoot
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to stage file: %v", err)
	}

	result := checkGitStatus()

	// staged but not committed should be informational (no warning)
	if !result.passed {
		t.Errorf("expected passed=true, got: %+v", result)
	}
	if result.warning {
		t.Error("expected warning=false for staged-only changes")
	}
	if result.name != ".sageox/ changes" {
		t.Errorf("unexpected name: %s", result.name)
	}
	if result.message != "staged, ready to commit" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckGitStatus_UntrackedFilesInSageox(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	// create untracked file
	sessionPath := filepath.Join(sageoxDir, "sessions.jsonl")
	if err := os.WriteFile(sessionPath, []byte("session data"), 0644); err != nil {
		t.Fatalf("failed to create sessions.jsonl: %v", err)
	}

	result := checkGitStatus()

	// untracked sessions.jsonl should show warning (unstaged)
	if !result.passed {
		t.Errorf("expected passed=true with warning, got: %+v", result)
	}
	if !result.warning {
		t.Error("expected warning=true for untracked files in .sageox")
	}
	if result.message != "unstaged" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckGitStatus_MixedStagedAndUnstaged(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	// create and commit first file
	config1Path := filepath.Join(sageoxDir, "config.json")
	if err := os.WriteFile(config1Path, []byte("{}"), 0644); err != nil {
		t.Fatalf("failed to create config.json: %v", err)
	}

	cmd := exec.Command("git", "add", ".sageox/config.json")
	cmd.Dir = gitRoot
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to add file: %v", err)
	}

	cmd = exec.Command("git", "commit", "-m", "Add config")
	cmd.Dir = gitRoot
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// modify committed file
	if err := os.WriteFile(config1Path, []byte("{\"new\":\"value\"}"), 0644); err != nil {
		t.Fatalf("failed to modify config.json: %v", err)
	}

	// create new file and stage it
	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Test"), 0644); err != nil {
		t.Fatalf("failed to create README.md: %v", err)
	}

	cmd = exec.Command("git", "add", ".sageox/README.md")
	cmd.Dir = gitRoot
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to stage README.md: %v", err)
	}

	result := checkGitStatus()

	// mixed staged and unstaged changes should show warning (unstaged takes precedence)
	if !result.passed {
		t.Errorf("expected passed=true with warning, got: %+v", result)
	}
	if !result.warning {
		t.Error("expected warning=true for mixed changes")
	}
	if result.message != "unstaged" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

// edge case tests for checkSageoxFilesTracked

func TestCheckSageoxFilesTracked_MixedTrackedUntracked(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	// create and track one file
	configPath := filepath.Join(sageoxDir, "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0644); err != nil {
		t.Fatalf("failed to create config.json: %v", err)
	}

	cmd := exec.Command("git", "add", ".sageox/config.json")
	cmd.Dir = gitRoot
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to add file: %v", err)
	}

	// create untracked file
	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Test"), 0644); err != nil {
		t.Fatalf("failed to create README.md: %v", err)
	}

	result := checkSageoxFilesTracked(false)

	// should fail because README.md is untracked
	if result.passed {
		t.Error("expected passed=false when some files are untracked")
	}
	if result.message != "untracked files" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckSageoxFilesTracked_FilesInGitignore(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	// create .gitignore that ignores config.json
	gitignorePath := filepath.Join(gitRoot, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte(".sageox/config.json\n"), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	// create file that should be tracked
	configPath := filepath.Join(sageoxDir, "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0644); err != nil {
		t.Fatalf("failed to create config.json: %v", err)
	}

	result := checkSageoxFilesTracked(false)

	// file is in .gitignore so untracked
	if result.passed {
		t.Error("expected passed=false when file is in .gitignore")
	}
	if result.message != "untracked files" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckSageoxFilesTracked_WithFixErrorScenario(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	// create file
	configPath := filepath.Join(sageoxDir, "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0644); err != nil {
		t.Fatalf("failed to create config.json: %v", err)
	}

	// simulate git error by removing .git directory (making it not a git repo)
	gitDir := filepath.Join(gitRoot, ".git")
	if err := os.RemoveAll(gitDir); err != nil {
		t.Fatalf("failed to remove .git: %v", err)
	}

	result := checkSageoxFilesTracked(true)

	// git add should fail but we handle it gracefully
	// the function should return early because findGitRoot returns empty
	if !result.skipped {
		t.Error("expected skipped=true when not in git repo after fix attempt")
	}
}
