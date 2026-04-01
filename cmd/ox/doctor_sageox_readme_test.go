//go:build !short

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckReadmeFile_NotFound(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .sageox dir so we test "file not found" not "not initialized"
	requireSageoxDir(t, gitRoot)

	result := checkReadmeFile(false)

	if result.passed {
		t.Error("expected passed=false when README.md not found")
	}
	if result.message != "not found" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckReadmeFile_NotFoundWithFix(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .sageox dir so we test "file not found" not "not initialized"
	requireSageoxDir(t, gitRoot)

	result := checkReadmeFile(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}
	if result.message != "created" {
		t.Errorf("unexpected message: %s", result.message)
	}

	// verify file was created
	readmePath := filepath.Join(gitRoot, ".sageox", "README.md")
	content, err := os.ReadFile(readmePath)
	if err != nil {
		t.Errorf("README.md should exist after fix: %v", err)
	}

	// verify content matches expected
	expectedContent := GetSageoxReadmeContent(nil)
	if string(content) != expectedContent {
		t.Error("README.md content does not match expected")
	}
}

func TestCheckReadmeFile_Empty(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create empty README.md
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.WriteFile(readmePath, []byte(""), 0644); err != nil {
		t.Fatalf("failed to create empty README.md: %v", err)
	}

	result := checkReadmeFile(false)

	if result.passed {
		t.Error("expected passed=false for empty README.md")
	}
	if result.message != "empty" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckReadmeFile_EmptyWithFix(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create empty README.md
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.WriteFile(readmePath, []byte(""), 0644); err != nil {
		t.Fatalf("failed to create empty README.md: %v", err)
	}

	result := checkReadmeFile(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}
	if result.message != "fixed (was empty)" {
		t.Errorf("unexpected message: %s", result.message)
	}

	// verify file was updated
	content, err := os.ReadFile(readmePath)
	if err != nil {
		t.Errorf("README.md should exist after fix: %v", err)
	}
	if len(content) == 0 {
		t.Error("README.md should not be empty after fix")
	}
}

func TestCheckReadmeFile_Stale(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create README.md with correct content but old modification time
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.WriteFile(readmePath, []byte(GetSageoxReadmeContent(nil)), 0644); err != nil {
		t.Fatalf("failed to create README.md: %v", err)
	}

	// set modification time to 8 days ago
	eightDaysAgo := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(readmePath, eightDaysAgo, eightDaysAgo); err != nil {
		t.Fatalf("failed to set file time: %v", err)
	}

	result := checkReadmeFile(false)

	if !result.passed {
		t.Error("expected passed=true for stale file (with warning)")
	}
	if !result.warning {
		t.Error("expected warning=true for stale file")
	}
	if result.message != "stale" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckReadmeFile_StaleWithFix(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create README.md with correct content but old modification time
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.WriteFile(readmePath, []byte(GetSageoxReadmeContent(nil)), 0644); err != nil {
		t.Fatalf("failed to create README.md: %v", err)
	}

	// set modification time to 8 days ago
	eightDaysAgo := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(readmePath, eightDaysAgo, eightDaysAgo); err != nil {
		t.Fatalf("failed to set file time: %v", err)
	}

	result := checkReadmeFile(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}
	if result.message != "refreshed" {
		t.Errorf("unexpected message: %s", result.message)
	}

	// verify file was updated
	content, err := os.ReadFile(readmePath)
	if err != nil {
		t.Errorf("README.md should exist after fix: %v", err)
	}

	expectedContent := GetSageoxReadmeContent(nil)
	if string(content) != expectedContent {
		t.Error("README.md should be updated with current content")
	}
}

func TestCheckReadmeFile_Fresh(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create fresh README.md
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.WriteFile(readmePath, []byte(GetSageoxReadmeContent(nil)), 0644); err != nil {
		t.Fatalf("failed to create README.md: %v", err)
	}

	result := checkReadmeFile(false)

	if !result.passed {
		t.Errorf("expected passed=true for fresh file, got: %+v", result)
	}
	if result.warning {
		t.Error("expected warning=false for fresh file")
	}
	if result.message != "ok" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckReadmeFile_Outdated(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create README.md with outdated content (different from expected template)
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Old Template\n\nThis is outdated content."), 0644); err != nil {
		t.Fatalf("failed to create README.md: %v", err)
	}

	result := checkReadmeFile(false)

	if !result.passed {
		t.Error("expected passed=true for outdated file (with warning)")
	}
	if !result.warning {
		t.Error("expected warning=true for outdated file")
	}
	if result.message != "outdated" {
		t.Errorf("unexpected message: %s, expected 'outdated'", result.message)
	}
	if !strings.Contains(result.detail, "update to latest version") {
		t.Errorf("expected detail to mention updating, got: %s", result.detail)
	}
}

func TestCheckReadmeFile_OutdatedWithFix(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create README.md with outdated content
	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Old Template\n\nOutdated content from old ox version."), 0644); err != nil {
		t.Fatalf("failed to create README.md: %v", err)
	}

	result := checkReadmeFile(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}
	if result.message != "updated to latest version" {
		t.Errorf("unexpected message: %s, expected 'updated to latest version'", result.message)
	}

	// verify file was updated
	content, err := os.ReadFile(readmePath)
	if err != nil {
		t.Errorf("README.md should exist after fix: %v", err)
	}
	expectedContent := GetSageoxReadmeContent(nil)
	if string(content) != expectedContent {
		t.Error("README.md should be updated with latest content")
	}
}

func TestCheckReadmeFile_SymLink(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")

	// create a target file with proper content
	targetPath := filepath.Join(gitRoot, "readme_target.md")
	if err := os.WriteFile(targetPath, []byte(GetSageoxReadmeContent(nil)), 0644); err != nil {
		t.Fatalf("failed to create target file: %v", err)
	}

	// create symlink to target
	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.Symlink(targetPath, readmePath); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	result := checkReadmeFile(false)

	// symlink should work - it follows the link and reads the content
	if !result.passed {
		t.Errorf("expected passed=true for symlink to valid file, got: %+v", result)
	}
}

func TestCheckReadmeFile_VeryOld(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	readmePath := filepath.Join(sageoxDir, "README.md")
	// use correct content to test pure age-based staleness
	if err := os.WriteFile(readmePath, []byte(GetSageoxReadmeContent(nil)), 0644); err != nil {
		t.Fatalf("failed to create README.md: %v", err)
	}

	// set modification time to 40 days ago (very old)
	fortyDaysAgo := time.Now().Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(readmePath, fortyDaysAgo, fortyDaysAgo); err != nil {
		t.Fatalf("failed to set file time: %v", err)
	}

	result := checkReadmeFile(false)

	if !result.passed {
		t.Error("expected passed=true for very old file (with warning)")
	}
	if !result.warning {
		t.Error("expected warning=true for very old file")
	}
	if result.message != "stale" {
		t.Errorf("unexpected message: %s", result.message)
	}
	if !strings.Contains(result.detail, "40 days old") {
		t.Errorf("expected detail to mention 40 days, got: %s", result.detail)
	}
}

func TestCheckReadmeFile_JustBarelyStale(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	readmePath := filepath.Join(sageoxDir, "README.md")
	// use correct content to test pure age-based staleness
	if err := os.WriteFile(readmePath, []byte(GetSageoxReadmeContent(nil)), 0644); err != nil {
		t.Fatalf("failed to create README.md: %v", err)
	}

	// set modification time to exactly 7 days + 1 hour ago
	sevenDaysOneHour := time.Now().Add(-7*24*time.Hour - 1*time.Hour)
	if err := os.Chtimes(readmePath, sevenDaysOneHour, sevenDaysOneHour); err != nil {
		t.Fatalf("failed to set file time: %v", err)
	}

	result := checkReadmeFile(false)

	if !result.passed {
		t.Error("expected passed=true for barely stale file (with warning)")
	}
	if !result.warning {
		t.Error("expected warning=true for barely stale file")
	}
	if result.message != "stale" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckReadmeFile_JustBelowStaleThreshold(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.WriteFile(readmePath, []byte(GetSageoxReadmeContent(nil)), 0644); err != nil {
		t.Fatalf("failed to create README.md: %v", err)
	}

	// set modification time to 6 days ago (just below 7 day threshold)
	sixDaysAgo := time.Now().Add(-6 * 24 * time.Hour)
	if err := os.Chtimes(readmePath, sixDaysAgo, sixDaysAgo); err != nil {
		t.Fatalf("failed to set file time: %v", err)
	}

	result := checkReadmeFile(false)

	if !result.passed {
		t.Errorf("expected passed=true for file below stale threshold, got: %+v", result)
	}
	if result.warning {
		t.Error("expected warning=false for file below stale threshold")
	}
	if result.message != "ok" {
		t.Errorf("unexpected message: %s", result.message)
	}
}
