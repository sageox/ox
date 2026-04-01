//go:build !short

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckSageoxGitignore_FixCreates verifies that when fix=true and .gitignore is missing,
// the check creates it with the required content.
func TestCheckSageoxGitignore_FixCreates(t *testing.T) {
	tmpDir := testGitRepo(t)
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(tmpDir)

	result := checkSageoxGitignore(true)

	if !result.passed {
		t.Errorf("expected check to pass, got passed=%v, message=%q, detail=%q",
			result.passed, result.message, result.detail)
	}

	if result.message != "created" {
		t.Errorf("expected message 'created', got %q", result.message)
	}

	// verify the file was created
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	if _, err := os.Stat(gitignorePath); os.IsNotExist(err) {
		t.Fatalf(".gitignore was not created at %s", gitignorePath)
	}

	// verify the content matches expected
	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("failed to read .gitignore: %v", err)
	}

	if string(content) != sageoxGitignoreContent {
		t.Errorf(".gitignore content does not match expected.\nGot:\n%s\n\nExpected:\n%s",
			string(content), sageoxGitignoreContent)
	}
}

// TestCheckSageoxGitignore_FixMerges verifies that when fix=true and .gitignore exists
// but is missing some required entries, the check merges them in.
func TestCheckSageoxGitignore_FixMerges(t *testing.T) {
	tmpDir := testGitRepo(t)
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	// create a .gitignore with some entries but missing required ones
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	existingContent := `# User custom entries
logs/
cache/
# Missing some required entries like !discovered.jsonl
`
	if err := os.WriteFile(gitignorePath, []byte(existingContent), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(tmpDir)

	result := checkSageoxGitignore(true)

	if !result.passed {
		t.Errorf("expected check to pass, got passed=%v, message=%q, detail=%q",
			result.passed, result.message, result.detail)
	}

	if result.message != "merged missing entries" {
		t.Errorf("expected message 'merged missing entries', got %q", result.message)
	}

	// verify the file was updated
	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("failed to read .gitignore: %v", err)
	}

	contentStr := string(content)

	// verify all required entries are present
	for _, required := range requiredGitignoreEntries {
		if !strings.Contains(contentStr, required) {
			t.Errorf("missing required entry %q in merged .gitignore:\n%s", required, contentStr)
		}
	}

	// verify the original user content was preserved
	if !strings.Contains(contentStr, "# User custom entries") {
		t.Errorf("user custom content was not preserved in merged .gitignore")
	}
}

