//go:build !short

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckReadmeFile_FixCreates verifies that when fix=true and README.md is missing,
// the check creates the file with the expected content.
func TestCheckReadmeFile_FixCreates(t *testing.T) {
	tmpDir := testGitRepo(t)
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(tmpDir)

	// run the check with fix=true
	result := checkReadmeFile(true)

	// verify the check passed
	if !result.passed {
		t.Errorf("expected check to pass, got passed=%v, message=%q, detail=%q",
			result.passed, result.message, result.detail)
	}

	// verify the message indicates creation
	if result.message != "created" {
		t.Errorf("expected message 'created', got %q", result.message)
	}

	// verify the file was actually created
	readmePath := filepath.Join(sageoxDir, "README.md")
	if _, err := os.Stat(readmePath); os.IsNotExist(err) {
		t.Fatalf("README.md was not created at %s", readmePath)
	}

	// verify the content matches the expected content
	content, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("failed to read created README.md: %v", err)
	}

	expectedContent := GetSageoxReadmeContent(nil)
	if string(content) != expectedContent {
		t.Errorf("README.md content does not match expected.\nGot:\n%s\n\nExpected:\n%s",
			string(content), expectedContent)
	}
}

// TestCheckReadmeFile_FixEmpty verifies that when fix=true and README.md exists but is empty,
// the check writes the expected content to the file.
func TestCheckReadmeFile_FixEmpty(t *testing.T) {
	tmpDir := testGitRepo(t)
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	// create an empty README.md
	readmePath := filepath.Join(sageoxDir, "README.md")
	if err := os.WriteFile(readmePath, []byte(""), 0644); err != nil {
		t.Fatalf("failed to create empty README.md: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(tmpDir)

	result := checkReadmeFile(true)

	if !result.passed {
		t.Errorf("expected check to pass, got passed=%v, message=%q, detail=%q",
			result.passed, result.message, result.detail)
	}

	if result.message != "fixed (was empty)" {
		t.Errorf("expected message 'fixed (was empty)', got %q", result.message)
	}

	// verify the file now has content
	content, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("failed to read README.md: %v", err)
	}

	expectedContent := GetSageoxReadmeContent(nil)
	if string(content) != expectedContent {
		t.Errorf("README.md content does not match expected after fix")
	}
}

// TestCheckReadmeFile_FixOutdatedContent verifies that when fix=true and README.md
// has outdated content, the check updates the file to the latest content.
// Failure prevented: stale README.md left behind after template changes.
func TestCheckReadmeFile_FixOutdatedContent(t *testing.T) {
	tmpDir := testGitRepo(t)
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	// create a README.md with old content (different from expected template)
	readmePath := filepath.Join(sageoxDir, "README.md")
	oldContent := "# Old SageOx README\nThis is outdated content."
	if err := os.WriteFile(readmePath, []byte(oldContent), 0644); err != nil {
		t.Fatalf("failed to create README.md: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(tmpDir)

	result := checkReadmeFile(true)

	if !result.passed {
		t.Errorf("expected check to pass, got passed=%v, message=%q, detail=%q",
			result.passed, result.message, result.detail)
	}

	// content mismatch is detected as "outdated" and fixed to latest version
	if result.message != "updated to latest version" {
		t.Errorf("expected message 'updated to latest version', got %q", result.message)
	}

	// verify the file was updated with current content
	content, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("failed to read README.md: %v", err)
	}

	expectedContent := GetSageoxReadmeContent(nil)
	if string(content) != expectedContent {
		t.Errorf("README.md was not updated to latest content")
	}
}

// TestMergeGitignoreEntries_RemovesConflicts verifies that conflicting entries are removed.
func TestMergeGitignoreEntries_RemovesConflicts(t *testing.T) {
	existingContent := `logs/
cache/
session.jsonl
sessions/
discovered.jsonl
!README.md
!config.json
!offline/
`

	merged, changed := mergeGitignoreEntries(existingContent)

	if !changed {
		t.Errorf("expected changed=true when conflicts exist, got changed=false")
	}

	// verify the conflicting entry "discovered.jsonl" was removed
	lines := strings.Split(merged, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "discovered.jsonl" {
			t.Errorf("conflicting entry 'discovered.jsonl' was not removed:\n%s", merged)
		}
	}

	// verify "!discovered.jsonl" is present
	if !strings.Contains(merged, "!discovered.jsonl") {
		t.Errorf("required entry '!discovered.jsonl' not found in merged content:\n%s", merged)
	}
}

// TestCheckReadmeFile_PermissionError verifies handling of permission errors
func TestCheckReadmeFile_PermissionError(t *testing.T) {
	gitRoot := testGitRepo(t)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	// make .sageox directory read-only to prevent writes
	if err := os.Chmod(sageoxDir, 0555); err != nil {
		t.Fatalf("failed to chmod .sageox: %v", err)
	}
	defer os.Chmod(sageoxDir, 0755) // restore permissions

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	result := checkReadmeFile(true)

	// should fail with a write error
	if result.passed {
		t.Error("expected passed=false when directory is read-only")
	}
	if !strings.Contains(result.message, "failed") {
		t.Errorf("expected message to contain 'failed', got %q", result.message)
	}
}
