//go:build !short

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/config"
)

func TestCheckBackupDirectories_NoBackups(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)

	// create a valid ledger directory (git repo, no .bak siblings)
	ledgerPath := filepath.Join(filepath.Dir(gitRoot), filepath.Base(gitRoot)+"_sageox", "test-endpoint", "ledger")
	if err := os.MkdirAll(ledgerPath, 0755); err != nil {
		t.Fatalf("failed to create ledger dir: %v", err)
	}
	defer os.RemoveAll(filepath.Join(filepath.Dir(gitRoot), filepath.Base(gitRoot)+"_sageox"))

	cmd := exec.Command("git", "init")
	cmd.Dir = ledgerPath
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	localCfg := &config.LocalConfig{
		Ledger: &config.LedgerConfig{
			Path: ledgerPath,
		},
	}
	if err := config.SaveLocalConfig(gitRoot, localCfg); err != nil {
		t.Fatalf("failed to save local config: %v", err)
	}

	result := checkBackupDirectories(false)

	if !result.passed {
		t.Errorf("expected passed=true when no backups exist, got: %+v", result)
	}
	if result.message != "none found" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckBackupDirectories_DetectsBackups(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)

	// create a ledger path and a .bak sibling
	ledgerPath := filepath.Join(filepath.Dir(gitRoot), filepath.Base(gitRoot)+"_sageox", "test-endpoint", "ledger")
	if err := os.MkdirAll(ledgerPath, 0755); err != nil {
		t.Fatalf("failed to create ledger dir: %v", err)
	}
	defer os.RemoveAll(filepath.Join(filepath.Dir(gitRoot), filepath.Base(gitRoot)+"_sageox"))

	cmd := exec.Command("git", "init")
	cmd.Dir = ledgerPath
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	localCfg := &config.LocalConfig{
		Ledger: &config.LedgerConfig{
			Path: ledgerPath,
		},
	}
	if err := config.SaveLocalConfig(gitRoot, localCfg); err != nil {
		t.Fatalf("failed to save local config: %v", err)
	}

	// create backup directories
	bakPath1 := ledgerPath + ".bak.1738652400"
	if err := os.MkdirAll(bakPath1, 0755); err != nil {
		t.Fatalf("failed to create backup dir: %v", err)
	}
	// add a file so it has non-zero size
	if err := os.WriteFile(filepath.Join(bakPath1, "test.txt"), []byte("backup content"), 0644); err != nil {
		t.Fatalf("failed to create file in backup: %v", err)
	}

	result := checkBackupDirectories(false)

	if result.passed && !result.warning {
		t.Error("expected warning when backups exist")
	}
}

func TestCheckBackupDirectories_FixRemovesCleanBackup(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)

	// create a ledger path
	ledgerPath := filepath.Join(filepath.Dir(gitRoot), filepath.Base(gitRoot)+"_sageox", "test-endpoint", "ledger")
	if err := os.MkdirAll(ledgerPath, 0755); err != nil {
		t.Fatalf("failed to create ledger dir: %v", err)
	}
	defer os.RemoveAll(filepath.Join(filepath.Dir(gitRoot), filepath.Base(gitRoot)+"_sageox"))

	cmd := exec.Command("git", "init")
	cmd.Dir = ledgerPath
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	localCfg := &config.LocalConfig{
		Ledger: &config.LedgerConfig{
			Path: ledgerPath,
		},
	}
	if err := config.SaveLocalConfig(gitRoot, localCfg); err != nil {
		t.Fatalf("failed to save local config: %v", err)
	}

	// create a clean backup (no .git, so always safe to remove)
	bakPath := ledgerPath + ".bak.1738652400"
	if err := os.MkdirAll(bakPath, 0755); err != nil {
		t.Fatalf("failed to create backup dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bakPath, "readme.txt"), []byte("old data"), 0644); err != nil {
		t.Fatalf("failed to create file in backup: %v", err)
	}

	// verify backup exists
	if _, err := os.Stat(bakPath); os.IsNotExist(err) {
		t.Fatal("backup directory should exist before fix")
	}

	result := checkBackupDirectories(true)

	if !result.passed {
		t.Errorf("expected passed=true after fixing, got: %+v", result)
	}
	if !strings.Contains(result.message, "removed 1") {
		t.Errorf("expected message about removal, got: %s", result.message)
	}

	// verify backup was removed
	if _, err := os.Stat(bakPath); !os.IsNotExist(err) {
		t.Error("backup directory should have been removed by fix")
	}
}