// TestCheckSageoxGitignore_FixRemovesConflicts verifies that when fix=true and .gitignore
// contains conflicting entries (e.g., "discovered.jsonl" when "!discovered.jsonl" is required),
// the check removes the conflicting entries.
func TestCheckSageoxGitignore_FixRemovesConflicts(t *testing.T) {
	tmpDir := testGitRepo(t)
	sageoxDir := filepath.Join(tmpDir, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	// create a .gitignore with conflicting entries
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	conflictingContent := `# Ignore everything
logs/
cache/
session.jsonl
sessions/
discovered.jsonl
README.md
config.json
offline/
`
	if err := os.WriteFile(gitignorePath, []byte(conflictingContent), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(tmpDir)

	result := checkSageoxGitignore(true)

	if !result.passed {
		t.Errorf("expected check to pass, got passed=%v, message=%q, detail=%q",
			result.passed, result.message, result.detail)
	}

	// the message should indicate changes were made
	if result.message != "merged missing entries" {
		t.Errorf("expected message 'merged missing entries', got %q", result.message)
	}

	// verify the file was updated
	content, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("failed to read .gitignore: %v", err)
	}

	contentStr := string(content)

	// verify conflicting entries were removed
	// "discovered.jsonl" should be removed because "!discovered.jsonl" is required
	lines := strings.Split(contentStr, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// check for the ignore version (without the !) as a standalone entry
		if trimmed == "discovered.jsonl" {
			t.Errorf("conflicting entry 'discovered.jsonl' was not removed from .gitignore:\n%s", contentStr)
		}
		if trimmed == "README.md" {
			t.Errorf("conflicting entry 'README.md' was not removed from .gitignore:\n%s", contentStr)
		}
		if trimmed == "config.json" {
			t.Errorf("conflicting entry 'config.json' was not removed from .gitignore:\n%s", contentStr)
		}
		if trimmed == "offline/" {
			t.Errorf("conflicting entry 'offline/' was not removed from .gitignore:\n%s", contentStr)
		}
	}

	// verify all required entries are present
	for _, required := range requiredGitignoreEntries {
		if !strings.Contains(contentStr, required) {
			t.Errorf("missing required entry %q in .gitignore after conflict removal:\n%s", required, contentStr)
		}
	}
}

// TestMergeGitignoreEntries_AddsMissing verifies that missing required entries are added.
func TestMergeGitignoreEntries_AddsMissing(t *testing.T) {
	existingContent := `logs/
cache/
`

	merged, changed := mergeGitignoreEntries(existingContent)

	if !changed {
		t.Errorf("expected changed=true when entries are missing, got changed=false")
	}

	// verify all required entries are present in the merged result
	for _, required := range requiredGitignoreEntries {
		if !strings.Contains(merged, required) {
			t.Errorf("required entry %q not found in merged content:\n%s", required, merged)
		}
	}
}

// TestMergeGitignoreEntries_PreservesComments verifies that user comments are preserved.
func TestMergeGitignoreEntries_PreservesComments(t *testing.T) {
	existingContent := `# My custom comment
logs/
cache/

# Another important note
session.jsonl
`

	merged, changed := mergeGitignoreEntries(existingContent)

	if !changed {
		t.Errorf("expected changed=true when entries are missing, got changed=false")
	}

	// verify user comments were preserved
	if !strings.Contains(merged, "# My custom comment") {
		t.Errorf("user comment '# My custom comment' was not preserved:\n%s", merged)
	}

	if !strings.Contains(merged, "# Another important note") {
		t.Errorf("user comment '# Another important note' was not preserved:\n%s", merged)
	}
}

// TestCheckGitignore_FixRemovesSageox verifies fix=true removes .sageox/ from root .gitignore
func TestCheckGitignore_FixRemovesSageox(t *testing.T) {
	gitRoot := testGitRepo(t)

	rootGitignorePath := filepath.Join(gitRoot, ".gitignore")
	gitignoreContent := "node_modules/\n.sageox/\n*.log"
	if err := os.WriteFile(rootGitignorePath, []byte(gitignoreContent), 0644); err != nil {
		t.Fatalf("failed to write .gitignore: %v", err)
	}

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	result := checkGitignore(true)
	if !result.passed {
		t.Errorf("expected passed=true, got false: %s", result.message)
	}
	if result.message != "fixed" {
		t.Errorf("expected message='fixed', got %q", result.message)
	}

	content, err := os.ReadFile(rootGitignorePath)
	if err != nil {
		t.Fatalf("failed to read .gitignore: %v", err)
	}

	if strings.Contains(string(content), ".sageox") {
		t.Error("expected .sageox/ to be removed from .gitignore")
	}

	if !strings.Contains(string(content), "node_modules/") {
		t.Error("expected node_modules/ to be preserved")
	}
	if !strings.Contains(string(content), "*.log") {
		t.Error("expected *.log to be preserved")
	}
}

// TestCheckGitignore_FixRemovesAllVariants verifies fix=true removes all .sageox/ variants
func TestCheckGitignore_FixRemovesAllVariants(t *testing.T) {
	variants := []string{".sageox", ".sageox/", "/.sageox", "/.sageox/"}

	for _, variant := range variants {
		t.Run(variant, func(t *testing.T) {
			gitRoot := testGitRepo(t)

			rootGitignorePath := filepath.Join(gitRoot, ".gitignore")
			gitignoreContent := "node_modules/\n" + variant + "\n*.log"
			if err := os.WriteFile(rootGitignorePath, []byte(gitignoreContent), 0644); err != nil {
				t.Fatalf("failed to write .gitignore: %v", err)
			}

			originalWd, _ := os.Getwd()
			defer os.Chdir(originalWd)
			os.Chdir(gitRoot)

			result := checkGitignore(true)
			if !result.passed {
				t.Errorf("expected passed=true, got false: %s", result.message)
			}

			content, err := os.ReadFile(rootGitignorePath)
			if err != nil {
				t.Fatalf("failed to read .gitignore: %v", err)
			}

			if strings.Contains(string(content), ".sageox") {
				t.Errorf("expected %q to be removed from .gitignore", variant)
			}
		})
	}
}

// TestCheckSageoxGitignore_PermissionError verifies handling of permission errors when fixing
func TestCheckSageoxGitignore_PermissionError(t *testing.T) {
	gitRoot := testGitRepo(t)
	sageoxDir := filepath.Join(gitRoot, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0755); err != nil {
		t.Fatalf("failed to create .sageox: %v", err)
	}

	// create .gitignore with missing entries
	gitignorePath := filepath.Join(sageoxDir, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte("logs/\n"), 0644); err != nil {
		t.Fatalf("failed to create .gitignore: %v", err)
	}

	// make .gitignore read-only
	if err := os.Chmod(gitignorePath, 0444); err != nil {
		t.Fatalf("failed to chmod .gitignore: %v", err)
	}
	defer os.Chmod(gitignorePath, 0644) // restore permissions

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	os.Chdir(gitRoot)

	result := checkSageoxGitignore(true)

	// should fail with update error
	if result.passed {
		t.Error("expected passed=false when .gitignore is read-only")
	}
	if result.message != "update failed" {
		t.Errorf("expected message='update failed', got %q", result.message)
	}
}
