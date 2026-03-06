//go:build !short

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetSageoxFilesToCommit_NoSageoxDir(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	files := GetSageoxFilesToCommit()

	if len(files) != 0 {
		t.Errorf("expected no files when .sageox/ does not exist, got: %v", files)
	}
}

func TestGetSageoxFilesToCommit_EmptySageoxDir(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	files := GetSageoxFilesToCommit()

	if len(files) != 0 {
		t.Errorf("expected no files when .sageox/ is empty, got: %v", files)
	}
}

func TestGetSageoxFilesToCommit_ExplicitFiles(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	// create explicit committable files
	testFiles := []string{"config.json", "README.md", ".gitignore"}
	for _, f := range testFiles {
		path := filepath.Join(sageoxDir, f)
		if err := os.WriteFile(path, []byte("test content"), 0644); err != nil {
			t.Fatalf("failed to create file %s: %v", f, err)
		}
	}

	files := GetSageoxFilesToCommit()

	if len(files) != len(testFiles) {
		t.Errorf("expected %d files, got %d: %v", len(testFiles), len(files), files)
	}

	// verify all files are present
	for _, expectedFile := range testFiles {
		expectedPath := filepath.Join(".sageox", expectedFile)
		found := false
		for _, f := range files {
			if f == expectedPath {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected file %s not found in result: %v", expectedPath, files)
		}
	}
}

func TestGetSageoxFilesToCommit_PatternFiles(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	// create files matching patterns
	testFiles := []string{"custom.json", "notes.md", "info.json"}
	for _, f := range testFiles {
		path := filepath.Join(sageoxDir, f)
		if err := os.WriteFile(path, []byte("test content"), 0644); err != nil {
			t.Fatalf("failed to create file %s: %v", f, err)
		}
	}

	files := GetSageoxFilesToCommit()

	// should include all json and md files
	if len(files) < len(testFiles) {
		t.Errorf("expected at least %d files, got %d: %v", len(testFiles), len(files), files)
	}

	for _, expectedFile := range testFiles {
		expectedPath := filepath.Join(".sageox", expectedFile)
		found := false
		for _, f := range files {
			if f == expectedPath {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected file %s not found in result: %v", expectedPath, files)
		}
	}
}

func TestGetSageoxFilesToCommit_NoDuplicates(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	// create config.json which matches both explicit list and pattern
	configPath := filepath.Join(sageoxDir, "config.json")
	if err := os.WriteFile(configPath, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create config.json: %v", err)
	}

	files := GetSageoxFilesToCommit()

	// verify no duplicates
	seen := make(map[string]bool)
	for _, f := range files {
		if seen[f] {
			t.Errorf("duplicate file in result: %s", f)
		}
		seen[f] = true
	}
}

func TestGetSageoxFilesToCommit_IgnoresNonMatchingFiles(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	// create files that should not be included
	ignoredFiles := []string{"sessions.jsonl", "cache.db", "temp.txt", "logs.log"}
	for _, f := range ignoredFiles {
		path := filepath.Join(sageoxDir, f)
		if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
			t.Fatalf("failed to create file %s: %v", f, err)
		}
	}

	// also create one file that should be included
	configPath := filepath.Join(sageoxDir, "config.json")
	if err := os.WriteFile(configPath, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create config.json: %v", err)
	}

	files := GetSageoxFilesToCommit()

	// verify ignored files are not included
	for _, ignoredFile := range ignoredFiles {
		ignoredPath := filepath.Join(".sageox", ignoredFile)
		for _, f := range files {
			if f == ignoredPath {
				t.Errorf("ignored file %s should not be in result: %v", ignoredFile, files)
			}
		}
	}

	// verify config.json is included
	configRelPath := filepath.Join(".sageox", "config.json")
	found := false
	for _, f := range files {
		if f == configRelPath {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("config.json should be in result: %v", files)
	}
}

func TestForceAddSageoxFiles_NoFiles(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	// should not error when no files to add
	err := ForceAddSageoxFiles()
	if err != nil {
		t.Errorf("expected no error when no files to add, got: %v", err)
	}
}

func TestForceAddSageoxFiles_AddsFiles(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	// create files
	configPath := filepath.Join(sageoxDir, "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0644); err != nil {
		t.Fatalf("failed to create config.json: %v", err)
	}

	err := ForceAddSageoxFiles()
	if err != nil {
		t.Errorf("expected no error when adding files, got: %v", err)
	}

	// verify file was added to git index
	cmd := exec.Command("git", "ls-files", ".sageox/config.json")
	cmd.Dir = gitRoot
	output, err := cmd.Output()
	if err != nil || len(output) == 0 {
		t.Error("config.json should be in git index after force add")
	}
}

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

func TestCheckGitignore_NoGitignore(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	result := checkGitignore(false)

	if !result.skipped {
		t.Error("expected skipped=true when no .gitignore")
	}
	if result.message != "no .gitignore" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckGitignore_NotIgnored(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .gitignore without .sageox entry
	gitignorePath := filepath.Join(gitRoot, ".gitignore")
	content := `*.log
*.tmp
node_modules/
`
	if err := os.WriteFile(gitignorePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	result := checkGitignore(false)

	if !result.passed {
		t.Errorf("expected passed=true when not ignored, got: %+v", result)
	}
	if result.message != "not ignored" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckGitignore_Ignored(t *testing.T) {
	tests := []struct {
		name  string
		entry string
	}{
		{"exact match", ".sageox"},
		{"with trailing slash", ".sageox/"},
		{"with leading slash", "/.sageox"},
		{"with both slashes", "/.sageox/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitRoot, cleanup := setupTempGitRepo(t)
			defer cleanup()

			restoreCwd := changeToDir(t, gitRoot)
			defer restoreCwd()

			// create .gitignore with .sageox entry
			gitignorePath := filepath.Join(gitRoot, ".gitignore")
			content := "*.log\n" + tt.entry + "\nnode_modules/\n"
			if err := os.WriteFile(gitignorePath, []byte(content), 0644); err != nil {
				t.Fatalf("failed to create .gitignore: %v", err)
			}

			result := checkGitignore(false)

			if result.passed {
				t.Error("expected passed=false when .sageox is ignored")
			}
			if result.message != ".sageox/ is ignored" {
				t.Errorf("unexpected message: %s", result.message)
			}
			if !strings.Contains(result.detail, ".gitignore") {
				t.Error("expected detail to mention .gitignore")
			}
		})
	}
}

func TestCheckGitignore_IgnoredWithFix(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .gitignore with .sageox entry
	gitignorePath := filepath.Join(gitRoot, ".gitignore")
	content := `*.log
.sageox
node_modules/
`
	if err := os.WriteFile(gitignorePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	result := checkGitignore(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}
	if result.message != "fixed" {
		t.Errorf("unexpected message: %s", result.message)
	}

	// verify .sageox was removed from .gitignore
	newContent, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("failed to read .gitignore: %v", err)
	}

	if strings.Contains(string(newContent), ".sageox") {
		t.Error(".sageox should be removed from .gitignore after fix")
	}

	// verify other entries are preserved
	if !strings.Contains(string(newContent), "*.log") {
		t.Error("*.log should be preserved in .gitignore")
	}
	if !strings.Contains(string(newContent), "node_modules/") {
		t.Error("node_modules/ should be preserved in .gitignore")
	}
}

func TestCheckGitignore_FixPreservesOtherEntries(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .gitignore with multiple entries including .sageox
	gitignorePath := filepath.Join(gitRoot, ".gitignore")
	content := `# Build artifacts
*.log
*.tmp

# Project specific
.sageox/

# Dependencies
node_modules/
vendor/

# IDE
.vscode/
.idea/
`
	if err := os.WriteFile(gitignorePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	result := checkGitignore(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}

	// verify .sageox was removed
	newContent, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("failed to read .gitignore: %v", err)
	}

	if strings.Contains(string(newContent), ".sageox") {
		t.Error(".sageox should be removed from .gitignore")
	}

	// verify all other entries and comments are preserved
	expectedEntries := []string{
		"# Build artifacts",
		"*.log",
		"*.tmp",
		"# Project specific",
		"# Dependencies",
		"node_modules/",
		"vendor/",
		"# IDE",
		".vscode/",
		".idea/",
	}

	for _, entry := range expectedEntries {
		if !strings.Contains(string(newContent), entry) {
			t.Errorf("expected entry %q to be preserved in .gitignore", entry)
		}
	}
}

func TestCheckGitignore_MultipleIgnorePatterns(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .gitignore with multiple .sageox patterns
	gitignorePath := filepath.Join(gitRoot, ".gitignore")
	content := `*.log
.sageox
/.sageox/
node_modules/
`
	if err := os.WriteFile(gitignorePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	// should detect the first occurrence
	result := checkGitignore(false)

	if result.passed {
		t.Error("expected passed=false when .sageox is ignored")
	}
}

func TestCheckGitignore_CaseInsensitiveNotMatched(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .gitignore with case-different entry
	gitignorePath := filepath.Join(gitRoot, ".gitignore")
	content := `*.log
.SAGEOX
node_modules/
`
	if err := os.WriteFile(gitignorePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	result := checkGitignore(false)

	// should pass because .SAGEOX is not the same as .sageox
	if !result.passed {
		t.Errorf("expected passed=true for case-different entry, got: %+v", result)
	}
	if result.message != "not ignored" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckGitignore_CommentedEntryNotMatched(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .gitignore with commented .sageox
	gitignorePath := filepath.Join(gitRoot, ".gitignore")
	content := `*.log
# .sageox
node_modules/
`
	if err := os.WriteFile(gitignorePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	result := checkGitignore(false)

	// should pass because # .sageox is a comment
	if !result.passed {
		t.Errorf("expected passed=true for commented entry, got: %+v", result)
	}
	if result.message != "not ignored" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckGitignore_WhitespaceHandling(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .gitignore with whitespace around .sageox
	gitignorePath := filepath.Join(gitRoot, ".gitignore")
	content := `*.log
   .sageox
node_modules/
`
	if err := os.WriteFile(gitignorePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	result := checkGitignore(false)

	// should detect .sageox even with surrounding whitespace
	if result.passed {
		t.Error("expected passed=false when .sageox is ignored with whitespace")
	}
	if result.message != ".sageox/ is ignored" {
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

// edge case tests for checkGitignore

func TestCheckGitignore_MultipleSageoxVariants(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .gitignore with multiple .sageox variants
	gitignorePath := filepath.Join(gitRoot, ".gitignore")
	content := `*.log
.sageox
/.sageox/
node_modules/
.sageox/cache/
`
	if err := os.WriteFile(gitignorePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	result := checkGitignore(false)

	// should detect first occurrence
	if result.passed {
		t.Error("expected passed=false when .sageox appears multiple times")
	}
	if result.message != ".sageox/ is ignored" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckGitignore_CommentsMixedWithEntry(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .gitignore with comments and .sageox
	gitignorePath := filepath.Join(gitRoot, ".gitignore")
	content := `# Logs
*.log

# SageOx directory (DO NOT IGNORE)
.sageox/

# Dependencies
node_modules/
`
	if err := os.WriteFile(gitignorePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	result := checkGitignore(false)

	// should detect .sageox/ even with surrounding comments
	if result.passed {
		t.Error("expected passed=false when .sageox/ is present with comments")
	}
	if result.message != ".sageox/ is ignored" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckGitignore_WithNegationPattern(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .gitignore with .sageox/ followed by negation pattern
	gitignorePath := filepath.Join(gitRoot, ".gitignore")
	content := `*.log
.sageox/
!.sageox/config.json
node_modules/
`
	if err := os.WriteFile(gitignorePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	result := checkGitignore(false)

	// should still detect .sageox/ even with negation pattern
	if result.passed {
		t.Error("expected passed=false when .sageox/ is ignored despite negation pattern")
	}
	if result.message != ".sageox/ is ignored" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

func TestCheckGitignore_FixRemovesOnlyFirstOccurrence(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .gitignore with multiple .sageox entries
	gitignorePath := filepath.Join(gitRoot, ".gitignore")
	content := `*.log
.sageox
/.sageox/
node_modules/
`
	if err := os.WriteFile(gitignorePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	result := checkGitignore(true)

	if !result.passed {
		t.Errorf("expected passed=true after fix, got: %+v", result)
	}

	// verify only first occurrence was removed
	newContent, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("failed to read .gitignore: %v", err)
	}

	// first occurrence should be removed
	lines := strings.Split(string(newContent), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == ".sageox" {
			t.Error("first .sageox occurrence should be removed")
		}
	}

	// second occurrence should still be present
	if !strings.Contains(string(newContent), "/.sageox/") {
		t.Error("second occurrence /.sageox/ should remain")
	}
}

func TestCheckGitignore_InlineCommentNotMatched(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .gitignore with inline comment
	gitignorePath := filepath.Join(gitRoot, ".gitignore")
	content := `*.log
temp/  # .sageox should not be ignored
node_modules/
`
	if err := os.WriteFile(gitignorePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	result := checkGitignore(false)

	// should pass because .sageox in inline comment is not an ignore pattern
	if !result.passed {
		t.Errorf("expected passed=true when .sageox is only in inline comment, got: %+v", result)
	}
	if result.message != "not ignored" {
		t.Errorf("unexpected message: %s", result.message)
	}
}

// edge case tests for GetSageoxFilesToCommit

func TestGetSageoxFilesToCommit_NestedDirectories(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	// create nested directory structure
	nestedDir := filepath.Join(sageoxDir, "subdir", "nested")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatalf("failed to create nested dir: %v", err)
	}

	// create file in nested directory
	nestedFile := filepath.Join(nestedDir, "data.json")
	if err := os.WriteFile(nestedFile, []byte("{}"), 0644); err != nil {
		t.Fatalf("failed to create nested file: %v", err)
	}

	files := GetSageoxFilesToCommit()

	// nested json files should not be included (patterns only match root level)
	for _, f := range files {
		if strings.Contains(f, "subdir") || strings.Contains(f, "nested") {
			t.Errorf("nested file should not be in result: %s", f)
		}
	}
}

func TestGetSageoxFilesToCommit_NoSageoxDirectory(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// no .sageox directory exists
	files := GetSageoxFilesToCommit()

	if len(files) != 0 {
		t.Errorf("expected no files when .sageox does not exist, got: %v", files)
	}
}

func TestGetSageoxFilesToCommit_VariousGlobPatterns(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	// create various files matching and not matching patterns
	testFiles := map[string]bool{
		"config.json":    true,  // explicit file
		"README.md":      true,  // explicit file
		".gitignore":     true,  // explicit file
		"custom.json":    true,  // matches *.json pattern
		"notes.md":       true,  // matches *.md pattern
		"data.jsonl":     false, // does not match any pattern
		"script.sh":      false, // does not match any pattern
		"temp.txt":       false, // does not match any pattern
		"sessions.jsonl": false, // does not match any pattern
		"cache.db":       false, // does not match any pattern
		"metadata.JSON":  false, // case sensitivity
		"readme.MD":      false, // case sensitivity
	}

	for filename := range testFiles {
		path := filepath.Join(sageoxDir, filename)
		if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
			t.Fatalf("failed to create file %s: %v", filename, err)
		}
	}

	files := GetSageoxFilesToCommit()

	// verify expected files are included
	for filename, shouldInclude := range testFiles {
		expectedPath := filepath.Join(".sageox", filename)
		found := false
		for _, f := range files {
			if f == expectedPath {
				found = true
				break
			}
		}
		if shouldInclude && !found {
			t.Errorf("expected file %s to be included but was not found", filename)
		}
		if !shouldInclude && found {
			t.Errorf("file %s should not be included but was found", filename)
		}
	}
}

func TestGetSageoxFilesToCommit_SymlinksHandled(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	// create actual file
	actualFile := filepath.Join(sageoxDir, "actual.json")
	if err := os.WriteFile(actualFile, []byte("{}"), 0644); err != nil {
		t.Fatalf("failed to create actual file: %v", err)
	}

	// create symlink to file
	symlinkPath := filepath.Join(sageoxDir, "link.json")
	if err := os.Symlink(actualFile, symlinkPath); err != nil {
		t.Skipf("skipping symlink test: %v", err)
	}

	files := GetSageoxFilesToCommit()

	// both actual file and symlink should be found
	foundActual := false
	foundLink := false
	for _, f := range files {
		if f == filepath.Join(".sageox", "actual.json") {
			foundActual = true
		}
		if f == filepath.Join(".sageox", "link.json") {
			foundLink = true
		}
	}

	if !foundActual {
		t.Error("actual.json should be included")
	}
	if !foundLink {
		t.Error("link.json should be included")
	}
}

func TestGetSageoxFilesToCommit_EmptyFilesIncluded(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	// create empty file
	emptyFile := filepath.Join(sageoxDir, "empty.json")
	if err := os.WriteFile(emptyFile, []byte(""), 0644); err != nil {
		t.Fatalf("failed to create empty file: %v", err)
	}

	files := GetSageoxFilesToCommit()

	// empty file should still be included
	found := false
	for _, f := range files {
		if f == filepath.Join(".sageox", "empty.json") {
			found = true
			break
		}
	}

	if !found {
		t.Error("empty.json should be included in files to commit")
	}
}

func TestForceAddSageoxFiles_MultipleFiles(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	// create multiple files
	testFiles := []string{"config.json", "README.md", ".gitignore", "custom.json"}
	for _, f := range testFiles {
		path := filepath.Join(sageoxDir, f)
		if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
			t.Fatalf("failed to create file %s: %v", f, err)
		}
	}

	err := ForceAddSageoxFiles()
	if err != nil {
		t.Errorf("expected no error when adding multiple files, got: %v", err)
	}

	// verify all files were added
	for _, f := range testFiles {
		relPath := filepath.Join(".sageox", f)
		cmd := exec.Command("git", "ls-files", relPath)
		cmd.Dir = gitRoot
		output, err := cmd.Output()
		if err != nil || len(output) == 0 {
			t.Errorf("file %s should be in git index after force add", f)
		}
	}
}

func TestForceAddSageoxFiles_OverridesGitignore(t *testing.T) {
	gitRoot, cleanup := setupTempGitRepo(t)
	defer cleanup()

	restoreCwd := changeToDir(t, gitRoot)
	defer restoreCwd()

	// create .gitignore that ignores .sageox
	gitignorePath := filepath.Join(gitRoot, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte(".sageox/\n"), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox dir: %v", err)
	}

	// create file
	configPath := filepath.Join(sageoxDir, "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0644); err != nil {
		t.Fatalf("failed to create config.json: %v", err)
	}

	err := ForceAddSageoxFiles()
	if err != nil {
		t.Errorf("expected no error with force add, got: %v", err)
	}

	// verify file was added despite .gitignore
	cmd := exec.Command("git", "ls-files", ".sageox/config.json")
	cmd.Dir = gitRoot
	output, err := cmd.Output()
	if err != nil || len(output) == 0 {
		t.Error("config.json should be in git index after force add despite .gitignore")
	}
}