func TestCheckBackupDirectories_FixSkipsDirtyGitBackup(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)

	// create a ledger path
	ledgerPath := filepath.Join(filepath.Dir(gitRoot), filepath.Base(gitRoot)+"_sageox", "test-endpoint", "ledger")
	if err := os.MkdirAll(ledgerPath, 0755); err != nil {
		t.Fatalf("failed to create ledger dir: %v", err)
	}
	defer os.RemoveAll(filepath.Join(filepath.Dir(gitRoot), filepath.Base(gitRoot)+"_sageox"))

	cmd := exec.Command("git", "init")
	cmd.Dir = ledgerPath
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	localCfg := &config.LocalConfig{
		Ledger: &config.LedgerConfig{
			Path: ledgerPath,
		},
	}
	if err := config.SaveLocalConfig(gitRoot, localCfg); err != nil {
		t.Fatalf("failed to save local config: %v", err)
	}

	// create a dirty backup (has .git and uncommitted changes)
	bakPath := ledgerPath + ".bak.1738652400"
	if err := os.MkdirAll(bakPath, 0755); err != nil {
		t.Fatalf("failed to create backup dir: %v", err)
	}

	// init as git repo
	initCmd := exec.Command("git", "init")
	initCmd.Dir = bakPath
	if err := initCmd.Run(); err != nil {
		t.Fatalf("failed to init backup git repo: %v", err)
	}

	// configure git identity for this repo
	configName := exec.Command("git", "config", "user.name", "Test User")
	configName.Dir = bakPath
	configName.Run()

	configEmail := exec.Command("git", "config", "user.email", "test@example.com")
	configEmail.Dir = bakPath
	configEmail.Run()

	// add an uncommitted file to make it dirty
	if err := os.WriteFile(filepath.Join(bakPath, "dirty.txt"), []byte("uncommitted change"), 0644); err != nil {
		t.Fatalf("failed to create dirty file: %v", err)
	}

	result := checkBackupDirectories(true)

	// should warn (not pass) because dirty backup was skipped
	if !result.warning {
		t.Errorf("expected warning when dirty backup is skipped, got: passed=%v warning=%v", result.passed, result.warning)
	}
	if !strings.Contains(result.message, "skipped") {
		t.Errorf("expected message to mention skipped, got: %s", result.message)
	}

	// verify dirty backup was NOT removed
	if _, err := os.Stat(bakPath); os.IsNotExist(err) {
		t.Error("dirty backup directory should NOT have been removed")
	}
}

func TestCheckBackupDirectories_FixRemovesCleanGitBackup(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)

	// create a ledger path
	ledgerPath := filepath.Join(filepath.Dir(gitRoot), filepath.Base(gitRoot)+"_sageox", "test-endpoint", "ledger")
	if err := os.MkdirAll(ledgerPath, 0755); err != nil {
		t.Fatalf("failed to create ledger dir: %v", err)
	}
	defer os.RemoveAll(filepath.Join(filepath.Dir(gitRoot), filepath.Base(gitRoot)+"_sageox"))

	cmd := exec.Command("git", "init")
	cmd.Dir = ledgerPath
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	localCfg := &config.LocalConfig{
		Ledger: &config.LedgerConfig{
			Path: ledgerPath,
		},
	}
	if err := config.SaveLocalConfig(gitRoot, localCfg); err != nil {
		t.Fatalf("failed to save local config: %v", err)
	}

	// create a clean git backup (has .git but no uncommitted changes)
	bakPath := ledgerPath + ".bak.1738652400"
	if err := os.MkdirAll(bakPath, 0755); err != nil {
		t.Fatalf("failed to create backup dir: %v", err)
	}

	initCmd := exec.Command("git", "init")
	initCmd.Dir = bakPath
	if err := initCmd.Run(); err != nil {
		t.Fatalf("failed to init backup git repo: %v", err)
	}

	result := checkBackupDirectories(true)

	if !result.passed {
		t.Errorf("expected passed=true after removing clean git backup, got: %+v", result)
	}

	// verify backup was removed
	if _, err := os.Stat(bakPath); !os.IsNotExist(err) {
		t.Error("clean git backup should have been removed")
	}
}

