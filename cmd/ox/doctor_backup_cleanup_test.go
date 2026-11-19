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

	// a backup with no .git and no files has nothing to lose; one holding any
	// file is kept (see TestCheckBackupDirectories_FixKeepsDataOutsideGit)
	bakPath := ledgerPath + ".bak.1738652400"
	if err := os.MkdirAll(filepath.Join(bakPath, "empty-subdir"), 0755); err != nil {
		t.Fatalf("failed to create backup dir: %v", err)
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

// TestCheckBackupDirectories_FixKeepsDataOutsideGit pins the Sacred-data rule:
// `ox doctor --fix` deletes a backup only when git can restore every file in it.
// A ledger self-heal moves .sageox/cache/sessions/ aside along with the clone,
// and that cache is gitignored, so `git status` alone reports such a backup as
// clean while it holds the only copy of a session recording.
func TestCheckBackupDirectories_FixKeepsDataOutsideGit(t *testing.T) {
	sessionRel := filepath.Join(".sageox", "cache", "sessions", "x")

	tests := []struct {
		name        string
		setup       func(t *testing.T, bakPath string)
		wantKept    bool
		wantSession bool // detail must name the backup's session folder
	}{
		{
			name: "clean git tree with gitignored session recording",
			setup: func(t *testing.T, bakPath string) {
				backupTestGit(t, bakPath, "init", "-q")
				backupTestWrite(t, filepath.Join(bakPath, ".gitignore"), ".sageox/cache/\n")
				backupTestWrite(t, filepath.Join(bakPath, "notes.md"), "committed")
				backupTestGit(t, bakPath, "add", ".gitignore", "notes.md")
				backupTestGit(t, bakPath, "commit", "-q", "-m", "init")
				backupTestWrite(t, filepath.Join(bakPath, sessionRel, "raw.jsonl"), "{}\n")
			},
			wantKept:    true,
			wantSession: true,
		},
		{
			name: "non-git self-heal backup holding a session recording",
			setup: func(t *testing.T, bakPath string) {
				backupTestWrite(t, filepath.Join(bakPath, sessionRel, "raw.jsonl"), "{}\n")
			},
			wantKept:    true,
			wantSession: true,
		},
		{
			name: "non-git backup holding an untracked file",
			setup: func(t *testing.T, bakPath string) {
				backupTestWrite(t, filepath.Join(bakPath, "readme.txt"), "old data")
			},
			wantKept: true,
		},
		{
			// the daemon moves a corrupt ledger aside; git cannot vouch for it
			name: "corrupt git repo",
			setup: func(t *testing.T, bakPath string) {
				if err := os.MkdirAll(filepath.Join(bakPath, ".git"), 0755); err != nil {
					t.Fatalf("create corrupt .git: %v", err)
				}
				backupTestWrite(t, filepath.Join(bakPath, "data.txt"), "unrecoverable")
			},
			wantKept: true,
		},
		{
			name: "clean git tree with every commit on its remote",
			setup: func(t *testing.T, bakPath string) {
				backupTestCommitted(t, bakPath)
				backupTestGit(t, bakPath, "update-ref", "refs/remotes/origin/main", "HEAD")
			},
			wantKept: false,
		},
		{
			// a clean tree says nothing about history: these commits are only here
			name: "clean git tree with commits no remote holds",
			setup: func(t *testing.T, bakPath string) {
				backupTestCommitted(t, bakPath)
			},
			wantKept: true,
		},
		{
			name: "clean git tree with a commit past its remote",
			setup: func(t *testing.T, bakPath string) {
				backupTestCommitted(t, bakPath)
				backupTestGit(t, bakPath, "update-ref", "refs/remotes/origin/main", "HEAD")
				backupTestWrite(t, filepath.Join(bakPath, "notes.md"), "unpushed edit")
				backupTestGit(t, bakPath, "commit", "-q", "-am", "unpushed")
			},
			wantKept: true,
		},
		{
			name: "clean git tree with a stash",
			setup: func(t *testing.T, bakPath string) {
				backupTestCommitted(t, bakPath)
				backupTestGit(t, bakPath, "update-ref", "refs/remotes/origin/main", "HEAD")
				backupTestWrite(t, filepath.Join(bakPath, "notes.md"), "stashed edit")
				backupTestGit(t, bakPath, "stash", "-q")
			},
			wantKept: true,
		},
		{
			// git status passes, but the history check cannot run: fail closed
			name: "history check fails",
			setup: func(t *testing.T, bakPath string) {
				backupTestCommitted(t, bakPath)
				backupTestGit(t, bakPath, "update-ref", "refs/remotes/origin/main", "HEAD")
				backupTestWrite(t, filepath.Join(bakPath, ".git", "refs", "heads", "broken"), strings.Repeat("12", 20)+"\n")
			},
			wantKept: true,
		},
	}

	for _, tt := range tests {
		// not parallel: checkBackupDirectories resolves the project from the cwd
		t.Run(tt.name, func(t *testing.T) {
			ledgerPath := setupBackupTestLedger(t)
			bakPath := ledgerPath + ".bak.1738652400" // 2025-02-04, past the 7-day threshold
			if err := os.MkdirAll(bakPath, 0755); err != nil {
				t.Fatalf("create backup dir: %v", err)
			}
			tt.setup(t, bakPath)

			result := checkBackupDirectories(true)

			_, statErr := os.Stat(bakPath)
			if kept := statErr == nil; kept != tt.wantKept {
				t.Fatalf("backup kept=%v, want %v (message=%q detail=%q)", kept, tt.wantKept, result.message, result.detail)
			}
			if !tt.wantKept {
				if !result.passed || result.warning {
					t.Errorf("expected a clean pass after removal, got: %+v", result)
				}
				return
			}
			if !result.warning || !strings.Contains(result.message, "skipped 1") {
				t.Errorf("expected warning naming the skipped backup, got: passed=%v warning=%v message=%q", result.passed, result.warning, result.message)
			}
			if !strings.Contains(result.detail, bakPath) {
				t.Errorf("detail should name the kept backup %s, got: %q", bakPath, result.detail)
			}
			if tt.wantSession {
				rawPath := filepath.Join(bakPath, sessionRel, "raw.jsonl")
				if _, err := os.Stat(rawPath); err != nil {
					t.Errorf("session recording must survive --fix: %v", err)
				}
				if sessionDir := filepath.Join(bakPath, sessionRel); !strings.Contains(result.detail, sessionDir) {
					t.Errorf("detail should name the session folder %s, got: %q", sessionDir, result.detail)
				}
			}
		})
	}
}

// setupBackupTestLedger creates a project repo configured with a git-initialized
// ledger, chdirs into the project, and returns the ledger path.
func setupBackupTestLedger(t *testing.T) string {
	t.Helper()
	gitRoot, cleanup := setupTempGitRepo(t)
	t.Cleanup(cleanup)
	t.Cleanup(changeToDir(t, gitRoot))
	requireSageoxDir(t, gitRoot)

	sageoxRoot := filepath.Join(filepath.Dir(gitRoot), filepath.Base(gitRoot)+"_sageox")
	t.Cleanup(func() { _ = os.RemoveAll(sageoxRoot) })
	ledgerPath := filepath.Join(sageoxRoot, "test-endpoint", "ledger")
	if err := os.MkdirAll(ledgerPath, 0755); err != nil {
		t.Fatalf("create ledger dir: %v", err)
	}
	backupTestGit(t, ledgerPath, "init", "-q")

	localCfg := &config.LocalConfig{Ledger: &config.LedgerConfig{Path: ledgerPath}}
	if err := config.SaveLocalConfig(gitRoot, localCfg); err != nil {
		t.Fatalf("save local config: %v", err)
	}
	return ledgerPath
}

// backupTestGit runs git in dir with a local identity, since tests run with an
// empty global git config.
func backupTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@test.sageox.ai"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// backupTestCommitted makes bakPath a repo whose working tree is fully committed.
func backupTestCommitted(t *testing.T, bakPath string) {
	t.Helper()
	backupTestGit(t, bakPath, "init", "-q", "-b", "main")
	backupTestWrite(t, filepath.Join(bakPath, "notes.md"), "committed")
	backupTestGit(t, bakPath, "add", "notes.md")
	backupTestGit(t, bakPath, "commit", "-q", "-m", "init")
}

func backupTestWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
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
