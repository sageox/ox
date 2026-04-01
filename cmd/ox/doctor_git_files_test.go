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

	// exact count: custom.json and info.json match *.json, notes.md matches *.md
	expectedPaths := make(map[string]bool)
	for _, f := range testFiles {
		expectedPaths[filepath.Join(".sageox", f)] = false
	}

	if len(files) != len(expectedPaths) {
		t.Fatalf("expected exactly %d files, got %d: %v", len(expectedPaths), len(files), files)
	}

	for _, f := range files {
		if _, ok := expectedPaths[f]; !ok {
			t.Errorf("unexpected file in result: %s", f)
		} else {
			expectedPaths[f] = true
		}
	}
	for path, found := range expectedPaths {
		if !found {
			t.Errorf("expected file %s not found in result: %v", path, files)
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