func TestCheckBackupDirectories_NotInGitRepo(t *testing.T) {
	skipIntegration(t)

	tmpDir, err := os.MkdirTemp("", "ox-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	restoreCwd := changeToDir(t, tmpDir)
	defer restoreCwd()

	result := checkBackupDirectories(false)

	if !result.skipped {
		t.Error("expected skipped=true when not in git repo")
	}
}

func TestInspectBackupDir_ParsesTimestamp(t *testing.T) {
	skipIntegration(t)

	tmpDir, err := os.MkdirTemp("", "ox-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// create a backup dir with known timestamp
	bakPath := filepath.Join(tmpDir, "ledger.bak.1738652400")
	if err := os.MkdirAll(bakPath, 0755); err != nil {
		t.Fatalf("failed to create backup dir: %v", err)
	}

	info := inspectBackupDir(bakPath)
	if info == nil {
		t.Fatal("expected non-nil info")
	}

	expectedTime := time.Unix(1738652400, 0)
	if !info.timestamp.Equal(expectedTime) {
		t.Errorf("expected timestamp %v, got %v", expectedTime, info.timestamp)
	}
	if info.hasGit {
		t.Error("expected hasGit=false for directory without .git")
	}
	if info.dirty {
		t.Error("expected dirty=false for non-git directory")
	}
}

func TestInspectBackupDir_DetectsGitRepo(t *testing.T) {
	skipIntegration(t)

	tmpDir, err := os.MkdirTemp("", "ox-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	bakPath := filepath.Join(tmpDir, "ledger.bak.1738652400")
	if err := os.MkdirAll(bakPath, 0755); err != nil {
		t.Fatalf("failed to create backup dir: %v", err)
	}

	// init as git repo
	initCmd := exec.Command("git", "init")
	initCmd.Dir = bakPath
	if err := initCmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	info := inspectBackupDir(bakPath)
	if info == nil {
		t.Fatal("expected non-nil info")
	}

	if !info.hasGit {
		t.Error("expected hasGit=true for directory with .git")
	}
	if info.dirty {
		t.Error("expected dirty=false for clean git repo")
	}
}

func TestInspectBackupDir_DetectsDirtyGitRepo(t *testing.T) {
	skipIntegration(t)

	tmpDir, err := os.MkdirTemp("", "ox-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	bakPath := filepath.Join(tmpDir, "ledger.bak.1738652400")
	if err := os.MkdirAll(bakPath, 0755); err != nil {
		t.Fatalf("failed to create backup dir: %v", err)
	}

	initCmd := exec.Command("git", "init")
	initCmd.Dir = bakPath
	if err := initCmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// add an untracked file to make it dirty
	if err := os.WriteFile(filepath.Join(bakPath, "dirty.txt"), []byte("data"), 0644); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}

	info := inspectBackupDir(bakPath)
	if info == nil {
		t.Fatal("expected non-nil info")
	}

	if !info.hasGit {
		t.Error("expected hasGit=true")
	}
	if !info.dirty {
		t.Error("expected dirty=true for repo with untracked files")
	}
	if len(info.dirtyFiles) == 0 {
		t.Error("expected dirtyFiles to be non-empty")
	}
}

func TestInspectBackupDir_ReturnsNilForNonDirectory(t *testing.T) {
	skipIntegration(t)

	tmpDir, err := os.MkdirTemp("", "ox-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// create a file (not a directory)
	filePath := filepath.Join(tmpDir, "ledger.bak.1738652400")
	if err := os.WriteFile(filePath, []byte("not a directory"), 0644); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}

	info := inspectBackupDir(filePath)
	if info != nil {
		t.Error("expected nil for non-directory path")
	}
}

func TestHumanSize(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{0, "0 B"},
		{100, "100 B"},
		{1024, "1 KB"},
		{1024 * 512, "512 KB"},
		{1024 * 1024, "1 MB"},
		{1024 * 1024 * 15, "15 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{1024 * 1024 * 1024 * 3, "3.0 GB"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := humanSize(tt.bytes)
			if result != tt.expected {
				t.Errorf("humanSize(%d) = %q, want %q", tt.bytes, result, tt.expected)
			}
		})
	}
}

func TestDirSize(t *testing.T) {
	skipIntegration(t)

	tmpDir, err := os.MkdirTemp("", "ox-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// create files with known sizes
	if err := os.WriteFile(filepath.Join(tmpDir, "file1.txt"), make([]byte, 100), 0644); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}
	subDir := filepath.Join(tmpDir, "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "file2.txt"), make([]byte, 200), 0644); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}

	size := dirSize(tmpDir)
	if size != 300 {
		t.Errorf("expected dirSize=300, got %d", size)
	}
}

func TestCheckBackupDirectories_MultipleBackups(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	requireSageoxDir(t, gitRoot)

	ledgerPath := filepath.Join(filepath.Dir(gitRoot), filepath.Base(gitRoot)+"_sageox", "test-endpoint", "ledger")
	if err := os.MkdirAll(ledgerPath, 0755); err != nil {
		t.Fatalf("failed to create ledger dir: %v", err)
	}
	defer os.RemoveAll(filepath.Join(filepath.Dir(gitRoot), filepath.Base(gitRoot)+"_sageox"))

	cmd := exec.Command("git", "init")
	cmd.Dir = ledgerPath
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	localCfg := &config.LocalConfig{
		Ledger: &config.LedgerConfig{
			Path: ledgerPath,
		},
	}
	if err := config.SaveLocalConfig(gitRoot, localCfg); err != nil {
		t.Fatalf("failed to save local config: %v", err)
	}

	// create multiple backup directories
	timestamps := []string{"1738652400", "1738566000", "1738479600"}
	for _, ts := range timestamps {
		bakPath := ledgerPath + ".bak." + ts
		if err := os.MkdirAll(bakPath, 0755); err != nil {
			t.Fatalf("failed to create backup dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(bakPath, "data.txt"), []byte("content"), 0644); err != nil {
			t.Fatalf("failed to create file: %v", err)
		}
	}

	result := checkBackupDirectories(false)

	if result.passed && !result.warning {
		t.Error("expected warning when multiple backups exist")
	}
}
