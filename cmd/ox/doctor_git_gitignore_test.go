//go:build !short

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

	// create .gitignore with a line that contains ".sageox" as part of an inline
	// comment. Git doesn't support inline comments (#), but checkGitignore uses
	// exact string matching against known patterns (.sageox, .sageox/, etc.),
	// so "temp/  # .sageox" won't match any of them.
	gitignorePath := filepath.Join(gitRoot, ".gitignore")
	content := `*.log
temp/  # .sageox should not be ignored
node_modules/
`
	if err := os.WriteFile(gitignorePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	result := checkGitignore(false)

	// passes because the line is "temp/  # .sageox ..." which doesn't match
	// any of the exact patterns checked (.sageox, .sageox/, /.sageox, /.sageox/)
	if !result.passed {
		t.Errorf("expected passed=true when .sageox appears only inside a non-matching line, got: %+v", result)
	}
	if result.message != "not ignored" {
		t.Errorf("unexpected message: %s", result.message)
	}
}
